package txcourse

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// taskResult 分开记录工作错误和 fn 自己的资源清理错误。预期 Canceled 不能
// 掩盖 Rollback/Conn.Close 的失败；owner 分类前必须同时检查这两个事实。
type taskResult struct {
	Err, CleanupErr error
}

// ownedTask 由创建它的实验函数独占。result 只交付一次工作结果；finished 是
// 所有 wrapper defer 完成后的广播。接收 result 不能证明 wrapper 已经退出。
// wrapperCleanup 只能在 finished 关闭之后读取，channel 关闭建立同步关系。
type ownedTask struct {
	cancel         context.CancelFunc
	result         <-chan taskResult
	finished       <-chan struct{}
	wrapperCleanup error
	pending        taskResult
	hasResult      bool
	joined         bool
	joinedErr      error
	joinedCleanup  error
}

// startOwned 由 cancelCase 和同步断言调用，fn 自己清理 SQL/Conn/Tx；cleanup 是
// wrapper 额外收尾。buffer1 避免 owner 提前离开时 worker 卡在结果发送。
func startOwned(ctx context.Context, fn func(context.Context) taskResult, cleanup func() error) *ownedTask {
	workCtx, cancel := context.WithCancel(ctx)
	result := make(chan taskResult, 1)
	finished := make(chan struct{})
	t := &ownedTask{cancel: cancel, result: result, finished: finished}
	go func() {
		// LIFO：cleanup -> cancel -> close(finished)。完成广播是最后一项。
		defer close(finished)
		defer cancel()
		if cleanup != nil {
			defer func() { t.wrapperCleanup = cleanup() }()
		}
		result <- fn(workCtx) // fn 的 defer 已执行，但 wrapper cleanup 还没有执行。
	}()
	return t
}

// join 由唯一 owner 调用，只以独立 ctx 等真正 Finished。prior 是 owner 已遇到的
// 原失败，不能因清理超时丢掉。超时后明确未 joined，后续可用新预算继续等待。
func (t *ownedTask) join(ctx context.Context, prior error) error {
	select {
	case <-t.finished:
		if !t.joined {
			if !t.hasResult {
				t.pending, t.hasResult = <-t.result, true
			}
			t.joinedCleanup = errors.Join(t.pending.CleanupErr, t.wrapperCleanup)
			t.joinedErr = errors.Join(t.pending.Err, t.joinedCleanup)
			t.joined = true
		}
		return errors.Join(prior, t.joinedErr)
	case <-ctx.Done():
		return errors.Join(prior, ErrJoin, ctx.Err())
	}
}

// stopAndJoin 在创建 worker 后马上 defer。取消是通知，join 才证明退出；
// Background 新预算防止已经取消的业务 ctx 使清理立即放弃。
func (t *ownedTask) stopAndJoin(err *error) {
	t.cancel()
	ctx, cancel := shortContext()
	defer cancel()
	hadJoined := t.joined
	joinedErr := t.join(ctx, nil)
	if !hadJoined {
		*err = errors.Join(*err, joinedErr)
	}
	// 正常路径已 join 并分类预期 Canceled；defer 再确认 Finished，但不把已处理的
	// 预期故障重新变成整个实验失败。提前 return 没分类时则必须保留所有原因。
}

// observe 只把 predicate 的实际状态作为屏障；ticker 是再次检查的节奏，
// 不是“等待固定时间所以 worker 应该到了”的调度猜测。owner 负责释放 timer。
func observe(ctx context.Context, label string, predicate func() (bool, error)) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		ready, err := predicate()
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待 %s 状态: %w", label, ctx.Err())
		case <-ticker.C:
		}
	}
}
