package lesson

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	m15 "go-demo/lessons/m15_structured_concurrency"
)

type CoreReport struct {
	Shutdown                                                             ShutdownReport
	Backend                                                              BackendSnapshot
	Amount                                                               int64
	FullRejected, ClosedRejected, EarlyCloseRejected, ResultWaitCanceled bool
}

// CoreDrain 是 10～20 分钟主线：A 正在真实 HTTP handler，B 在唯一队列位，C 被拒绝。
// stop() 可控触发 NotifyContext，服务停止入口后放 A 的门，A/B 均完成，再关闭客户端。
// 本函数拥有 backend、service 和辅助 shutdown caller；任何提前失败也先解除门，再独立预算加入。
func CoreDrain(ctx context.Context) (r CoreReport, resultErr error) {
	backend, err := NewOrderBackend()
	if err != nil {
		return r, err
	}
	defer func() { resultErr = errors.Join(resultErr, backend.Close()); r.Backend = backend.Snapshot() }()
	client := NewOrderClient(backend.URL)
	service, err := NewService(ctx, 1, 1, time.Hour, client.Fetch, client.Close, backend.Release)
	if err != nil {
		return r, errors.Join(err, client.Close())
	}
	// signal ctx 是停止服务入口的生命周期通知；work ctx 仍是独立归属预算 ctx，
	// 因此收到通知时不会顺带取消 A/B。stop() 同时注销信号资源并模拟通知。
	lifeCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	var caller sync.WaitGroup
	callerDone := make(chan struct{})
	callerStarted := false
	var ownedReport ShutdownReport
	var shutdownErr error
	defer func() {
		stop()
		backend.Release() // 一定先解除业务等待；不能拿已取消的 lifeCtx 派生清理预算。
		cleanCtx, cancel := context.WithTimeout(context.Background(), CleanupBudget)
		defer cancel()
		_, cleanupErr := service.Shutdown(cleanCtx)
		resultErr = errors.Join(resultErr, cleanupErr)
		if callerStarted {
			if err := await(cleanCtx, callerDone, "核心 shutdown caller join"); err != nil {
				resultErr = errors.Join(resultErr, err)
			} else {
				caller.Wait()
			}
		}
	}()
	if err := await(ctx, service.Ready(), "后台生产者 ready"); err != nil {
		return r, err
	}
	ticketA, err := service.Submit("A")
	if err != nil {
		return r, err
	}
	if err := await(ctx, backend.Entered(), "A handler 已进入提交门"); err != nil {
		return r, err
	}
	if _, err := service.Submit("B"); err != nil {
		return r, err
	}
	_, queueErr := service.Submit("C")
	r.FullRejected = errors.Is(queueErr, m15.ErrQueueFull)
	if !r.FullRejected {
		return r, fmt.Errorf("C 应满队列拒绝: %w", queueErr)
	}
	// 取消只影响这次 Result 等待；A 所有权已属于 service，仍停在真实 handler 门前。
	observerCtx, cancelObserver := context.WithCancel(ctx)
	cancelObserver()
	_, waitErr := ticketA.Result(observerCtx)
	r.ResultWaitCanceled = errors.Is(waitErr, context.Canceled)
	r.EarlyCloseRejected = client.Close() != nil
	if !r.ResultWaitCanceled || !r.EarlyCloseRejected {
		return r, errors.New("单项等待/客户端关闭边界没有观察到")
	}
	stop()
	if err := await(ctx, lifeCtx.Done(), "可控停止通知"); err != nil {
		return r, err
	}
	callerStarted = true
	caller.Go(func() { ownedReport, shutdownErr = service.Shutdown(ctx); close(callerDone) })
	if err := await(ctx, service.EntranceStopped(), "服务停止接纳"); err != nil {
		return r, err
	}
	_, closedErr := service.Submit("D")
	r.ClosedRejected = errors.Is(closedErr, m15.ErrClosed)
	if !r.ClosedRejected {
		return r, errors.New("停机后 D 不能被接纳")
	}
	backend.Release()
	if err := await(ctx, callerDone, "正常 drain caller 完成"); err != nil {
		return r, err
	}
	caller.Wait()
	r.Shutdown = ownedReport
	if shutdownErr != nil {
		return r, shutdownErr
	}
	for _, row := range r.Shutdown.FinalRows {
		if row.Err != nil {
			return r, row.Err
		}
		r.Amount += row.Price
	}
	if r.Amount != 14300 || len(r.Shutdown.FinalRows) != 2 || !client.Closed() {
		return r, errors.New("A/B 业务金额或客户端终态不正确")
	}
	if err := await(ctx, service.Done(), "服务全部资源 Done"); err != nil {
		return r, err
	}
	return r, nil
}

type ProbeReport struct {
	Name                                     string
	Shutdown                                 ShutdownReport
	FullRejected, ClosedRejected, RepeatSame bool
}

