package orderbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// KafkaClient拥有驱动client的后台连接/group任务。Done在Leave和Close真正返回后
// 才关闭；业务ACK/Poll结果均不是资源Done。关闭调用的工作goroutine也被独立join。
type KafkaClient struct {
	K      *kgo.Client
	cancel context.CancelFunc
	once   sync.Once
	done   chan struct{}
	err    error
}

// NewKafkaClient由Broker或App构造，使用单节点真实协议。所有client借同一端点，
// 不创建broker，不改全局设置。producer默认幂等/AllISRAcks只涉及Kafka，不能代替inbox。
func NewKafkaClient(cfg Config, clientID string, opts ...kgo.Opt) (*KafkaClient, error) {
	life, cancel := context.WithCancel(context.Background())
	base := []kgo.Opt{kgo.SeedBrokers(cfg.KafkaAddress), kgo.WithContext(life), kgo.ClientID(clientID), kgo.DisableClientMetrics(), kgo.DialTimeout(2 * time.Second), kgo.RequestTimeoutOverhead(time.Second), kgo.RequestRetries(2), kgo.RetryTimeout(4 * time.Second), kgo.RecordDeliveryTimeout(4 * time.Second), kgo.ProduceRequestTimeout(2 * time.Second), kgo.RequiredAcks(kgo.AllISRAcks())}
	k, e := kgo.NewClient(append(base, opts...)...)
	if e != nil {
		cancel()
		return nil, e
	}
	return &KafkaClient{K: k, cancel: cancel, done: make(chan struct{})}, nil
}

// JoinDone由owner用全新Background预算调用。channel关闭的同步关系使任务最终err
// 可被安全读取；ctx取消只是通知，不能把未关闭Done说成已退出。
func JoinDone(done <-chan struct{}, name string, budget time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return CleanupCause(fmt.Errorf("%s最终Done未到: %w", name, ctx.Err()))
	}
}

// Close先允许rebalance并Leave，再取消驱动lifetime并Close。once启动一个关闭任务，
// caller必须等最终done；重复调用等同一关闭结果，不重复创建任务/删除group。
func (c *KafkaClient) Close() error {
	c.once.Do(func() {
		go func() {
			defer close(c.done)
			defer func() {
				c.cancel()
				if p := recover(); p != nil {
					cause, ok := p.(error)
					if !ok {
						cause = fmt.Errorf("Kafka Close panic: %v", p)
					}
					c.err = errors.Join(c.err, cause)
				}
			}()
			c.K.AllowRebalance()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			c.err = c.K.LeaveGroupContext(ctx)
			cancel()
			c.cancel()
			c.K.CloseAllowingRebalance()
		}()
	})
	if e := JoinDone(c.done, "Kafka client", 10*time.Second); e != nil {
		return e
	}
	return CleanupCause(c.err)
}

// Broker借registry精确登记自己的topic/group，拥有一个admin兼producer client。
// App的consumer client是另一对象；Broker不会Close仍活跃的App。
type Broker struct {
	Client       *KafkaClient
	Admin        *kadm.Client
	Topic, Group string
	reg          *registry
	Out          io.Writer
}

// OpenBroker只核对metadata advertised地址，不把健康检查当业务验收。
func OpenBroker(ctx context.Context, c Config, namespace string, reg *registry, out io.Writer) (b *Broker, err error) {
	client, e := NewKafkaClient(c, namespace+"-producer")
	if e != nil {
		return nil, e
	}
	transferred := false
	defer func() {
		if !transferred {
			clean := client.Close()
			if p := recover(); p != nil {
				cause, ok := p.(error)
				if !ok {
					cause = fmt.Errorf("Broker构造panic: %v", p)
				}
				panic(errors.Join(cause, clean))
			}
			err = errors.Join(err, clean)
		}
	}()
	run := strings.TrimPrefix(namespace, "m37_")
	b = &Broker{Client: client, Admin: kadm.NewClient(client.K), Topic: "golearn.m37." + run, Group: "golearn.m37." + run + ".consumer", reg: reg, Out: out}
	m, e := b.Admin.BrokerMetadata(ctx)
	if e != nil {
		return nil, e
	}
	if len(m.Brokers) != 1 {
		return nil, errors.New("预期单broker")
	}
	for _, broker := range m.Brokers {
		if net.JoinHostPort(broker.Host, fmt.Sprint(broker.Port)) != c.KafkaAddress {
			return nil, errors.New("advertised地址与索引不符")
		}
	}
	transferred = true
	return b, nil
}

