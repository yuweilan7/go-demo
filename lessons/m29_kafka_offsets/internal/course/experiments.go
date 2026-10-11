package course

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/twmb/franz-go/pkg/kgo"
)

var errInjected = errors.New("实验注入：业务失败或消费者在业务/offset 窗口退出")

// Run 由 CLI 或真实集成测试调用；它是 admin、业务文件以及所有子实验的 owner。
// 具名 err 允许 defer 聚合清理错误。后注册的 defer 先执行：先关闭 Ledger，
// 再删除精确 topics/groups，最后关闭 admin；各子实验已先等待自己 consumer Done。
func Run(ctx context.Context, mode, index, control string, out io.Writer) (err error) {
	trace := &Trace{out: out}
	trace.Say("M29 runtime=%s mode=%s", runtime.Version(), mode)
	if mode == "offline" {
		w := NewWatermark(0)
		n, a := w.Complete(1)
		trace.Say("OFFLINE complete=1 safe_next=%d advanced=%t", n, a)
		if err = require(n == 0 && !a, "较大 offset 不能跨过缺口"); err != nil {
			return err
		}
		n, a = w.Complete(0)
		trace.Say("OFFLINE complete=0 safe_next=%d advanced=%t", n, a)
		return require(n == 2 && a, "连续完成后才能前进到2；本模式未连接 Kafka")
	}
	if mode != "core" && mode != "broker-fault" {
		return fmt.Errorf("未知 mode")
	}
	cfg, err := ReadConfig(index)
	if err != nil {
		return err
	}
	var nonce [8]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	runID := "golearn.m29.r" + hex.EncodeToString(nonce[:]) // 64位随机后缀；每次运行 topic/group 独占。
	trace.Say("RUN_NAMESPACE=%s endpoint=%s registered_server=%s", runID, cfg.Address, cfg.ServerVersion)
	h, err := NewHarness(ctx, cfg, trace, runID)
	if err != nil {
		return err
	}
	defer func() { combineCleanup(&err, h.Cleanup()) }()
	if err = os.MkdirAll(".local", 0700); err != nil {
		return err
	}
	l, err := OpenLedger(".local/"+runID+".business.jsonl", trace)
	if err != nil {
		return err
	}
	defer func() { combineCleanup(&err, l.Close()) }()
	if mode == "broker-fault" {
		return BrokerFault(ctx, h, l, control)
	}
	if err = KeyOrder(ctx, h, l); err != nil {
		return err
	}
	if err = OffsetWindows(ctx, h, l); err != nil {
		return err
	}
	if err = Rebalance(ctx, h, l); err != nil {
		return err
	}
	trace.Say("CORE_ASSERTIONS_PASS（所有业务实验已返回；随后执行文件/topic/group/client 清理）")
	return nil
}

// ReadN 在当前 consumer 所有权窗口内累计恰好 n 条。slice 元素是驱动 record 指针，
// 只读且不复制底层 key/value；PollRecords 不会返回超过 remaining 的记录。
// 调用者处理完后必须 AllowRebalance，故本函数不提前释放 owner 的处理窗口。
func ReadN(ctx context.Context, cl *kgo.Client, n int, trace *Trace) ([]*kgo.Record, error) {
	var all []*kgo.Record
	for len(all) < n {
		rs, err := Poll(ctx, cl, n-len(all), trace)
		if err != nil {
			return nil, err
		}
		all = append(all, rs...)
	}
	return all, nil
}

