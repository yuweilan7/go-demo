package course

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// ManagedConsumer 由 Rebalance 创建并负责 Stop。run 是本课一个 poll owner goroutine；
// kgo 另有 fetch/heartbeat/组协调任务，归 ClientOwner。所有可变回调状态由 mu 保护。
// change 是广播状态变化 channel：更新者 close 旧 channel 后换新，观察者重新检查谓词。
type ManagedConsumer struct {
	mu                         sync.Mutex
	assignment                 []int32
	revoked, blocked, lost     int
	generation                 int
	committed                  map[string]bool
	active                     *Worker
	change                     chan struct{}
	loopErr, callbackErr       error
	owner                      *ClientOwner
	cancel                     context.CancelFunc
	done                       chan struct{}
	gate                       chan struct{}
	entered                    chan struct{}
	gateID, name, topic, group string
	gatedOwner                 string
	h                          *Harness
	l                          *Ledger
}

// StartConsumer 同步注册 callbacks 后启动唯一 poll goroutine，交出带停止/Done 的 owner。
// gateID 仅第一个 consumer 的一条消息被挂起；空字符串的第二 consumer 正常处理。
func StartConsumer(ctx context.Context, h *Harness, l *Ledger, topic, group, name, gateID string) (m *ManagedConsumer, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	loopCtx, cancel := context.WithCancel(ctx)
	m = &ManagedConsumer{change: make(chan struct{}), done: make(chan struct{}), gate: make(chan struct{}), entered: make(chan struct{}), committed: make(map[string]bool),
		cancel: cancel, gateID: gateID, name: name, topic: topic, group: group, h: h, l: l}
	opts := ConsumerOptions(topic, group)
	opts = append(opts,
		kgo.OnPartitionsAssigned(m.assigned), kgo.OnPartitionsRevoked(m.revoking), kgo.OnPartitionsLost(m.losing),
		kgo.OnPartitionsCallbackBlocked(m.callbackBlocked))
	m.owner, err = NewClient(h.Config, h.Trace, h.RunID+"."+name, opts...)
	if err != nil {
		cancel()
		return nil, err
	}
	h.UseGroup(group) // consumer已经创建，将开始poll；同group第二成员不重复登记清理。
	go m.run(loopCtx)
	return m, nil
}

// notifyLocked 只允许持有 m.mu 的更新者调用；所有等待者都会被同一次 close 唤醒。
// 等待者醒来后必须重新看 assignment，通知本身不能代表“已经有两个分区”。
func (m *ManagedConsumer) notifyLocked() { close(m.change); m.change = make(chan struct{}) }

// assigned 由 kgo group-management goroutine 调用，发生在新的分区开始 fetch 之前。
// 回调 map 的 slice 拷到本对象私有数组；不能把别人的可变 slice 长期共享给测试。
func (m *ManagedConsumer) assigned(_ context.Context, _ *kgo.Client, partitions map[string][]int32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assignment = append([]int32(nil), partitions[m.topic]...)
	slices.Sort(m.assignment)
	m.generation++
	m.h.Trace.Say("ASSIGNED owner=%s generation=%d partitions=%v", m.name, m.generation, m.assignment)
	m.notifyLocked()
}

// revoking 由 kgo 调用。BlockRebalanceOnPoll 保证该回调在 run 已经 Stop/join active
// worker、处理完 commit、AllowRebalance 之后执行。active 非nil意味着课程生命周期出错。
// 本课每批仅一条、成功后已commit，没有待提交工作；被取消的消息不commit，交新owner重投。
func (m *ManagedConsumer) revoking(_ context.Context, _ *kgo.Client, partitions map[string][]int32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil {
		m.callbackErr = errors.Join(m.callbackErr, fmt.Errorf("revoke 时仍有 active worker"))
	}
	m.revoked++
	m.assignment = nil
	m.h.Trace.Say("REVOKED owner=%s partitions=%v old_worker_joined=%t", m.name, partitions[m.topic], m.active == nil)
	m.notifyLocked()
}

// losing 处理成员被踢/鉴权等彻底失去所有权的路径；不冒险commit。
// lost 与普通 revoke 分开计数；正常实验若出现 lost 就失败，不能解释为成功rebalance。
func (m *ManagedConsumer) losing(_ context.Context, _ *kgo.Client, partitions map[string][]int32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lost++
	m.assignment = nil
	m.callbackErr = errors.Join(m.callbackErr, fmt.Errorf("owner=%s partitions lost", m.name))
	if m.active != nil {
		m.active.Cancel()
	}
	m.h.Trace.Say("LOST owner=%s partitions=%v", m.name, partitions[m.topic])
	m.notifyLocked()
}