// Observe用状态谓词确定完成，ticker只限制查询频率；没有Sleep猜服务已就绪。
func Observe(ctx context.Context, predicate func() (bool, error)) error {
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
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Provision先直接metadata/Describe证明精确名字不存在，再登记attempt再发CREATE。
// WithAuthorizedOps在kadm1.19触发实际请求，避免旧“topic不存在”缓存误当新状态。
func (b *Broker) Provision(ctx context.Context) error {
	m, e := b.Admin.Metadata(kadm.WithAuthorizedOps(ctx), b.Topic)
	if e != nil {
		return e
	}
	d, ok := m.Topics[b.Topic]
	if !ok || !errors.Is(d.Err, kerr.UnknownTopicOrPartition) {
		return errors.New("topic存在或不存在证明不足")
	}
	groups, e := b.Admin.DescribeGroups(ctx, b.Group)
	if e != nil {
		return e
	}
	g, ok := groups[b.Group]
	if !ok || !(errors.Is(g.Err, kerr.GroupIDNotFound) || (g.Err == nil && g.State == "Dead" && len(g.Members) == 0)) {
		return errors.New("group存在或不存在证明不足")
	}
	if e = b.reg.add("topic", b.Topic); e != nil {
		return e
	}
	rs, e := b.Admin.CreateTopics(ctx, 1, 1, map[string]*string{"retention.ms": kadm.StringPtr("600000"), "retention.bytes": kadm.StringPtr("1048576")}, b.Topic)
	if e != nil {
		return e
	}
	if e = rs.Error(); e != nil {
		return e
	}
	if e = b.reg.add("group", b.Group); e != nil {
		return e
	}
	return Observe(ctx, func() (bool, error) {
		m, e := b.Admin.Metadata(kadm.WithAuthorizedOps(ctx), b.Topic)
		if e != nil {
			return false, e
		}
		d, ok := m.Topics[b.Topic]
		p, has := d.Partitions[0]
		return ok && d.Err == nil && has && p.Err == nil && p.Leader >= 0, nil
	})
}

// ConsumerOptions固定单group成员/单partition/串行max1。禁止自动commit；每条记录
// 的效果成功且缓存失效成功后，调用CommitRecords确认安全前缀，随后允许rebalance。
func ConsumerOptions(topic, group string) []kgo.Opt {
	return []kgo.Opt{kgo.ConsumerGroup(group), kgo.ConsumeTopics(topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()), kgo.DisableAutoCommit(), kgo.BlockRebalanceOnPoll(), kgo.Balancers(kgo.RangeBalancer()), kgo.SessionTimeout(6 * time.Second), kgo.HeartbeatInterval(time.Second), kgo.RebalanceTimeout(12 * time.Second), kgo.FetchMaxWait(100 * time.Millisecond)}
}

// Publish由relay/显式重复事件实验调用。Record指针一旦给驱动便不再修改Key/Value；
// ProduceSync等待broker ACK，返回partition/offset才记录接受事实。error保持UNKNOWN。
func (b *Broker) Publish(ctx context.Context, event Event) (*kgo.Record, error) {
	payload, e := json.Marshal(event)
	if e != nil {
		return nil, e
	}
	r := &kgo.Record{Topic: b.Topic, Key: []byte(event.Order.ID), Value: payload}
	if e = b.Client.K.ProduceSync(ctx, r).FirstErr(); e != nil {
		return r, errors.Join(ErrUnknown, e)
	}
	fmt.Fprintf(b.Out, "KAFKA_ACK event=%s partition=%d offset=%d\n", event.EventID, r.Partition, r.Offset)
	return r, nil
}

// RelayOne将已提交outbox发布，然后独立标sent。ACK与sent之间有真实重复窗口；
// hook失败会保留sent=false，但Kafka消息已存在，恢复仍发布同一event_id/payload。
func (b *Broker) RelayOne(ctx context.Context, s *Store, h Hooks) (bool, error) {
	event, found, e := s.Pending(ctx)
	if e != nil || !found {
		return false, e
	}
	if _, e = b.Publish(ctx, event); e != nil {
		return false, e
	}
	if h.AfterRelayACK != nil {
		if e = h.AfterRelayACK(ctx, event); e != nil {
			// 已拿到真实broker ACK；hook取消/错误不能被App当成纯停机取消吞掉。
			// sent尚未确认，恢复仍可重投；保留UNKNOWN和原cause供owner对账。
			return false, errors.Join(ErrUnknown, e)
		}
	}
	if e = s.MarkSent(ctx, event); e != nil {
		return false, e
	}
	fmt.Fprintf(b.Out, "OUTBOX_SENT event=%s\n", event.EventID)
	return true, nil
}

// ProcessRecord是唯一consumer业务入口：验证真实record→Apply Tx→失效→offset。
// SQL效果已经提交后，hook/失效/offset失败都不能回滚效果；同group重投依靠inbox。
// 每次重复仍失效，避免上次SQL成功/Redis失败后永远保留PENDING缓存。
func (b *Broker) ProcessRecord(ctx context.Context, c *KafkaClient, s *Store, cache *Cache, record *kgo.Record, h Hooks) error {
	var event Event
	if e := json.Unmarshal(record.Value, &event); e != nil {
		return e
	}
	if record.Topic != b.Topic || record.Partition != 0 || string(record.Key) != event.Order.ID {
		return ErrConflict
	}
	applied, e := s.Apply(ctx, event, record.Topic, record.Partition, record.Offset)
	if e != nil {
		return e
	}
	fmt.Fprintf(b.Out, "CONSUMER_DB_COMMIT event=%s offset=%d applied=%t\n", event.EventID, record.Offset, applied)
	if h.AfterConsumeCommit != nil {
		if e = h.AfterConsumeCommit(ctx, event); e != nil {
			return errors.Join(ErrUnknown, e)
		}
	}
	if e = cache.Invalidate(ctx, event.Order.Owner, event.Order.ID); e != nil {
		return errors.Join(ErrUnknown, e)
	}
	fmt.Fprintf(b.Out, "CACHE_INVALIDATED event=%s offset=%d\n", event.EventID, record.Offset)
	if e = c.K.CommitRecords(ctx, record); e != nil {
		return errors.Join(ErrUnknown, e)
	}
	fmt.Fprintf(b.Out, "OFFSET_COMMIT event=%s next=%d\n", event.EventID, record.Offset+1)
	c.K.AllowRebalance()
	return nil
}

// Offset读取broker持久group进度；未提交位置是-1，绝不是consumer本地poll位置。
func (b *Broker) Offset(ctx context.Context) (int64, error) {
	r, e := b.Admin.FetchOffsets(ctx, b.Group)
	if e != nil {
		return 0, e
	}
	o, ok := r.Lookup(b.Topic, 0)
	if !ok {
		return -1, nil
	}
	return o.At, o.Err
}

// Records为独立断言创建无group显式partition reader，先读end确定有限消息数量。
// 读取不会推进业务group。owner从创建即defer Close，不在断言失败后漏驱动任务。
func (b *Broker) Records(ctx context.Context, cfg Config) (records []*kgo.Record, err error) {
	ends, e := b.Admin.ListEndOffsets(kadm.WithAuthorizedOps(ctx), b.Topic)
	if e != nil {
		return nil, e
	}
	o, ok := ends.Lookup(b.Topic, 0)
	if !ok || o.Err != nil {
		return nil, errors.Join(o.Err, errors.New("缺partition0 end"))
	}
	if o.Offset > 8 {
		return nil, errors.New("超出课程8条上限")
	}
	if o.Offset == 0 {
		return nil, nil
	}
	c, e := NewKafkaClient(cfg, b.reg.data.Namespace+"-audit", kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{b.Topic: {0: kgo.NewOffset().AtStart()}}))
	if e != nil {
		return nil, e
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	for int64(len(records)) < o.Offset {
		f := c.K.PollRecords(ctx, 1)
		if es := f.Errors(); len(es) > 0 {
			return nil, es[0].Err
		}
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		for _, r := range f.Records() {
			if r.Offset != int64(len(records)) {
				return nil, errors.New("audit offset不是连续0开始")
			}
			records = append(records, r)
		}
	}
	return records, nil
}

// Cleanup只删除精确attempt账本，不Broad扫描。先确认group无成员，避免删活跃任务。
// 删除响应后再次读broker状态与offset，topic逻辑不存在由直接metadata证明。
func (b *Broker) Cleanup(ctx context.Context) (err error) {
	d := b.reg.snapshot()
	return b.cleanupManifest(ctx, d)
}

// cleanupManifest由原owner的Resources清理入口调用，允许已关闭原client借fresh客户端
// 执行同一精确账本；不会更改fresh registry或扩大名称作用域。
func (b *Broker) cleanupManifest(ctx context.Context, d Manifest) (err error) {
	if e := d.Validate(); e != nil {
		return e
	}
	if d.Namespace != b.reg.snapshot().Namespace {
		return ErrInvalid
	}
	for _, group := range d.Groups {
		e := Observe(ctx, func() (bool, error) {
			rs, e := b.Admin.DescribeGroups(ctx, group)
			if e != nil {
				return false, e
			}
			g, ok := rs[group]
			if !ok {
				return false, errors.New("Describe缺精确group")
			}
			if errors.Is(g.Err, kerr.GroupIDNotFound) {
				return true, nil
			}
			if g.Err != nil {
				return false, g.Err
			}
			return len(g.Members) == 0, nil
		})
		if e == nil {
			rs, q := b.Admin.DeleteGroups(ctx, group)
			e = q
			if e == nil {
				e = rs.Error()
			}
			if errors.Is(e, kerr.GroupIDNotFound) {
				e = nil
			}
		}
		if e == nil {
			rs, q := b.Admin.DescribeGroups(ctx, group)
			e = q
			g, ok := rs[group]
			if e == nil {
				e = Assert(ok && (errors.Is(g.Err, kerr.GroupIDNotFound) || (g.Err == nil && g.State == "Dead" && len(g.Members) == 0)), "删除group后不存在")
			}
		}
		if e == nil {
			offsets, q := b.Admin.FetchOffsets(ctx, group)
			e = q
			if e == nil {
				e = Assert(len(offsets) == 0, "删除group后offset为空")
			}
		}
		fmt.Fprintf(b.Out, "CLEANUP_GROUP name=%s verified=%t\n", group, e == nil)
		err = errors.Join(err, e)
	}
	for _, topic := range d.Topics {
		rs, e := b.Admin.DeleteTopics(ctx, topic)
		if e == nil {
			e = rs.Error()
		}
		if errors.Is(e, kerr.UnknownTopicOrPartition) {
			e = nil
		}
		if e == nil {
			e = Observe(ctx, func() (bool, error) {
				m, e := b.Admin.Metadata(kadm.WithAuthorizedOps(ctx), topic)
				if e != nil {
					return false, e
				}
				d, ok := m.Topics[topic]
				return ok && errors.Is(d.Err, kerr.UnknownTopicOrPartition), nil
			})
		}
		fmt.Fprintf(b.Out, "CLEANUP_TOPIC name=%s verified=%t\n", topic, e == nil)
		err = errors.Join(err, e)
	}
	return err
}
