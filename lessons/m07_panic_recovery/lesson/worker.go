package lesson

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrWorkerCleanupTimeout 表示 owner 已用完独立收尾预算，仍未确认 worker 退出。
// 它与原调用 context 的取消原因一起返回；不能把收尾超时写成“已清理成功”。
var ErrWorkerCleanupTimeout = errors.New("worker 独立收尾预算耗尽")

// 这是进入收尾时重新开始计算的时间长度，不是调用方业务 context 剩余的 deadline。
const workerCleanupBudget = 3 * time.Second

// TaskResult 是 worker 完成后交给 owner 的一次性值。
// Failure 保存 panic 栈；State=FAILED 表示不能当作成功；Joined 只在 owner 观察 done 后为 true。
// Reserved 只有 Joined=true 时才是清理后的共享状态快照；收尾超时时不能读取活跃 worker 的状态。
type TaskResult struct {
	TaskID   string
	State    string
	Reserved int
	Failure  *PanicFailure
	Events   []string
	Joined   bool
}

// inventoryState 模拟 owner 与 worker 都持有指针的库存对象。
// 本实验仅有一个 writer；owner 必须等 done 后再读，不能因为 recover 了就提前检查它。
type inventoryState struct{ reserved int }

// RunWorker 是任务 owner，由 demo/test 调用，拥有 results、done 两个 channel。
// ctx 限定业务等待；任务启动后即使 ctx 取消，owner 仍用另一个三秒预算等 done。
// 取消通知不会强杀 goroutine，也不能成为取消清理等待的理由。
func RunWorker(ctx context.Context, repair bool) (TaskResult, error) {
	return runWorker(ctx, repair, workerCleanupBudget, nil)
}

// workerHooks 只给同包同步实验提供可观察屏障，正常入口传 nil。
// beforeCleanup 停在任务恢复后/释放预占前；beforeExit 停在外层退出清理/close(done)前。
// afterResult 标记 owner 已拿到结果；onCancel 标记 owner 从哪个等待阶段转入收尾。
// onTaskComplete 接收 worker 独占的完成值；done 的创建权由测试交给 owner，关闭权仍只属 worker。
// 这些回调只协调测试，不承担业务恢复，也不使用 sleep 让调度“碰巧按顺序”。
type workerHooks struct {
	beforeCleanup  func()
	beforeExit     func()
	afterResult    func()
	onCancel       func(stage string, cleanupCtx context.Context)
	onTaskComplete func(TaskResult)
	done           chan struct{}
}

// runWorker 是 RunWorker 与有屏障测试共用的真实实现，没有另造取消逻辑替身。
// cleanupBudget 由本函数的 owner 策略提供，不从可能已取消的 ctx 继承。
func runWorker(ctx context.Context, repair bool, cleanupBudget time.Duration, hooks *workerHooks) (TaskResult, error) {
	if ctx.Err() != nil {
		// 启动前即可确认取消时，不创建 goroutine；取消与这次检查之后的启动仍可能竞争。
		// 一旦实际启动，下面两个取消分支都必须承担 join 责任。
		return TaskResult{}, workerContextError(ctx, "启动前取消")
	}
	if cleanupBudget <= 0 {
		return TaskResult{}, errors.New("worker 收尾预算必须大于零")
	}
	results := make(chan TaskResult, 1)
	done := make(chan struct{}) // struct{} 不带数据；关闭它表示 goroutine 已走完退出清理。
	if hooks != nil && hooks.done != nil {
		done = hooks.done
	}
	inventory := &inventoryState{}
	go func() {
		// 注册在最外层，任务的恢复、预占清理、结果发送及外层收尾先完成，再 close(done)。
		defer func() {
			if hooks != nil && hooks.beforeExit != nil {
				hooks.beforeExit()
			}
			close(done)
		}()
		result := runTask(inventory, repair, hooks)
		if hooks != nil && hooks.onTaskComplete != nil {
			hooks.onTaskComplete(result)
		}
		results <- result
		// 唯一一个结果放入容量 1 的缓冲，不需要 owner 此刻仍在业务接收分支。
		// 但缓冲只避免发送卡住，不能替代 owner 等待 done 的退出证明。
	}()
	var result TaskResult
	select {
	case result = <-results:
		// 收到结果证明 runTask 已返回，尚需观察 done 才确认外层 defer 也已完成。
		if hooks != nil && hooks.afterResult != nil {
			hooks.afterResult()
		}
	case <-ctx.Done():
		return finishCanceledWorker(ctx, "等待结果时取消", cleanupBudget, done, results, inventory, result, false, hooks)
	}
	select {
	case <-done:
		// channel 关闭与接收建立同步；现在读共享指针指向的库存状态不会和 worker 写入并发。
		result.Reserved = inventory.reserved
		result.Joined = true
		if ctx.Err() != nil {
			// 结果与取消同时就绪时 select 可能选 done，仍保留已经观察到的调用取消。
			return result, workerContextError(ctx, "完成时观察到取消")
		}
		return result, nil
	case <-ctx.Done():
		return finishCanceledWorker(ctx, "等待退出时取消", cleanupBudget, done, results, inventory, result, true, hooks)
	}
}

