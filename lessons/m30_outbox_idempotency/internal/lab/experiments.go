package lab

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Lab是本轮合成业务资源的owner。所有操作按Run顺序执行，Store/Broker不跨
// goroutine并发修改；唯一异步Wait/producer/驱动关闭任务均由其创建者真join。
type Lab struct {
	Store    *Store
	Broker   *Broker
	Config   Config
	RunID    string
	Launcher Launcher
	Out      io.Writer
}

// Run由cmd/demo和显式集成测试调用。入口ctx给整课预算，清理始终另开Background
// 预算；任一错误停止后续case，但defer继续逐项精确清理并Join全部原因。
func Run(ctx context.Context, out io.Writer, launcher Launcher) (err error) {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	hexID := hex.EncodeToString(random[:])
	prefix := "m30_" + hexID
	runID := "golearn.m30." + hexID
	if err = os.MkdirAll(".local", 0700); err != nil {
		return err
	}
	dir, err := filepath.Abs(filepath.Join(".local", prefix))
	if err != nil {
		return err
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return err
	} // 不覆盖一个已有run目录。
	launcher.Directory = dir
	if launcher.Executable == "" {
		launcher.Executable, err = os.Executable()
		if err != nil {
			return err
		}
	}
	// 在任何SQL/metadata/Create/consumer触及名字之前落盘精确资源登记，无凭据。
	var registry struct {
		Prefix, RunID          string
		Tables, Topics, Groups []string
	}
	registry.Prefix, registry.RunID = prefix, runID
	for _, suffix := range tableSuffixes {
		registry.Tables = append(registry.Tables, prefix+"_"+suffix)
	}
	for _, name := range caseNames {
		registry.Topics = append(registry.Topics, runID+"."+name)
		registry.Groups = append(registry.Groups, runID+"."+name+".group")
	}
	registry.Groups = append(registry.Groups, runID+".offset-bad.repair")
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "resources.json"), data, 0600); err != nil {
		return err
	}
	db, err := openDB(ctx, cfg, 2)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, db.Close())
		stats := db.Stats()
		err = errors.Join(err, require(stats.OpenConnections == 0 && stats.InUse == 0, "DB close仍有连接"))
		fmt.Fprintf(out, "FINAL_DONE db_open=%d db_inuse=%d\n", stats.OpenConnections, stats.InUse)
	}()
	s := &Store{DB: db, Prefix: prefix, Out: out}
	defer func() { err = errors.Join(err, s.Cleanup()) }()
	if err = s.Create(ctx); err != nil {
		return err
	}
	b, err := openBroker(ctx, cfg, out)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, b.Cleanup()) }()
	l := &Lab{s, b, cfg, runID, launcher, out}
	fmt.Fprintf(out, "M30_BEGIN run=%s mysql=%s kafka=%s single_relay=true partitions=1\n", runID, cfg.MySQLVersion, cfg.KafkaVersion)
	for _, experiment := range []func(context.Context) error{l.badDoubleWrite, l.lostACK, l.orderCrashes, l.relayCrash, l.consumerCrashes, l.effectRollback, l.offsetBeforeCommit, l.outOfOrder} {
		if err = experiment(ctx); err != nil {
			return err
		}
	}
	fmt.Fprintln(out, "BUSINESS_ALL_PASS: 持久SQL、broker事件和安全offset逐项核验完成")
	if err = l.totals(ctx); err != nil {
		return err
	}
	return nil // 真正M30_PASS由main在Run全部defer清理后输出。
}

var caseNames = []string{"db-first", "kafka-first", "ack-lost", "order-before", "order-after", "relay", "consumer-inbox", "consumer-commit", "effect-fail", "offset-bad", "ordering"}

