package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/sony/gobreaker/v2"
)

// Run由demo调用，各组同步执行，组ctx作安全上限；fixture和Task的清理不继承已取消组ctx。
func Run(ctx context.Context, out io.Writer, scenario string) error {
	trace := &Trace{out: out, start: time.Now()}
	groups := []struct {
		name string
		fn   func(context.Context, *Trace) error
	}{{"core", core}, {"amplify", amplify}, {"breaker", breakerExperiment}, {"isolation", isolation}, {"limits", limits}}
	matched := false
	for _, group := range groups {
		if scenario != "all" && scenario != group.name {
			continue
		}
		matched = true
		if err := ctx.Err(); err != nil {
			return err
		}
		trace.event("owner", "=== %s ===", group.name)
		scope, cancel := context.WithTimeout(ctx, 8*time.Second)
		err := group.fn(scope, trace)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", group.name, err)
		}
		if err := check(ownedTasks.Load() == 0, "ownedTasks=%d", ownedTasks.Load()); err != nil {
			return err
		}
		trace.event("owner", "PASS %s owned_tasks=0", group.name)
	}
	if !matched {
		return fmt.Errorf("未知scenario %q", scenario)
	}
	return nil
}

// withFixture是同步实验资源边界；具名err让defer聚合业务失败与listener/handler/body清理失败。
func withFixture(trace *Trace, fn func(*Fixture, *Client) error) (err error) {
	f, err := newFixture(trace)
	if err != nil {
		return err
	}
	c := newClient(4)
	defer func() {
		c.Close()
		combine(&err, f.close())
		combine(&err, check(c.bodies.Load() == 0, "client response bodies=%d", c.bodies.Load()))
		trace.event("client-owner", "bodies=%d err=%v", c.bodies.Load(), err)
	}()
	return fn(f, c)
}

// core由Run调用，先跑GET重试边界，再跑写未知/查询幂等，最后跑真实取消后停止重试。
func core(ctx context.Context, trace *Trace) error {
	if err := withFixture(trace, func(f *Fixture, c *Client) error { return retryCore(ctx, trace, f, c) }); err != nil {
		return err
	}
	if err := withFixture(trace, func(f *Fixture, c *Client) error { return orders(ctx, trace, f, c) }); err != nil {
		return err
	}
	if err := withFixture(trace, func(f *Fixture, c *Client) error { return untrustedWrites(ctx, trace, f, c) }); err != nil {
		return err
	}
	return withFixture(trace, func(f *Fixture, c *Client) error { return cancellation(ctx, trace, f, c) })
}