// workerContextError 同时保留 context 的状态与自定义 cause。
// WithCancelCause 的 cause 可能是业务请求中止原因；只返回 ctx.Err() 会丢掉这个信息。
// context 自身提供的 Err 是固定取消哨兵，Cause 相同时直接包装，避免重复消息。
func workerContextError(ctx context.Context, stage string) error {
	if cause := context.Cause(ctx); cause != ctx.Err() {
		return fmt.Errorf("owner %s: %w", stage, errors.Join(ctx.Err(), cause))
	}
	return fmt.Errorf("owner %s: %w", stage, ctx.Err())
}

// finishCanceledWorker 由 runWorker 的两个取消分支调用，继续拥有这个已启动 worker。
// received 指出是否已经接走唯一结果；done 关闭时保证结果已发送、共享状态停止变化。
// 调用 ctx 已取消，因此从 Background 建独立收尾 context，绝不再选原 ctx.Done()。
func finishCanceledWorker(ctx context.Context, stage string, budget time.Duration, done <-chan struct{}, results <-chan TaskResult, inventory *inventoryState, result TaskResult, received bool, hooks *workerHooks) (TaskResult, error) {
	callErr := workerContextError(ctx, stage)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), budget)
	defer cleanupCancel() // owner 释放自己的收尾 timer，不把它交给 worker。
	if hooks != nil && hooks.onCancel != nil {
		hooks.onCancel(stage, cleanupCtx)
	}
	select {
	case <-done:
		// 已结束，继续读最终快照；即使清理成功，调用被取消的事实仍必须返回。
	case <-cleanupCtx.Done():
		// done 与预算同时就绪时，优先核实真正完成，避免 select 偶然选预算而误报超时。
		select {
		case <-done:
		default:
			// 未获退出确认，禁止读取 inventory；它仍可能被 worker 修改。
			// Joined=false 和明确错误承认资源结局尚未确认，不声称 ctx 已杀掉 goroutine。
			result.TaskID = "order-7001"
			result.State = "CLEANUP_INCOMPLETE"
			result.Joined = false
			return result, errors.Join(callErr, fmt.Errorf("%w: %w", ErrWorkerCleanupTimeout, cleanupCtx.Err()))
		}
	}
	if !received {
		result = <-results // close(done) 发生在唯一结果发送之后，此时已有缓冲值，不会再等业务工作。
	}
	result.Reserved = inventory.reserved
	result.Joined = true
	return result, callErr
}

// runTask 在 worker 自己的 goroutine 执行，所以这里的直接 defer 能捕获自己的 panic。
// 所有字段都由当前 goroutine 独占，返回后通过 channel 的同步交给 owner 读取。
func runTask(inventory *inventoryState, repair bool, hooks *workerHooks) (result TaskResult) {
	result.TaskID = "order-7001"
	result.State = "RUNNING"
	result.Events = append(result.Events, "worker.running")
	defer func() {
		if value := recover(); value != nil {
			result.State = "FAILED"
			result.Failure = captureFailure(result.TaskID, "reservation", value)
			result.Events = append(result.Events, "worker.failed")
			if hooks != nil && hooks.beforeCleanup != nil {
				hooks.beforeCleanup()
			}
			if repair {
				// recover 不改共享业务事实；此处明确释放该任务拥有的预占，才恢复不变量。
				inventory.reserved = 0
				result.Events = append(result.Events, "worker.reservation-released")
			}
			result.Events = append(result.Events, "worker.return-failure")
		}
	}()
	inventory.reserved = 1
	result.Events = append(result.Events, "worker.reserved")
	panic("库存预占后计算失败")
}
