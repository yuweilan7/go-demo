package lesson

import (
	"context"
	"errors"
	"time"
)

// runHTTP 是本课核心实验，Run 顺序执行成功、主动取消、晚副作用和重试对照。
// 入口拥有一套真实 TCP 服务；每个子实验使用独立合成订单，互不污染计数。
func runHTTP(scope context.Context, t *trace) (resultErr error) {
	lab := newHTTPLab(scope, t)
	defer func() { resultErr = errors.Join(resultErr, lab.close()) }()
	if err := httpSuccess(scope, lab); err != nil {
		return err
	}
	if err := httpActiveCancel(scope, lab); err != nil {
		return err
	}
	if err := httpLateEffect(scope, lab); err != nil {
		return err
	}
	if err := httpRetry(scope, lab, "", 2); err != nil {
		return err
	}
	return httpRetry(scope, lab, "stable-payment-001", 1)
}

// httpSuccess 由 runHTTP 调用，证明成功不只看 HTTP status，还读回执并查效果。
// request 的 cancel 和 clientTask 都由本函数持有，正常/失败路径用同一 defer 收尾。
func httpSuccess(scope context.Context, lab *httpLab) (resultErr error) {
	ctx, cancel := context.WithTimeout(WithRequestID(scope, "m13-success"), 2*time.Second)
	op := lab.register("success", true, true)
	task := lab.startCharge(ctx, op, chargeCommand{Order: "success-order"})
	defer func() {
		cancel()
		resultErr = errors.Join(resultErr, join(task.done, "success client"))
	}()
	if err := wait(scope, op.started, "success handler 开始"); err != nil {
		return err
	}
	op.release()
	result, err := awaitClient(scope, task)
	if err != nil {
		return err
	}
	if err := wait(scope, op.exited, "success handler 退出"); err != nil {
		return err
	}
	return check(result.err == nil && result.receipt.Total == 1 && lab.state.count("success-order") == 1, "成功回执和实际效果应各为 1: %+v", result)
}

// httpActiveCancel 由 runHTTP 调用。started 是服务端确已进入 gate 等待的屏障。
// 在 gate 尚未放行时主动 cancel，修复 handler 从 r.Context.Done 返回，效果为零。
// 这个受控次序证明“本次取消在 apply 前”，不是承诺所有取消都能撤销写入。
func httpActiveCancel(scope context.Context, lab *httpLab) (resultErr error) {
	ctx, cancel := context.WithCancel(scope)
	op := lab.register("active-cancel", true, false)
	task := lab.startCharge(ctx, op, chargeCommand{Order: "cancel-order"})
	defer func() {
		cancel()
		resultErr = errors.Join(resultErr, join(task.done, "active-cancel client"))
	}()
	if err := wait(scope, op.started, "cancel handler 开始"); err != nil {
		return err
	}
	lab.t.event("request-owner", "主动 cancel；执行 gate 仍关闭")
	cancel()
	result, err := awaitClient(scope, task)
	if err != nil {
		return err
	}
	if err := wait(scope, op.observed, "服务端观察断连取消"); err != nil {
		return err
	}
	if err := wait(scope, op.exited, "取消 handler 退出"); err != nil {
		return err
	}
	return check(errors.Is(result.err, context.Canceled) && lab.state.count("cancel-order") == 0 && !closed(op.effected), "主动取消应返回 Canceled，本次执行 gate 前没有效果")
}

// httpLateEffect 由 runHTTP 调用。危险 handler 故意不等请求 Done，模拟未实现取消的依赖。
// owner 先拿到客户端 DeadlineExceeded，再放行业务 gate，确保晚副作用是因果关系。
func httpLateEffect(scope context.Context, lab *httpLab) (resultErr error) {
	ctx, cancel := context.WithTimeout(scope, 400*time.Millisecond)
	op := lab.register("late-effect", false, false)
	task := lab.startCharge(ctx, op, chargeCommand{Order: "late-order"})
	defer func() {
		cancel()
		resultErr = errors.Join(resultErr, join(task.done, "late-effect client"))
	}()
	if err := wait(scope, op.started, "late handler 开始"); err != nil {
		return err
	}
	result, err := awaitClient(scope, task)
	if err != nil {
		return err
	}
	if err := check(errors.Is(result.err, context.DeadlineExceeded) && lab.state.count("late-order") == 0 && !closed(op.exited), "客户端已经超时，服务端仍在 gate；效果此刻为 0"); err != nil {
		return err
	}
	lab.t.event("request-owner", "客户端只知道结果 UNKNOWN；本地观察当前 effect=0，handler 尚未退出")
	op.release()
	if err := wait(scope, op.exited, "晚副作用 handler 退出"); err != nil {
		return err
	}
	if err := check(closed(op.effected) && lab.state.count("late-order") == 1, "客户端超时之后服务端仍完成一次效果"); err != nil {
		return err
	}
	lab.t.event("reconciler", "独立查询 late-order=1：已执行；客户端的 timeout 没有自动回滚")
	return nil
}

// httpRetry 由 runHTTP 分别以空/固定幂等键调用，用同一业务订单比较重复写风险。
// expected 是独立效果数要求：无防重为 2，固定键为 1。
// 原请求先超时且仍未结束，再让 retry 成功，最后恢复原请求；这模拟响应丢失窗口。
func httpRetry(scope context.Context, lab *httpLab, idempotencyKey string, expected int) (resultErr error) {
	label := "unsafe"
	if idempotencyKey != "" {
		label = "idempotent"
	}
	command := chargeCommand{Order: label + "-order", IdempotencyKey: idempotencyKey}
	originalCtx, cancelOriginal := context.WithTimeout(scope, 400*time.Millisecond)
	original := lab.register(label+"-original", false, false)
	originalTask := lab.startCharge(originalCtx, original, command)
	defer func() {
		cancelOriginal()
		resultErr = errors.Join(resultErr, join(originalTask.done, label+" original client"))
	}()
	if err := wait(scope, original.started, "original handler"); err != nil {
		return err
	}
	first, err := awaitClient(scope, originalTask)
	if err != nil {
		return err
	}
	if err := check(errors.Is(first.err, context.DeadlineExceeded), "original 应超时"); err != nil {
		return err
	}
	lab.t.event("retry-owner", "%s 原请求结果 UNKNOWN；发起一次受控重试（生产不能无条件照做）", label)
	retryCtx, cancelRetry := context.WithTimeout(scope, 2*time.Second)
	retry := lab.register(label+"-retry", true, false)
	retryTask := lab.startCharge(retryCtx, retry, command)
	defer func() {
		cancelRetry()
		resultErr = errors.Join(resultErr, join(retryTask.done, label+" retry client"))
	}()
	if err := wait(scope, retry.started, "retry handler"); err != nil {
		return err
	}
	retry.release()
	second, err := awaitClient(scope, retryTask)
	if err != nil {
		return err
	}
	if err := check(second.err == nil && second.receipt.Total == 1, "retry 第一次效果回执应为 1: %+v", second); err != nil {
		return err
	}
	original.release()
	if err := wait(scope, original.exited, "original 最终退出"); err != nil {
		return err
	}
	if err := wait(scope, retry.exited, "retry 最终退出"); err != nil {
		return err
	}
	actual := lab.state.count(command.Order)
	lab.t.event("reconciler", "%s 最终独立查询 effects=%d expected=%d", label, actual, expected)
	return check(actual == expected, "%s 重试效果应为 %d，实际 %d", label, expected, actual)
}