// KeyOrder 由 Run 首先执行：真实 key 分区 producer→broker ack→组消费读回→业务文件。
// 三个 order 各两步。默认 StickyKeyPartitioner 的 key hash 让同 key 落同 partition，
// 但 topic 增加分区/换 partitioner 会改变映射，key 本身不是跨集群顺序锁。
func KeyOrder(ctx context.Context, h *Harness, l *Ledger) (err error) {
	h.Trace.Say("E1 key/partition/order：开始")
	topic, err := h.Topic(ctx, "keyed", 2)
	if err != nil {
		return err
	}
	group := h.Group("keyed.consumer")
	partitioner := kgo.StickyKeyPartitioner(nil)
	o, err := NewClient(h.Config, h.Trace, h.RunID+".keyproducer", kgo.RecordPartitioner(partitioner))
	if err != nil {
		return err
	}
	defer func() { combineCleanup(&err, o.Close()) }()
	// 不同 key 仍可能散列到同一分区。fixture用锁定驱动的公开partitioner API
	// 选择两个分区各一个合成key，再加第三个订单；它不是broker分配的替代证据。
	// 下面仍逐条核验实际ACK和真实消费读回，两分区覆盖断言不会被删掉。
	var keys []string
	chosen := make(map[int]bool)
	topicPartitioner := partitioner.ForTopic(topic)
	for i := 0; i < 64 && len(keys) < 2; i++ {
		key := fmt.Sprintf("order-%d", i)
		p := topicPartitioner.Partition(&kgo.Record{Key: []byte(key)}, 2)
		if !chosen[p] {
			chosen[p] = true
			keys = append(keys, key)
		}
	}
	if err = require(len(keys) == 2, "教学key候选必须覆盖两个散列分区"); err != nil {
		return err
	}
	keys = append(keys, "order-extra")
	var records []*kgo.Record
	for step := 0; step < 2; step++ {
		for _, key := range keys {
			e := Event{ID: fmt.Sprintf("%s.key.%s.%d", h.RunID, key, step), Order: key, Step: step}
			value, _ := json.Marshal(e)
			records = append(records, &kgo.Record{Topic: topic, Key: []byte(key), Value: value})
		}
	}
	if err = ProduceAndWait(ctx, o, records...); err != nil {
		return err
	}
	acks := make(map[string]*kgo.Record)
	keyPartition := make(map[string]int32)
	keyOffset := make(map[string]int64)
	partitions := make(map[int32]bool)
	for _, r := range records {
		e, _ := Decode(r)
		if p, exists := keyPartition[e.Order]; exists {
			if err = require(p == r.Partition && keyOffset[e.Order] < r.Offset, "同key分区和顺序不变"); err != nil {
				return err
			}
		}
		keyPartition[e.Order] = r.Partition
		keyOffset[e.Order] = r.Offset
		partitions[r.Partition] = true
		acks[e.ID] = r
		h.Trace.Say("PRODUCE_ACK key=%s value=%s p=%d offset=%d", r.Key, r.Value, r.Partition, r.Offset)
	}
	if err = require(len(partitions) == 2, "本组固定教学key应覆盖两个真实分区"); err != nil {
		return err
	}
	return h.WithConsumer(ctx, topic, group, func(cl *kgo.Client) error {
		defer cl.AllowRebalance()
		rs, e := ReadN(ctx, cl, len(records), h.Trace)
		if e != nil {
			return e
		}
		seen := make(map[string]bool)
		last := make(map[int32]int64)
		for _, r := range rs {
			event, e := Decode(r)
			if e != nil {
				return e
			}
			ack := acks[event.ID]
			if e = require(ack != nil && !seen[event.ID] && bytes.Equal(ack.Key, r.Key) && bytes.Equal(ack.Value, r.Value) && ack.Partition == r.Partition && ack.Offset == r.Offset, "消费读回必须与 broker ack 的 key/value/partition/offset 完全一致"); e != nil {
				return e
			}
			if previous, ok := last[r.Partition]; ok {
				if e = require(r.Offset > previous, "每分区读取顺序递增"); e != nil {
					return e
				}
			}
			seen[event.ID] = true
			last[r.Partition] = r.Offset
			if e = ApplyRecord(ctx, l, r, "keyed", "serial", true); e != nil {
				return e
			}
			if e = h.Commit(ctx, cl, group, r); e != nil {
				return e
			}
		}
		h.Trace.Say("E1_PASS readback=%d partitions=2；跨分区 Records 遍历顺序不代表全局业务顺序", len(rs))
		return nil
	})
}

// OffsetWindows 用同一个真实单分区 topic、五个独占 group 分别复现两类崩溃窗口、
// 异步 worker 越过低 offset、连续 watermark 修复。Ledger 的 scope 让实验互不污染。
func OffsetWindows(ctx context.Context, h *Harness, l *Ledger) error {
	topic, err := h.Topic(ctx, "offsets", 1)
	if err != nil {
		return err
	}
	events := []Event{{h.RunID + ".offset.0", "order-offset", 0}, {h.RunID + ".offset.1", "order-offset", 1}, {h.RunID + ".offset.2", "order-offset", 2}}
	if _, err = h.Produce(ctx, topic, events, []int32{0, 0, 0}); err != nil {
		return err
	}
	if err = LossWindow(ctx, h, l, topic, events[0].ID); err != nil {
		return err
	}
	if err = DuplicateWindow(ctx, h, l, topic, events[0].ID, false); err != nil {
		return err
	}
	if err = DuplicateWindow(ctx, h, l, topic, events[0].ID, true); err != nil {
		return err
	}
	if err = OutOfOrder(ctx, h, l, topic, events[0].ID, false); err != nil {
		return err
	}
	return OutOfOrder(ctx, h, l, topic, events[0].ID, true)
}

