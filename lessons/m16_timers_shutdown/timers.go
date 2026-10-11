package lesson

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// TimerReport 给 demo/测试一份库契约的观察值；时间差只作本次观察，不断言毫秒精度。
type TimerReport struct {
	FirstStop, SecondStop                            bool
	ResetStopped, ResetActive                        bool
	TimerChannelCap, TickerChannelCap                int
	AfterFuncChannelNil, CallbackStop, CallbackReset bool
	CallbackRuns                                     int32
	StopDidNotJoin, StopDidNotClose                  bool
	MissedTickSpan                                   time.Duration
}

// TimerProbe 由 demo 首先调用，把普通 channel timer 与回调 timer 分开讲。
// hour 不是猜测调度的 Sleep：它把“不应到期”这个状态设得足够远；Reset(0)则主动触发事件。
// 所有同步都靠真实接收或 started/release 屏障，外部 ctx 是观察总上限。
func TimerProbe(ctx context.Context) (r TimerReport, resultErr error) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	r.TimerChannelCap = cap(timer.C)
	r.FirstStop = timer.Stop()
	r.SecondStop = timer.Stop()
	r.ResetStopped = timer.Reset(0) // false 表示此前已停止；新的计时依然成功建立。
	select {
	case <-timer.C:
	case <-ctx.Done():
		return r, ctx.Err()
	}
	if timer.Reset(time.Hour) { // 上一轮值已消费，重置前 inactive，所以返回 false。
		return r, errors.New("已消费的 timer Reset 应返回 false")
	}
	r.ResetActive = timer.Reset(time.Hour)
	if !timer.Stop() {
		return r, errors.New("活动的 channel timer Stop 应成功")
	}
	select {
	case <-timer.C:
		return r, errors.New("Stop 后出现旧值或 channel 被关闭")
	default:
		r.StopDidNotClose = true // 单次立即检查，无忙转；若 C 已关闭，接收分支会立即就绪。
	}

	// AfterFunc 没有供接收的 C；回调在另一 goroutine 内。两个回调都冻结在同一个 release 门，
	// 所以可以证明 Stop/Reset 返回后，已经启动的第一轮仍然没有完成。
	started := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	release := newGate()
	var callbacks, joiner sync.WaitGroup
	var runs atomic.Int32
	callbacks.Add(1) // 必须先登记，再允许回调启动；不能让 Wait 看见计数零后才 Add。
	callbackTimer := time.AfterFunc(0, func() {
		defer callbacks.Done()
		index := int(runs.Add(1)) - 1
		close(started[index]) // 每轮只关闭自己的通知；close 发布“已进入回调”的事实。
		<-release.C           // 当前等待的是实验门，不是 Timer.Stop，也不是 timer.C。
	})
	joined := make(chan struct{})
	// joiner 只能在第二轮 Add 完成后启动；错误提前返回时也先进入下面的 defer 再等。
	joinStarted := false
	defer func() {
		if callbackTimer.Stop() {
			callbacks.Done()
		} // 尚未启动的那轮被取消，没有回调替我们 Done。
		release.release()
		if !joinStarted {
			joiner.Go(func() { callbacks.Wait(); close(joined) })
		}
		joinCleanup(&resultErr, release.release, joined, joiner.Wait)
		r.CallbackRuns = runs.Load()
	}()
	r.AfterFuncChannelNil = callbackTimer.C == nil
	if err := await(ctx, started[0], "第一轮 AfterFunc 已启动"); err != nil {
		return r, err
	}
	r.CallbackStop = callbackTimer.Stop() // started 屏障保证已经开始，因此 false 并且不会 join。
	r.StopDidNotJoin = !r.CallbackStop    // 这时 release 未关闭，第一轮不可能走过等待点。
	callbacks.Add(1)
	r.CallbackReset = callbackTimer.Reset(0)
	if r.CallbackReset {
		// 已启动回调对应的 timer inactive，正常不应走到这里；取消计数避免错误路径自己卡死。
		callbacks.Done()
		return r, errors.New("已开始回调的 Reset 应返回 false")
	}
	joiner.Go(func() { callbacks.Wait(); close(joined) })
	joinStarted = true
	if err := await(ctx, started[1], "第二轮 AfterFunc 已启动"); err != nil {
		return r, err
	}
	release.release()
	if err := await(ctx, joined, "两个 AfterFunc 回调 join"); err != nil {
		return r, err
	}
	joiner.Wait()
	r.CallbackRuns = runs.Load()

	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	r.TickerChannelCap = cap(ticker.C)
	// 暂不读取 ticker，用一次真正 timer 等待至少八个周期。这里延迟接收是被测业务，
	// 不是靠睡眠猜某个 goroutine 是否已启动。慢接收后两条时间戳的间隔用于展示丢 tick。
	pause := time.NewTimer(40 * time.Millisecond)
	defer pause.Stop()
	select {
	case <-pause.C:
	case <-ctx.Done():
		return r, ctx.Err()
	}
	var first, second time.Time
	select {
	case first = <-ticker.C:
	case <-ctx.Done():
		return r, ctx.Err()
	}
	select {
	case second = <-ticker.C:
	case <-ctx.Done():
		return r, ctx.Err()
	}
	r.MissedTickSpan = second.Sub(first)
	ticker.Reset(time.Hour) // Ticker.Reset 无 bool 返回值，正周期才合法。
	ticker.Stop()           // 只停止触发，不关闭 C，也不取消正在做任务的接收者。
	select {
	case <-ticker.C:
		return r, errors.New("Ticker.Stop 后收到旧 tick 或 channel 已关闭")
	default:
	}
	return r, nil
}

