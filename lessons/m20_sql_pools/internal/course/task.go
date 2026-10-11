package course

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// outcome 是一次 worker 的值结果。Err 是主操作错误，CleanupErr 是资源收尾错误；
// 两者独立保留，避免“预期取消”掩盖 Rollback/Conn.Close 的真实失败。
type outcome struct {
	Value      int64
	Err        error
	CleanupErr error
}

// task 的唯一 owner 是创建它的实验函数。result容量1只交付一次工作结果；
// finished是不可消费的完成广播，wrapper的全部清理/cancel defer之后最后关闭。
// pending/joined只由owner读写；wrapperCleanupErr只在收到finished关闭后读取，
// channel关闭建立同步关系，不能在只收到result时提前读取它。
type task struct {
	cancel            context.CancelFunc
	result            <-chan outcome
	finished          <-chan struct{}
	pending           *outcome
	joined            *outcome
	wrapperCleanupErr error
}

// startTask 由池/SQL 故障实验调用。timeout 控制工作预算，fn 在独立 goroutine 中
// 执行同步 DB/Tx I/O；创建 goroutine 既不自动等待也不延长 owner 的生命周期。
func startTask(parent context.Context, timeout time.Duration, fn func(context.Context) outcome) *task {
	return startOwnedTask(parent, timeout, fn, nil)
}

// startOwnedTask是startTask与确定性资源门测试共同使用的wrapper入口。
// fn负责SQL/业务资源，cleanup可负责wrapper附加资源；后者在结果已发送后仍可能
// 等待/失败，所以“结果可读”必须与“全部清理完毕”使用两个不同channel表达。
func startOwnedTask(parent context.Context, timeout time.Duration, fn func(context.Context) outcome, cleanup func() error) *task {
	workCtx, cancel := context.WithTimeout(parent, timeout)
	result := make(chan outcome, 1)
	finished := make(chan struct{})
	worker := &task{cancel: cancel, result: result, finished: finished}
	go func() {
		// defer后注册先执行：cleanup→cancel→close(finished)。finished从不发送值，
		// 关闭后任何观察者都可重复等待，读取result不会消耗完成事实。
		defer close(finished)
		defer cancel()
		if cleanup != nil {
			defer func() { worker.wrapperCleanupErr = cleanup() }()
		}
		result <- fn(workCtx) // fn自己的defer已执行，wrapper的defer此时尚未执行。
	}()
	return worker
}

// join只由owner调用。它先等finished，再读取/合并结果并缓存；即使result已被
// 提前消费到pending，或已经有joined缓存，也必须确认finished已关闭。
// ctx必须是独立预算，不能用已经DeadlineExceeded的工作ctx来“等待清理”。
func (worker *task) join(ctx context.Context) (outcome, error) {
	select {
	case <-worker.finished:
		// 这里才有wrapper全部清理完成的同步事实，result或pending不能替代它。
	case <-ctx.Done():
		return outcome{}, fmt.Errorf("owner 等待 worker Finished: %w", ctx.Err())
	}
	if worker.joined != nil {
		return *worker.joined, nil
	}
	if worker.pending == nil {
		select {
		case result := <-worker.result:
			worker.pending = &result
		default:
			// wrapper先发result，最后关闭finished；这里无结果表示内部契约被破坏，
			// 直接返回诊断，不能在一个不可能再发送的channel上永久等待。
			return outcome{}, errors.New("Finished已关闭但工作结果缺失")
		}
	}
	result := *worker.pending
	result.CleanupErr = errors.Join(result.CleanupErr, worker.wrapperCleanupErr)
	worker.joined = &result
	return result, nil
}

// stopAndJoin 注册在 startTask 后立即 defer。所有提前 return 都先发取消，再以新
// 的5秒预算等待真正退出；主错误与未读 worker 错误/清理错误均保留在错误链中。
func (worker *task) stopAndJoin(mainErr *error) {
	worker.cancel()
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hadJoined := worker.joined != nil
	result, joinErr := worker.join(cleanupCtx)
	if !hadJoined && result.Err != nil {
		*mainErr = errors.Join(*mainErr, fmt.Errorf("提前退出时 worker 结果: %w", result.Err))
	}
	*mainErr = errors.Join(*mainErr, joinErr, result.CleanupErr)
}

// observe 为实验 owner 提供可观察屏障。ticker 只是再次查看状态的节奏；
// 只有 predicate 返回 true 才认为条件已发生，不用固定 sleep 猜测调度或 I/O。
func observe(ctx context.Context, label string, predicate func() (bool, error)) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		ok, err := predicate()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待 %s 屏障: %w", label, ctx.Err())
		case <-ticker.C:
		}
	}
}

// joinWithin 是正常路径等待 worker 的入口，仍使用独立预算而非该 worker 的 ctx。
func joinWithin(worker *task) (outcome, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return worker.join(ctx)
}