// Cleanup只删Create已确认独占的精确表，逐项独立预算；DROP IF EXISTS允许
// 创建响应丢失/部分创建后恢复，information_schema读回0才证明逻辑清理完成。
func (s *Store) Cleanup() (err error) {
	for _, name := range s.Owned {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
		_, e := s.DB.ExecContext(ctx, "DROP TABLE IF EXISTS `"+name+"`")
		if e == nil {
			var n int
			e = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", name).Scan(&n)
			if e == nil {
				e = require(n == 0, "精确表删除后仍存在")
			}
		}
		cancel()
		err = errors.Join(err, e)
		fmt.Fprintf(s.Out, "CLEANUP_TABLE name=%s verified=%t\n", name, e == nil)
	}
	return
}

// setup创建一次case的SKU/topic/group，所有名字先在Run登记，Topic/Group再次确认
// 没有他人对象才加入内存清理授权。Request的key属于同一run且可跨子进程重用。
func (l *Lab) setup(ctx context.Context, name string) (Request, string, string, error) {
	r := Request{Key: l.Store.Prefix + "-" + name, SKU: name, Qty: 1}
	topic := l.RunID + "." + name
	group := topic + ".group"
	if e := l.Store.Seed(ctx, name); e != nil {
		return r, topic, group, e
	}
	if e := l.Broker.Topic(ctx, topic); e != nil {
		return r, topic, group, e
	}
	if e := l.Broker.Group(ctx, group); e != nil {
		return r, topic, group, e
	}
	return r, topic, group, nil
}

// withProducer把client生命期限制在同步fn，fn返回前所有produce辅助任务已join。
func (l *Lab) withProducer(fn func(*Client) error, opts ...kgo.Opt) (err error) {
	c, err := newClient(l.Config, opts...)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	return fn(c)
}