// LossWindow 故意先 commit offset=0（broker next=1），随后在业务前失败。
// 此错误是被实验断言识别的哨兵；不能为了继续运行把所有未知 error 都吞掉。
func LossWindow(ctx context.Context, h *Harness, l *Ledger, topic, id string) error {
	group := h.Group("loss")
	h.Trace.Say("E2_BAD 先提交offset再业务失败")
	err := h.WithConsumer(ctx, topic, group, func(cl *kgo.Client) error {
		defer cl.AllowRebalance()
		rs, e := ReadN(ctx, cl, 1, h.Trace)
		if e != nil {
			return e
		}
		if e = h.Commit(ctx, cl, group, rs[0]); e != nil {
			return e
		}
		h.Trace.Say("INJECT_FAILURE before_business offset=%d", rs[0].Offset)
		return errInjected
	})
	if !errors.Is(err, errInjected) {
		return fmt.Errorf("预期业务故障却得到: %w", err)
	}
	// 如果 Close 也失败，errors.Join 保留了它，不能把带清理错误的预期故障算 PASS。
	if err != errInjected {
		return fmt.Errorf("故障复现的清理失败: %w", err)
	}
	return h.WithConsumer(ctx, topic, group, func(cl *kgo.Client) error {
		defer cl.AllowRebalance()
		rs, e := ReadN(ctx, cl, 1, h.Trace)
		if e != nil {
			return e
		}
		a, _ := l.Counts("loss", id, "")
		if e = require(rs[0].Offset == 1 && a == 0, "restart 跳过0且业务次数仍0"); e != nil {
			return e
		}
		h.Trace.Say("E2_PASS lost_event=%s business=0 restart_offset=1", id)
		return nil
	})
}

// DuplicateWindow 在业务文件 Sync 后、commit 前退出，再用同 group 重建 consumer。
// dedupe=false 展示两次真实效果；dedupe=true 仍重投两次却只应用一次。
func DuplicateWindow(ctx context.Context, h *Harness, l *Ledger, topic, id string, dedupe bool) error {
	scope := "duplicate"
	if dedupe {
		scope = "idempotent"
	}
	group := h.Group(scope)
	h.Trace.Say("E3_WINDOW scope=%s 业务成功但未提交offset", scope)
	err := h.WithConsumer(ctx, topic, group, func(cl *kgo.Client) error {
		defer cl.AllowRebalance()
		rs, e := ReadN(ctx, cl, 1, h.Trace)
		if e != nil {
			return e
		}
		if e = ApplyRecord(ctx, l, rs[0], scope, "before-restart", dedupe); e != nil {
			return e
		}
		return errInjected
	})
	if err != errInjected {
		return fmt.Errorf("预期未commit故障（包括清理检查）: %w", err)
	}
	return h.WithConsumer(ctx, topic, group, func(cl *kgo.Client) error {
		defer cl.AllowRebalance()
		rs, e := ReadN(ctx, cl, 1, h.Trace)
		if e != nil {
			return e
		}
		event, e := Decode(rs[0])
		if e != nil {
			return e
		}
		if e = require(rs[0].Offset == 0 && event.ID == id, "同group必须重投同一event_id/offset0"); e != nil {
			return e
		}
		if e = ApplyRecord(ctx, l, rs[0], scope, "after-restart", dedupe); e != nil {
			return e
		}
		if e = h.Commit(ctx, cl, group, rs[0]); e != nil {
			return e
		}
		a, b := l.Counts(scope, id, "")
		want := 2
		if dedupe {
			want = 1
		}
		if e = require(a == 2 && b == want, "attempts=%d effects=%d want_effects=%d", a, b, want); e != nil {
			return e
		}
		h.Trace.Say("E3_PASS scope=%s event_id=%s attempts=%d effects=%d", scope, id, a, b)
		return nil
	})
}

// Worker 是每个实验自己创建的业务任务，不是 Kafka 的后台 fetch goroutine。
// Result 缓冲1使 owner 早退时发送不阻塞；Done 单独关闭，证明 defer/收尾全部执行完。
type Worker struct {
	Cancel        context.CancelFunc
	Result        chan error
	Done, Started chan struct{}
}

// StartWorker 由 OutOfOrder 或 managed consumer 调用。gate=nil 表示立即处理；
// 否则在 gate/ctx.Done 上等待。panic 在本 goroutine 的 defer 转 error，不能靠 main recover。
// gate的<-chan限定本函数只能接收，关闭权限归创建者；两分支同时就绪时select无取消优先。
// 所以下面还在work前检查Err，Ledger也在副作用临界区检查；已开始的效果仍须owner等待Done。
func StartWorker(parent context.Context, gate <-chan struct{}, work func(context.Context) error) *Worker {
	ctx, cancel := context.WithCancel(parent)
	w := &Worker{Cancel: cancel, Result: make(chan error, 1), Done: make(chan struct{}), Started: make(chan struct{})}
	go func() {
		defer close(w.Done)
		defer cancel()
		var err error
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("业务worker panic: %v", p)
			}
			w.Result <- err
		}()
		close(w.Started) // 真正由任务发布“开始进入处理/等待阶段”，不是 owner 自己假定已启动。
		if gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				err = ctx.Err()
				return
			}
		}
		if err = ctx.Err(); err != nil {
			return
		}
		err = work(ctx)
	}()
	return w
}