// StopOnlyProbe 是有救援门的故障实验。坏 worker 只等 ticker.C，Stop 并不使接收就绪，
// 所以它不会退出；正常生产代码必须像下方修复一样 select ctx.Done。
// 实验 owner 用 Reset 注入最后一个 tick 让坏 worker 读到 atomic rescue 标志，仅用于回收。
func StopOnlyProbe(ctx context.Context) (observed bool, resultErr error) {
	ticker := time.NewTicker(time.Hour)
	ready, done := make(chan struct{}), make(chan struct{})
	var rescue atomic.Bool
	var workers sync.WaitGroup
	workers.Go(func() {
		defer close(done)
		close(ready)
		for {
			<-ticker.C // BUG：没有业务取消分支；Stop 后的 channel 仍开着，因此这一行继续等待。
			if rescue.Load() {
				return // fixture 救援，不能称为“Stop 成功取消了 worker”。
			}
		}
	})
	defer func() {
		rescue.Store(true)
		ticker.Reset(time.Nanosecond)
		joinCleanup(&resultErr, func() {}, done, workers.Wait)
		ticker.Stop()
	}()
	if err := await(ctx, ready, "坏 worker 接收入口"); err != nil {
		return false, err
	}
	ticker.Stop()
	select {
	case <-done:
		return false, errors.New("没有 tick/rescue 时坏 worker 不可能正常退出")
	default:
		observed = true // 这是协议上的不可能退出：唯一 return 要先收到 tick，而触发源已停止。
	}
	return observed, nil
}

// CancelTickerProbe 是相同业务的修复：owner 创建取消范围，worker 同时等待 ctx 与 tick。
// owner cancel 后必须 Wait；defer ticker.Stop 只在 worker 返回时执行，不是返回的原因。
func CancelTickerProbe(ctx context.Context) (resultErr error) {
	workCtx, cancel := context.WithCancel(ctx)
	ready, done := make(chan struct{}), make(chan struct{})
	var worker sync.WaitGroup
	worker.Go(func() {
		defer close(done)
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		close(ready)
		for {
			select {
			case <-workCtx.Done():
				return
			case <-ticker.C:
				// 一轮扫描放在这里；如果扫描也会等待 I/O，还要将 workCtx 传入那一层。
			}
		}
	})
	defer func() { joinCleanup(&resultErr, cancel, done, worker.Wait) }()
	if err := await(ctx, ready, "修复 worker 已创建 ticker"); err != nil {
		return err
	}
	cancel() // 仅通知；下面真正等待 done + Wait 才证明退出。
	return await(ctx, done, "修复 worker 退出")
}

func (r TimerReport) String() string {
	return fmt.Sprintf("Stop=%t/%t Reset(stopped/active)=%t/%t C.cap(timer/ticker)=%d/%d AfterFunc.C=nil:%t Stop=%t Reset=%t callbacks=%d Stop不join=%t 慢接收tick跨度=%v",
		r.FirstStop, r.SecondStop, r.ResetStopped, r.ResetActive, r.TimerChannelCap, r.TickerChannelCap,
		r.AfterFuncChannelNil, r.CallbackStop, r.CallbackReset, r.CallbackRuns, r.StopDidNotJoin, r.MissedTickSpan.Round(time.Millisecond))
}