// callbackBlocked 由 kgo 独立 signal goroutine 调用，只发取消通知，绝不在回调内 join。
// 否则 poll owner 等结果、回调等待poll、worker又等gate，可能形成死锁。
// active 共享指针在 mu 下读取；worker done 必须由 run 的独立清理预算实际等待。
func (m *ManagedConsumer) callbackBlocked(_ context.Context, _ *kgo.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blocked++
	if m.active != nil {
		m.active.Cancel()
	}
	m.h.Trace.Say("CALLBACK_BLOCKED owner=%s active=%t cancel_not_join=true", m.name, m.active != nil)
	m.notifyLocked()
}

// run 是消费者唯一 poll/commit owner。PollRecords(max=1) 返回后暂挡revoke；
// 每条业务还限12秒，以防通知遗漏。所有错误路径都先停止并join worker，再AllowRebalance。
// panic 在本goroutine入口转换error；defer顺序让 loopErr 最终可见后才close(done)。
func (m *ManagedConsumer) run(ctx context.Context) {
	defer close(m.done)
	defer m.cancel()
	defer func() {
		if p := recover(); p != nil {
			m.loopErr = errors.Join(m.loopErr, fmt.Errorf("consumer panic: %v", p))
		}
		m.owner.Client.AllowRebalance()
	}()
	for ctx.Err() == nil {
		rs, err := Poll(ctx, m.owner.Client, 1, m.h.Trace)
		if err != nil {
			if ctx.Err() == nil {
				m.loopErr = err
			}
			return
		}
		if len(rs) == 0 {
			m.owner.Client.AllowRebalance()
			continue
		}
		if err = m.process(ctx, rs[0]); err != nil {
			// process已把正常停机的Canceled转nil；剩下的错误包括join失败，
			// 即使ctx也到期，仍必须保留，不能因“正在关闭”吞掉清理错误。
			m.loopErr = err
			return
		}
	}
}

// process 被 run 同步调用，一次只处理一条。worker 可以阻塞在gate，但不会让poll owner
// 无限等待；callbackBlocked 取消它，process 真正等待Done后才撤销 active/开放rebalance。
func (m *ManagedConsumer) process(ctx context.Context, r *kgo.Record) (err error) {
	defer m.owner.Client.AllowRebalance()
	e, err := Decode(r)
	if err != nil {
		return err
	}
	workCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	m.mu.Lock()
	var gate <-chan struct{}
	gated := e.ID == m.gateID
	if gated {
		gate = m.gate
		m.gateID = ""
	} // 本owner只阻塞一次；重分配回来时仍可处理重投。
	workerOwner := fmt.Sprintf("%s.g%d", m.name, m.generation)
	if gated {
		m.gatedOwner = workerOwner
	}
	w := StartWorker(workCtx, gate, func(c context.Context) error { return ApplyRecord(c, m.l, r, "rebalance", workerOwner, true) })
	m.active = w
	m.mu.Unlock()
	defer func() {
		combineCleanup(&err, w.Stop()) // 独立清理预算join真正Done，不能仅消费Result。
		m.mu.Lock()
		m.active = nil
		m.notifyLocked()
		m.mu.Unlock()
		m.h.Trace.Say("WORKER_JOINED owner=%s event_id=%s", m.name, e.ID)
	}()
	if gated {
		select {
		case <-w.Started:
		case <-ctx.Done():
			return nil // 正常停止；defer仍cancel/join worker，再AllowRebalance。
		}
		m.h.Trace.Say("OLD_WORK_INFLIGHT owner=%s event_id=%s p=%d gate_open=false", m.name, e.ID, r.Partition)
		close(m.entered) // 真实任务已进入阶段；Rebalance 此后才启动第二consumer。
	}
	err = w.ResultBeforeDeadline(workCtx)
	if errors.Is(err, context.Canceled) && ctx.Err() == nil {
		// rebalance催停，业务gate仍未释放，因此没有执行该条效果，也不推进offset。
		m.h.Trace.Say("BOUNDED_ABORT owner=%s event_id=%s 未commit，等待新owner重投", m.name, e.ID)
		return nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil
		} // 正常关闭；worker defer仍由此函数join。
		return err
	}
	// 此处仍挡住revoke；没有跨owner commit。callbackBlocked 已发取消不撤销已成功的效果。
	if err = m.h.Commit(ctx, m.owner.Client, m.group, r); err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			// 停机恰落在业务完成/提交窗口：保留至少一次重投语义，不能假称commit成功。
			m.h.Trace.Say("STOP_DURING_COMMIT owner=%s event_id=%s 进度未确认", m.name, e.ID)
			return nil
		}
		return err
	}
	m.mu.Lock()
	m.committed[e.ID] = true
	m.notifyLocked()
	m.mu.Unlock()
	return nil
}

