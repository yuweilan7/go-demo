package course

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

const cleanupBudget = 8 * time.Second

// ClientOwner 把驱动后台 goroutine 的生命周期交给明确 owner；业务 ctx 只约束请求。
// WithContext 使用本对象的 lifetime，防止总预算到期立即毁掉用于最终 Leave 的连接。
// once/done 让重复 Close 借用同一次收尾，既不并发关闭，也不会把 cancel 当 join。
type ClientOwner struct {
	Client   *kgo.Client
	cancel   context.CancelFunc
	once     sync.Once
	done     chan struct{}
	closeErr error
	trace    *Trace
	id       string
}

// NewClient 由实验同步调用。opts 是本次额外选项 slice；基础 opts 与 opts 组合后，
// kgo.NewClient 才创建连接管理任务。失败时立即取消 lifetime，不交出半初始化资源。
func NewClient(cfg Config, trace *Trace, id string, opts ...kgo.Opt) (*ClientOwner, error) {
	lifetime, cancel := context.WithCancel(context.Background())
	base := []kgo.Opt{
		kgo.SeedBrokers(cfg.Address), kgo.ClientID(id), kgo.WithContext(lifetime),
		kgo.DialTimeout(2 * time.Second), kgo.RequestTimeoutOverhead(2 * time.Second),
		kgo.RetryTimeout(5 * time.Second), kgo.RequestRetries(3), kgo.RecordDeliveryTimeout(5 * time.Second),
		kgo.ProduceRequestTimeout(3 * time.Second),
		// AllISRAcks 等待 ISR 确认；单节点只有一个副本，不等于多节点容灾。
		// 不关闭默认幂等 producer；它阻止 Kafka 写重试重复，不代替业务幂等。
		kgo.RequiredAcks(kgo.AllISRAcks()), kgo.DisableClientMetrics(),
	}
	switch cfg.Mechanism {
	case "plain":
		base = append(base, kgo.SASL(plain.Auth{User: cfg.Username, Pass: cfg.Password}.AsMechanism()))
	case "scram-sha-256":
		base = append(base, kgo.SASL(scram.Auth{User: cfg.Username, Pass: cfg.Password}.AsSha256Mechanism()))
	case "scram-sha-512":
		base = append(base, kgo.SASL(scram.Auth{User: cfg.Username, Pass: cfg.Password}.AsSha512Mechanism()))
	}
	cl, err := kgo.NewClient(append(base, opts...)...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("创建 Kafka client %s: %w", id, err)
	}
	return &ClientOwner{Client: cl, cancel: cancel, done: make(chan struct{}), trace: trace, id: id}, nil
}

// Close 由最外层借用者在 worker 已停止后调用。先允许 rebalance，用独立有限预算
// 发送 LeaveGroup；随后取消 lifetime 解除驱动等待，再调用 Close 真正回收 goroutine。
// closeErr 在 done 关闭前写入，channel close→receive 给读取方提供内存可见性。
func (o *ClientOwner) Close() error {
	o.once.Do(func() {
		go func() {
			defer close(o.done) // 最后一个 defer：不是把结果送出就宣称资源已结束。
			defer o.cancel()    // 即使Close实现发生panic，也要通知驱动停止，不能漏掉取消。
			defer func() {
				if p := recover(); p != nil {
					o.closeErr = errors.Join(o.closeErr, fmt.Errorf("client关闭panic，资源状态未完全确认: %v", p))
				}
			}()
			o.Client.AllowRebalance()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			o.closeErr = o.Client.LeaveGroupContext(ctx)
			cancel()
			o.cancel()
			o.Client.CloseAllowingRebalance()
			o.trace.Say("CLIENT_DONE id=%s leave_error=%t", o.id, o.closeErr != nil)
		}()
	})
	if err := joinDone(o.done, "client "+o.id); err != nil {
		return err
	}
	return o.closeErr
}

// Harness 只由 Run 顺序操作。Topics 保存 CreateTopics 真正成功的精确名字；
// Groups 只保存本 run 真正启动consumer时登记的名字。清理不扫描/删除前缀下所有对象。
type Harness struct {
	Config         Config
	Admin          *kadm.Client
	Owner          *ClientOwner
	Trace          *Trace
	RunID          string
	Topics, Groups []string
}