// consume同步借一条consumer，所有失败都Close，再新建同group才算恢复。
// max1确保本课offset是处理完成的连续前缀；未调用CommitRecords的失败不能前移。
func (l *Lab) consume(ctx context.Context, topic, group string, fail bool) (event Event, err error) {
	c, err := newClient(l.Config, consumerOpts(topic, group)...)
	if err != nil {
		return event, err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	event, _, err = l.Store.ConsumeOne(ctx, c, true, fail, nil, nil)
	return
}

// send由坏顺序/顺序实验借用producer；返回nil才有ACK。caller仍需SQL/inbox/offset
// 的独立证据，不能把producer返回和业务完成混为一件事。
func (l *Lab) send(ctx context.Context, topic string, e Event) error {
	return l.withProducer(func(c *Client) error { _, q := produce(ctx, c, topic, e); return q })
}

// badDoubleWrite以真实SQL+Kafka比较两个坏顺序。DB-first发送前断自己的TCP；
// Kafka-first在Tx内写完订单后真实发送ACK，再返回业务失败使Tx回滚。
func (l *Lab) badDoubleWrite(ctx context.Context) error {
	fmt.Fprintln(l.Out, "=== E01 两种双写坏顺序 ===")
	r, topic, _, err := l.setup(ctx, "db-first")
	if err != nil {
		return err
	}
	e, err := l.Store.Place(ctx, r, false, nil, nil)
	if err != nil {
		return err
	}
	fault := &ProduceFault{Before: true}
	err = l.withProducer(func(c *Client) error { _, q := produce(ctx, c, topic, e); return q }, kgo.Dialer(fault.Dial), kgo.DisableIdempotentWrite(), kgo.RequestRetries(0), kgo.RecordDeliveryTimeout(time.Second))
	if q := require(err != nil && !errors.Is(err, ErrCleanup) && fault.Connected.Load(), "DB-first必须命中真实socket发送失败且清理成功"); q != nil {
		return q
	}
	fmt.Fprintf(l.Out, "SEND_FAILED classification=UNKNOWN before_produce=%t cause=%v\n", fault.Before, err)
	es, q := l.Broker.Records(ctx, l.Config, topic)
	if q != nil {
		return q
	}
	if q = require(len(es) == 0, "DB-first broker必须独立确认没有本事件"); q != nil {
		return q
	}
	if q = l.Store.Verify(ctx, r.SKU, Snapshot{Stock: 9, SourceVersion: 1, Projected: 10, Orders: 1}); q != nil {
		return q
	}
	r, topic, group, err := l.setup(ctx, "kafka-first")
	if err != nil {
		return err
	}
	orphan := Event{eventID(r.Key), r.Key, r.SKU, 1, 1}
	_, err = l.Store.Place(ctx, r, false, func() error {
		if q := l.send(ctx, topic, orphan); q != nil {
			return q
		}
		return ErrEffect
	}, nil)
	if !errors.Is(err, ErrEffect) || errors.Is(err, ErrCleanup) {
		return errors.Join(err, errors.New("Kafka-first没有命中ACK后Tx回滚"))
	}
	es, q = l.Broker.Records(ctx, l.Config, topic)
	if q != nil {
		return q
	}
	if q = require(len(es) == 1 && es[0] == orphan, "Kafka-first实broker孤儿事件不符"); q != nil {
		return q
	}
	if _, q = l.consume(ctx, topic, group, false); q != nil {
		return q
	}
	if q = l.Broker.VerifyOffset(ctx, topic, group, 1); q != nil {
		return q
	}
	return l.Store.Verify(ctx, r.SKU, Snapshot{Stock: 10, Projected: 9, Version: 1, Effects: 1, Inbox: 1})
}

// lostACK复现“返回失败但broker已接受”。仪器截断真实Produce响应后client失败；
// 权威broker读出相同Event，原key返回原订单，绝不制造新ID/再次扣库存。
func (l *Lab) lostACK(ctx context.Context) error {
	fmt.Fprintln(l.Out, "=== E02 Produce ACK丢失，UNKNOWN查账 ===")
	r, topic, group, err := l.setup(ctx, "ack-lost")
	if err != nil {
		return err
	}
	e, err := l.Store.Place(ctx, r, false, nil, nil)
	if err != nil {
		return err
	}
	fault := &ProduceFault{}
	err = l.withProducer(func(c *Client) error { _, q := produce(ctx, c, topic, e); return q }, kgo.Dialer(fault.Dial), kgo.DisableIdempotentWrite(), kgo.RequestRetries(0), kgo.RecordDeliveryTimeout(2*time.Second))
	if q := require(err != nil && !errors.Is(err, ErrCleanup) && fault.Connected.Load() && fault.Forwarded.Load() && fault.Dropped.Load(), "必须真实转发Produce并截断其broker响应且清理成功"); q != nil {
		return errors.Join(q, err)
	}
	fmt.Fprintf(l.Out, "ACK_LOST classification=UNKNOWN produce_forwarded=true response_dropped=true cause=%v\n", err)
	es, q := l.Broker.Records(ctx, l.Config, topic)
	if q != nil {
		return q
	}
	if q = require(len(es) == 1 && es[0] == e, "ACK丢失后应按原ID/value读到1条"); q != nil {
		return q
	}
	old, found, q := l.Store.QueryOrder(ctx, r.Key)
	if q != nil {
		return q
	}
	if q = require(found && old == e, "原key查询未匹配已提交订单"); q != nil {
		return q
	}
	again, q := l.Store.Place(ctx, r, false, nil, nil)
	if q != nil {
		return q
	}
	if q = require(again == e, "相同key必须返回相同event"); q != nil {
		return q
	}
	if _, q = l.consume(ctx, topic, group, false); q != nil {
		return q
	}
	if q = l.Broker.VerifyOffset(ctx, topic, group, 1); q != nil {
		return q
	}
	return l.Store.Verify(ctx, r.SKU, Snapshot{Stock: 9, SourceVersion: 1, Projected: 9, Version: 1, Effects: 1, Orders: 1, Inbox: 1})
}

// childSpec由各case构造父已拥有的资源，重启使用完全相同topic/group/key/eventID。
func (l *Lab) childSpec(r Request, topic, group, mode string) ChildSpec {
	return ChildSpec{Prefix: l.Store.Prefix, Topic: topic, Group: group, Mode: mode, Request: r, EventID: eventID(r.Key)}
}

// relayNormal用独立重启OS进程恢复pending outbox，正常子进程Wait退出0才继续验证。
func (l *Lab) relayNormal(ctx context.Context, r Request, topic, group string) error {
	return runChild(ctx, l.Launcher, l.childSpec(r, topic, group, "relay-recover"), false, l.Out)
}

// orderCrashes验证commit前和commit后两个真实进程窗口。前者锁定SELECT等回滚;
// 后者父请求响应未得到，按原key权威查询恢复COMMITTED，重复下单不会再次扣源库存。
func (l *Lab) orderCrashes(ctx context.Context) error {
	fmt.Fprintln(l.Out, "=== E03 订单Tx前后强杀与原key恢复 ===")
	for _, name := range []string{"order-before", "order-after"} {
		r, topic, group, err := l.setup(ctx, name)
		if err != nil {
			return err
		}
		if err = runChild(ctx, l.Launcher, l.childSpec(r, topic, group, name), true, l.Out); err != nil {
			return err
		}
		want := Snapshot{Stock: 10, Projected: 10}
		if name == "order-after" {
			want.Stock = 9
			want.SourceVersion = 1
			want.Orders = 1
			want.Outbox = 1
		}
		if err = l.Store.Verify(ctx, name, want); err != nil {
			return err
		}
		e, found, err := l.Store.QueryOrder(ctx, r.Key)
		if err != nil {
			return err
		}
		if err = require(found == (name == "order-after"), "原key查询与真实commit窗口不符"); err != nil {
			return err
		}
		if found {
			fmt.Fprintf(l.Out, "REQUEST_RECOVERY original_key=%s state=COMMITTED event=%s\n", r.Key, e.ID)
		}
		e, err = l.Store.Place(ctx, r, true, nil, nil)
		if err != nil {
			return err
		}
		if _, err = l.Store.Place(ctx, r, true, nil, nil); err != nil {
			return err
		}
		changed := r
		changed.Qty = 2
		if _, q := l.Store.Place(ctx, changed, true, nil, nil); !errors.Is(q, ErrKeyConflict) {
			return errors.Join(q, errors.New("原key不得更换quantity"))
		}
		if err = require(e.ID == eventID(r.Key), "下单稳定ID错误"); err != nil {
			return err
		}
		// 单独在实库重插已提交原订单，让MySQL真正返回1062；withTx完整回滚后，
		// 调用Place同一恢复函数查原key。没有伪造rollback/网络失败，也不新增订单。
		duplicateErr := l.Store.withTx(ctx, func(tx *sql.Tx) error {
			_, q := tx.ExecContext(ctx, "INSERT INTO "+l.Store.table("orders")+" VALUES (?,?,?,?,?)", e.Request, e.ID, e.SKU, e.Qty, e.Version)
			return q
		})
		var duplicate *mysql.MySQLError
		if !errors.As(duplicateErr, &duplicate) || duplicate.Number != 1062 {
			return errors.Join(duplicateErr, errors.New("E03实库未返回1062"))
		}
		original, recovered, q := recoverOrderDuplicate(r, duplicateErr, func() (Event, bool, error) {
			return l.Store.QueryOrder(ctx, r.Key)
		})
		if q != nil {
			return q
		}
		if err = require(recovered && original == e, "E03安全回滚后的真实1062未恢复原订单"); err != nil {
			return err
		}
		fmt.Fprintf(l.Out, "ORDER_DUPLICATE_RECOVERY sku=%s sql=1062 rollback_safe=true original_match=true\n", name)
		if err = l.relayNormal(ctx, r, topic, group); err != nil {
			return err
		}
		if _, err = l.consume(ctx, topic, group, false); err != nil {
			return err
		}
		if err = l.Broker.VerifyOffset(ctx, topic, group, 1); err != nil {
			return err
		}
		if err = l.Store.Verify(ctx, name, Snapshot{Stock: 9, SourceVersion: 1, Projected: 9, Version: 1, Effects: 1, Orders: 1, Outbox: 1, Inbox: 1, Sent: 1}); err != nil {
			return err
		}
	}
	return nil
}

// relayCrash先命中真实ACK/sent尚未更新的进程强杀，重启后再ACK一次。
// 两条broker记录ID/value完全相同，消费者两次投递只提交一次SQL效果。
func (l *Lab) relayCrash(ctx context.Context) error {
	fmt.Fprintln(l.Out, "=== E04 relay ACK后强杀，重复投递效果唯一 ===")
	r, topic, group, err := l.setup(ctx, "relay")
	if err != nil {
		return err
	}
	e, err := l.Store.Place(ctx, r, true, nil, nil)
	if err != nil {
		return err
	}
	if err = runChild(ctx, l.Launcher, l.childSpec(r, topic, group, "relay-ack"), true, l.Out); err != nil {
		return err
	}
	if err = l.Store.Verify(ctx, r.SKU, Snapshot{Stock: 9, SourceVersion: 1, Projected: 10, Orders: 1, Outbox: 1}); err != nil {
		return err
	}
	if err = l.relayNormal(ctx, r, topic, group); err != nil {
		return err
	}
	es, err := l.Broker.Records(ctx, l.Config, topic)
	if err != nil {
		return err
	}
	if err = require(len(es) == 2 && es[0] == e && es[1] == e, "relay崩溃恢复必须产生2条同ID事件"); err != nil {
		return err
	}
	for i := int64(1); i <= 2; i++ {
		if _, err = l.consume(ctx, topic, group, false); err != nil {
			return err
		}
		if err = l.Broker.VerifyOffset(ctx, topic, group, i); err != nil {
			return err
		}
	}
	changed := e
	changed.Qty = 2
	if _, q := l.Store.Apply(ctx, changed, false, nil, nil); !errors.Is(q, ErrKeyConflict) || errors.Is(q, ErrCleanup) {
		return errors.Join(q, errors.New("相同eventID不同payload必须拒绝"))
	}
	return l.Store.Verify(ctx, r.SKU, Snapshot{Stock: 9, SourceVersion: 1, Projected: 9, Version: 1, Effects: 1, Orders: 1, Outbox: 1, Inbox: 1, Sent: 1})
}

// consumerCrashes分别在inbox未提交、业务已提交而offset未提交处强杀。
// 恢复是同group的新OS进程；inbox与投影都来自实库，不能靠进程内seen map幂等。
func (l *Lab) consumerCrashes(ctx context.Context) error {
	fmt.Fprintln(l.Out, "=== E05 consumer两个强杀窗口与重启 ===")
	for _, mode := range []string{"consumer-inbox", "consumer-commit"} {
		r, topic, group, err := l.setup(ctx, mode)
		if err != nil {
			return err
		}
		if _, err = l.Store.Place(ctx, r, true, nil, nil); err != nil {
			return err
		}
		if err = l.relayNormal(ctx, r, topic, group); err != nil {
			return err
		}
		childMode := "consume-inbox"
		if mode == "consumer-commit" {
			childMode = "consume-commit"
		}
		if err = runChild(ctx, l.Launcher, l.childSpec(r, topic, group, childMode), true, l.Out); err != nil {
			return err
		}
		want := Snapshot{Stock: 9, SourceVersion: 1, Projected: 10, Orders: 1, Outbox: 1, Sent: 1}
		if mode == "consumer-commit" {
			want.Projected = 9
			want.Version = 1
			want.Effects = 1
			want.Inbox = 1
		}
		if err = l.Store.Verify(ctx, r.SKU, want); err != nil {
			return err
		}
		if err = l.Broker.VerifyOffset(ctx, topic, group, -1); err != nil {
			return err
		}
		if err = runChild(ctx, l.Launcher, l.childSpec(r, topic, group, "consume-recover"), false, l.Out); err != nil {
			return err
		}
		if err = l.Broker.VerifyOffset(ctx, topic, group, 1); err != nil {
			return err
		}
		if err = l.Store.Verify(ctx, r.SKU, Snapshot{Stock: 9, SourceVersion: 1, Projected: 9, Version: 1, Effects: 1, Orders: 1, Outbox: 1, Inbox: 1, Sent: 1}); err != nil {
			return err
		}
	}
	return nil
}

// effectRollback不是进程崩溃例；它单独验证inbox插入后业务返回ErrEffect时同Tx回滚。
// 失败后Close消费者、读offset仍-1，恢复原消息业务才成功和提交next1。
func (l *Lab) effectRollback(ctx context.Context) error {
	fmt.Fprintln(l.Out, "=== E06 inbox成功后效果失败必须rollback ===")
	r, topic, group, err := l.setup(ctx, "effect-fail")
	if err != nil {
		return err
	}
	if _, err = l.Store.Place(ctx, r, true, nil, nil); err != nil {
		return err
	}
	if err = l.relayNormal(ctx, r, topic, group); err != nil {
		return err
	}
	_, err = l.consume(ctx, topic, group, true)
	if !errors.Is(err, ErrEffect) || errors.Is(err, ErrCleanup) {
		return errors.Join(err, errors.New("未触发效果失败"))
	}
	if err = l.Store.Verify(ctx, r.SKU, Snapshot{Stock: 9, SourceVersion: 1, Projected: 10, Orders: 1, Outbox: 1, Sent: 1}); err != nil {
		return err
	}
	if err = l.Broker.VerifyOffset(ctx, topic, group, -1); err != nil {
		return err
	}
	if _, err = l.consume(ctx, topic, group, false); err != nil {
		return err
	}
	if err = l.Broker.VerifyOffset(ctx, topic, group, 1); err != nil {
		return err
	}
	return l.Store.Verify(ctx, r.SKU, Snapshot{Stock: 9, SourceVersion: 1, Projected: 9, Version: 1, Effects: 1, Orders: 1, Outbox: 1, Inbox: 1, Sent: 1})
}

// offsetBeforeCommit明确错误顺序：子进程已commit offset、尚未业务时强杀。
// 同group重建后ctx空poll读不到offset0，SQL效果仍0；修复用登记的独占repair group
// 重放确切原事件（非全局reset），业务成功后commit，证明源消息仍在broker。
func (l *Lab) offsetBeforeCommit(ctx context.Context) (err error) {
	fmt.Fprintln(l.Out, "=== E07 先offset后业务会丢效果，修复顺序 ===")
	r, topic, group, err := l.setup(ctx, "offset-bad")
	if err != nil {
		return err
	}
	if _, err = l.Store.Place(ctx, r, true, nil, nil); err != nil {
		return err
	}
	if err = l.relayNormal(ctx, r, topic, group); err != nil {
		return err
	}
	if err = runChild(ctx, l.Launcher, l.childSpec(r, topic, group, "offset-before"), true, l.Out); err != nil {
		return err
	}
	if err = l.Broker.VerifyOffset(ctx, topic, group, 1); err != nil {
		return err
	}
	if err = l.Store.Verify(ctx, r.SKU, Snapshot{Stock: 9, SourceVersion: 1, Projected: 10, Orders: 1, Outbox: 1, Sent: 1}); err != nil {
		return err
	}
	c, err := newClient(l.Config, consumerOpts(topic, group)...)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.Close()) }() // 创建成功立即登记owner，覆盖任何提前返回。
	// 先确认组实际恢复到Stable成员1，才开始短的“已提交位置没有消息”读预算。
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = observe(probeCtx, func() (bool, error) {
		// 组加入需要poll推进，但空topic会真实等fetch；每次只借200ms子预算。
		pollCtx, pollCancel := context.WithTimeout(probeCtx, 200*time.Millisecond)
		fetches := c.K.PollRecords(pollCtx, 1)
		pollCancel()
		for _, q := range fetches.Errors() {
			if !errors.Is(q.Err, context.DeadlineExceeded) {
				return false, q.Err
			}
		}
		c.K.AllowRebalance()
		ds, q := l.Broker.Admin.DescribeGroups(probeCtx, group)
		if q != nil {
			return false, q
		}
		d := ds[group]
		return d.Err == nil && d.State == "Stable" && len(d.Members) == 1, nil
	})
	cancel()
	if err != nil {
		return errors.Join(err, c.Close())
	}
	emptyCtx, emptyCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	_, _, pollErr := pollOne(emptyCtx, c)
	emptyCancel()
	closeErr := c.Close()
	if closeErr != nil {
		return closeErr
	}
	if !errors.Is(pollErr, context.DeadlineExceeded) {
		return errors.Join(pollErr, errors.New("恢复group意外重新读到了已跳过消息"))
	}
	repair := l.RunID + ".offset-bad.repair"
	if err = l.Broker.Group(ctx, repair); err != nil {
		return err
	}
	if _, err = l.consume(ctx, topic, repair, false); err != nil {
		return err
	}
	if err = l.Broker.VerifyOffset(ctx, topic, repair, 1); err != nil {
		return err
	}
	return l.Store.Verify(ctx, r.SKU, Snapshot{Stock: 9, SourceVersion: 1, Projected: 9, Version: 1, Effects: 1, Orders: 1, Outbox: 1, Inbox: 1, Sent: 1})
}