// retryCore拥有各次Retry的同步调用；统计从客户端ctx读取，handler次数/body原文是独立服务端证据。
func retryCore(ctx context.Context, trace *Trace, f *Fixture, c *Client) error {
	p := Policy{Total: 2 * time.Second, MaxAttempts: 3, Base: 10 * time.Millisecond, Cap: 40 * time.Millisecond}
	get := func(path string) Spec { return Spec{Method: "GET", URL: f.URL + path, Idempotent: true} }
	r, s, err := Retry(ctx, c, get("/flaky"), p, trace)
	if e := check(err == nil && r.Code == "OK" && len(s.Attempts) == 3 && f.count("/flaky") == 3, "flaky应503/503/200，err=%v calls=%d", err, f.count("/flaky")); e != nil {
		return e
	}
	for _, a := range s.Attempts {
		if e := check(a.Deadline.Equal(s.Deadline), "尝试%d不应重置总deadline", a.Number); e != nil {
			return e
		}
	}
	_, s, err = Retry(ctx, c, get("/always503"), p, trace)
	if e := check(err != nil && len(s.Attempts) == 3 && f.count("/always503") == 3, "最大次数必须包括第一次且真实调用3次"); e != nil {
		return e
	}
	_, s, err = Retry(ctx, c, get("/reject"), p, trace)
	var rejected *HTTPError
	if e := check(errors.As(err, &rejected) && rejected.Status == 422 && rejected.Code == "OUT_OF_STOCK" && len(s.Attempts) == 1 && f.count("/reject") == 1, "明确拒绝不能靠重试修复: %v", err); e != nil {
		return e
	}
	if err := unsafeBodyReplay(ctx, c, f.URL+"/body-fault", "stock-query"); err != nil {
		return err
	}
	faultBodies := f.bodyCopies("/body-fault")
	if e := check(len(faultBodies) == 2 && faultBodies[0] == "stock-query" && faultBodies[1] == "", "危险共享Reader第二次真实body应为空: %v", faultBodies); e != nil {
		return e
	}
	trace.event("body-owner", "共享Reader反例server bodies=%q；修复每attempt新Reader", faultBodies)
	// GET带body仅是重放实验协议；真实JSON创建订单在orders里，不把可重放字节等同可重放业务。
	replay := Spec{Method: "GET", URL: f.URL + "/replay", Body: []byte(`{"query":"stock"}`), Replayable: true, Idempotent: true}
	_, s, err = Retry(ctx, c, replay, p, trace)
	bodies := f.bodyCopies("/replay")
	if e := check(err == nil && len(s.Attempts) == 2 && len(bodies) == 2 && bodies[0] == string(replay.Body) && bodies[1] == bodies[0], "真实server两次都应收到完整相同body"); e != nil {
		return e
	}
	replay.Replayable = false
	_, s, err = Retry(ctx, c, replay, p, trace)
	if e := check(errors.Is(err, ErrBodyNotReplayable) && len(s.Attempts) == 1 && f.count("/replay") == 3, "不可重放体阻止第二次派发: %v", err); e != nil {
		return e
	}
	_, s, err = Retry(ctx, c, get("/retry-after"), p, trace)
	if e := check(err == nil && f.count("/retry-after") == 2 && len(s.Waits) == 1 && s.Waits[0] >= time.Second && s.Attempts[1].Start.Sub(s.Attempts[0].Start) >= time.Second, "真实Retry-After=1不得提前尝试: %v", err); e != nil {
		return e
	}
	// 总预算小于server建议等待：返回deadline并只进入handler一次，不能绕过建议继续轰炸。
	_, s, err = Retry(ctx, c, get("/retry-after-long"), Policy{Total: 80 * time.Millisecond, MaxAttempts: 3}, trace)
	if e := check(errors.Is(err, context.DeadlineExceeded) && f.count("/retry-after-long") == 1 && len(s.Attempts) == 1, "Retry-After超剩余总预算应停止: %v", err); e != nil {
		return e
	}
	// /always503的次数在前一调用之后用增量核对；耗时只打印，不作精确调度承诺。
	before := f.count("/always503")
	started := time.Now()
	budgetPolicy := Policy{Total: 150 * time.Millisecond, MaxAttempts: 10, Base: 80 * time.Millisecond, Cap: 160 * time.Millisecond}
	_, s, err = Retry(ctx, c, get("/always503"), budgetPolicy, trace)
	if e := check(errors.Is(err, context.DeadlineExceeded) && len(s.Attempts) < 10 && f.count("/always503")-before == len(s.Attempts), "总预算先耗尽且调用数对应: %v", err); e != nil {
		return e
	}
	for _, a := range s.Attempts {
		if e := check(a.Deadline.Equal(s.Deadline), "预算实验deadline重置"); e != nil {
			return e
		}
	}
	trace.event("budget-owner", "total=%s attempts=%d max=10 deadline共同继承", time.Since(started).Round(time.Millisecond), len(s.Attempts))
	g := f.gate("slow-budget")
	_, s, err = Retry(ctx, c, get("/slow?gate=slow-budget"), Policy{Total: 190 * time.Millisecond, Attempt: 70 * time.Millisecond, MaxAttempts: 3, Base: time.Millisecond, Cap: time.Millisecond}, trace)
	if e := check(errors.Is(err, context.DeadlineExceeded) && f.count("/slow") >= 1 && f.count("/slow") <= 3 && closed(g.Entered), "真实慢请求在总预算中失败: %v", err); e != nil {
		return e
	}
	for _, a := range s.Attempts {
		if e := check(!a.Deadline.After(s.Deadline), "单attempt预算不能晚于总deadline"); e != nil {
			return e
		}
	}
	trace.event("core-owner", "GET次数上限、jitter、body replay、Retry-After、slow/total deadline PASS")
	return nil
}