// NewHarness 核验 metadata 的 advertised 地址，比只测 bootstrap TCP 更接近真实协议。
// 如果返回地址与唯一登记的 loopback 隧道不符，在生产消息之前失败并回收 client。
func NewHarness(ctx context.Context, cfg Config, trace *Trace, runID string) (h *Harness, err error) {
	o, err := NewClient(cfg, trace, runID+".admin")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			combineCleanup(&err, o.Close())
		}
	}()
	a := kadm.NewClient(o.Client)
	m, err := a.BrokerMetadata(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取 Kafka metadata: %w", err)
	}
	if err = require(len(m.Brokers) == 1, "单节点课程预期一个 broker，实际=%d", len(m.Brokers)); err != nil {
		return nil, err
	}
	for _, b := range m.Brokers {
		address := net.JoinHostPort(b.Host, fmt.Sprint(b.Port))
		trace.Say("METADATA broker_id=%d advertised=%s registered_version=%s", b.NodeID, address, cfg.ServerVersion)
		if err = require(address == cfg.Address, "advertised=%s 与索引=%s 不一致", address, cfg.Address); err != nil {
			return nil, err
		}
	}
	return &Harness{Config: cfg, Admin: a, Owner: o, Trace: trace, RunID: runID}, nil
}

// Topic 建立一个实验专属 topic；保留时间十分钟、每分区1MiB、小消息与单副本。
// 若创建失败不加入清理列表，尤其 TopicAlreadyExists 时不能删除碰巧同名的他人对象。
func (h *Harness) Topic(ctx context.Context, suffix string, partitions int32) (string, error) {
	topic := h.RunID + "." + suffix
	responses, err := h.Admin.CreateTopics(ctx, partitions, 1, map[string]*string{
		"retention.ms": kadm.StringPtr("600000"), "retention.bytes": kadm.StringPtr("1048576"),
	}, topic)
	if err != nil {
		return "", fmt.Errorf("建立专属 topic: %w", err)
	}
	if err = responses.Error(); err != nil {
		return "", err
	}
	h.Topics = append(h.Topics, topic)
	h.Trace.Say("TOPIC_CREATED topic=%s partitions=%d", topic, partitions)
	return topic, nil
}

// Group 产生本 run 的精确名字，重启消费者传回同一名字才会读同一份 broker offset。
func (h *Harness) Group(suffix string) string {
	return h.RunID + "." + suffix
}

// UseGroup 由consumer创建者在启动poll之前调用。Group仅构造名字，不创建broker对象；
// producer阶段早退不能把一个从未使用的group当已创建资源删除。重建同group只登记一次。
// Harness由Run顺序操作，因此这个slice没有并发写，不需要另设Mutex。
func (h *Harness) UseGroup(group string) {
	if !slices.Contains(h.Groups, group) {
		h.Groups = append(h.Groups, group)
	}
}

// Cleanup 由 Run 的 defer 调用，此时所有 consumer 已经离组并 Done。
// 每项网络清理使用独立预算，聚合 group/topic/client 错误，不能用一项成功覆盖另一项。
func (h *Harness) Cleanup() (err error) {
	for _, g := range h.Groups {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
		rs, e := h.Admin.DeleteGroups(ctx, g)
		cancel()
		if e == nil {
			e = rs.Error()
		}
		// 没有 poll 成功的早退可能根本未建立 group；不存在就是已经没有该资源。
		if errors.Is(e, kerr.GroupIDNotFound) {
			e = nil
		}
		combineCleanup(&err, e)
		h.Trace.Say("GROUP_CLEANUP group=%s error=%t", g, e != nil)
		if e == nil {
			combineCleanup(&err, h.VerifyGroupGone(g))
		}
	}
	for _, topic := range h.Topics {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
		rs, e := h.Admin.DeleteTopics(ctx, topic)
		cancel()
		if e == nil {
			e = rs.Error()
		}
		combineCleanup(&err, e)
		h.Trace.Say("TOPIC_CLEANUP topic=%s broker_delete_accepted=%t", topic, e == nil)
		if e == nil {
			combineCleanup(&err, h.VerifyTopicGone(topic))
		}
	}
	combineCleanup(&err, h.Owner.Close())
	return
}

// VerifyGroupGone 在DeleteGroup成功后读取真实broker group状态；不只信本地client已Close。
// GROUP_ID_NOT_FOUND或Dead且无成员表示此精确group无存活owner；其它协议错误保留失败。
func (h *Harness) VerifyGroupGone(group string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
	defer cancel()
	rs, err := h.Admin.DescribeGroups(ctx, group)
	if err != nil {
		return err
	}
	d, ok := rs[group]
	if err = require(ok && (errors.Is(d.Err, kerr.GroupIDNotFound) || (d.Err == nil && d.State == "Dead" && len(d.Members) == 0)), "删除后group应不存在或Dead，group=%s state=%s members=%d err=%v", group, d.State, len(d.Members), d.Err); err != nil {
		return err
	}
	h.Trace.Say("GROUP_GONE_VERIFIED group=%s state=%s", group, d.State)
	return nil
}

