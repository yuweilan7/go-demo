// Package lifecycle 用真实 goroutine 和 channel 区分取消通知、观察取消、清理完成和等待退出。
package lifecycle

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// Worker 由 Start 创建，入口/测试是 owner。四个 channel 表示四项不同事实，不存业务消息。
// Started/Observed/Done 只有 worker 关闭；release 只有 owner 通过 ReleaseCleanup 关闭。
type Worker struct {
	Started  <-chan struct{}
	Observed <-chan struct{}
	Done     <-chan struct{}
	cancel   context.CancelFunc
	release  chan struct{}
	once     sync.Once
	cleaned  atomic.Bool
}

// Start 由演示/测试调用。child ctx 的取消范围只属于这个 worker；调用方必须 Stop、ReleaseCleanup、等 Done。
// 启动 goroutine 后本函数立即返回，返回值不是“后台已启动/已经退出”的证据，要等对应 channel。
func Start(parent context.Context) *Worker {
	ctx, cancel := context.WithCancel(parent)
	started, observed, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	w := &Worker{Started: started, Observed: observed, Done: done, cancel: cancel, release: make(chan struct{})}
	go func() {
		// defer 注册的是关闭 done 的动作：即便将来清理函数 panic，栈展开也会执行。
		// 本例没有 recover；不可据此推断 panic 后进程还能继续。
		defer close(done)
		close(started)
		<-ctx.Done() // 真正的等待点 1：Stop 或 parent 截止使 Done 关闭，worker 才恢复执行。
		close(observed)
		<-w.release           // 真正的等待点 2：取消不绕过清理屏障；owner 能稳定观察“已取消但尚未退出”。
		w.cleaned.Store(true) // 原子读写使 owner 可安全检查；只证明本例标记清理完成，不证明外部资源协议。
	}()
	return w
}

// Stop 由 owner 发停止通知。CancelFunc 幂等，但调用返回不能证明后台已观察通知，更不能证明 Done 已关闭。
func (w *Worker) Stop() { w.cancel() }

// ReleaseCleanup 由 owner 放开合成清理屏障。Once 允许成功路径和 t.Cleanup 都调用而不重复 close 导致 panic。
func (w *Worker) ReleaseCleanup() { w.once.Do(func() { close(w.release) }) }

// Cleaned 由 owner 在不同观察点读取实际清理标志，不通过“Stop 已返回”猜测它。
func (w *Worker) Cleaned() bool { return w.cleaned.Load() }

// Await 由真实时间下的入口/测试调用，等待指定事实或诊断上限。ctx 超时只让等待者返回，不会释放 worker。
// channel 参数为 <-chan，表示本函数没有发送/关闭的所有权；label 把失败定位到具体等待点。
func Await(ctx context.Context, signal <-chan struct{}, label string) error {
	select {
	case <-signal:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待 %s: %w", label, ctx.Err())
	}
}

// RunJob 由 TestBackgroundErrorReturnedToOwner 调用，演示后台错误回传，替代在 worker 中 t.Fatal。
// result 缓冲为 1，允许 worker 在 owner 提前超时后仍交付本次结果并退出；job 自己负责它内部的资源清理。
// 这里只启动一次有界、立即返回的课程 job；任意阻塞 job 仍必须另有取消协议，不能靠缓冲自动停止。
func RunJob(job func() error) <-chan error {
	result := make(chan error, 1)
	go func() {
		defer close(result)
		result <- job() // 同步调用 job；其 defer 执行完、返回 error 后，才把 error 发给 owner。
	}()
	return result
}