// Stop 由 worker owner 的所有早退 defer 调用；Cancel 只通知，joinDone 才等待收尾。
func (w *Worker) Stop() error { w.Cancel(); return joinDone(w.Done, "business worker") }

// ResultBeforeDeadline 让业务结果等待有上限；读到结果仍不等于 join，调用者还须 Stop。
func (w *Worker) ResultBeforeDeadline(ctx context.Context) error {
	select {
	case err := <-w.Result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// OutOfOrder 用 channel gate 保证高 offset=1 先完成；没有 sleep 猜 worker 的调度。
// 坏例只 commit 1 然后 cancel 0，新 consumer 从2开始；修复保留 pending1，等0后才commit1。
func OutOfOrder(ctx context.Context, h *Harness, l *Ledger, topic, id string, repair bool) error {
	scope := "unordered.bad"
	if repair {
		scope = "unordered.watermark"
	}
	group := h.Group(scope)
	h.Trace.Say("E4_START scope=%s", scope)
	err := h.WithConsumer(ctx, topic, group, func(cl *kgo.Client) (err error) {
		defer cl.AllowRebalance()
		rs, err := ReadN(ctx, cl, 2, h.Trace)
		if err != nil {
			return err
		}
		if err = require(rs[0].Offset == 0 && rs[1].Offset == 1, "单分区前两条0/1"); err != nil {
			return err
		}
		gate := make(chan struct{})
		low := StartWorker(ctx, gate, func(c context.Context) error { return ApplyRecord(c, l, rs[0], scope, "low", false) })
		defer func() { combineCleanup(&err, low.Stop()) }()
		// 先等待low任务自己发布Started，再启动high；创建指针本身不说明goroutine已运行。
		// gate仍由owner保持未关闭，因此low即使已经获调度也不能进入实际业务写入。
		select {
		case <-low.Started:
		case <-ctx.Done():
			return ctx.Err() // defer仍cancel low并独立join真实Done，不能漏掉已启动任务。
		}
		high := StartWorker(ctx, nil, func(c context.Context) error {
			if e := ApplyRecord(c, l, rs[1], scope, "high", false); e != nil {
				return e
			}
			if !repair {
				// 危险点：异步业务worker自行提交完成位置，而低offset任务仍挂着。
				// CommitRecords在这个worker中同步等待；Kafka不知道另一个任务还未处理。
				return h.Commit(c, cl, group, rs[1])
			}
			return nil
		})
		defer func() { combineCleanup(&err, high.Stop()) }()
		if err = high.ResultBeforeDeadline(ctx); err != nil {
			return err
		}
		if err = joinDone(high.Done, "high worker"); err != nil {
			return err
		}
		if !repair {
			// 保持 low gate 关闭前状态；主动停止模拟进程在低offset完成前退出。
			low.Cancel()
			if err = joinDone(low.Done, "canceled low worker"); err != nil {
				return err
			}
			h.Trace.Say("E4_BAD high=1完成且next=2；low=0被取消，没有业务效果")
			return nil
		}
		w := NewWatermark(rs[0].Offset)
		next, advanced := w.Complete(rs[1].Offset)
		if err = require(next == 0 && !advanced, "高offset完成不能前移commit水位"); err != nil {
			return err
		}
		h.Trace.Say("WATERMARK complete=1 safe_next=0，未提交")
		close(gate) // 唯一 gate owner 是本函数；现在才许可 low 从接收等待进入业务。
		if err = low.ResultBeforeDeadline(ctx); err != nil {
			return err
		}
		if err = joinDone(low.Done, "released low worker"); err != nil {
			return err
		}
		next, advanced = w.Complete(rs[0].Offset)
		if err = require(next == 2 && advanced, "0/1均完成后safe_next=2"); err != nil {
			return err
		}
		if err = h.Commit(ctx, cl, group, rs[1]); err != nil {
			return err
		}
		h.Trace.Say("WATERMARK complete=0 safe_next=2，commit记录1即next2")
		return nil
	})
	if err != nil {
		return err
	}
	return h.WithConsumer(ctx, topic, group, func(cl *kgo.Client) error {
		defer cl.AllowRebalance()
		rs, e := ReadN(ctx, cl, 1, h.Trace)
		if e != nil {
			return e
		}
		a, _ := l.Counts(scope, id, "")
		want := 0
		if repair {
			want = 1
		}
		if e = require(rs[0].Offset == 2 && a == want, "恢复offset2，低offset业务次数=%d want=%d", a, want); e != nil {
			return e
		}
		h.Trace.Say("E4_PASS scope=%s restart_offset=2 low_business=%d", scope, a)
		return nil
	})
}
