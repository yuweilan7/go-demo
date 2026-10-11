package lab

import (
	"context"
	"errors"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// errJoinDeadline让owner区分“worker返回错误”与“还没等到worker”。后一种
// 不能把joined设true，必须继续关闭其I/O/取消并在defer里完成第二次清理等待。
var errJoinDeadline = errors.New("worker未在独立清理预算内join")

// ownedTask 属于创建它的scenario/test owner。result由工作goroutine写一次，
// finished只在工作函数（包含其defer）返回后关闭；owner观察关闭后才读取result。
// 关闭channel到接收的同步关系保护result，不需要为此再加一把mutex。
type ownedTask struct {
	finished chan struct{}
	result   error
}

func newTask() *ownedTask { return &ownedTask{finished: make(chan struct{})} }

// start 由owner在注册defer清理后调用一次。wrapper先注册最终完成信号，再执行
// work；work返回之前其内部所有defer已经结束，然后存result，最后关闭finished。
// finished是可重复观察的广播，不会因一次消费结果而失去后续join的依据。
func (t *ownedTask) start(work func() error) {
	go func() {
		defer close(t.finished)
		t.result = work()
	}()
}

// joinTask 由创建goroutine的owner调用。它等完成广播而不是一次业务结果；
// 收尾不用已经取消的业务ctx，另给3s预算。超时不能把任务标成已完成。
func joinTask(task *ownedTask) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	select {
	case <-task.finished:
		return task.result
	case <-ctx.Done():
		return errJoinDeadline
	}
}

// cancelExperiment 用入口取消与真实socket写屏障对照v1.15.0的两段契约。
// sendCtx只属于Publish调用；scenario ctx属于Connection watcher，二者故意不同，
// 否则watcher关socket会被误认成PublishWithContext自己中断了I/O。
func cancelExperiment(s *scenario) (result error) {
	canceled, cancelAlready := context.WithCancel(s.ctx)
	cancelAlready()
	before := s.pub.Ch.GetNextPublishSeqNo()
	err := s.pub.Ch.PublishWithContext(canceled, s.top.WorkX, "work", true, false, amqp.Publishing{MessageId: s.id("already-canceled"), Body: []byte("never-sent")})
	if err = check(errors.Is(err, context.Canceled) && before == s.pub.Ch.GetNextPublishSeqNo(), "入口已取消ctx应直接返回Canceled并且不增加publish序号"); err != nil {
		return err
	}
	q, err := Barrier(s.pub.Ch, s.top.WorkQ)
	if err != nil {
		return err
	}
	if err = check(q.Messages == 0, "入口取消消息不应入队"); err != nil {
		return err
	}
	fmt.Fprintln(s.log, "CANCEL before_call error=context.Canceled publish_seq_unchanged ready=0")
	g := newGate()
	// NotifyPublish返回Go通知流，不是消息队列。确认等待由下方owner独立管理，
	// 不把sendCtx自动当成确认ctx；缓冲容纳本实验唯一一个确认。
	confirms := s.pub.Ch.NotifyPublish(make(chan amqp.Confirmation, 1))
	s.pubSession.Wire.arm(g)
	sendCtx, cancelSend := context.WithCancel(s.ctx)
	defer cancelSend()
	done := newTask()
	joined := false
	// 立即注册解除屏障+join；从此每个提前返回分支都会收回自己创建的publisher。
	defer func() {
		g.open()
		cancelSend()
		if !joined {
			// sendCtx无法终止开始后的I/O；提前失败时owner主动关闭自己的
			// socket再join，不能只发cancel通知就留下publisher继续执行。
			_ = s.pubSession.Wire.Close()
			result = errors.Join(result, joinTask(done))
		}
	}()
	done.start(func() error {
		return s.pub.Ch.PublishWithContext(sendCtx, s.top.WorkX, "work", true, false, amqp.Publishing{
			MessageId: s.id("inflight-canceled"), DeliveryMode: amqp.Persistent,
			Body: []byte("coupon:inflight-canceled"),
		})
	})
	select {
	case <-g.entered:
	case <-done.finished:
		joined = true
		return fmt.Errorf("未进入写屏障就结束: %v", done.result)
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.pub.Closed:
		return errors.New("等待写屏障时publisher channel提前关闭")
	}
	cancelSend()
	<-sendCtx.Done() // 这里证明通知已经发出；下一个select证明Write尚未因通知返回。
	select {
	case <-done.finished:
		joined = true
		return fmt.Errorf("in-flight Publish被ctx提前中断: %v", done.result)
	default:
	}
	fmt.Fprintln(s.log, "CANCEL during_Write ctx_done=true Publish_still_blocked=true; release真实socket写屏障")
	g.open()
	// 已进入Publish的这个调用在解除I/O后仍返回nil，即使sendCtx已经取消。
	// 这只证明写帧返回；下方owner用未取消的scenario ctx独立等待真实confirm。
	err = joinTask(done)
	joined = !errors.Is(err, errJoinDeadline)
	if err != nil {
		return err
	}
	select {
	case confirmed, ok := <-confirms:
		if err = check(ok && confirmed.Ack && confirmed.DeliveryTag == before, "在途发送需有真实publisher ACK confirm"); err != nil {
			return err
		}
	case <-s.pub.Closed:
		return errors.New("等待在途确认时publisher提前关闭")
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	fmt.Fprintln(s.log, "CANCEL in_flight Publish_return=nil ctx_still_canceled=true separate_confirm_owner=ACK")
	if _, err = Barrier(s.pub.Ch, s.top.WorkQ); err != nil {
		return err
	}
	sess, c, err := s.consumer("cancel-observer", 1)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, sess.Close()) }()
	d, err := c.Next(s.ctx)
	if err != nil {
		return err
	}
	if err = check(d.MessageId == s.id("inflight-canceled"), "ctx取消后应仍观察到实际发送消息"); err != nil {
		return err
	}
	if err = s.commitAndAck(c, d); err != nil {
		return err
	}
	fmt.Fprintln(s.log, "CANCEL after_send real_delivery=inflight-canceled effects=1 => 停止等待不会撤回消息")
	return check(s.ledger.Count(s.id("inflight-canceled")) == 1, "取消边界业务验证失败")
}

