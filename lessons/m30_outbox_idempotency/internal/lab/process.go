package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
)

// ChildSpec完全无秘密，是父进程登记的精确业务资源和窗口。File在本模块.local；
// 子进程只操作父已创建对象，从原服务索引内存读凭据，不自行创建/删除共享对象。
type ChildSpec struct {
	Prefix, Topic, Group, Mode, Gate string
	Request                          Request
	EventID                          string
}
type Launcher struct {
	Executable string
	Args       []string
	Directory  string
}

// gate只供实验传给Place/RelayOne/Apply，在真实持久/ACK点之后原子发布ready。
// 发布后当前goroutine停在ctx.Done等父强杀；强杀不会执行Go defer。
// 这条门表示已到这个点，不表示任务finalDone、offset提交或进程已经退出。
func gate(ctx context.Context, path string) error {
	if e := os.WriteFile(path+".tmp", []byte("ready"), 0600); e != nil {
		return e
	}
	if e := os.Rename(path+".tmp", path); e != nil {
		return e
	}
	<-ctx.Done()
	return ctx.Err()
}

// Child是cmd/demo和test helper共同入口，通过M30_CHILD_SPEC进入独立OS进程。
// 普通恢复路径有defer Close；崩溃路径父Process.Kill让这些defer刻意不执行，
// parent必须Wait、权威查询并最终清理。子池只有1连接，所有数据库操作串行。
func Child(path string, out io.Writer) (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	var spec ChildSpec
	if err = readJSON(path, &spec); err != nil {
		return err
	}
	if !prefixPattern.MatchString(spec.Prefix) {
		return errors.New("子进程非法资源前缀")
	}
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	db, err := openDB(ctx, cfg, 1)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	s := &Store{DB: db, Prefix: spec.Prefix, Out: out}
	mark := func() error { return gate(ctx, spec.Gate) } // 闭包借用当前ctx/spec，未启动另一个goroutine。
	switch spec.Mode {
	case "order-before":
		_, err = s.Place(ctx, spec.Request, true, mark, nil)
	case "order-after":
		_, err = s.Place(ctx, spec.Request, true, nil, mark)
	case "relay-ack", "relay-recover":
		c, e := newClient(cfg)
		if e != nil {
			return e
		}
		defer func() { err = errors.Join(err, c.Close()) }()
		var hook func() error
		if spec.Mode == "relay-ack" {
			hook = mark
		}
		err = s.RelayOne(ctx, c, spec.Topic, spec.EventID, hook)
	case "consume-inbox", "consume-commit", "consume-recover", "offset-before":
		c, e := newClient(cfg, consumerOpts(spec.Topic, spec.Group)...)
		if e != nil {
			return e
		}
		defer func() { err = errors.Join(err, c.Close()) }()
		if spec.Mode == "offset-before" {
			// 明确坏例：先向broker确认r+1，再故障暂停；尚未创建SQL事务。
			r, _, e := pollOne(ctx, c)
			if e != nil {
				return e
			}
			if e = c.K.CommitRecords(ctx, r); e != nil {
				return e
			}
			return mark()
		}
		var inboxHook, commitHook func() error
		if spec.Mode == "consume-inbox" {
			inboxHook = mark
		}
		if spec.Mode == "consume-commit" {
			commitHook = mark
		}
		_, _, err = s.ConsumeOne(ctx, c, true, false, inboxHook, commitHook)
	default:
		return errors.New("未知子进程模式")
	}
	return err
}

