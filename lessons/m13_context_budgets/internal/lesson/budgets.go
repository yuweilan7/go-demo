package lesson

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const requestBudget = 900 * time.Millisecond
const queueDelay = 300 * time.Millisecond

// budgetResult 是 usecase 交给请求 owner 的结果，不把计时信息塞进 Context Value。
// queue/execute 是本次实测时间，不是调度顺序或严格时延 SLA。
type budgetResult struct {
	receipt        receipt
	err            error
	queue, execute time.Duration
}

// runBudgets 由 Run 调用，比较正确继承和每层 Background + 完整预算的危险版本。
// 两个版本都经过真实队列槽和本地 HTTP，使用独立订单/handler 状态。
func runBudgets(scope context.Context, t *trace) (resultErr error) {
	lab := newHTTPLab(scope, t)
	defer func() { resultErr = errors.Join(resultErr, lab.close()) }()
	if err := budgetCase(scope, lab, false); err != nil {
		return err
	}
	if err := budgetCase(scope, lab, true); err != nil {
		return err
	}
	return queueExpiry(scope, lab)
}

// queueExpiry 是 runBudgets 的额外分支：整个请求 budget 被排队吃完，HTTP 不应派发。
// slots 在整个观察期都被占满；只在取得结果且 join 后清理这个初始占位 token。
func queueExpiry(scope context.Context, lab *httpLab) (resultErr error) {
	ctx, cancel := context.WithTimeout(scope, 60*time.Millisecond)
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	queued := make(chan struct{})
	gatewayReady := make(chan context.Context, 1)
	results := make(chan budgetResult, 1)
	done := make(chan struct{})
	op := lab.register("queue-expiry", true, false)
	go func() {
		defer close(done)
		results <- budgetUsecase(ctx, scope, slots, queued, gatewayReady, lab, op, time.Now(), false)
	}()
	defer func() {
		cancel()
		resultErr = errors.Join(resultErr, join(done, "queue expiry usecase"))
		<-slots
	}()
	if err := wait(scope, queued, "queue expiry 已排队"); err != nil {
		return err
	}
	select {
	case result := <-results:
		lab.t.event("queue-owner", "全部预算消耗在 queue=%dms execution=%dms err=%v；HTTP 未派发", result.queue.Milliseconds(), result.execute.Milliseconds(), result.err)
		return check(errors.Is(result.err, context.DeadlineExceeded) && result.execute == 0 && !closed(op.started), "排队超时应发生在 HTTP 前")
	case <-scope.Done():
		return scope.Err()
	}
}

// budgetCase 是请求 owner。它先占满容量为 1 的 slots，把 usecase 真正挡在排队处。
// queued 屏障确认任务已来到获取槽的位置后，才开始 300ms 排队计时并释放槽。
// 原 request deadline 到达后才决定放行错误版本 HTTP，保证总预算已被突破。
func budgetCase(scope context.Context, lab *httpLab, rebase bool) (resultErr error) {
	label := "budget-fixed"
	if rebase {
		label = "budget-rebased"
	}
	startedAt := time.Now()
	request, cancelRequest := context.WithTimeout(scope, requestBudget)
	requestDeadline, _ := request.Deadline()
	lab.t.event(label, "request deadline=t0+%dms；总预算包含排队", requestDeadline.Sub(startedAt).Milliseconds())
	defer cancelRequest()
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	// 初始 token 模拟已有任务占用连接/限流槽。关闭函数只取出这一个 token 一次，
	// 不关闭 slots，避免后来 usecase 归还/获取槽时向已关闭 channel 发送。
	releaseQueueGate := make(chan struct{})
	releaseQueue := newOnceGate(releaseQueueGate)
	queueOwnerDone := make(chan struct{})
	go func() {
		defer close(queueOwnerDone)
		// 这个 goroutine 仅持有初始槽，owner 在正常/异常路径都会 releaseQueue。
		_ = wait(scope, releaseQueueGate, "owner 释放初始槽")
		<-slots
	}()
	queued := make(chan struct{})
	gatewayReady := make(chan context.Context, 1)
	results := make(chan budgetResult, 1)
	done := make(chan struct{})
	op := lab.register(label, true, false)
	go func() {
		defer close(done)
		results <- budgetUsecase(request, scope, slots, queued, gatewayReady, lab, op, startedAt, rebase)
	}()
	defer func() {
		// 通知 -> 解除实验屏障 -> 等待：三件事需要按 owner 的收尾职责分别完成。
		cancelRequest()
		releaseQueue()
		op.release()
		resultErr = errors.Join(resultErr, join(queueOwnerDone, "初始槽 owner"), join(done, "budget usecase"))
	}()
	if err := wait(scope, queued, "usecase 已到队列"); err != nil {
		return err
	}
	if err := holdFor(scope, queueDelay); err != nil {
		return err
	}
	releaseQueue()
	var gateway context.Context
	select {
	case gateway = <-gatewayReady:
	case <-scope.Done():
		return scope.Err()
	}
	gatewayDeadline, _ := gateway.Deadline()
	if !rebase {
		if err := check(gatewayDeadline.Equal(requestDeadline), "正确版本 gateway 必须继承同一个绝对 deadline"); err != nil {
			return err
		}
	} else if err := check(gatewayDeadline.After(requestDeadline), "危险版本 gateway deadline 被重新延后"); err != nil {
		return err
	}
	if err := wait(scope, op.started, "budget HTTP handler"); err != nil {
		return err
	}
	if err := wait(scope, request.Done(), "原 request 总预算到期"); err != nil {
		return err
	}
	if rebase {
		// 请求已到期，错误版本却仍有自己的新预算且 HTTP gate 未放行。
		// 放行之后成功的回执证明整链已经违反原 caller 的 deadline。
		if err := check(gateway.Err() == nil && !closed(done), "原请求到期时危险 gateway 应仍在 I/O 等待"); err != nil {
			return err
		}
		lab.t.event("budget-owner", "原 request Err=%v，但 Background gateway Err=%v；放行晚执行", request.Err(), gateway.Err())
		// 在“原预算已耗尽”这一已知状态下继续等待 150ms，展示新预算多给的执行窗口。
		// 这是有业务状态锚点的时间实验，不是在猜 handler 是否已经开始。
		if err := holdFor(scope, queueDelay/2); err != nil {
			return err
		}
		op.release()
	}
	var result budgetResult
	select {
	case result = <-results:
	case <-scope.Done():
		return scope.Err()
	}
	if err := wait(scope, op.exited, "budget handler 退出"); err != nil {
		return err
	}
	count := lab.state.count(label + "-order")
	lab.t.event("budget-owner", "%s queue=%dms execution=%dms total=%dms effects=%d err=%v", label, result.queue.Milliseconds(), result.execute.Milliseconds(), time.Since(startedAt).Milliseconds(), count, result.err)
	if rebase {
		return check(result.err == nil && result.receipt.Total == 1 && count == 1, "危险版本在原 deadline 后仍成功产生效果")
	}
	return check(errors.Is(result.err, context.DeadlineExceeded) && count == 0, "修复版本共享预算，到期 I/O 返回且本次 gate 前无效果")
}

