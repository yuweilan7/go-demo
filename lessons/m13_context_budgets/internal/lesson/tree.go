package lesson

import (
	"context"
	"errors"
	"time"
)

// runTree 由 Run 调用，演示 root -> request -> children 的通知树。
// 它持有所有 cancel 和任务完成屏障；worker 只观察 ctx，不反向取消 owner。
func runTree(scope context.Context, t *trace) (resultErr error) {
	background := context.Background()
	todo := context.TODO()
	if err := check(background.Done() == nil && todo.Done() == nil, "根 Context 的 Done 应为 nil"); err != nil {
		return err
	}
	t.event("root", "Background/TODO: Done=nil Err=nil；nil channel 在 select 中禁用该分支")

	root, cancelRoot := context.WithCancelCause(scope)
	defer cancelRoot(nil)
	request, cancelRequest := context.WithCancelCause(root)
	defer cancelRequest(nil)
	childA, cancelA := context.WithCancelCause(request)
	defer cancelA(nil)
	childB, cancelB := context.WithCancel(request)
	defer cancelB()
	localCause := errors.New("库存分支主动放弃")
	rootCause := errors.New("进程 owner 关闭请求范围")
	// A 先被自己的 owner 取消，其首次 Cause 固定下来；不会取消父/兄弟。
	cancelA(localCause)
	if err := wait(scope, childA.Done(), "child-A Done"); err != nil {
		return err
	}
	if err := check(root.Err() == nil && request.Err() == nil && childB.Err() == nil, "子取消不能向上或横向传播"); err != nil {
		return err
	}
	t.event("child-A", "Err=%v Cause=%v；request/child-B 仍活跃", childA.Err(), context.Cause(childA))

	ready := make(chan struct{})
	observed := make(chan struct{})
	releaseCleanup := make(chan struct{})
	done := make(chan struct{})
	cleanupOnce := newOnceGate(releaseCleanup)
	// goroutine 的 owner 是 runTree。worker 捕获的是 childB 接口值和共享 channel，
	// 不是 Context 深拷贝；同一个底层取消状态允许被多个 goroutine 并发读取。
	go cancellationWorker(scope, childB, ready, observed, releaseCleanup, done, t)
	defer func() {
		cleanupOnce()
		resultErr = errors.Join(resultErr, join(done, "tree worker 收尾"))
	}()
	if err := wait(scope, ready, "worker ready"); err != nil {
		return err
	}
	t.event("root-owner", "调用 cancelRoot(rootCause)，这里只发停止通知")
	cancelRoot(rootCause)
	if err := wait(scope, observed, "worker 已观察取消"); err != nil {
		return err
	}
	if err := check(!closed(done), "观察取消后仍被 cleanup gate 挡住，不应已经完成"); err != nil {
		return err
	}
	if err := check(errors.Is(childB.Err(), context.Canceled) && errors.Is(context.Cause(childB), rootCause), "B 应继承父级首次原因"); err != nil {
		return err
	}
	if err := check(errors.Is(context.Cause(childA), localCause), "A 已取消，后续父取消不改写其首次原因"); err != nil {
		return err
	}
	t.event("owner", "cancel 已返回，worker 已观察，但清理未完成；A 保留自己的 Cause")
	cleanupOnce()
	if err := wait(scope, done, "worker done"); err != nil {
		return err
	}
	t.event("owner", "收到 done：worker 已执行 defer 清理并返回")

	deadlineCtx, deadlineCancel := context.WithDeadline(scope, time.Now().Add(time.Hour))
	deadlineCancel()
	if err := check(errors.Is(deadlineCtx.Err(), context.Canceled), "到期前手动取消应为 Canceled"); err != nil {
		return err
	}
	timeoutCause := errors.New("报价总预算耗尽")
	timed, cancelTimed := context.WithTimeoutCause(scope, 40*time.Millisecond, timeoutCause)
	defer cancelTimed()
	if err := wait(scope, timed.Done(), "timeout Done"); err != nil {
		return err
	}
	if err := check(errors.Is(timed.Err(), context.DeadlineExceeded) && errors.Is(context.Cause(timed), timeoutCause), "deadline 分类与业务 Cause 应分别保留"); err != nil {
		return err
	}
	t.event("deadline", "Err=%v Cause=%v；WithDeadline 手动提前取消的 Err=Canceled", timed.Err(), context.Cause(timed))
	return nil
}

// cancellationWorker 由 runTree 创建，只拥有自己的执行和清理，不拥有 cancel。
// ready 是开始运行，observed 是确已从 Done 返回，done 是清理后的最终结束。
// 三个不同事实不能共用一个“完成”channel，否则 owner 会过早返回。
func cancellationWorker(scope, ctx context.Context, ready, observed chan<- struct{}, releaseCleanup <-chan struct{}, done chan<- struct{}, t *trace) {
	// 后注册的 defer 先执行：清理日志在 close(done) 之前发生。
	defer close(done)
	defer t.event("worker", "defer：请求专属资源已清理")
	close(ready)
	<-ctx.Done()
	t.event("worker", "从 <-ctx.Done() 返回：观察取消 Err=%v Cause=%v", ctx.Err(), context.Cause(ctx))
	close(observed)
	// 这是教学用清理屏障；scope 是独立 watchdog，异常时也有退出上限。
	_ = wait(scope, releaseCleanup, "释放 cleanup gate")
}
