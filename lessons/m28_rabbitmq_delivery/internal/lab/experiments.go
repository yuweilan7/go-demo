package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// scenario 由 Run 串行建立。pubSession 是拓扑/发布/最终删除的 owner，其他
// consumer sessions 由各实验自己 defer Close；ledger 与 broker 之间无隐含事务。
type scenario struct {
	ctx        context.Context
	cfg        Config
	top        Topology
	pubSession *Session
	pub        *Publisher
	log        io.Writer
	ledger     *Ledger
}

// id 把可读业务标签放进本run命名空间；不同复跑即使都叫poison也不是同一业务键。
// README使用短标签方便读图，真实MessageId/ledger键一律带完整run/scenario。
func (s *scenario) id(label string) string { return s.top.WorkQ + ":" + label }

// Run 由入口或显式真实 integration test 调用。每个实验 9s、全部 90s，
// 所有资源名基于纳秒 run ID；并发复跑不共用 queue、exchange、consumer 或账本。
func Run(ctx context.Context, cfg Config, only string, out io.Writer) error {
	run := fmt.Sprintf("golearn.m28.r%d", time.Now().UnixNano())
	fmt.Fprintf(out, "M28 run=%s Go=%s client=amqp091-go/v1.15.0 registered_server=%s\n", run, runtime.Version(), cfg.ServerVersion)
	steps := []struct {
		name string
		fn   func(*scenario) error
	}{
		{"confirm", confirmExperiment}, {"return", returnExperiment}, {"cancel", cancelExperiment}, {"recovery", recoveryExperiment},
		{"duplicate", duplicateExperiment}, {"retry", retryExperiment}, {"multiple", multipleExperiment}, {"tag", tagExperiment},
	}
	matched := false
	// map没有某个字符串键时读到bool零值false；核心入口只选这四组完整实验。
	core := map[string]bool{"confirm": true, "return": true, "recovery": true, "duplicate": true}
	for _, step := range steps {
		if only != "all" && only != step.name && !(only == "core" && core[step.name]) {
			continue
		}
		matched = true
		if err := execute(ctx, cfg, Names(run+"."+step.name), out, step.fn); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
		fmt.Fprintf(out, "PASS %-9s broker assertion + independent business assertion; deleted=3 queues/3 exchanges\n", step.name)
	}
	if !matched {
		return fmt.Errorf("未知 -only=%q", only)
	}
	fmt.Fprintln(out, "M28 ALL SELECTED PASS; sessions closed, cancellation watchers joined, owned topology removed")
	return nil
}

// execute 是一个实验的资源边界。具名 result 允许 defer 用 errors.Join 合并
// 主实验错误与清理错误；即使消费者提前关闭/超时，也用新的清理 ctx 删除本run。
// defer闭包捕获的是同一个result变量：return err先给它赋值，随后按逆序执行
// 所有defer并追加清理错误，最后caller才接到完整结果；不是复制提前成功的结果。
func execute(parent context.Context, cfg Config, t Topology, out io.Writer, fn func(*scenario) error) (result error) {
	ctx, cancel := context.WithTimeout(parent, 9*time.Second)
	defer cancel()
	s, err := Connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, s.Close()) }()
	p, err := NewPublisher(s)
	if err != nil {
		return err
	}
	// 先注册精确清理，再声明拓扑；声明中途失败也尝试清理已经创建的对象。
	defer func() {
		cleanup, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		cs, err := Connect(cleanup, cfg)
		if err != nil {
			result = errors.Join(result, fmt.Errorf("清理拨号: %w", err))
			return
		}
		defer func() { result = errors.Join(result, cs.Close()) }()
		result = errors.Join(result, t.Delete(cs))
	}()
	if err = t.Declare(p.Ch); err != nil {
		return err
	}
	// Properties是AMQP字段表，动态值通过comma-ok类型断言取字符串；缺少真正
	// 协议版本就返回错误，不能用索引字符串顶替本次连接握手的事实。
	actual, ok := s.Conn.Properties["version"].(string)
	if !ok || actual == "" {
		return errors.New("AMQP握手未返回有效服务端版本")
	}
	fmt.Fprintf(out, "BEGIN %s broker_version=%s durable=true persistent=2 manual_ack=true max_length=32\n", t.WorkQ, actual)
	return fn(&scenario{ctx, cfg, t, s, p, out, NewLedger()})
}

// consumer 建立全新连接，交给当前 scenario 拥有。调用方成功后立即 defer Close；
// 创建 channel/Qos/Consume 失败时，本函数先 Close/join 部分资源再返回合并错误。
func (s *scenario) consumer(suffix string, prefetch int) (*Session, *Consumer, error) {
	sess, err := Connect(s.ctx, s.cfg)
	if err != nil {
		return nil, nil, err
	}
	c, err := NewConsumer(sess, s.top, s.top.WorkQ+"."+suffix, prefetch)
	if err != nil {
		return nil, nil, errors.Join(err, sess.Close())
	}
	return sess, c, nil
}