// outOfOrder手动反序发送同SKU v2/v1，但源SQL/outbox真实记录两笔订单。
// consumer在v2检测ErrVersion并回滚inbox，offset保持-1。本课契约是停止该分区，
// 修复须显式对账源库：先处理同topic确切v1，再重试v2，最后提交已完成连续前缀。
func (l *Lab) outOfOrder(ctx context.Context) (err error) {
	fmt.Fprintln(l.Out, "=== E08 同聚合版本乱序检测与受控恢复 ===")
	r, topic, group, err := l.setup(ctx, "ordering")
	if err != nil {
		return err
	}
	v1, err := l.Store.Place(ctx, r, true, nil, nil)
	if err != nil {
		return err
	}
	r.Key += "-second"
	v2, err := l.Store.Place(ctx, r, true, nil, nil)
	if err != nil {
		return err
	}
	if err = l.send(ctx, topic, v2); err != nil {
		return err
	}
	if err = l.send(ctx, topic, v1); err != nil {
		return err
	}
	_, err = l.consume(ctx, topic, group, false)
	if !errors.Is(err, ErrVersion) || errors.Is(err, ErrCleanup) {
		return errors.Join(err, errors.New("v2未触发顺序检测"))
	}
	if err = l.Broker.VerifyOffset(ctx, topic, group, -1); err != nil {
		return err
	}
	if err = l.Store.Verify(ctx, r.SKU, Snapshot{Stock: 8, SourceVersion: 2, Projected: 10, Orders: 2, Outbox: 2}); err != nil {
		return err
	}
	c, err := newClient(l.Config, consumerOpts(topic, group)...)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	r0, e0, err := pollOne(ctx, c)
	if err != nil {
		return err
	}
	r1, e1, err := pollOne(ctx, c)
	if err != nil {
		return err
	}
	if err = require(r0.Offset == 0 && r1.Offset == 1 && e0 == v2 && e1 == v1, "修复重投必须读相同topic的真实反序事件"); err != nil {
		return err
	}
	// 只有读到真实v1并核对源订单匹配后才执行受控恢复；最多暂存2条，非通用缓冲框架。
	for _, e := range []Event{e1, e0} {
		source, ok, q := l.Store.QueryOrder(ctx, e.Request)
		if q != nil {
			return q
		}
		if q = require(ok && source == e, "修复事件需源库权威匹配"); q != nil {
			return q
		}
		if _, q = l.Store.Apply(ctx, e, false, nil, nil); q != nil {
			return q
		}
	}
	if err = c.K.CommitRecords(ctx, r1); err != nil {
		return err
	}
	c.K.AllowRebalance()
	if err = l.Broker.VerifyOffset(ctx, topic, group, 2); err != nil {
		return err
	}
	// 已ACK两条的outbox精确标记是此受控恢复步骤；生产relay应维护自己的发送顺序。
	for _, e := range []Event{v1, v2} {
		if _, err = l.Store.DB.ExecContext(ctx, "UPDATE "+l.Store.table("outbox")+" SET sent=TRUE WHERE event_id=?", e.ID); err != nil {
			return err
		}
	}
	return l.Store.Verify(ctx, r.SKU, Snapshot{Stock: 8, SourceVersion: 2, Projected: 8, Version: 2, Effects: 2, Orders: 2, Outbox: 2, Inbox: 2, Sent: 2})
}