// ServiceProbe 把空闲、在途、满队列、超预算、已取消预算、任务 panic 路径放在同一套 A/B 数据。
// 这里的 Fetch 是辅助单测用内存函数，不作为真实 HTTP 业务验收；协议证据来自 CoreDrain。
// timeout/canceled 故意让 A 在不看 ctx 的门上等待，验证 fallback 必须先放门再 Wait。
func ServiceProbe(ctx context.Context, name string) (r ProbeReport, resultErr error) {
	r.Name = name
	if name != "idle" && name != "running" && name != "full" && name != "timeout" && name != "canceled-budget" && name != "panic" {
		return r, errors.New("未知 probe")
	}
	started, release := newGate(), newGate()
	closed := false // 只由 shutdown coordinator 写，owner 在 coordinator join 后才读。
	fetch := func(workCtx context.Context, id string) (int64, error) {
		if id == "A" {
			started.release()
			<-release.C // 故意忽略取消的错误形态；实验 owner/fallback 都具有明确释放权。
		}
		if err := workCtx.Err(); err != nil {
			return 0, err
		}
		if name == "panic" {
			panic("订单扫描配置错误")
		}
		if id == "A" {
			return 12500, nil
		}
		return 1800, nil
	}
	service, err := NewService(ctx, 1, 1, time.Hour, fetch, func() error { closed = true; return nil }, release.release)
	if err != nil {
		return r, err
	}
	defer func() {
		release.release()
		cleanCtx, cancel := context.WithTimeout(context.Background(), CleanupBudget)
		defer cancel()
		_, cleanupErr := service.Shutdown(cleanCtx)
		// timeout/canceled 的主错误是预期实验输入；若资源仍未释放，不能因为“预期失败”吞掉清理错误。
		if cleanupErr != nil && !errors.Is(cleanupErr, context.DeadlineExceeded) && !errors.Is(cleanupErr, context.Canceled) {
			resultErr = errors.Join(resultErr, cleanupErr)
		}
		if err := await(cleanCtx, service.Done(), "probe defer 实际资源 join"); err != nil {
			resultErr = errors.Join(resultErr, err)
		} else if !closed {
			resultErr = errors.Join(resultErr, errors.New("probe 客户端未关闭"))
		}
	}()
	if err := await(ctx, service.Ready(), "probe 生产者 ready"); err != nil {
		return r, err
	}
	if name != "idle" {
		if _, err := service.Submit("A"); err != nil {
			return r, err
		}
		if err := await(ctx, started.C, "probe A 已执行"); err != nil {
			return r, err
		}
		if _, err := service.Submit("B"); err != nil {
			return r, err
		}
		_, fullErr := service.Submit("C")
		r.FullRejected = errors.Is(fullErr, m15.ErrQueueFull)
		if !r.FullRejected {
			return r, errors.New("A 占 worker/B 占队列时 C 应拒绝")
		}
	}
	drainCtx := ctx
	var cancel context.CancelFunc = func() {}
	if name == "timeout" {
		drainCtx, cancel = context.WithTimeout(ctx, 25*time.Millisecond)
	}
	if name == "canceled-budget" {
		drainCtx, cancel = context.WithCancel(ctx)
		cancel()
	}
	defer cancel()
	callerDone := make(chan struct{})
	var caller sync.WaitGroup
	var firstErr error
	caller.Go(func() { _, firstErr = service.Shutdown(drainCtx); close(callerDone) })
	defer func() { joinCleanup(&resultErr, release.release, callerDone, caller.Wait) }()
	if err := await(ctx, service.EntranceStopped(), "probe 入口 stopped"); err != nil {
		return r, err
	}
	_, closedErr := service.Submit("D")
	r.ClosedRejected = errors.Is(closedErr, m15.ErrClosed)
	if name != "timeout" && name != "canceled-budget" {
		release.release()
	}
	if err := await(ctx, callerDone, "probe 第一次 shutdown 返回"); err != nil {
		return r, err
	}
	caller.Wait()
	if name == "timeout" && !errors.Is(firstErr, context.DeadlineExceeded) {
		return r, fmt.Errorf("未观察到 drain 超预算: %w", firstErr)
	}
	if name == "canceled-budget" && !errors.Is(firstErr, context.Canceled) {
		return r, fmt.Errorf("未观察到已取消预算: %w", firstErr)
	}
	// 用新预算再次等待同一个 coordinator；这不是重启 Shutdown，也不会再次关资源。
	cleanCtx, cleanCancel := context.WithTimeout(context.Background(), CleanupBudget)
	defer cleanCancel()
	r.Shutdown, _ = service.Shutdown(cleanCtx)
	repeated, _ := service.Shutdown(cleanCtx)
	r.RepeatSame = len(repeated.FinalRows) == len(r.Shutdown.FinalRows) && repeated.Err == r.Shutdown.Err
	if err := await(cleanCtx, service.Done(), "probe 最终资源 Done"); err != nil {
		return r, err
	}
	if !r.Shutdown.ProducerJoined || !r.Shutdown.WorkersJoined || !r.Shutdown.ClientClosed || !r.ClosedRejected || !r.RepeatSame {
		return r, errors.New("probe 停机终态不满足")
	}
	if name == "timeout" || name == "canceled-budget" {
		if len(r.Shutdown.UnknownAtDeadline) != 2 {
			return r, errors.New("预算失败时 A/B 都应尚无终态")
		}
		for _, row := range r.Shutdown.FinalRows {
			if row.Err == nil {
				return r, errors.New("Abort 后未开始/被释放的任务应报告取消")
			}
		}
	} else if name == "panic" {
		for _, row := range r.Shutdown.FinalRows {
			var panicErr *m15.PanicError
			if !errors.As(row.Err, &panicErr) {
				return r, errors.New("任务 panic 必须成为 Ticket 可观察错误")
			}
		}
	} else if name != "idle" {
		if len(r.Shutdown.FinalRows) != 2 || r.Shutdown.FinalRows[0].Price+r.Shutdown.FinalRows[1].Price != 14300 {
			return r, errors.New("drain 订单金额丢失")
		}
	}
	return r, nil
}

