package course

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// CleanupLimit 是每个实验清理阶段的独立兜底上限。屏障建立因果顺序，时间只防止回归永久挂起。
const CleanupLimit = 3 * time.Second

// StateRow 是同一状态下三种操作的独立观察：每列都使用新 channel，避免上一列改变下一列状态。
type StateRow struct {
	State   string
	Receive string
	Send    string
	Close   string
}

// ChannelStates 由 demo/测试调用。nil/empty/full/closed 等状态由 factory 在当前 goroutine
// 构造，没有并发修改，所以非阻塞探测有稳定含义；直接 nil send/receive 的永久等待不实际执行。
func ChannelStates(trace *Trace) []StateRow {
	factories := []struct {
		name string
		make func() chan int
	}{
		{"nil", func() chan int { return nil }},
		{"open unbuffered empty", func() chan int { return make(chan int) }},
		{"open buffered empty cap=1", func() chan int { return make(chan int, 1) }},
		{"open buffered zero partial", func() chan int { ch := make(chan int, 2); ch <- 0; return ch }},
		{"open buffered zero full", func() chan int { ch := make(chan int, 1); ch <- 0; return ch }},
		{"closed buffered zero", func() chan int { ch := make(chan int, 1); ch <- 0; close(ch); return ch }},
		{"closed drained", func() chan int { ch := make(chan int); close(ch); return ch }},
	}
	rows := make([]StateRow, 0, len(factories))
	for _, factory := range factories {
		row := StateRow{factory.name, receiveOutcome(factory.make()), sendOutcome(factory.make()), closeOutcome(factory.make())}
		rows = append(rows, row)
		trace.Log("state", "state_row", "%s | receive=%s | send=%s | close=%s", row.State, row.Receive, row.Send, row.Close)
	}
	return rows
}

// receiveOutcome 在状态表中尝试一次接收。default 说明“这次不能立即完成”，
// 不是证明 channel 永远不会有发送者；当前工厂没有并发参与者，状态不会自行改变。
func receiveOutcome(ch <-chan int) string {
	select {
	case value, ok := <-ch:
		return fmt.Sprintf("value=%d ok=%t", value, ok)
	default:
		return "would block"
	}
}

// sendOutcome 在状态表中尝试一次发送。向 closed 发送属于 runtime panic，
// recover 必须位于发生 panic 的同一 goroutine 的 deferred function，才能把它记为一格观察。
func sendOutcome(ch chan<- int) (outcome string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = fmt.Sprintf("panic: %v", recovered)
		}
	}()
	select {
	case ch <- 7:
		return "accepted"
	default:
		return "would block"
	}
}

// closeOutcome 每次获得单独的 channel。具名返回 outcome 是本函数的结果变量，
// panic 展开时 defer 的 recover 修改它，之后函数直接返回；不会从 close(ch) 后重新继续。
func closeOutcome(ch chan int) (outcome string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = fmt.Sprintf("panic: %v", recovered)
		}
	}()
	close(ch)
	return "closed successfully"
}

// DrainClosed 证明合法零值与结束必须看 ok，close 不清空缓冲。本函数拥有 ch 的全部操作。
func DrainClosed(trace *Trace) []struct {
	Value int
	OK    bool
} {
	ch := make(chan int, 2)
	ch <- 0
	ch <- 7
	close(ch)
	var observations []struct {
		Value int
		OK    bool
	}
	for index := 0; index < 4; index++ {
		value, ok := <-ch
		observations = append(observations, struct {
			Value int
			OK    bool
		}{value, ok})
		trace.Log("drain", "receive", "read=%d value=%d ok=%t", index+1, value, ok)
	}
	return observations
}

// Rendezvous 由 demo 调用，拥有一个发送 goroutine 和两个屏障。started 只证明发送函数
// 已到达发送前，不冒充 goroutine 栈上的阻塞状态；无接收者时发送语义保证不能完成。
func Rendezvous(ctx context.Context, trace *Trace) (err error) {
	ch := make(chan int)
	started, done := make(chan struct{}), make(chan struct{})
	consumerState, seenBySender := 0, 0
	defer func() { err = errors.Join(err, cleanupAwait(done, "会合发送者清理")) }()
	go func() {
		defer close(done)
		close(started)
		select {
		case ch <- 42:
			// consumerState 的写在接收前；无缓冲 receive 在 send 返回前同步，所以这里可安全读取。
			seenBySender = consumerState
			trace.Log("sender", "send_returned", "无缓冲 send 返回：接收已参与会合，不证明业务处理完成")
		case <-ctx.Done():
			trace.Log("sender", "cancel_observed", "%v", ctx.Err())
		}
	}()
	if err := await(ctx, started, "发送前屏障"); err != nil {
		return err
	}
	select {
	case <-done:
		return fmt.Errorf("在接收前发送者已经结束: %v", ctx.Err())
	default:
		trace.Log("owner", "no_receiver", "没有接收者，send 尚不能完成")
	}
	// owner 先写业务状态，再参与无缓冲会合。sender 读取在 send 返回之后，不能套用到 buffered ch。
	consumerState = 77
	select {
	case value := <-ch:
		trace.Log("consumer", "handoff_received", "value=%d；现在消费方才开始业务处理", value)
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := await(ctx, done, "发送者退出"); err != nil {
		return err
	}
	if seenBySender != 77 {
		return fmt.Errorf("无缓冲反向同步失效: seen=%d", seenBySender)
	}
	trace.Log("owner", "reverse_sync", "consumerState=77 写在 receive 前，sender 在 send 返回后安全读到 %d", seenBySender)
	return nil
}

// RangeNeedsClose 展示危险版本的源函数已返回，range 仍不结束；owner 在收到 sourceReturned
// 后知道没有后续发送，执行修复 close 并等待 receiver 退出。故障窗口有屏障和取消兜底。
func RangeNeedsClose(ctx context.Context, trace *Trace) (err error) {
	ch := make(chan int, 1)
	sourceReturned, firstSeen, receiverDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	// 此 goroutine 故意没有 close(ch)。它完成最后一次发送而返回这一事实，不会改变 ch 的开关状态。
	go func() { ch <- 0; close(sourceReturned) }()
	go func() {
		defer close(receiverDone)
		for value := range ch {
			trace.Log("range-reader", "range_value", "收到合法零值 %d", value)
			close(firstSeen) // 只有一个值，所以这个通知恰好发一次。
		}
		trace.Log("range-reader", "range_returned", "只有 close 且排空才使 range 自然结束")
	}()
	// 无论后面断言是否失败，都先确认来源完成最后发送，再关闭并等待 reader 收尾。
	defer func() {
		if sourceErr := cleanupAwait(sourceReturned, "range 来源最后发送"); sourceErr != nil {
			err = errors.Join(err, sourceErr)
			return // 未证明发送完成就不能 close；此处保留明确的清理失败。
		}
		close(ch)
		err = errors.Join(err, cleanupAwait(receiverDone, "range 接收者清理"))
	}()
	if err := await(ctx, sourceReturned, "来源返回"); err != nil {
		return err
	}
	if err := await(ctx, firstSeen, "range 收到第一条"); err != nil {
		return err
	}
	select {
	case <-receiverDone:
		return fmt.Errorf("尚未 close 的 range 不应自然结束")
	default:
		trace.Log("owner", "range_still_open", "来源已返回且值已收，range 仍不能结束；现在由已知无后续发送的 owner 修复 close")
	}
	return nil // 返回前 defer 才 close(ch)，receiver 结束后才真正返回 caller。
}
