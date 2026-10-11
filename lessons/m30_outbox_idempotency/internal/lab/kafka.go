package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

const cleanupBudget = 10 * time.Second

var ErrCleanup = errors.New("资源清理/真实join失败")

// cleanupError给独立清理失败保留类别；预期发送/业务失败不得遮住它。
func cleanupError(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(ErrCleanup, err)
}

// Client拥有kgo创建的连接/fetch/group后台goroutine。业务ctx只约束一次操作，
// lifetime是owner的停止通知；done在真正驱动Close返回后关闭，不能和ACK混成一个事实。
type Client struct {
	K        *kgo.Client
	cancel   context.CancelFunc
	once     sync.Once
	done     chan struct{}
	closeErr error
}

// newClient 的caller必须立即defer Close。默认幂等producer/AllISRAcks沿用M29契约，
// 单ISR只是本broker确认；Kafka幂等producer不能覆盖SQL效果或跨新producer进程重复。
func newClient(cfg Config, opts ...kgo.Opt) (*Client, error) {
	life, cancel := context.WithCancel(context.Background())
	base := []kgo.Opt{kgo.SeedBrokers(cfg.KafkaAddress), kgo.WithContext(life), kgo.ClientID("m30-course"), kgo.DisableClientMetrics(),
		kgo.DialTimeout(2 * time.Second), kgo.RequestTimeoutOverhead(time.Second), kgo.RequestRetries(2), kgo.RetryTimeout(4 * time.Second),
		kgo.RecordDeliveryTimeout(4 * time.Second), kgo.ProduceRequestTimeout(2 * time.Second), kgo.RequiredAcks(kgo.AllISRAcks())}
	k, e := kgo.NewClient(append(base, opts...)...)
	if e != nil {
		cancel()
		return nil, e
	}
	return &Client{K: k, cancel: cancel, done: make(chan struct{})}, nil
}

// joinDone由所有goroutine owner调用；新Background预算不会沿用已超时的业务ctx。
// channel close→接收完成建立可见性，调用者仅在此后读取任务的err字段。
func joinDone(done <-chan struct{}, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
	defer cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return cleanupError(fmt.Errorf("%s未完成真实join: %w", name, ctx.Err()))
	}
}

// Close先LeaveGroup，再取消lifetime并CloseAllowingRebalance。once让重复保护defer
// 等同一份收尾；独立done区分“发停止通知”和“资源已回收”。错误不被原业务失败覆盖。
func (c *Client) Close() error {
	c.once.Do(func() {
		go func() {
			defer close(c.done)
			defer c.cancel()
			defer func() {
				if p := recover(); p != nil {
					c.closeErr = errors.Join(c.closeErr, fmt.Errorf("client Close panic: %v", p))
				}
			}()
			c.K.AllowRebalance()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			c.closeErr = c.K.LeaveGroupContext(ctx)
			cancel()
			c.cancel()
			c.K.CloseAllowingRebalance()
		}()
	})
	if e := joinDone(c.done, "Kafka client"); e != nil {
		return e
	}
	return cleanupError(c.closeErr)
}

// produce 由relay/坏顺序实验调用。Record指针交给驱动后不再改Key/Value。
// ProduceSync内部等promise，rough delivery timeout不等于严格任务寿命，所以owner
// 在ctx到期时实际关闭自己的client解除等待，之后仍以独立预算等任务done。
func produce(ctx context.Context, c *Client, topic string, e Event) (r *kgo.Record, err error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	r = &kgo.Record{Topic: topic, Key: []byte(e.SKU), Value: b}
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var workErr error
		defer func() {
			if p := recover(); p != nil {
				workErr = fmt.Errorf("ProduceSync任务panic: %v", p)
			}
			result <- workErr
		}()
		workErr = c.K.ProduceSync(ctx, r).FirstErr()
	}()
	defer func() {
		if err != nil {
			err = errors.Join(err, c.Close())
		}
		err = errors.Join(err, joinDone(done, "ProduceSync"))
	}()
	select {
	case err = <-result:
		return r, err
	case <-ctx.Done():
		return r, ctx.Err()
	}
}