// orders沿网络POST -> handler.commit -> 断TCP -> UNKNOWN -> GET查询 -> 同key重试，并独立查账本。
// 这里故意对照危险手动重试；正常自动Retry在没有幂等协议时停在第一次Unknown。
func orders(ctx context.Context, trace *Trace, f *Fixture, c *Client) error {
	body := []byte(`{"sku":"sku-m33","qty":1}`)
	lost := Spec{Method: "POST", URL: f.URL + "/lost-order", Body: body, Replayable: true}
	_, s, err := Retry(ctx, c, lost, Policy{Total: time.Second, MaxAttempts: 3}, trace)
	var unknown *Unknown
	if e := check(errors.As(err, &unknown) && errors.Is(err, ErrUnsafeRetry) && len(s.Attempts) == 1 && f.Ledger.snapshot() == 1 && f.count("/lost-order") == 1, "接受写后丢响应必须UNKNOWN且禁止盲自动重试: %v", err); e != nil {
		return e
	}
	// 错误复现：人为绕过重试安全判断，另发没有key的POST；两次本来是一个逻辑订单，账本却2行。
	lost.URL = f.URL + "/orders"
	r, _, err := Retry(ctx, c, lost, Policy{Total: time.Second, MaxAttempts: 1}, trace)
	if e := check(err == nil && r.Receipt.ID == 2 && f.Ledger.snapshot() == 2, "危险盲重试必须真实重复2行"); e != nil {
		return e
	}
	trace.event("order-owner", "危险对照：client UNKNOWN后换请求，无key导致rows=2")
	safe := Spec{Method: "POST", URL: f.URL + "/lost-order", Key: "order-m33-stable", Body: body, Replayable: true, Idempotent: true}
	_, _, err = Retry(ctx, c, safe, Policy{Total: time.Second, MaxAttempts: 1}, trace)
	if e := check(errors.As(err, &unknown) && f.Ledger.snapshot() == 3, "带key响应仍可能UNKNOWN"); e != nil {
		return e
	}
	// 对账有自己的明确owner和有限预算，生产可异步转交持久任务；不偷偷重置原请求继续执行写。
	reconcile, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	r, _, err = Retry(reconcile, c, Spec{Method: "GET", URL: f.URL + "/status?key=order-m33-stable", Idempotent: true}, Policy{Total: time.Second, MaxAttempts: 2}, trace)
	if e := check(err == nil && r.Receipt.ID == 3 && r.Receipt.Order == (Order{SKU: "sku-m33", Qty: 1}) && f.Ledger.snapshot() == 3, "GET查账必须确认真实订单与参数: %v", err); e != nil {
		return e
	}
	safe.URL = f.URL + "/orders"
	r, _, err = Retry(ctx, c, safe, Policy{Total: time.Second, MaxAttempts: 2}, trace)
	if e := check(err == nil && r.Receipt.ID == 3 && r.Receipt.Duplicate && f.Ledger.snapshot() == 3, "安全重放只返回原回执，rows仍3"); e != nil {
		return e
	}
	safe.Body = []byte(`{"sku":"sku-m33","qty":2}`)
	_, s, err = Retry(ctx, c, safe, Policy{Total: time.Second, MaxAttempts: 3}, trace)
	var conflict *HTTPError
	if e := check(errors.As(err, &conflict) && conflict.Status == 409 && len(s.Attempts) == 1 && f.Ledger.snapshot() == 3, "同key不同参数拒绝且无副作用"); e != nil {
		return e
	}
	trace.event("order-owner", "修复对照：UNKNOWN -> GET receipt=3 -> 同key duplicate=true -> conflict409，rows=3")
	return nil
}