// multipleExperiment 调用相同真实broker路径，唯一关键差异是Ack的multiple位。
// 坏例B先完成就Ack(true)会连A一起确认；修复用Ack(false)使A保持broker责任。
func multipleExperiment(s *scenario) error {
	if err := concurrentAck(s, "bad", true); err != nil {
		return err
	}
	return concurrentAck(s, "fixed", false)
}

// concurrentAck 的两个goroutine由本函数拥有：A在release/ctx屏障等待；B实际
// 提交账本再报告完成。owner读B结果后才ack，随后cancel A并真实join，最后断连接。
// 不假设goroutine创建顺序或sleep时长；started保证A已经进入未提交等待。
// v1.15.0的Channel文档不保证跨goroutine共享；两个业务worker不调用Channel方法，
// 协议操作由本函数串行执行，危险来自业务完成顺序与multiple范围不同。
func concurrentAck(s *scenario, label string, multiple bool) (result error) {
	a, b := s.id(label+"-slow-A"), s.id(label+"-fast-B")
	for _, id := range []string{a, b} {
		if err := s.pub.Publish(s.ctx, s.top.WorkX, "work", id, nil); err != nil {
			return err
		}
	}
	sess, c, err := s.consumer(label, 2)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, sess.Close()) }()
	da, err := c.Next(s.ctx)
	if err != nil {
		return err
	}
	db, err := c.Next(s.ctx)
	if err != nil {
		return err
	}
	if err = check(da.MessageId == a && db.MessageId == b && da.DeliveryTag < db.DeliveryTag, "同channel delivery顺序/内容不符"); err != nil {
		return err
	}
	workers, cancelWorkers := context.WithCancel(s.ctx)
	started := make(chan struct{})
	release := make(chan struct{}) // owner故意不释放；本次断连前取消A，模拟其业务没完成。
	aDone, bDone := newTask(), newTask()
	aJoined, bJoined := false, false
	defer func() {
		cancelWorkers()
		if !aJoined {
			err := joinTask(aDone)
			if !errors.Is(err, context.Canceled) {
				result = errors.Join(result, err)
			}
		}
		if !bJoined {
			result = errors.Join(result, joinTask(bDone))
		}
	}()
	aDone.start(func() error {
		close(started)
		select {
		case <-release:
			_, err := s.ledger.Commit(da.MessageId)
			return err
		case <-workers.Done():
			return workers.Err()
		}
	})
	bDone.start(func() error { _, err := s.ledger.Commit(db.MessageId); return err })
	select {
	case <-started:
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	err = joinTask(bDone)
	bJoined = !errors.Is(err, errJoinDeadline)
	if err != nil {
		return err
	}
	if err = check(s.ledger.Count(a) == 0 && s.ledger.Count(b) == 1, "B完成屏障必须真实证明A未提交/B已提交"); err != nil {
		return err
	}
	if err = db.Ack(multiple); err != nil {
		return err
	}
	if _, err = Barrier(c.Ch, s.top.WorkQ); err != nil {
		return err
	}
	cancelWorkers()
	err = joinTask(aDone)
	aJoined = !errors.Is(err, errJoinDeadline)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	if err = sess.Close(); err != nil {
		return err
	}
	// 关消费者连接以后才能把ready=0用于“未确认的A也没有回队列”的证明。
	q, err := Barrier(s.pub.Ch, s.top.WorkQ)
	if err != nil {
		return err
	}
	if multiple {
		fmt.Fprintf(s.log, "MULTIPLE_BAD ack_tag=%d multiple=true earlier_tag=%d ready_after_close=%d A_effects=0 B_effects=1\n", db.DeliveryTag, da.DeliveryTag, q.Messages)
		return check(q.Messages == 0 && s.ledger.Count(a) == 0, "multiple坏例应真实误确认未完成的A")
	}
	q, err = awaitReady(s.ctx, s.pub.Ch, s.top.WorkQ, 1)
	if err != nil {
		return err
	}
	if err = check(q.Messages == 1, "Ack(false)应保留A重投责任，ready=%d", q.Messages); err != nil {
		return err
	}
	fresh, restored, err := s.consumer("multiple-fixed-recovery", 1)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, fresh.Close()) }()
	replay, err := restored.Next(s.ctx)
	if err != nil {
		return err
	}
	if err = check(replay.Redelivered && replay.MessageId == a, "修复应重投A"); err != nil {
		return err
	}
	if err = s.commitAndAck(restored, replay); err != nil {
		return err
	}
	fmt.Fprintln(s.log, "MULTIPLE_FIXED ack(false) A_redelivered=true A_effects=1 B_effects=1")
	return check(s.ledger.Count(a) == 1 && s.ledger.Count(b) == 1, "修复后业务效果必须各一次")
}