// WaitAssignment 由实验入口调用；mu下保存change快照，避免“检查后才订阅”丢失通知。
// 不使用sleep猜rebalance何时结束；deadline仅限制等待，并不制造assignment。
func (m *ManagedConsumer) WaitAssignment(ctx context.Context, n int) error {
	for {
		m.mu.Lock()
		ok := len(m.assignment) == n
		changed := m.change
		e := m.callbackErr
		m.mu.Unlock()
		if e != nil {
			return e
		}
		if ok {
			return nil
		}
		select {
		case <-changed:
		case <-m.done:
			return fmt.Errorf("consumer 在取得%d分区前结束: %w", n, m.loopErr)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Snapshot 只返回assignment私有副本和计数，断言不能改m的真实所有权状态。
func (m *ManagedConsumer) Snapshot() ([]int32, int, int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int32(nil), m.assignment...), m.revoked, m.blocked, m.lost
}

// WaitCommit 由Rebalance入口等待“业务已完成且broker offset已读回验证”，
// 不能只等BUSINESS日志就关闭consumer，那个窗口仍可能尚未commit。
func (m *ManagedConsumer) WaitCommit(ctx context.Context, id string) error {
	for {
		m.mu.Lock()
		ok := m.committed[id]
		changed := m.change
		cb := m.callbackErr
		m.mu.Unlock()
		if cb != nil {
			return cb
		}
		if ok {
			return nil
		}
		select {
		case <-changed:
		case <-m.done:
			return fmt.Errorf("commit前consumer结束: %w", m.loopErr)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Stop 由Rebalance所有早退defer及正常退出路径调用，可重复。先取消poll/worker并join
// poll goroutine，再独立关闭Kafka驱动。读取loopErr在done之后，具有channel同步保证。
func (m *ManagedConsumer) Stop() (err error) {
	m.cancel()
	joined := joinDone(m.done, "consumer "+m.name)
	if joined == nil {
		err = m.loopErr // 只在真实Done之后读loopErr，不能在超时路径与run并发读写。
	} else {
		combineCleanup(&err, joined)
	}
	// 即使poll任务join未通过也必须继续cancel/Close驱动，解除网络与内部等待。
	// 不能因第一项清理失败就漏掉其余资源；最终仍向入口返回聚合失败。
	combineCleanup(&err, m.owner.Close())
	if joined != nil {
		if retry := joinDone(m.done, "consumer "+m.name+" after client close"); retry == nil {
			err = errors.Join(err, m.loopErr)
		} else {
			combineCleanup(&err, retry)
		}
	}
	m.mu.Lock()
	cb := m.callbackErr
	m.mu.Unlock()
	combineCleanup(&err, cb)
	return
}

// Rebalance 做真实加入/退出实验：old先拥有2分区且挂起一条业务，new加入促成回调，
// old被催停且join→Allow→revoke→新assignment；new退出后old重新拿回两分区。
func Rebalance(ctx context.Context, h *Harness, l *Ledger) (err error) {
	h.Trace.Say("E5_START 真实第二consumer加入/退出")
	topic, err := h.Topic(ctx, "rebalance", 2)
	if err != nil {
		return err
	}
	group := h.Group("rebalance.consumer")
	gateID := h.RunID + ".rb.gated"
	old, err := StartConsumer(ctx, h, l, topic, group, "old", gateID)
	if err != nil {
		return err
	}
	defer func() { combineCleanup(&err, old.Stop()) }()
	if err = old.WaitAssignment(ctx, 2); err != nil {
		return err
	}
	if err = h.VerifyMembers(ctx, topic, group, 1); err != nil {
		return err
	}
	// gate消息固定在partition1；RangeBalancer最终由哪位成员拿1
	// 不是语言保证，不断言consumer名字和分区编号固定对应。
	if _, err = h.Produce(ctx, topic, []Event{{gateID, "order-gated", 0}}, []int32{1}); err != nil {
		return err
	}
	select {
	case <-old.entered:
	case <-old.done:
		return fmt.Errorf("old提前结束: %w", old.loopErr)
	case <-ctx.Done():
		return ctx.Err()
	}
	newConsumer, err := StartConsumer(ctx, h, l, topic, group, "new", "")
	if err != nil {
		return err
	}
	defer func() { combineCleanup(&err, newConsumer.Stop()) }()
	if err = old.WaitAssignment(ctx, 1); err != nil {
		return err
	}
	if err = newConsumer.WaitAssignment(ctx, 1); err != nil {
		return err
	}
	if err = h.VerifyMembers(ctx, topic, group, 2); err != nil {
		return err
	}
	pOld, rev, blocked, lost := old.Snapshot()
	pNew, _, _, _ := newConsumer.Snapshot()
	if err = require(len(pOld) == 1 && len(pNew) == 1 && rev >= 1 && blocked >= 1 && lost == 0 && pOld[0] != pNew[0], "真正revoke/blocked/不重叠assignment old=%v new=%v revoke=%d blocked=%d lost=%d", pOld, pNew, rev, blocked, lost); err != nil {
		return err
	}
	h.Trace.Say("REBALANCE_VERIFIED old=%v new=%v revoke=%d blocked=%d", pOld, pNew, rev, blocked)
	old.mu.Lock()
	gatedOwner := old.gatedOwner
	old.mu.Unlock()
	initialAttempts, _ := l.Counts("rebalance", gateID, gatedOwner)
	if err = require(initialAttempts == 0, "被取消并join的旧generation不能有业务效果"); err != nil {
		return err
	}
	// gate尚未关闭：旧worker已经取消/join；后续关闭gate不会复活它。
	// 若旧consumer又拥有partition1，新的generation可处理同event；我们核验的是旧任务无迟到副作用。
	close(old.gate)
	// 旧任务已结束；新generation或new拥有partition1，必须真实完成原消息并commit。
	// 只验证旧任务停止还不够：这里另外验证重分配后原业务没有被整个实验漏掉。
	if pNew[0] == 1 {
		err = newConsumer.WaitCommit(ctx, gateID)
	} else {
		err = old.WaitCommit(ctx, gateID)
	}
	if err != nil {
		return err
	}
	gateAttempts, gateEffects := l.Counts("rebalance", gateID, "")
	if err = require(gateAttempts == 1 && gateEffects == 1, "新owner/generation必须恰好完成原消息一次"); err != nil {
		return err
	}
	marker := h.RunID + ".rb.new"
	if _, err = h.Produce(ctx, topic, []Event{{marker, "order-new", 0}}, []int32{pNew[0]}); err != nil {
		return err
	}
	if err = newConsumer.WaitCommit(ctx, marker); err != nil {
		return err
	}
	if err = newConsumer.Stop(); err != nil {
		return err
	}
	if err = old.WaitAssignment(ctx, 2); err != nil {
		return err
	}
	if err = h.VerifyMembers(ctx, topic, group, 1); err != nil {
		return err
	}
	pOld, rev, _, lost = old.Snapshot()
	if err = require(len(pOld) == 2 && rev >= 2 && lost == 0, "new离组后old恢复2分区且再次revoke"); err != nil {
		return err
	}
	tail := h.RunID + ".rb.tail"
	if _, err = h.Produce(ctx, topic, []Event{{tail, "order-tail", 0}}, []int32{0}); err != nil {
		return err
	}
	if err = old.WaitCommit(ctx, tail); err != nil {
		return err
	}
	if err = old.Stop(); err != nil {
		return err
	}
	if err = newConsumer.Stop(); err != nil {
		return err
	}
	lateAttempts, _ := l.Counts("rebalance", gateID, gatedOwner)
	if err = require(lateAttempts == 0, "交接并join后旧generation迟到副作用必须为0"); err != nil {
		return err
	}
	a, b := l.Counts("rebalance", "", "")
	if err = require(a == 3 && b == 3, "rebalance三条业务均实际完成且无重复效果 attempts=%d effects=%d", a, b); err != nil {
		return err
	}
	// 两个consumer的真正Done已join，再发送任何gate通知也没有旧任务可运行。
	h.Trace.Say("E5_PASS join后副作用稳定 attempts=%d effects=%d canceled_owner=%s late_effects=%d old_assignment_before_stop=%v", a, b, gatedOwner, lateAttempts, pOld)
	return nil
}
