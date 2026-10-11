package lesson

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// runTraps 由 Run 调用，复现三个可编译但违背业务取消契约的写法。
// 每个危险例都有独立 watchdog/恢复 owner；反例可被观察，不向进程外遗留任务。
func runTraps(scope context.Context, t *trace) error {
	if err := ignoredTimeout(scope, t); err != nil {
		return err
	}
	if err := backgroundLoop(scope, t, false); err != nil {
		return err
	}
	if err := backgroundLoop(scope, t, true); err != nil {
		return err
	}
	return lateCancel(scope, t)
}

// ignoredTimeout 由 runTraps 调用，证明“有 timeout 对象”不等于函数会提前返回。
// ctx.Done 到期后，函数仍在另一个 channel 上等；owner 放行后效果才出现。
func ignoredTimeout(scope context.Context, t *trace) (resultErr error) {
	ctx, cancel := context.WithTimeout(scope, 50*time.Millisecond)
	defer cancel()
	ready, gate, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := newOnceGate(gate)
	var effects atomic.Int32
	go unobservantWork(scope, ctx, ready, gate, done, &effects, t)
	defer func() {
		release()
		resultErr = errors.Join(resultErr, join(done, "不观察 ctx 的任务"))
	}()
	if err := wait(scope, ready, "unobservant ready"); err != nil {
		return err
	}
	if err := wait(scope, ctx.Done(), "业务 timeout"); err != nil {
		return err
	}
	if err := check(!closed(done) && effects.Load() == 0, "创建 timeout 但不观察它，函数不应自行返回"); err != nil {
		return err
	}
	t.event("timeout-owner", "ctx.Err=%v，但 unobservantWork 仍等 gate；cancel 不会杀 goroutine", ctx.Err())
	release()
	if err := wait(scope, done, "unobservant 最终退出"); err != nil {
		return err
	}
	return check(effects.Load() == 1, "timeout 后危险函数仍执行了效果")
}

// unobservantWork 由 ignoredTimeout 创建。ctx 是业务取消范围，但反例不把它放进等待。
// scope 是实验安全范围；观察 ctx.Err 仅用于效果后的日志，不能撤销已做的效果。
// effects 是共享计数器指针，atomic 避免 owner 查询时形成数据竞争。
func unobservantWork(scope, ctx context.Context, ready chan<- struct{}, gate <-chan struct{}, done chan<- struct{}, effects *atomic.Int32, t *trace) {
	defer close(done)
	defer t.event("unobservant", "defer 清理完成")
	close(ready)
	if wait(scope, gate, "unobservant gate") != nil {
		return
	}
	effects.Add(1)
	t.event("unobservant", "效果已发生，才读取 ctx.Err=%v；这不是取消修复", ctx.Err())
}