// check 给真实实验提供独立断言：条件不成立就返回到 execute 的统一收尾，
// 不调用 log.Fatal/os.Exit，因为那会跳过当前 goroutine 的 defer。
func check(ok bool, format string, args ...any) error {
	if !ok {
		return fmt.Errorf(format, args...)
	}
	return nil
}

// commitAndAck 是修复后的业务顺序：先账本原子提交/查重，再确认当前delivery。
// error 原样交给 scenario；失败时不 ack，让连接关闭后的 broker 重投保留恢复机会。
func (s *scenario) commitAndAck(c *Consumer, d amqp.Delivery) error {
	applied, err := s.ledger.Commit(d.MessageId)
	if err != nil {
		return err
	}
	fmt.Fprintf(s.log, "COMMIT id=%s tag=%d redelivered=%v applied=%v effects=%d\n", d.MessageId, d.DeliveryTag, d.Redelivered, applied, s.ledger.Count(d.MessageId))
	if err = d.Ack(false); err != nil {
		return err
	}
	_, err = Barrier(c.Ch, s.top.WorkQ)
	if err == nil {
		fmt.Fprintf(s.log, "ACK id=%s tag=%d multiple=false broker_barrier=ok effects=%d\n", d.MessageId, d.DeliveryTag, s.ledger.Count(d.MessageId))
	}
	return err
}

// confirmExperiment 故意先不启动 consumer，保证 confirm 先于业务提交这个
// 关系由编排屏障保证。随后 prefetch=1 阻止第二条越过第一条未确认delivery。
func confirmExperiment(s *scenario) (result error) {
	for _, id := range []string{s.id("confirm-A"), s.id("confirm-B")} {
		if err := s.pub.Publish(s.ctx, s.top.WorkX, "work", id, nil); err != nil {
			return err
		}
	}
	q, err := Barrier(s.pub.Ch, s.top.WorkQ)
	if err != nil {
		return err
	}
	if err = check(q.Messages == 2 && s.ledger.Count(s.id("confirm-A")) == 0, "confirm 后 ready=%d effects=%d，应为2/0", q.Messages, s.ledger.Count(s.id("confirm-A"))); err != nil {
		return err
	}
	fmt.Fprintf(s.log, "CONFIRM ready=2 consumer_not_started effects=0 => broker接受不等于业务完成\n")
	sess, c, err := s.consumer("core", 1)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, sess.Close()) }()
	d, err := c.Next(s.ctx)
	if err != nil {
		return err
	}
	// broker 为 prefetch 留一条未确认额度；ready=1 的真实inspect独立于本地delivery。
	q, err = Barrier(c.Ch, s.top.WorkQ)
	if err != nil {
		return err
	}
	if err = check(q.Messages == 1 && d.MessageId == s.id("confirm-A") && d.DeliveryMode == amqp.Persistent && s.ledger.Count(d.MessageId) == 0, "prefetch/manual ack/persistent 观察不符"); err != nil {
		return err
	}
	fmt.Fprintf(s.log, "DELIVER tag=%d ready=1 business_effects=0 prefetch=1 => 第一条仍待处理/ack\n", d.DeliveryTag)
	if err = s.commitAndAck(c, d); err != nil {
		return err
	}
	d, err = c.Next(s.ctx)
	if err != nil {
		return err
	}
	if err = s.commitAndAck(c, d); err != nil {
		return err
	}
	return check(s.ledger.Count(s.id("confirm-A")) == 1 && s.ledger.Count(s.id("confirm-B")) == 1, "正常业务效果必须各为1")
}

// returnExperiment 给存在的 exchange 一个没有binding匹配的routing key。
// RabbitMQ 的 basic.return 和 publisher basic.ack 可同时成立，读取各自通知。
func returnExperiment(s *scenario) error {
	if err := s.pub.Publish(s.ctx, s.top.WorkX, "missing-binding", s.id("unroutable"), nil); err != nil {
		return err
	}
	select {
	case ret, ok := <-s.pub.Returns:
		if !ok {
			return errors.New("return 通知提前关闭")
		}
		if err := check(ret.ReplyCode == 312 && ret.MessageId == s.id("unroutable") && ret.RoutingKey == "missing-binding", "mandatory return 内容不符"); err != nil {
			return err
		}
		q, err := Barrier(s.pub.Ch, s.top.WorkQ)
		if err != nil {
			return err
		}
		fmt.Fprintf(s.log, "RETURN code=%d message_id=%s broker_confirm=ack ready=%d effects=0\n", ret.ReplyCode, ret.MessageId, q.Messages)
		return check(q.Messages == 0 && s.ledger.Count(ret.MessageId) == 0, "unroutable 不应产生业务效果")
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.pub.Closed:
		return errors.New("等待 return 时 publisher channel 提前关闭")
	}
}