// budgetUsecase 由 budgetCase 的 goroutine 调用：request -> service -> HTTP gateway。
// ctx 是 caller 传来的请求预算；slots 是共享限流槽；其他 channel 是教学观察屏障。
// 它拥有自己派生的两个 cancel，但不拥有 caller 的 cancel，也不关闭共享 slots。
func budgetUsecase(ctx, scope context.Context, slots chan struct{}, queued chan<- struct{}, gatewayReady chan<- context.Context, lab *httpLab, op *operation, startedAt time.Time, rebase bool) budgetResult {
	serviceParent := ctx
	if rebase {
		// 反例：这里创建了新根，把 caller 的 deadline/取消传播切断。
		// 这个 timeout 自身有界且 defer cancel，仍然违反“整链总预算”的业务契约。
		serviceParent = context.Background()
	}
	service, cancelService := context.WithTimeout(serviceParent, requestBudget)
	defer cancelService()
	serviceDeadline, _ := service.Deadline()
	lab.t.event(op.id, "service deadline=t0+%dms；获取槽时已经使用 service ctx", serviceDeadline.Sub(startedAt).Milliseconds())
	queueStart := time.Now()
	close(queued)
	select {
	case slots <- struct{}{}:
		// 发送 token 表示现在占有容量。退出时接收自己的 token，归还槽。
		defer func() { <-slots }()
	case <-service.Done():
		return budgetResult{err: fmt.Errorf("排队预算耗尽: %w", service.Err()), queue: time.Since(queueStart)}
	case <-scope.Done():
		return budgetResult{err: scope.Err(), queue: time.Since(queueStart)}
	}
	queuedFor := time.Since(queueStart)
	gatewayParent := service
	if rebase {
		// 二次重置：300ms 排队没有从这一层预算扣除，deadline 被推到 t0+1200ms 左右。
		gatewayParent = context.Background()
	}
	gateway, cancelGateway := context.WithTimeout(gatewayParent, requestBudget)
	defer cancelGateway()
	gatewayDeadline, _ := gateway.Deadline()
	lab.t.event(op.id, "gateway deadline=t0+%dms remaining=%dms；queue 已耗费 %dms", gatewayDeadline.Sub(startedAt).Milliseconds(), time.Until(gatewayDeadline).Milliseconds(), queuedFor.Milliseconds())
	// 缓冲为 1，owner 异常返回也不会把 usecase 卡在教学信息发送上。
	gatewayReady <- gateway
	executionStart := time.Now()
	result, err := lab.charge(gateway, op, chargeCommand{Order: op.id + "-order"})
	return budgetResult{receipt: result, err: err, queue: queuedFor, execute: time.Since(executionStart)}
}