// VerifyTopicGone 只读当前精确topic的metadata，循环观察broker异步删除的实际状态。
// 锁定版kmsg.MetadataRequest默认AllowAutoTopicCreation=false，kadm不会把被删topic重建。
// ticker仅限制查询频率；UnknownTopicOrPartition才是退出条件，不靠固定sleep假定删除已结束。
func (h *Harness) VerifyTopicGone(topic string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		m, err := h.Admin.Metadata(ctx, topic)
		if err != nil {
			return err
		}
		d, ok := m.Topics[topic]
		if ok && errors.Is(d.Err, kerr.UnknownTopicOrPartition) {
			h.Trace.Say("TOPIC_GONE_VERIFIED topic=%s", topic)
			return nil
		}
		if ok && d.Err != nil {
			return d.Err
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("等待topic实际删除: %w", ctx.Err())
		}
	}
}

// VerifyMembers 由Rebalance在稳定分配后调用，以broker DescribeGroups独立核验成员和分区。
// 客户端callback真实来自组协议，但再读broker可避免把本地snapshot误当稳定组全貌。
func (h *Harness) VerifyMembers(ctx context.Context, topic, group string, count int) error {
	ds, err := h.Admin.DescribeGroups(ctx, group)
	if err != nil {
		return err
	}
	d, ok := ds[group]
	if err = require(ok && d.Err == nil && d.State == "Stable" && len(d.Members) == count, "broker group=%s state=%s members=%d want=%d err=%v", group, d.State, len(d.Members), count, d.Err); err != nil {
		return err
	}
	seen := make(map[int32]bool)
	for _, member := range d.Members {
		// kadm包装的是协议解析后interface动态值；AsConsumer返回typed pointer和bool。
		// bool=false不能当成“分区空”，而是协议类型不符，直接交错误给实验owner。
		assignment, ok := member.Assigned.AsConsumer()
		if !ok {
			return fmt.Errorf("group assignment不是consumer协议")
		}
		for _, t := range assignment.Topics {
			if t.Topic == topic {
				for _, p := range t.Partitions {
					if seen[p] {
						return fmt.Errorf("broker分配partition%d重复", p)
					}
					seen[p] = true
					h.Trace.Say("BROKER_ASSIGNMENT client_id=%s partition=%d", member.ClientID, p)
				}
			}
		}
	}
	if err = require(len(seen) == 2, "broker应精确分配本topic的2分区"); err != nil {
		return err
	}
	h.Trace.Say("BROKER_GROUP_VERIFIED state=%s members=%d partitions=%d", d.State, len(d.Members), len(seen))
	return nil
}

// Produce 由实验调用；events、partitions 长度必须一致。Record 的 *指针交给驱动后
// 不再修改 key/value；ProduceSync 内部仍异步发送，但本调用同步等待所有 broker 结果。
func (h *Harness) Produce(ctx context.Context, topic string, events []Event, partitions []int32) (records []*kgo.Record, err error) {
	o, err := NewClient(h.Config, h.Trace, h.RunID+".producer", kgo.RecordPartitioner(kgo.ManualPartitioner()))
	if err != nil {
		return nil, err
	}
	defer func() { combineCleanup(&err, o.Close()) }()
	if len(events) != len(partitions) {
		return nil, fmt.Errorf("生产数据与 partition 数量不一致")
	}
	for i, e := range events {
		value, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		records = append(records, &kgo.Record{Topic: topic, Partition: partitions[i], Key: []byte(e.Order), Value: value})
	}
	if err = ProduceAndWait(ctx, o, records...); err != nil {
		return nil, fmt.Errorf("broker ack 失败: %w", err)
	}
	for _, r := range records {
		h.Trace.Say("PRODUCE_ACK topic=%s key=%s value=%s p=%d offset=%d", r.Topic, r.Key, r.Value, r.Partition, r.Offset)
	}
	return records, nil
}