// backgroundLoop 由 runTraps 调用。fixed=false 时循环中的每个任务都从 Background 派生，
// 即使它们各自实现 Done 观察，也收不到本次 request 的取消；fixed=true 才传 request。
// 它拥有三个 child cancel 和 done；循环结束/函数返回不会自动替它等待 goroutine。
func backgroundLoop(scope context.Context, t *trace, fixed bool) (resultErr error) {
	request, cancelRequest := context.WithCancel(scope)
	defer cancelRequest()
	ready := make(chan struct{}, 3)
	gate := make(chan struct{})
	release := newOnceGate(gate)
	results := make(chan error, 3)
	children := make([]context.Context, 0, 3)
	cancels := make([]context.CancelFunc, 0, 3)
	dones := make([]chan struct{}, 0, 3)
	var effects atomic.Int32
	for index := 0; index < 3; index++ {
		parent := context.Background()
		if fixed {
			parent = request
		}
		child, cancelChild := context.WithTimeout(parent, 2*time.Second)
		children = append(children, child)
		cancels = append(cancels, cancelChild)
		done := make(chan struct{})
		dones = append(dones, done)
		// 参数按值传入 goroutine：index 是本次编号，ctx 接口仍引用对应 child 状态。
		// 不是闭包在未来随意读取一个不断变化的业务变量。
		go loopWorker(scope, child, index, ready, gate, results, done, &effects, t)
	}
	defer func() {
		cancelRequest()
		for _, cancel := range cancels {
			cancel()
		}
		release()
		for _, done := range dones {
			resultErr = errors.Join(resultErr, join(done, "循环 worker"))
		}
	}()
	for index := 0; index < 3; index++ {
		select {
		case <-ready:
		case <-scope.Done():
			return scope.Err()
		}
	}
	cancelRequest()
	for _, child := range children {
		if fixed {
			if err := wait(scope, child.Done(), "修复版 child 取消"); err != nil {
				return err
			}
		} else if err := check(child.Err() == nil, "Background child 不会收到 request 取消"); err != nil {
			return err
		}
	}
	t.event("loop-owner", "fixed=%t request 已取消；危险版 child 仍活跃，修复版 child 已取消", fixed)
	// 修复版保持 gate 不可用，只让 Done 分支就绪，避免用随机 select 次序断言。
	if !fixed {
		release()
	}
	for index := 0; index < 3; index++ {
		select {
		case err := <-results:
			if fixed && !errors.Is(err, context.Canceled) {
				return check(false, "修复循环应返回 Canceled: %v", err)
			}
			if !fixed && err != nil {
				return err
			}
		case <-scope.Done():
			return scope.Err()
		}
	}
	for _, done := range dones {
		if err := wait(scope, done, "loop worker done"); err != nil {
			return err
		}
	}
	want := int32(3)
	if fixed {
		want = 0
	}
	t.event("loop-owner", "fixed=%t effects=%d；已 join 三个任务", fixed, effects.Load())
	return check(effects.Load() == want, "循环 fixed=%t，effects 应为 %d", fixed, want)
}

// loopWorker 由 backgroundLoop 创建，当前真正的阻塞点是 gate/ctx.Done/scope.Done select。
// results 为 3 个任务留足容量；worker 拥有 done 的关闭，owner 拥有 gate 与 cancel。
func loopWorker(scope, ctx context.Context, index int, ready chan<- struct{}, gate <-chan struct{}, results chan<- error, done chan<- struct{}, effects *atomic.Int32, t *trace) {
	defer close(done)
	ready <- struct{}{}
	var err error
	select {
	case <-gate:
		effects.Add(1)
	case <-ctx.Done():
		err = ctx.Err()
	case <-scope.Done():
		err = scope.Err()
	}
	t.event("loop-worker", "index=%d 返回 err=%v", index, err)
	results <- err
}

// lateCancel 由 runTraps 调用，复现“业务已经返回，却把 cancel 留到很晚才调用”。
// 本函数故意仍保存 cancel 给恢复 owner，既能观察未释放状态，又不真正遗留长 timer。
// go vet 能检查一些丢弃 CancelFunc 的路径，却无法证明这种跨函数 owner 一定及时调用。
func lateCancel(scope context.Context, t *trace) error {
	ctx, recoveryCancel := completedButStillLeased(scope)
	defer recoveryCancel()
	if err := check(ctx.Err() == nil && !closed(ctx.Done()), "业务返回后尚未调用 cancel，范围仍活跃"); err != nil {
		return err
	}
	t.event("lease-owner", "危险版：业务已完成，未 cancel 的一分钟 child 仍活跃；恢复 owner 现在 cancel")
	recoveryCancel()
	if err := wait(scope, ctx.Done(), "恢复 cancel"); err != nil {
		return err
	}
	fixed := completedAndReleased(scope)
	if err := check(errors.Is(fixed.Err(), context.Canceled), "修复版业务返回前已执行 defer cancel"); err != nil {
		return err
	}
	t.event("lease-owner", "修复版：取得 cancel 后立即 defer，本次成功也会释放取消范围")
	return nil
}

// completedButStillLeased 模拟业务 callee 把 cancel 所有权交回上层，而上层忘了及时调用。
// ctx 是观测接口，CancelFunc 是清理权；返回它使正常 go vet 不误判成故意遗失，
// 但业务返回不调用 cancel 的事实仍由 lateCancel 验证。生产接口应明确 owner 或内部 defer。
func completedButStillLeased(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	return ctx, cancel
}

// completedAndReleased 模拟短业务 callee，自己拥有派生范围，在成功返回也释放 timer。
// 返回 ctx 仅供本实验查询退出事实；真实业务通常只返回自己的结果和 error。
func completedAndReleased(parent context.Context) context.Context {
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	return ctx
}