// untrustedWrites由core在独立账本中运行。四种handler都先commit再返回不可信201响应，
// caller有稳定key/可重放body/MaxAttempts=3，仍必须只派发一次，然后GET原key查询业务事实。
// 查询确认成功后直接返回回执即可，不需要再POST；handler计数证明没有以协议错误为由盲重放。
func untrustedWrites(ctx context.Context, trace *Trace, f *Fixture, c *Client) error {
	for _, fault := range []struct {
		path  string
		cause error
	}{{"/corrupt-order", ErrInvalidOrderResponse}, {"/large-order", ErrResponseTooLarge}, {"/no-receipt-order", ErrInvalidReceipt}, {"/mismatched-order", ErrInvalidReceipt}} {
		before := f.Ledger.snapshot()
		key := fmt.Sprintf("protocol-m33-%d", before+1)
		spec := Spec{Method: "POST", URL: f.URL + fault.path, Key: key, Body: []byte(`{"sku":"protocol-m33","qty":3}`), Replayable: true, Idempotent: true}
		_, s, err := Retry(ctx, c, spec, Policy{Total: time.Second, MaxAttempts: 3}, trace)
		var unknown *Unknown
		if e := check(errors.As(err, &unknown) && errors.Is(err, fault.cause) && len(s.Attempts) == 1 && s.Attempts[0].Status == 201 && f.count(fault.path) == 1 && f.Ledger.snapshot() == before+1, "不可信写响应必须UNKNOWN且禁止自动重放 path=%s err=%v", fault.path, err); e != nil {
			return e
		}
		r, _, queryErr := Retry(ctx, c, Spec{Method: "GET", URL: f.URL + "/status?key=" + key, Idempotent: true}, Policy{Total: time.Second, MaxAttempts: 2}, trace)
		if e := check(queryErr == nil && r.Receipt.ID == before+1 && r.Receipt.Order == (Order{SKU: "protocol-m33", Qty: 3}) && f.Ledger.snapshot() == before+1 && f.count(fault.path) == 1, "原key查询必须确认真实已提交效果 path=%s err=%v", fault.path, queryErr); e != nil {
			return e
		}
		trace.event("protocol-owner", "%s HTTP201但UNKNOWN -> 原key GET确认id=%d rows=%d，POST calls=1", fault.path, r.Receipt.ID, f.Ledger.snapshot())
	}
	// 额外用空body演示有副作用写。错误地标Idempotent=true也不能代替稳定key，不能重放3次。
	before := f.Ledger.snapshot()
	_, s, err := Retry(ctx, c, Spec{Method: "POST", URL: f.URL + "/empty-lost-order", Idempotent: true, Replayable: true}, Policy{Total: time.Second, MaxAttempts: 3}, trace)
	var unknown *Unknown
	if e := check(errors.As(err, &unknown) && errors.Is(err, ErrUnsafeRetry) && len(s.Attempts) == 1 && f.count("/empty-lost-order") == 1 && f.Ledger.snapshot() == before+1, "空body POST不能绕过key guard: %v", err); e != nil {
		return e
	}
	trace.event("protocol-owner", "空body服务端默认订单已提交rows=%d，无key停止一次并保留UNKNOWN", f.Ledger.snapshot())
	return nil
}

// cancellation由core调用，owner等真实503后的backoff通知，再取消child并独立join。
// OnBackoff只关闭专属channel，任务此刻仍可能在timer；取消必须让等待返回，不能再派发attempt2。
func cancellation(ctx context.Context, trace *Trace, f *Fixture, c *Client) (err error) {
	backoffEntered := make(chan struct{})
	task := startTask(ctx, func(work context.Context) taskResult {
		r, s, e := Retry(work, c, Spec{Method: "GET", URL: f.URL + "/always503", Idempotent: true}, Policy{Total: 2 * time.Second, MaxAttempts: 3, Base: time.Second, Cap: time.Second, OnBackoff: func(time.Duration) { close(backoffEntered) }}, trace)
		return taskResult{Reply: r, Stats: s, Err: e}
	}, nil)
	defer func() { combine(&err, task.StopAndJoin(nil)) }()
	if err = wait(ctx, backoffEntered, "真实503后的backoff"); err != nil {
		return err
	}
	task.cancel()
	result, waitErr := receive(ctx, task)
	if waitErr != nil {
		return waitErr
	}
	if err = task.StopAndJoin(nil); err != nil {
		return err
	}
	trace.event("cancel-owner", "result=%v attempts=%d handler=%d Done=%t", result.Err, len(result.Stats.Attempts), f.count("/always503"), closed(task.Done))
	return check(errors.Is(result.Err, context.Canceled) && len(result.Stats.Attempts) == 1 && f.count("/always503") == 1, "取消后应停止后续尝试")
}