// totals在全部case结束后独立查账，用业务预期常量而非把之前结果简单相加。
// DB-first缺一个下游效果、Kafka-first多一个孤儿效果，合计恰好相等不代表各case正确；
// 所以本函数只补总量守恒检查，前面的逐SKU不变量仍然不可省略。
func (l *Lab) totals(ctx context.Context) error {
	var orders, qty, stock, inbox, outbox, sent, projection, effects int64
	queries := []struct {
		sql  string
		dest []*int64
	}{
		{"SELECT COUNT(*),COALESCE(SUM(qty),0) FROM " + l.Store.table("orders"), []*int64{&orders, &qty}},
		{"SELECT SUM(qty) FROM " + l.Store.table("stock"), []*int64{&stock}},
		{"SELECT COUNT(*) FROM " + l.Store.table("inbox"), []*int64{&inbox}},
		{"SELECT COUNT(*),SUM(sent) FROM " + l.Store.table("outbox"), []*int64{&outbox, &sent}},
		{"SELECT SUM(qty),SUM(effects) FROM " + l.Store.table("projection"), []*int64{&projection, &effects}},
	}
	for _, q := range queries {
		args := make([]any, len(q.dest))
		for i, p := range q.dest {
			args[i] = p
		}
		if e := l.Store.DB.QueryRowContext(ctx, q.sql).Scan(args...); e != nil {
			return e
		}
	}
	if e := require(orders == 11 && qty == 11 && stock == 99 && inbox == 11 && outbox == 9 && sent == 9 && projection == 99 && effects == 11, "最终SQL总量 order=%d qty=%d stock=%d inbox=%d outbox=%d sent=%d projection=%d effects=%d", orders, qty, stock, inbox, outbox, sent, projection, effects); e != nil {
		return e
	}
	wants := []int{0, 1, 1, 1, 1, 2, 1, 1, 1, 1, 2}
	totalRecords := 0
	for i, name := range caseNames {
		es, e := l.Broker.Records(ctx, l.Config, l.RunID+"."+name)
		if e != nil {
			return e
		}
		if e = require(len(es) == wants[i], "最终broker topic=%s records=%d want=%d", name, len(es), wants[i]); e != nil {
			return e
		}
		totalRecords += len(es)
	}
	fmt.Fprintf(l.Out, "FINAL_AUDIT orders=%d ordered_qty=%d stock=%d inbox=%d outbox=%d sent=%d projection=%d effects=%d broker_records=%d\n", orders, qty, stock, inbox, outbox, sent, projection, effects, totalRecords)
	return require(totalRecords == 12, "最终broker应有12条包含1次重复重投")
}