// tagExperiment 用没有收到delivery的新channel误发别的channel的tag。
// Ack返回nil只是写帧；随后NotifyClose才给真实406 unknown delivery tag，
// Connection仍可用于新建consumer并完成原消息，展示channel级错误的隔离范围。
func tagExperiment(s *scenario) (result error) {
	if err := s.pub.Publish(s.ctx, s.top.WorkX, "work", s.id("channel-owned-tag"), nil); err != nil {
		return err
	}
	sess, c, err := s.consumer("tag-owner", 1)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, sess.Close()) }()
	d, err := c.Next(s.ctx)
	if err != nil {
		return err
	}
	foreign, err := sess.Conn.Channel()
	if err != nil {
		return err
	}
	closed := foreign.NotifyClose(make(chan *amqp.Error, 1))
	// 故意错用数字；Delivery.Ack(false)本会调用原Acknowledger，下面直接指定foreign。
	if err = foreign.Ack(d.DeliveryTag, false); err != nil {
		return err
	}
	select {
	case e, ok := <-closed:
		if err = check(ok && e.Code == 406, "错误channel ack应产生406协议异常"); err != nil {
			return err
		}
		fmt.Fprintf(s.log, "TAG_FOREIGN Ack_return=nil NotifyClose_code=%d owning_channel_still_open=%v effects=0\n", e.Code, !c.Ch.IsClosed())
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	if err = s.commitAndAck(c, d); err != nil {
		return err
	}
	return check(s.ledger.Count(d.MessageId) == 1 && !sess.Conn.IsClosed(), "channel级协议异常不应破坏拥有者的业务处理")
}