// RelayOne是唯一relay的单步协议：读提交后的outbox→真实ACK→sent更新。
// afterACK在broker已经返回成功、SQL尚未标记时定位崩溃；它不模拟ACK。
// sent更新是另一事务，崩溃后Pending仍会读取原ID并重投，消费者必须用inbox。
func (s *Store) RelayOne(ctx context.Context, c *Client, topic, id string, afterACK func() error) (err error) {
	e, err := s.Pending(ctx, id)
	if err != nil {
		return err
	}
	r, err := produce(ctx, c, topic, e)
	if err != nil {
		return errors.Join(ErrUnknown, err)
	}
	fmt.Fprintf(s.Out, "ACK event=%s partition=%d offset=%d outbox_sent=false\n", e.ID, r.Partition, r.Offset)
	if afterACK != nil {
		if err = afterACK(); err != nil {
			return err
		}
	}
	result, err := s.DB.ExecContext(ctx, "UPDATE "+s.table("outbox")+" SET sent=TRUE WHERE event_id=? AND sent=FALSE", id)
	if err != nil {
		return errors.Join(ErrUnknown, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	return require(n == 1, "单relay标记sent必须影响1行")
}

// consumerOpts沿用M29关闭自动提交、Poll期间阻止rebalance的真实客户端契约。
// 本课每个group只有一个成员、一个partition、串行处理max1；无需推测并发水位。
func consumerOpts(topic, group string) []kgo.Opt {
	return []kgo.Opt{kgo.ConsumerGroup(group), kgo.ConsumeTopics(topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(), kgo.BlockRebalanceOnPoll(), kgo.Balancers(kgo.RangeBalancer()),
		kgo.SessionTimeout(6 * time.Second), kgo.HeartbeatInterval(time.Second), kgo.RebalanceTimeout(12 * time.Second), kgo.FetchMaxWait(100 * time.Millisecond)}
}

// pollOne是consumer真正的网络等待点。PollRecords可在空fetch后返回，所以按ctx
// 循环；每分区error都检查，不只用一个“函数无报错”日志假定已拿到有效业务事件。
func pollOne(ctx context.Context, c *Client) (*kgo.Record, Event, error) {
	for {
		f := c.K.PollRecords(ctx, 1)
		if es := f.Errors(); len(es) > 0 {
			return nil, Event{}, es[0].Err
		}
		if e := ctx.Err(); e != nil {
			return nil, Event{}, e
		}
		if rs := f.Records(); len(rs) > 0 {
			r := rs[0]
			var e Event
			if q := json.Unmarshal(r.Value, &e); q != nil {
				return nil, e, q
			}
			if string(r.Key) != e.SKU || r.Partition != 0 {
				return nil, e, errors.New("单分区/SKU key契约不符")
			}
			return r, e, nil
		}
		c.K.AllowRebalance()
	}
}

// ConsumeOne由子进程/parent恢复入口调用。before/inbox/commit门各位于实际SQL阶段；
// commitOffset=false只用于“业务提交但进度未确认”的明确故障窗口。
// 正常返回只有SQL Commit成功后才CommitRecords(r.Offset+1)，失败不得越过这条记录。
func (s *Store) ConsumeOne(ctx context.Context, c *Client, commitOffset, failEffect bool, afterInbox, afterCommit func() error) (e Event, r *kgo.Record, err error) {
	r, e, err = pollOne(ctx, c)
	if err != nil {
		return e, r, err
	}
	applied, err := s.Apply(ctx, e, failEffect, afterInbox, afterCommit)
	if err != nil {
		return e, r, err
	}
	fmt.Fprintf(s.Out, "BUSINESS_COMMIT event=%s applied=%t offset=%d\n", e.ID, applied, r.Offset)
	if commitOffset {
		if err = c.K.CommitRecords(ctx, r); err != nil {
			return e, r, errors.Join(ErrUnknown, err)
		}
	}
	c.K.AllowRebalance()
	return e, r, nil
}

// Broker是Run的admin owner，Topics/Groups保存创建/触及前登记的精确名字。
// 先确认不存在才允许Own；Cleanup不做前缀扫描或删除其它课程对象。
type Broker struct {
	Client         *Client
	Admin          *kadm.Client
	Topics, Groups []string
	Out            io.Writer
}

// openBroker核对metadata advertised真实地址；READY索引不是业务写读的替代证据。
func openBroker(ctx context.Context, cfg Config, out io.Writer) (b *Broker, err error) {
	c, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, c.Close())
		}
	}()
	b = &Broker{Client: c, Admin: kadm.NewClient(c.K), Out: out}
	m, err := b.Admin.BrokerMetadata(ctx)
	if err != nil {
		return nil, err
	}
	if len(m.Brokers) != 1 {
		return nil, errors.New("本课预期单broker")
	}
	for _, broker := range m.Brokers {
		if net.JoinHostPort(broker.Host, fmt.Sprint(broker.Port)) != cfg.KafkaAddress {
			return nil, errors.New("advertised与登记loopback不一致")
		}
	}
	return b, nil
}