// amplify用application循环、SDK Retry与真正/gateway服务端Retry组成三层，读取叶handler计数。
func amplify(ctx context.Context, trace *Trace) error {
	for _, fixed := range []bool{false, true} {
		err := withFixture(trace, func(f *Fixture, c *Client) error {
			sdkAttempts, serverAttempts := 3, 2
			if fixed {
				sdkAttempts, serverAttempts = 1, 1
			}
			for app := 1; app <= 2; app++ {
				_, _, err := Retry(ctx, c, Spec{Method: "GET", URL: fmt.Sprintf("%s/gateway?server_attempts=%d", f.URL, serverAttempts), Idempotent: true}, Policy{Total: 2 * time.Second, MaxAttempts: sdkAttempts, Base: time.Millisecond, Cap: time.Millisecond}, trace)
				if err == nil {
					return errors.New("故障依赖不应被编造成成功")
				}
			}
			trace.event("amplify-owner", "fixed=%t app=2 sdk=%d server=%d gateway=%d leaf=%d", fixed, sdkAttempts, serverAttempts, f.count("/gateway"), f.count("/always503"))
			return check(f.count("/gateway") == 2*sdkAttempts && f.count("/always503") == 2*sdkAttempts*serverAttempts, "三层必须由真实调用观察到乘法")
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// breakerExperiment通过真实错误打开库，再等确定的open timeout到期；半开probe真实卡在HTTP gate。
// 第二probe被库拒绝不会进入handler；owner放行第一probe，成功使库恢复closed。
func breakerExperiment(ctx context.Context, trace *Trace) error {
	return withFixture(trace, func(f *Fixture, c *Client) (err error) {
		opened := make(chan struct{})
		cb := breaker(trace, opened, 100*time.Millisecond, time.Second)
		bad := Spec{Method: "GET", URL: f.URL + "/always503", Idempotent: true}
		for range 2 {
			if _, e := throughBreaker(ctx, cb, c, bad, trace); e == nil {
				return errors.New("故障调用不能成功")
			}
		}
		if err = wait(ctx, opened, "breaker open通知"); err != nil {
			return err
		}
		_, e := throughBreaker(ctx, cb, c, bad, trace)
		fallback, fallbackErr := unavailable(e)
		if err = check(errors.Is(e, gobreaker.ErrOpenState) && f.count("/always503") == 2 && fallback.Code == "INVENTORY_UNAVAILABLE" && fallback.Receipt.ID == 0 && fallbackErr != nil, "open快速失败且降级不能编造库存成功"); err != nil {
			return err
		}
		// 从已确认open之后计时超过100ms；这是测试真实状态超时，不是猜goroutine先后。
		if err = pause(ctx, 110*time.Millisecond); err != nil {
			return err
		}
		g := f.gate("half-open-probe")
		probeSpec := Spec{Method: "GET", URL: f.URL + "/probe?gate=half-open-probe", Idempotent: true}
		task := startTask(ctx, func(work context.Context) taskResult {
			r, e := throughBreaker(work, cb, c, probeSpec, trace)
			return taskResult{Reply: r, Err: e}
		}, nil)
		defer func() { combine(&err, task.StopAndJoin(g.Open)) }()
		if err = wait(ctx, g.Entered, "half-open第一真实probe"); err != nil {
			return err
		}
		if err = check(cb.State() == gobreaker.StateHalfOpen, "probe未完成时必须half-open"); err != nil {
			return err
		}
		_, e = throughBreaker(ctx, cb, c, probeSpec, trace)
		if err = check(errors.Is(e, gobreaker.ErrTooManyRequests) && f.count("/probe") == 1, "半开只允许一个并发probe"); err != nil {
			return err
		}
		g.Open()
		r, waitErr := receive(ctx, task)
		if waitErr != nil {
			return waitErr
		}
		if err = task.StopAndJoin(g.Open); err != nil {
			return err
		}
		if err = wait(ctx, g.Done, "probe handler返回"); err != nil {
			return err
		}
		if err = check(r.Err == nil && r.Reply.Code == "OK" && cb.State() == gobreaker.StateClosed, "probe成功恢复closed"); err != nil {
			return err
		}
		_, e = throughBreaker(ctx, cb, c, Spec{Method: "GET", URL: f.URL + "/healthy", Idempotent: true}, trace)
		trace.event("breaker-owner", "真实probe=1，第二probe拒绝，恢复closed healthy=%d", f.count("/healthy"))
		return check(e == nil && f.count("/healthy") == 1, "恢复后真实健康调用")
	})
}

// isolation给故障依赖独立槽/队列/Transport，健康路径自己的Transport仍可完成真实HTTP。
// 每个任务一创建就登记defer，排队ctx到期/任意屏障早退出都先cancel/unblock后独立join。
func isolation(ctx context.Context, trace *Trace) error {
	return withFixture(trace, func(f *Fixture, c *Client) (err error) {
		badClient := newClient(1)
		defer badClient.Close()
		bulk := newBulkhead(1, 1)
		g := f.gate("isolated-slow")
		slow := Spec{Method: "GET", URL: f.URL + "/slow?gate=isolated-slow", Idempotent: true}
		first := startTask(ctx, func(work context.Context) taskResult { return bulk.call(work, badClient, slow, nil, trace) }, nil)
		defer func() {
			combine(&err, first.StopAndJoin(g.Open))
			combine(&err, check(badClient.bodies.Load() == 0, "badClient bodies未归零"))
		}()
		if err = wait(ctx, g.Entered, "slow handler已占槽"); err != nil {
			return err
		}
		queued := make(chan struct{})
		second := startTask(ctx, func(work context.Context) taskResult { return bulk.call(work, badClient, slow, queued, trace) }, nil)
		defer func() { combine(&err, second.StopAndJoin(nil)) }()
		if err = wait(ctx, queued, "第二任务真正排队"); err != nil {
			return err
		}
		active, pending := bulk.snapshot()
		third := bulk.call(ctx, badClient, slow, nil, trace)
		if err = check(active == 1 && pending == 1 && errors.Is(third.Err, ErrQueueFull) && f.count("/slow") == 1, "队列上限且拒绝不派发HTTP"); err != nil {
			return err
		}
		// 故障handler的Release仍关着，健康HTTP已返回才断言健康路径未被故障依赖拖住。
		healthy, _, e := Retry(ctx, c, Spec{Method: "GET", URL: f.URL + "/healthy", Idempotent: true}, Policy{Total: time.Second, MaxAttempts: 1}, trace)
		if err = check(e == nil && healthy.Code == "OK" && f.count("/healthy") == 1 && !closed(g.Release) && !closed(first.Done), "健康路径必须在slow gate关闭时完成"); err != nil {
			return err
		}
		second.cancel()
		r, waitErr := receive(ctx, second)
		if waitErr != nil {
			return waitErr
		}
		if err = second.StopAndJoin(nil); err != nil {
			return err
		}
		if err = check(errors.Is(r.Err, context.Canceled) && f.count("/slow") == 1, "排队取消没有下游调用"); err != nil {
			return err
		}
		first.cancel()
		r, waitErr = receive(ctx, first)
		if waitErr != nil {
			return waitErr
		}
		if err = first.StopAndJoin(g.Open); err != nil {
			return err
		}
		if err = wait(ctx, g.Done, "slow handler退出"); err != nil {
			return err
		}
		active, pending = bulk.snapshot()
		trace.event("isolation-owner", "slow=%d healthy=%d active=%d queue=%d，工作取消后分别join", f.count("/slow"), f.count("/healthy"), active, pending)
		return check(errors.Is(r.Err, context.Canceled) && active == 0 && pending == 0, "取消归还槽与排队容量")
	})
}

// limits对照开始速率与并发槽的不同生命周期：一个已结束请求仍占当前速率窗口，拒绝不发网络。
func limits(ctx context.Context, trace *Trace) error {
	return withFixture(trace, func(f *Fixture, c *Client) error {
		l := &SpacingLimiter{period: 100 * time.Millisecond}
		spec := Spec{Method: "GET", URL: f.URL + "/healthy", Idempotent: true}
		first := rateCall(ctx, l, c, spec, trace)
		second := rateCall(ctx, l, c, spec, trace)
		if e := check(first.Err == nil && errors.Is(second.Err, ErrRateLimited) && f.count("/healthy") == 1, "请求已完成仍可被速率窗口拒绝"); e != nil {
			return e
		}
		// 从首次真实调用完成后开始等待一个窗口，保证第三次已经过期；不用sleep推测并发执行。
		if e := pause(ctx, 110*time.Millisecond); e != nil {
			return e
		}
		third := rateCall(ctx, l, c, spec, trace)
		trace.event("rate-owner", "第一次成功，第二次入口拒绝，窗口后第三次成功，HTTP calls=%d", f.count("/healthy"))
		return check(third.Err == nil && f.count("/healthy") == 2, "速率窗口恢复应真实调用2次")
	})
}

// statusCode供独立测试定位error动态类型；errors.As读取的是HTTPError指针别名，不比较文案。
func statusCode(err error) int {
	var h *HTTPError
	if errors.As(err, &h) {
		return h.Status
	}
	return 0
}
