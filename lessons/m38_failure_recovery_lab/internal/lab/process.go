package lab

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// childSpec 仅传无秘密身份与原owner授予的manifest路径。LoadConfig在子进程重新
// 从已有 credential_file 读内存秘密。Identity用来拒绝旧日志或别的PID碰巧同名。
type childSpec struct {
	Mode, Namespace, Identity, Grant, Registry, Key string
	Race                                            bool
}
type phase struct {
	Mode, Namespace, Identity, Stage string
	PID                              int
}

// Worker 只由 cmd/demo 的显式 -child 调用。它是唯一业务写者，父观察池max1；
// 自己池max3。defer在正常返回时关client，真实Kill不执行Go defer，父进程必须
// Wait它退出，然后用fresh连接恢复原事实，不能期待child执行数据清理。
func Worker(ctx context.Context, path string, input io.Reader, out io.Writer) (err error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	var spec childSpec
	if len(raw) > 8192 || json.Unmarshal(raw, &spec) != nil {
		return b.ErrInvalid
	}
	grant, e := b.ReadManifest(spec.Grant)
	if e != nil {
		return e
	}
	if grant.Namespace != spec.Namespace {
		return b.ErrInvalid
	}
	actualRace := false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "-race" && setting.Value == "true" {
				actualRace = true
			}
		}
	}
	if spec.Race && !actualRace {
		return errors.New("父要求race child但实际build metadata无-race")
	}
	fmt.Fprintf(out, "CHILD_BUILD pid=%d identity=%s actual_race=%t\n", os.Getpid(), spec.Identity, actualRace)
	cfg, e := b.LoadConfig()
	if e != nil {
		return e
	}
	r, e := b.OpenResources(ctx, cfg, spec.Namespace, spec.Registry, out)
	if e != nil {
		return e
	}
	r.SQL.SetMaxOpenConns(3)
	defer func() { err = errors.Join(err, r.Close()) }()
	if e = r.BorrowCacheKeys(grant); e != nil {
		return e
	}
	// barrier在实际业务hook同步运行。stdout一行JSON报告阶段；stdin等待的是父
	// owner的RELEASE。父在目标窗口Kill时该读取永不返回，进程由OS终止而不是取消。
	barrier := func(stage string) error {
		payload, _ := json.Marshal(phase{Mode: spec.Mode, Namespace: spec.Namespace, Identity: spec.Identity, Stage: stage, PID: os.Getpid()})
		fmt.Fprintf(out, "IPC %s\n", payload)
		line, e := bufio.NewReader(input).ReadString('\n')
		if e != nil {
			return e
		}
		if strings.TrimSpace(line) != "RELEASE" {
			return b.ErrInvalid
		}
		return nil
	}
	switch spec.Mode {
	case "commit":
		_, e = r.Store.Create(ctx, command(spec.Key, "child-create"), b.Hooks{AfterOrderCommit: func(context.Context, b.Order) error { return barrier("ORDER_COMMITTED_BEFORE_PUBLISH") }})
	case "relay":
		_, e = r.Broker.RelayOne(ctx, r.Store, b.Hooks{AfterRelayACK: func(context.Context, b.Event) error { return barrier("KAFKA_ACK_BEFORE_SENT") }})
	case "consume":
		c, q := b.NewKafkaClient(cfg, spec.Namespace+"-kill-consumer", b.ConsumerOptions(r.Broker.Topic, r.Broker.Group)...)
		if q != nil {
			return q
		}
		defer func() { err = errors.Join(err, c.Close()) }()
		record, q := b.PollOne(ctx, c)
		if q != nil {
			return q
		}
		e = r.Broker.ProcessRecord(ctx, c, r.Store, r.Cache, record, b.Hooks{AfterConsumeCommit: func(context.Context, b.Event) error { return barrier("CONSUME_DB_COMMIT_BEFORE_OFFSET") }})
	default:
		return b.ErrInvalid
	}
	return e
}

// process 保存当前 cmd 的实际OS身份与阶段消息。Wait只由一个goroutine调用；
// done关闭后waitErr可读。stdout drain有独立readerDone，Wait结束不替代日志读取任务。
type process struct {
	cmd                       *exec.Cmd
	identity, namespace, mode string
	phase                     chan phase
	done, readerDone          chan struct{}
	waitErr, readErr          error
	stdin                     io.WriteCloser
	out                       io.Writer
	mu                        sync.Mutex
	killed                    bool
	stdinClosed               bool
}

// childBinary 在模块 .local 编译课程入口。race测试显式传环境开关，child也带race
// 编译；普通go test的SKIP不计实库。编译ctx独立上限120秒，不覆盖任何用户产物。
func childBinary(ctx context.Context) (string, error) {
	path, e := filepath.Abs(filepath.Join(".local", "m38-child.exe"))
	if e != nil {
		return "", e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return "", e
	}
	args := []string{"build"}
	if os.Getenv("GOLEARN_M38_CHILD_RACE") == "1" {
		args = append(args, "-race")
	}
	args = append(args, "-o", path, "./cmd/demo")
	buildCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	c := exec.CommandContext(buildCtx, "go", args...)
	// 测试包cwd在internal/lab，定位到当前module。只允许已知两个工作目录。
	if _, e = os.Stat("go.mod"); e != nil {
		c.Dir = filepath.Join("..", "..")
	}
	output, e := c.CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("child build: %w: %s", e, output)
	}
	return path, nil
}