// recoveryExperiment 对照先ack丢业务与先commit再断连重复。
// Close 只作用本课消费者连接；恢复重新执行 Declare/Qos/Consume，没有重启broker。
func recoveryExperiment(s *scenario) (result error) {
	if err := s.pub.Publish(s.ctx, s.top.WorkX, "work", s.id("early-ack"), nil); err != nil {
		return err
	}
	old, c, err := s.consumer("bad", 1)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, old.Close()) }()
	d, err := c.Next(s.ctx)
	if err != nil {
		return err
	}
	if err = d.Ack(false); err != nil {
		return err
	} // 明确危险代码：业务还没执行就交还broker责任。
	if _, err = Barrier(c.Ch, s.top.WorkQ); err != nil {
		return err
	}
	if err = old.Close(); err != nil {
		return err
	}
	q, err := Barrier(s.pub.Ch, s.top.WorkQ)
	if err != nil {
		return err
	}
	if err = check(q.Messages == 0 && s.ledger.Count(s.id("early-ack")) == 0, "先ack坏例应真实丢掉处理机会"); err != nil {
		return err
	}
	fmt.Fprintln(s.log, "EARLY_ACK connection_closed ready=0 effects=0 => 业务未处理且broker不会再投")
	if err = s.pub.Publish(s.ctx, s.top.WorkX, "work", s.id("commit-before-disconnect"), nil); err != nil {
		return err
	}
	old2, c2, err := s.consumer("commit-then-fault", 1)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, old2.Close()) }()
	d2, err := c2.Next(s.ctx)
	if err != nil {
		return err
	}
	if _, err = s.ledger.Commit(d2.MessageId); err != nil {
		return err
	}
	fmt.Fprintf(s.log, "COMMIT_WITHOUT_ACK id=%s tag=%d effects=%d next=close-own-socket\n", d2.MessageId, d2.DeliveryTag, s.ledger.Count(d2.MessageId))
	// 在明确commit完成后直接断开socket，没有发ack；CloseDeadline负责清理并join。
	if err = old2.Wire.Close(); err != nil {
		return err
	}
	// socket.Close返回只证明本机关闭动作，库的reader还要观察网络错误并关闭
	// 原AMQP channel。等真实NotifyClose屏障之后再验证旧Ack，避免把清理竞态
	// 误写成“立即所有对象都关闭”。这里预期的是此次自造故障，无其他consumer。
	select {
	case <-c2.Closed:
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	if err = old2.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
		return err
	}
	if err = check(d2.Ack(false) != nil, "原channel关闭后旧Delivery.Ack必须失败"); err != nil {
		return err
	}
	fresh, c3, err := s.consumer("reconnected", 1)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, fresh.Close()) }()
	replay, err := c3.Next(s.ctx)
	if err != nil {
		return err
	}
	if err = check(replay.Redelivered && replay.MessageId == d2.MessageId, "未ack断连后应重投同业务消息"); err != nil {
		return err
	}
	if err = s.commitAndAck(c3, replay); err != nil {
		return err
	}
	if err = check(s.ledger.Count(replay.MessageId) == 1, "断连重投后只能有一次业务效果"); err != nil {
		return err
	}
	fmt.Fprintf(s.log, "RECOVERY old_tag=%d new_tag=%d redelivered=true effects=1 topology+Qos+consumer_recreated\n", d2.DeliveryTag, replay.DeliveryTag)
	// 断连重建之后继续处理新任务，证明恢复不只读到一条旧消息就结束。
	if err = s.pub.Publish(s.ctx, s.top.WorkX, "work", s.id("after-reconnect"), nil); err != nil {
		return err
	}
	next, err := c3.Next(s.ctx)
	if err != nil {
		return err
	}
	if err = s.commitAndAck(c3, next); err != nil {
		return err
	}
	return check(s.ledger.Count(s.id("after-reconnect")) == 1, "恢复后持续处理失败")
}

// duplicateExperiment 真正向broker发布两次相同message_id，不是本地重复调用
// handler。broker仍投递两条；业务幂等键由Ledger自己消费并原子查重。
func duplicateExperiment(s *scenario) (result error) {
	for range 2 {
		if err := s.pub.Publish(s.ctx, s.top.WorkX, "work", s.id("duplicate-id"), nil); err != nil {
			return err
		}
	}
	q, err := Barrier(s.pub.Ch, s.top.WorkQ)
	if err != nil {
		return err
	}
	if err = check(q.Messages == 2, "相同message_id仍应入队两次，ready=%d", q.Messages); err != nil {
		return err
	}
	sess, c, err := s.consumer("idempotent", 1)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, sess.Close()) }()
	for range 2 {
		d, err := c.Next(s.ctx)
		if err != nil {
			return err
		}
		if err = s.commitAndAck(c, d); err != nil {
			return err
		}
	}
	fmt.Fprintln(s.log, "DUPLICATE broker_deliveries=2 business_effects=1 => message_id是属性，幂等是业务约束")
	return check(s.ledger.Count(s.id("duplicate-id")) == 1, "重复消息产生重复业务效果")
}