// ProduceAndWait 由两种producer实验调用，拥有执行ProduceSync的辅助goroutine。
// 本版幂等producer的RecordDeliveryTimeout只是rough timeout，不保证ctx取消立即完成。
// 所以owner在ctx到期时取消client lifetime并Close，解除内部网络/缓冲等待，再独立join。
// result缓冲1防止调用者早退堵住任务；done关闭在result发送之后，不能只读result就返回。
func ProduceAndWait(ctx context.Context, o *ClientOwner, records ...*kgo.Record) (err error) {
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var produceErr error
		defer func() {
			if p := recover(); p != nil {
				produceErr = fmt.Errorf("producer任务panic: %v", p)
			}
			result <- produceErr
		}()
		produceErr = o.Client.ProduceSync(ctx, records...).FirstErr()
	}()
	defer func() {
		if err != nil {
			// cancel只通知驱动；Close使缓冲记录收到失败promise，随后仍等本任务done。
			o.cancel()
			combineCleanup(&err, o.Close())
		}
		combineCleanup(&err, joinDone(done, "ProduceSync task"))
	}()
	select {
	case err = <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ConsumerOptions 统一手动提交与有界 poll 的组配置；不配置静态 InstanceID，Close 会离组。
// RangeBalancer 是 eager 分配，退出/加入都 revoke 再 assign，方便直接观察完整 owner 变化。
func ConsumerOptions(topic, group string) []kgo.Opt {
	return []kgo.Opt{kgo.ConsumerGroup(group), kgo.ConsumeTopics(topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(), kgo.BlockRebalanceOnPoll(), kgo.Balancers(kgo.RangeBalancer()),
		kgo.SessionTimeout(6 * time.Second), kgo.HeartbeatInterval(time.Second), kgo.RebalanceTimeout(15 * time.Second),
		kgo.FetchMaxWait(200 * time.Millisecond), kgo.FetchMaxBytes(1 << 20)}
}

// WithConsumer 管理一次消费者生命期。fn 是同步借用回调，返回前已结束自己的 worker。
// 所有错误/故障分支都执行 defer Close，之后再以同 group 新建 client 才观察恢复。
func (h *Harness) WithConsumer(ctx context.Context, topic, group string, fn func(*kgo.Client) error) (err error) {
	// 业务预算已过期就不要新启动client任务；fn仍由调用者闭包借用同一个ctx。
	// 资源的独立关闭预算在ClientOwner，不沿用这里的过期ctx。
	if err = ctx.Err(); err != nil {
		return err
	}
	o, err := NewClient(h.Config, h.Trace, h.RunID+".once", ConsumerOptions(topic, group)...)
	if err != nil {
		return err
	}
	defer func() { combineCleanup(&err, o.Close()) }()
	h.UseGroup(group)
	return fn(o.Client)
}

// Poll 是真正网络消费等待点；max 限制一次返回数量，ctx 限制空分区/故障等待。
// 不能只看 fetches.Err：分区错误必须逐一检查；成功时每条 value 都解码并打印。
func Poll(ctx context.Context, cl *kgo.Client, max int, trace *Trace) ([]*kgo.Record, error) {
	fetches := cl.PollRecords(ctx, max)
	if es := fetches.Errors(); len(es) > 0 {
		return nil, fmt.Errorf("poll partition=%d: %w", es[0].Partition, es[0].Err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records := fetches.Records()
	for _, r := range records {
		if _, err := Decode(r); err != nil {
			return nil, err
		}
		trace.Say("FETCH topic=%s key=%s value=%s p=%d offset=%d", r.Topic, r.Key, r.Value, r.Partition, r.Offset)
	}
	return records, nil
}

// Decode 从驱动 record 指针读取合成业务值，返回 Event 值副本。Key/Value 是 []byte
// 共享底层数组，读完不修改；只有 event_id、order_id/key 一致才允许业务副作用。
func Decode(r *kgo.Record) (Event, error) {
	var e Event
	if err := json.Unmarshal(r.Value, &e); err != nil {
		return e, fmt.Errorf("offset=%d value JSON 无效", r.Offset)
	}
	if e.ID == "" || e.Order == "" || e.Order != string(r.Key) {
		return e, fmt.Errorf("消息业务契约无效 p=%d offset=%d", r.Partition, r.Offset)
	}
	return e, nil
}

// ApplyRecord 将 Kafka 元信息和值转换为文件业务效果；Scope/Owner 来自当前实验，
// 稳定 event_id 来自原始消息，故重新消费不会制造一个新业务唯一键。
func ApplyRecord(ctx context.Context, l *Ledger, r *kgo.Record, scope, owner string, dedupe bool) error {
	e, err := Decode(r)
	if err != nil {
		return err
	}
	return l.Apply(ctx, Effect{Scope: scope, Owner: owner, EventID: e.ID, Topic: r.Topic, Partition: r.Partition, Offset: r.Offset}, dedupe)
}

// Commit 由处理 owner 同步调用。CommitRecords 将 r.Offset+1 发给 broker，并检查每分区
// 响应错误；返回 nil 后再以独立 admin FetchOffsets 读取 broker 事实。
func (h *Harness) Commit(ctx context.Context, cl *kgo.Client, group string, r *kgo.Record) error {
	if err := cl.CommitRecords(ctx, r); err != nil {
		return fmt.Errorf("offset 提交失败: %w", err)
	}
	rs, err := h.Admin.FetchOffsets(ctx, group)
	if err != nil {
		return err
	}
	o, ok := rs.Lookup(r.Topic, r.Partition)
	if err = require(ok && o.Err == nil && o.At == r.Offset+1, "broker offset group=%s p=%d want=%d got=%d exists=%t", group, r.Partition, r.Offset+1, o.At, ok); err != nil {
		return err
	}
	h.Trace.Say("COMMIT_VERIFIED group=%s p=%d next=%d", group, r.Partition, o.At)
	return nil
}