// startProcess 完成管道后Start；成功即登记session，任何后续断言失败都会stop。
// 子进程使用模块当前工作目录，registry/spec均为本run绝对路径，不写主调度目录。
func (s *session) startProcess(binary, mode, key string) (p *process, err error) {
	var token [16]byte
	if _, e := rand.Read(token[:]); e != nil {
		return nil, e
	}
	identity := hex.EncodeToString(token[:])
	grant, e := filepath.Abs(filepath.Join(s.dir, "original.json"))
	if e != nil {
		return nil, e
	}
	registry, e := filepath.Abs(filepath.Join(s.dir, mode+"-child.json"))
	if e != nil {
		return nil, e
	}
	specPath, e := filepath.Abs(filepath.Join(s.dir, mode+"-spec.json"))
	if e != nil {
		return nil, e
	}
	spec := childSpec{Mode: mode, Namespace: s.original.Namespace, Identity: identity, Grant: grant, Registry: registry, Key: key, Race: os.Getenv("GOLEARN_M38_CHILD_RACE") == "1"}
	raw, _ := json.Marshal(spec)
	if e = os.WriteFile(specPath, raw, 0600); e != nil {
		return nil, e
	}
	c := exec.Command(binary, "-child", specPath)
	// 使用父owner自有os.Pipe，Cmd.Wait不会关闭reader。部分构造/Start失败时
	// 立即关闭已取得fd；Start成功后父关自己的writer，让子退出产生真实EOF。
	stdout, writer, e := os.Pipe()
	if e != nil {
		return nil, e
	}
	stdin, e := c.StdinPipe()
	if e != nil {
		_ = stdout.Close()
		_ = writer.Close()
		return nil, e
	}
	c.Stdout = writer
	c.Stderr = s.out
	p = &process{cmd: c, identity: identity, namespace: s.original.Namespace, mode: mode, phase: make(chan phase, 1), done: make(chan struct{}), readerDone: make(chan struct{}), stdin: stdin, out: s.out}
	if e = c.Start(); e != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = writer.Close()
		return nil, e
	}
	_ = writer.Close()
	s.processes = append(s.processes, p)
	go func() {
		defer close(p.readerDone)
		defer stdout.Close()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 65536)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintln(s.out, line)
			if strings.HasPrefix(line, "IPC ") {
				var event phase
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "IPC ")), &event) == nil {
					select {
					case p.phase <- event:
					default:
						p.readErr = errors.New("child阶段消息超过上限")
					}
				}
			}
		}
		p.readErr = errors.Join(p.readErr, scanner.Err())
	}()
	go func() { defer close(p.done); p.waitErr = c.Wait() }()
	identityRaw, _ := json.Marshal(phase{Mode: mode, Namespace: p.namespace, Identity: identity, Stage: "STARTED", PID: c.Process.Pid})
	if e = os.WriteFile(filepath.Join(s.dir, mode+"-pid.json"), identityRaw, 0600); e != nil {
		err = errors.Join(e, p.stop())
		return nil, err
	}
	fmt.Fprintf(s.out, "PROCESS_STARTED mode=%s pid=%d identity=%s namespace=%s\n", mode, c.Process.Pid, identity, p.namespace)
	return p, nil
}

// atStage 先认证实际PID/identity/namespace，再由caller做SQL/broker审计。只匹配
// 日志词或等固定秒数都无法证明进程停在这次目标窗口。
func (p *process) atStage(ctx context.Context, stage string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case event := <-p.phase:
		return b.Assert(event.PID == p.cmd.Process.Pid && event.Identity == p.identity && event.Namespace == p.namespace && event.Mode == p.mode && event.Stage == stage, "可信child实际阶段 expected=%s actual=%+v", stage, event)
	case <-p.done:
		return errors.Join(p.waitErr, errors.New("child目标阶段前已退出"))
	case <-ctx.Done():
		return ctx.Err()
	}
}

// kill 只操作保存在cmd里的本课PID，不按名字/端口杀进程。Kill返回成功还不够，
// 必须Wait读到非零ExitError并drain最终Done，随后父才可以打开恢复业务client。
func (p *process) kill() error {
	if e := p.cmd.Process.Kill(); e != nil {
		return fmt.Errorf("真实Kill失败: %w", e)
	}
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	if e := b.JoinDone(p.done, "OS Process.Wait", 10*time.Second); e != nil {
		return e
	}
	if e := b.JoinDone(p.readerDone, "child stdout drain", 5*time.Second); e != nil {
		return e
	}
	var exit *exec.ExitError
	if e := b.Assert(errors.As(p.waitErr, &exit) && !exit.Success(), "Kill后必须真实非零退出 actual=%v", p.waitErr); e != nil {
		return e
	}
	p.closeStdin()
	fmt.Fprintf(p.out, "PROCESS_KILLED_JOINED mode=%s pid=%d identity=%s actual_exit=%v\n", p.mode, p.cmd.Process.Pid, p.identity, p.waitErr)
	return p.readErr
}

func (p *process) stop() error {
	p.closeStdin()
	select {
	case <-p.done:
		if e := b.JoinDone(p.readerDone, "child stdout final", 5*time.Second); e != nil {
			return e
		}
		p.mu.Lock()
		killed := p.killed
		p.mu.Unlock()
		if killed {
			return p.readErr
		}
		return errors.Join(p.waitErr, p.readErr)
	default:
		return p.kill()
	}
}

func (p *process) closeStdin() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.stdinClosed {
		_ = p.stdin.Close()
		p.stdinClosed = true
	}
}

// safe 的真实退出证据与业务 ExitError 分开。Kill导致非零退出是预期故障；
// Wait/reader任一没结束仍然禁止session删状态，不能靠p.stop返回过就放行。
func (p *process) safe() bool {
	select {
	case <-p.done:
	default:
		return false
	}
	select {
	case <-p.readerDone:
	default:
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stdinClosed
}