// runChild是父进程的唯一子进程owner。Start成功后立即登记Wait goroutine与保护
// defer：任一父错误都Kill自己的子进程并独立Wait；错误结果和Wait真正完成分开。
// crash=true只在ready存在时强杀，所以崩溃位置来自真实执行的门而非固定sleep。
func runChild(ctx context.Context, l Launcher, spec ChildSpec, crash bool, out io.Writer) (err error) {
	path := filepath.Join(l.Directory, spec.Mode+"-"+spec.Request.SKU+".json")
	spec.Gate = path + ".ready"
	b, e := json.Marshal(spec)
	if e != nil {
		return e
	}
	if e = os.WriteFile(path, b, 0600); e != nil {
		return e
	}
	log, e := os.OpenFile(path+".log", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, log.Close()) }()
	cmd := exec.Command(l.Executable, l.Args...)
	cmd.Env = append(os.Environ(), "M30_CHILD_SPEC="+path)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Dir = l.Directory
	if e = cmd.Start(); e != nil {
		return e
	}
	done := make(chan struct{})
	var waitErr error
	go func() { defer close(done); waitErr = cmd.Wait() }() // 只有这一个goroutine调用Wait。
	joined := false
	defer func() {
		if !joined {
			killErr := cmd.Process.Kill()
			if errors.Is(killErr, os.ErrProcessDone) {
				killErr = nil
			}
			err = errors.Join(err, killErr, joinDone(done, "子进程保护Wait"))
		}
	}()
	if crash {
		e = observe(ctx, func() (bool, error) {
			select {
			case <-done:
				return false, errors.Join(errors.New("子进程在ready前退出，查看专属日志"), waitErr)
			default:
			}
			_, q := os.Stat(spec.Gate)
			if errors.Is(q, os.ErrNotExist) {
				return false, nil
			}
			return q == nil, q
		})
		if e != nil {
			return e
		}
		if e = cmd.Process.Kill(); e != nil {
			return e
		}
		if e = joinDone(done, "强杀子进程Wait"); e != nil {
			return e
		}
		joined = true
		if waitErr == nil {
			return errors.New("强杀进程却成功退出，崩溃证明不足")
		}
		fmt.Fprintf(out, "PROCESS_CRASH mode=%s pid=%d ready=true killed=true wait_done=true exit=%v\n", spec.Mode, cmd.Process.Pid, waitErr)
		return nil // 只在确实命中窗口并有失败退出的实验分支接受强杀退出码。
	}
	select {
	case <-done:
		joined = true
		fmt.Fprintf(out, "PROCESS_EXIT mode=%s pid=%d wait_done=true success=%t\n", spec.Mode, cmd.Process.Pid, waitErr == nil)
		return waitErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RecoverRecordedGroup是异常恢复入口：操作者先确认原owner进程已退出，再提供
// 本run资源登记和实际失败日志中的精确group名。只处理这一名字，不做前缀删除。
// 文件登记不是他人对象的所有权证明，因此本接口要求实际日志已证明是该run触及对象。
func RecoverRecordedGroup(path, name string, out io.Writer) (err error) {
	var registry struct {
		Prefix, RunID string
		Groups        []string
	}
	if err = readJSON(path, &registry); err != nil {
		return err
	}
	if !prefixPattern.MatchString(registry.Prefix) || registry.RunID != "golearn.m30."+registry.Prefix[4:] {
		return errors.New("非M30登记")
	}
	allowed := false
	for _, g := range registry.Groups {
		if g == name {
			allowed = true
		}
	}
	if !allowed {
		return errors.New("精确group不在本run登记中")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	b, err := openBroker(ctx, cfg, out)
	if err != nil {
		return err
	}
	b.Groups = []string{name}
	return b.Cleanup()
}

// VerifyRecordedAbsent供失败轮最终只读核验。登记包含本run计划名字，未创建对象
// 也应不存在；这里只逐个精确查询，不删除任何对象、不从前缀扫描外推其它资源。
// inspector自己拥有DB/admin client，任一查询失败仍分别Close并保留原原因。
func VerifyRecordedAbsent(path string, out io.Writer) (err error) {
	var registry struct {
		Prefix, RunID          string
		Tables, Topics, Groups []string
	}
	if err = readJSON(path, &registry); err != nil {
		return err
	}
	if !prefixPattern.MatchString(registry.Prefix) || registry.RunID != "golearn.m30."+registry.Prefix[4:] {
		return errors.New("非M30登记")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	db, err := openDB(ctx, cfg, 1)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, cleanupError(db.Close())) }()
	b, err := openBroker(ctx, cfg, out)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, b.Client.Close()) }()
	for _, name := range registry.Tables {
		var n int
		if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", name).Scan(&n); err != nil {
			return err
		}
		if err = require(n == 0, "登记表仍存在: %s", name); err != nil {
			return err
		}
	}
	for _, name := range registry.Topics {
		m, q := b.Admin.Metadata(kadm.WithAuthorizedOps(ctx), name)
		if q != nil {
			return q
		}
		d, ok := m.Topics[name]
		if err = require(ok && errors.Is(d.Err, kerr.UnknownTopicOrPartition), "登记topic仍存在: %s", name); err != nil {
			return err
		}
	}
	for _, name := range registry.Groups {
		ds, q := b.Admin.DescribeGroups(ctx, name)
		if q != nil {
			return q
		}
		d, ok := ds[name]
		if err = require(ok && (errors.Is(d.Err, kerr.GroupIDNotFound) || (d.Err == nil && d.State == "Dead" && len(d.Members) == 0)), "登记group仍存在: %s", name); err != nil {
			return err
		}
		offsets, q := b.Admin.FetchOffsets(ctx, name)
		if q != nil {
			return q
		}
		if err = require(len(offsets) == 0, "登记group还有offset: %s", name); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "RECORDED_ABSENT run=%s tables=%d topics=%d groups=%d real_metadata=true offsets_empty=true\n", registry.RunID, len(registry.Tables), len(registry.Topics), len(registry.Groups))
	return nil
}
