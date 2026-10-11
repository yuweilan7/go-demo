package lesson

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// requestIDKey 是包内私有的可比较类型，外包不能碰巧用字符串同名 key 覆盖。
// Context 的并发安全不使 Value 指向的 map/slice/对象自动并发安全，所以只存不可变 string。
type requestIDKey struct{}

// WithRequestID 由请求入口调用，把日志关联 ID 随 ctx 向下传给 adapter。
// 返回派生 ctx，而不修改 parent；订单、金额、事务仍使用显式函数参数。
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// RequestID 由日志或 client adapter 调用；缺少元数据不是 panic，也不是业务参数默认值。
// .(string) 的 comma-ok 形式分别返回动态值和类型匹配事实，让 caller 明确选择缺省行为。
func RequestID(ctx context.Context) (string, bool) {
	requestID, ok := ctx.Value(requestIDKey{}).(string)
	return requestID, ok
}

// runLifecycle 由 Run 调用，展示脱离请求与 AfterFunc 回调的 owner 契约。
// 两种“独立继续”的行为都显式给出截止时间和完成等待，不能靠 main 退出回收。
func runLifecycle(scope context.Context, t *trace) error {
	if err := detachedOwner(scope, t); err != nil {
		return err
	}
	return afterFuncOwner(scope, t)
}

// detachedOwner 是请求外任务的 owner。它从请求继承 trace 元数据，但另建执行预算。
// raw WithoutCancel 自身没有 deadline、Done、Err/Cause，不应直接拿去无限等。
func detachedOwner(scope context.Context, t *trace) (resultErr error) {
	request, cancelRequest := context.WithTimeout(WithRequestID(scope, "m13-detached"), time.Hour)
	defer cancelRequest()
	raw := context.WithoutCancel(request)
	_, hasDeadline := raw.Deadline()
	if err := check(!hasDeadline && raw.Done() == nil && raw.Err() == nil && context.Cause(raw) == nil, "WithoutCancel 应剥离 deadline/取消原因，Done 为 nil"); err != nil {
		return err
	}
	requestID, ok := RequestID(raw)
	if err := check(ok && requestID == "m13-detached", "脱离请求仍可读取不可变元数据"); err != nil {
		return err
	}
	// 这里 owner 明确接受继续任务的职责：90ms 独立预算，cancel 和 done 都保存。
	// request 取消不影响它；进程 scope 仍由 worker 的第二个观察分支保留紧急停止能力。
	backgroundTask, cancelTask := context.WithTimeout(raw, 90*time.Millisecond)
	done := make(chan struct{})
	results := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		defer close(done)
		defer t.event("detached-task", "defer 清理完成")
		close(started)
		select {
		case <-backgroundTask.Done():
			results <- backgroundTask.Err()
		case <-scope.Done():
			results <- scope.Err()
		}
	}()
	defer func() {
		cancelTask()
		resultErr = errors.Join(resultErr, join(done, "detached task"))
	}()
	if err := wait(scope, started, "detached started"); err != nil {
		return err
	}
	cancelRequest()
	if err := check(request.Err() != nil && raw.Err() == nil, "请求取消不传播到 WithoutCancel"); err != nil {
		return err
	}
	t.event("detached-owner", "request Err=%v，raw Done=nil/Err=nil/Cause=nil；trace=%s，自有 90ms budget", request.Err(), requestID)
	select {
	case err := <-results:
		if err := check(errors.Is(err, context.DeadlineExceeded), "独立任务应被自己的 deadline 通知停止"); err != nil {
			return err
		}
	case <-scope.Done():
		return scope.Err()
	}
	if err := wait(scope, done, "detached 清理后的完成"); err != nil {
		return err
	}
	t.event("detached-owner", "独立任务观察自己的 deadline，owner 已 join；保留元数据不等于永久保留请求资源")
	return nil
}

// afterFuncOwner 是回调生命周期 owner，比较 stop 在回调尚未开始/已经开始时的结果。
// context.AfterFunc 负责另起 goroutine 调用 callback；stop 不负责等待这个 goroutine。
// owner 因而必须持有 started/gate/done，stop false 后也不能直接复用回调正在操作的资源。
func afterFuncOwner(scope context.Context, t *trace) (resultErr error) {
	before, cancelBefore := context.WithCancel(scope)
	defer cancelBefore()
	var prevented atomic.Int32
	stopBefore := context.AfterFunc(before, func() { prevented.Add(1) })
	if err := check(stopBefore(), "ctx 活跃且 callback 尚未触发，首次 stop 应阻止执行"); err != nil {
		return err
	}
	cancelBefore()
	if err := check(prevented.Load() == 0, "stop true 后 callback 不会运行"); err != nil {
		return err
	}
	t.event("AfterFunc", "尚未开始：stop=true，之后 cancel 也不会触发这个 callback")

	ctx, cancel := context.WithCancel(scope)
	started, gate, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := newOnceGate(gate)
	stop := context.AfterFunc(ctx, func() {
		// callback 在独立 goroutine 中运行，只拥有这一次资源清理。
		// close(done) 后 owner 才能确认函数退出；stop() 的 false 没有这个含义。
		defer close(done)
		defer t.event("callback", "资源清理实际完成；回调将返回")
		close(started)
		_ = wait(scope, gate, "callback cleanup gate")
	})
	defer func() {
		cancel()
		release()
		// 回调被取消触发的事实确定后，始终 join；不依赖 stop 的返回值猜是否已完成。
		resultErr = errors.Join(resultErr, join(done, "AfterFunc callback"))
	}()
	cancel()
	if err := wait(scope, started, "AfterFunc callback 已开始"); err != nil {
		return err
	}
	if err := check(!stop() && !closed(done), "回调已开始：stop=false 立即返回，并没有等待回调完成"); err != nil {
		return err
	}
	t.event("AfterFunc-owner", "已开始：stop=false 已返回，但 callback 仍等 cleanup gate")
	release()
	if err := wait(scope, done, "AfterFunc callback 最终完成"); err != nil {
		return err
	}
	t.event("AfterFunc-owner", "done 关闭才证明回调实际收尾；重复 stop=false 也可能仅表示已被停止过")
	return nil
}