// retryExperiment 先展示一次有上限的 transient nack/requeue，再用消息header
// 携带毒消息尝试次数。retry队列的TTL由真实broker调度，owner等待Next而非sleep。
func retryExperiment(s *scenario) (result error) {
	for _, id := range []string{s.id("transient"), s.id("poison")} {
		if err := s.pub.Publish(s.ctx, s.top.WorkX, "work", id, amqp.Table{"attempt": int32(0)}); err != nil {
			return err
		}
	}
	sess, c, err := s.consumer("retry", 1)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, sess.Close()) }()
	transientAttempts, poisonAttempts := 0, 0
	// 最多 5 次delivery：transient两次，poison初次+两次retry。任何额外循环都失败。
	for range 5 {
		d, err := c.Next(s.ctx)
		if err != nil {
			return err
		}
		switch d.MessageId {
		case s.id("transient"):
			transientAttempts++
			if transientAttempts == 1 {
				if err = d.Nack(false, true); err != nil {
					return err
				}
				fmt.Fprintln(s.log, "TRANSIENT attempt=1 nack(requeue=true)；本run只允许一次立即requeue")
			} else {
				if err = check(transientAttempts == 2 && d.Redelivered, "transient应只重投一次"); err != nil {
					return err
				}
				if err = s.commitAndAck(c, d); err != nil {
					return err
				}
			}
		case s.id("poison"):
			poisonAttempts++
			attempt, ok := d.Headers["attempt"].(int32)
			if err = check(ok && attempt >= 0 && attempt <= 2, "毒消息attempt header无效"); err != nil {
				return err
			}
			fmt.Fprintf(s.log, "POISON attempt=%d business_failed effects=0\n", attempt)
			if attempt < 2 {
				// 新消息attempt随broker保存，进程恢复不会把计数清零。先confirm新消息，
				// 再ack旧delivery；此窗口仍可重复，因此生产handler还需要业务幂等。
				if err = s.pub.Publish(s.ctx, s.top.RetryX, "retry", d.MessageId, amqp.Table{"attempt": attempt + 1}); err != nil {
					return err
				}
				if err = d.Ack(false); err != nil {
					return err
				}
			} else {
				if err = d.Nack(false, false); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("retry收到未知消息=%s", d.MessageId)
		}
	}
	// DLQ 使用真实basic.get读取；deadline/timer只是等待观察上限，不能代替证据。
	dead, err := awaitGet(s.ctx, s.pub.Ch, s.top.DeadQ)
	if err != nil {
		return err
	}
	if err = check(dead.MessageId == s.id("poison") && dead.Headers["x-death"] != nil && poisonAttempts == 3 && transientAttempts == 2, "DLQ内容或有限次数不符"); err != nil {
		return err
	}
	if err = dead.Ack(false); err != nil {
		return err
	}
	if _, err = Barrier(s.pub.Ch, s.top.DeadQ); err != nil {
		return err
	}
	fmt.Fprintf(s.log, "DLQ message_id=%s poison_attempts=3 x-death_present=true effects=%d transient_effects=%d\n", dead.MessageId, s.ledger.Count(s.id("poison")), s.ledger.Count(s.id("transient")))
	return check(s.ledger.Count(s.id("poison")) == 0 && s.ledger.Count(s.id("transient")) == 1, "失败处理不得产生成功业务效果")
}

// awaitReady 等到真实QueueInspect的ready数达到预期。关闭连接到服务端重投
// 涉及不同参与者，close返回不等于队列瞬时已变化；这里以可观察状态作为屏障。
func awaitReady(ctx context.Context, ch *amqp.Channel, q string, want int) (amqp.Queue, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := Barrier(ch, q)
		if err != nil {
			return state, err
		}
		if state.Messages == want {
			return state, nil
		}
		select {
		case <-ctx.Done():
			return state, ctx.Err()
		case <-ticker.C:
		}
	}
}

// awaitGet 由DLQ owner调用。Go timer仅控制再次只读观察broker的频率；成功来自
// actual basic.get delivery，不断言固定150ms或以sleep推测消息已经迁移。
func awaitGet(ctx context.Context, ch *amqp.Channel, q string) (amqp.Delivery, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		d, ok, err := ch.Get(q, false)
		if err != nil {
			return d, err
		}
		if ok {
			return d, nil
		}
		select {
		case <-ctx.Done():
			return d, ctx.Err()
		case <-ticker.C:
		}
	}
}