// Topic在metadata确认不存在后登记到cleanup数组，再调用CREATE；随机碰撞绝不收编。
// 创建请求响应丢失仍可按已确认独占的精确名字清理，避免只登记成功返回而漏资源。
func (b *Broker) Topic(ctx context.Context, name string) error {
	m, e := b.Admin.Metadata(kadm.WithAuthorizedOps(ctx), name)
	if e != nil {
		return e
	}
	t, ok := m.Topics[name]
	if !ok || !errors.Is(t.Err, kerr.UnknownTopicOrPartition) {
		return errors.New("topic不为空或不存在证明不足")
	}
	b.Topics = append(b.Topics, name)
	rs, e := b.Admin.CreateTopics(ctx, 1, 1, map[string]*string{"retention.ms": kadm.StringPtr("600000"), "retention.bytes": kadm.StringPtr("1048576")}, name)
	if e != nil {
		return e
	}
	if e = rs.Error(); e != nil {
		return e
	}
	return observe(ctx, func() (bool, error) {
		// 锁定kadm1.19的WithAuthorizedOps使Metadata实际Request，避开缺失topic
		// 的缓存；创建/删除的证明都必须来自这一轮broker响应。
		m, e := b.Admin.Metadata(kadm.WithAuthorizedOps(ctx), name)
		if e != nil {
			return false, e
		}
		d, ok := m.Topics[name]
		p, has := d.Partitions[0]
		return ok && d.Err == nil && has && p.Err == nil && p.Leader >= 0, nil
	})
}

// Group只在创建前确认无活跃/已有对象，登记后可多次重启同group。
func (b *Broker) Group(ctx context.Context, name string) error {
	rs, e := b.Admin.DescribeGroups(ctx, name)
	if e != nil {
		return e
	}
	d, ok := rs[name]
	if !ok || !(errors.Is(d.Err, kerr.GroupIDNotFound) || (d.Err == nil && d.State == "Dead" && len(d.Members) == 0)) {
		return errors.New("group已经存在或不存在证明不足")
	}
	b.Groups = append(b.Groups, name)
	return nil
}

// Offset读取broker权威进度。不存在offset用-1表示；不是本地已poll位置。
func (b *Broker) Offset(ctx context.Context, topic, group string) (int64, error) {
	rs, e := b.Admin.FetchOffsets(ctx, group)
	if e != nil {
		return 0, e
	}
	o, ok := rs.Lookup(topic, 0)
	if !ok {
		return -1, nil
	}
	if o.Err != nil {
		return 0, o.Err
	}
	return o.At, nil
}

// VerifyOffset由实验写入预期常量。消费一条offset0后safe next必须为1，
// 处理失败/崩溃before commit时不能出现1；串行处理使安全前缀就是这一条成功记录。
func (b *Broker) VerifyOffset(ctx context.Context, topic, group string, want int64) error {
	n, e := b.Offset(ctx, topic, group)
	if e != nil {
		return e
	}
	fmt.Fprintf(b.Out, "BROKER_OFFSET group=%s next=%d\n", group, n)
	return require(n == want, "group=%s next=%d want=%d", group, n, want)
}

