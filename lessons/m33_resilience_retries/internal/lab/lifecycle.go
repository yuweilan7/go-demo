// Package lab 通过真实 HTTP/1.1 与当前进程的合成订单副作用讲解依赖治理。
package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Trace 是整组实验共享的日志 owner；Mutex 保护 writer，让不同 handler/任务日志不交错写。
// 参数按值复制，但 *Trace 指向同一个锁和输出对象；它不是自动并发安全的任意 io.Writer。
type Trace struct {
	mu    sync.Mutex
	out   io.Writer
	start time.Time
}

// event 由 owner、handler 或任务调用；日志只说明当前事件，真正断言还要读取账本和完成信号。
func (t *Trace) event(who, format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.out, "+%04dms %-16s %s\n", time.Since(t.start).Milliseconds(), who, fmt.Sprintf(format, args...))
}

// wait 同步等一次关闭广播，ctx 只限制观察者的等待。它不取消产生 signal 的工作。
// <-chan 表示本函数只有接收权限，不会替 owner 关闭别人的 channel。
func wait(ctx context.Context, signal <-chan struct{}, label string) error {
	select {
	case <-signal:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待 %s: %w", label, ctx.Err())
	}
}

// join 用新的独立有限预算确认真正 Done。已取消的工作 ctx 不能用于收尾等待。
func join(signal <-chan struct{}, label string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return wait(ctx, signal, label)
}

// check 根据业务不变量产生普通 error；实验失败会返回入口，而不是 recover/panic 伪装成功。
func check(ok bool, format string, args ...any) error {
	if !ok {
		return fmt.Errorf("断言失败: "+format, args...)
	}
	return nil
}

// closed 是瞬时观察，只在 gate 保证工作不能越过当前阶段时用于证明尚未完成。
func closed(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

// pause 是可取消 timer 等待。timer 在本函数返回时 Stop；用于真实退避/窗口到期，不猜调度。
func pause(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ctx.Err() // 即使 timer 与取消同时可读，也不继续已知取消后的下一次尝试。
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Gate 区分服务端进入、owner 放行、服务端观察取消、handler 返回路径四项事实。
// 所有关闭都用 Once：重复请求/早返回清理不会 double close；Done 仍只是该路由的观察信号。
type Gate struct {
	Entered, Release, Canceled, Done chan struct{}
	enter, open, observed, finish    sync.Once
}

// newGate 由 fixture/实验创建；未关闭 channel 是当前还没有发生的事实。
func newGate() *Gate {
	return &Gate{Entered: make(chan struct{}), Release: make(chan struct{}), Canceled: make(chan struct{}), Done: make(chan struct{})}
}

// Open 由正常流程或失败清理调用，只放行阻塞工作，必须另外 join Done。
func (g *Gate) Open() { g.open.Do(func() { close(g.Release) }) }

// taskResult 是 worker 一次性返回给 owner 的业务结果；它不是 goroutine 完成广播。
type taskResult struct {
	Reply Reply
	Stats Stats
	Err   error
}

// Task 拥有 child ctx、一个 result 和独立 Done。结果容量1使 owner 早退不会卡住 worker 发送。
// cleanupErr 只在 Done 关闭前写入；owner 接收 Done 后读取，channel 形成同步关系。
type Task struct {
	cancel      context.CancelFunc
	Result      chan taskResult
	Done        chan struct{}
	JoinWaiting chan struct{}
	joining     sync.Once
	cleanupErr  error
}

var ownedTasks atomic.Int64

// startTask 由并发实验/测试调用。fn 使用 child 的工作预算；after 仅用于清理故障实验。
// caller 创建后必须立即注册 StopAndJoin，所有早返回/t.Fatal 路径都要 cancel/unblock/join。
func startTask(ctx context.Context, fn func(context.Context) taskResult, after func() error) *Task {
	child, cancel := context.WithCancel(ctx)
	t := &Task{cancel: cancel, Result: make(chan taskResult, 1), Done: make(chan struct{}), JoinWaiting: make(chan struct{})}
	ownedTasks.Add(1)
	go func() {
		// 按 LIFO：工作/附加清理完成后减计数、cancel，最后关闭 Done；接收 result 还可能早一步。
		defer close(t.Done)
		defer cancel()
		defer ownedTasks.Add(-1)
		t.Result <- fn(child)
		if after != nil {
			t.cleanupErr = after()
		}
	}()
	return t
}

// StopAndJoin 是资源 owner 的收尾动作：取消只是通知，unblock 解除实验屏障，然后独立等待。
// 不再次读取 Result，因此 owner 先消费业务结果之后仍能反复 join；清理 error 不会被丢弃。
func (t *Task) StopAndJoin(unblock func()) error {
	t.cancel()
	if unblock != nil {
		unblock()
	}
	if !closed(t.Done) {
		// JoinWaiting只记录owner确实到达完成等待，不能当Done。测试在清理gate仍关时观测此信号。
		t.joining.Do(func() { close(t.JoinWaiting) })
	}
	if err := join(t.Done, "owned task Done"); err != nil {
		return err
	}
	return t.cleanupErr
}

// receive 只等业务结果；观察ctx到期返回错误时，调用者的 defer 仍负责等待 Task.Done。
func receive(ctx context.Context, t *Task) (taskResult, error) {
	select {
	case r := <-t.Result:
		return r, nil
	case <-ctx.Done():
		return taskResult{}, fmt.Errorf("结果观察结束，任务尚未 join: %w", ctx.Err())
	}
}

// combine 是具名返回实验的 defer 帮手；保留主错误和清理错误，用 errors.Is 可分别查询原因。
func combine(result *error, cleanup error) { *result = errors.Join(*result, cleanup) }