// ParentCancelProbe 单独验证 M15 的直接契约：父 ctx 取消通知正在运行任务，但空队列接收
// 仍要由 owner Close/Abort 才结束。单项 Ticket.Result 已返回也不代表 pool.Joined 已关闭。
func ParentCancelProbe(ctx context.Context) (resultErr error) {
	parent, cancelParent := context.WithCancel(ctx)
	started := newGate()
	pool, err := m15.NewPool(parent, 1, 1, false)
	if err != nil {
		cancelParent()
		return err
	}
	defer func() {
		cancelParent()
		pool.Abort(context.Canceled)
		cleanCtx, cancel := context.WithTimeout(context.Background(), CleanupBudget)
		defer cancel()
		resultErr = errors.Join(resultErr, pool.Wait(cleanCtx))
	}()
	ticket, err := pool.Submit(ctx, "A", func(workCtx context.Context, _ string) (int64, error) {
		started.release()
		<-workCtx.Done()
		return 0, workCtx.Err()
	}, nil)
	if err != nil {
		return err
	}
	if err := await(ctx, started.C, "父取消测试工作已进入"); err != nil {
		return err
	}
	cancelParent()
	row, err := ticket.Result(ctx)
	if err != nil || !errors.Is(row.Err, context.Canceled) {
		return errors.Join(err, errors.New("父取消必须进入单项终态"))
	}
	select {
	case <-pool.Joined():
		return errors.New("尚未 Close 的空队列 worker 不能已 joined")
	default:
	}
	pool.Close()
	return pool.Wait(ctx)
}

// HTTPAbortProbe 在和Core相同的真实HTTP门上耗尽正常drain预算。预算到期时A/B未知，
// fallback通知客户端取消再放服务端门；远端提交与取消传播不是同一个原子事件，因此
// 最终账本可能有A，也可能没有A。这里返回实际查询事实，绝不把“客户端返回取消”当作撤销。
func HTTPAbortProbe(ctx context.Context) (r CoreReport, resultErr error) {
	backend, err := NewOrderBackend()
	if err != nil {
		return r, err
	}
	defer func() { resultErr = errors.Join(resultErr, backend.Close()); r.Backend = backend.Snapshot() }()
	client := NewOrderClient(backend.URL)
	service, err := NewService(ctx, 1, 1, time.Hour, client.Fetch, client.Close, backend.Release)
	if err != nil {
		return r, errors.Join(err, client.Close())
	}
	defer func() {
		backend.Release()
		cleanCtx, cancel := context.WithTimeout(context.Background(), CleanupBudget)
		defer cancel()
		_, _ = service.Shutdown(cleanCtx) // 正常drain的超时属于被测主错；下面单独确认最终Done。
		resultErr = errors.Join(resultErr, await(cleanCtx, service.Done(), "真实HTTP Abort最终资源"))
	}()
	if err := await(ctx, service.Ready(), "HTTP Abort生产者ready"); err != nil {
		return r, err
	}
	if _, err := service.Submit("A"); err != nil {
		return r, err
	}
	if err := await(ctx, backend.Entered(), "HTTP Abort A已进入远端"); err != nil {
		return r, err
	}
	if _, err := service.Submit("B"); err != nil {
		return r, err
	}
	drainCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	_, firstErr := service.Shutdown(drainCtx)
	if !errors.Is(firstErr, context.DeadlineExceeded) {
		return r, fmt.Errorf("真实HTTP未触发drain deadline: %w", firstErr)
	}
	cleanCtx, cleanCancel := context.WithTimeout(context.Background(), CleanupBudget)
	defer cleanCancel()
	r.Shutdown, _ = service.Shutdown(cleanCtx)
	if !errors.Is(r.Shutdown.Err, context.DeadlineExceeded) || len(r.Shutdown.UnknownAtDeadline) != 2 || len(r.Shutdown.FinalRows) != 2 {
		return r, errors.New("真实HTTP超时快照/主错/终态缺失")
	}
	if err := await(cleanCtx, service.Done(), "真实HTTP超时后的全部资源"); err != nil {
		return r, err
	}
	for _, row := range r.Shutdown.FinalRows {
		if row.Err == nil {
			r.Amount += row.Price
		}
	}
	return r, nil
}