// Records用无group显式partition/offset读真实broker内容，不改变任何consumer进度。
// 先读end限制数量，避免等待“不存在的第二条”；断言配合具体event/key证明接受事实。
func (b *Broker) Records(ctx context.Context, cfg Config, topic string) (events []Event, err error) {
	ends, err := b.Admin.ListEndOffsets(kadm.WithAuthorizedOps(ctx), topic)
	if err != nil {
		return nil, err
	}
	o, ok := ends.Lookup(topic, 0)
	if !ok || o.Err != nil {
		return nil, errors.Join(o.Err, fmt.Errorf("缺少partition0 end: offsets=%+v", ends))
	}
	if o.Offset == 0 {
		return nil, nil
	}
	if o.Offset > 20 {
		return nil, errors.New("课程消息超出20条上限")
	}
	c, err := newClient(cfg, kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: {0: kgo.NewOffset().AtStart()}}))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	for int64(len(events)) < o.Offset {
		r, e, q := pollOne(ctx, c)
		if q != nil {
			return nil, q
		}
		if r.Offset != int64(len(events)) {
			return nil, errors.New("课程topic意外offset缺口")
		}
		events = append(events, e)
	}
	fmt.Fprintf(b.Out, "BROKER_READ topic=%s records=%d\n", topic, len(events))
	return
}

// Cleanup由Run最后调用；每项使用新预算，删除后实际协议读回，而非仅信删除返回。
// ticker只限查询频率，真实UnknownTopicOrPartition才代表逻辑删除结束。
func (b *Broker) Cleanup() (err error) {
	for _, group := range b.Groups {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
		// 强杀成员没有Leave响应，broker可能保留到session过期。先按实际Describe
		// 等无成员再删除，NON_EMPTY_GROUP不是“已清理”，不靠固定sleep盲删。
		e := observe(ctx, func() (bool, error) {
			ds, q := b.Admin.DescribeGroups(ctx, group)
			if q != nil {
				return false, q
			}
			d, ok := ds[group]
			if !ok {
				return false, errors.New("group describe缺少目标")
			}
			if errors.Is(d.Err, kerr.GroupIDNotFound) {
				return true, nil
			}
			if d.Err != nil {
				return false, d.Err
			}
			return len(d.Members) == 0, nil
		})
		var rs kadm.DeleteGroupResponses
		if e == nil {
			rs, e = b.Admin.DeleteGroups(ctx, group)
		}
		if e == nil {
			e = rs.Error()
		}
		if errors.Is(e, kerr.GroupIDNotFound) {
			e = nil
		}
		if e == nil {
			ds, q := b.Admin.DescribeGroups(ctx, group)
			e = q
			if e == nil {
				d, ok := ds[group]
				e = require(ok && (errors.Is(d.Err, kerr.GroupIDNotFound) || (d.Err == nil && d.State == "Dead" && len(d.Members) == 0)), "group清理后状态无效")
			}
		}
		cancel()
		err = errors.Join(err, e)
		fmt.Fprintf(b.Out, "CLEANUP_GROUP name=%s verified=%t\n", group, e == nil)
	}
	for _, topic := range b.Topics {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
		rs, e := b.Admin.DeleteTopics(ctx, topic)
		if e == nil {
			e = rs.Error()
		}
		if errors.Is(e, kerr.UnknownTopicOrPartition) {
			e = nil
		}
		if e == nil {
			e = observe(ctx, func() (bool, error) {
				m, q := b.Admin.Metadata(kadm.WithAuthorizedOps(ctx), topic)
				if q != nil {
					return false, q
				}
				d, ok := m.Topics[topic]
				if ok && errors.Is(d.Err, kerr.UnknownTopicOrPartition) {
					return true, nil
				}
				if ok && d.Err != nil {
					return false, d.Err
				}
				return false, nil
			})
		}
		cancel()
		err = errors.Join(err, e)
		fmt.Fprintf(b.Out, "CLEANUP_TOPIC name=%s verified=%t\n", topic, e == nil)
	}
	return errors.Join(err, b.Client.Close())
}

// observe在具体状态谓词和ctx之间等待；不会靠固定sleep声称server已完成。
func observe(ctx context.Context, predicate func() (bool, error)) error {
	t := time.NewTicker(30 * time.Millisecond)
	defer t.Stop()
	for {
		ok, e := predicate()
		if e != nil {
			return e
		}
		if ok {
			return nil
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
