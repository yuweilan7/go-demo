package lesson

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Policy string

const (
	Overlap Policy = "overlap" // 最多两轮同时执行；超出上限跳过，防止每 tick 无界启动 goroutine。
	Serial  Policy = "serial"  // 当前 loop 同步做一轮；真实 ticker 期间可能丢 tick，无应用任务队列。
	Skip    Policy = "skip"    // loop 继续读触发；已有一轮在途就记录 skipped，不启动新工作。
)

type ScheduleEvent struct {
	Tick   int
	Action string
}
type ScheduleReport struct {
	Policy                     Policy
	Received, Started, Skipped int
	Errors                     []error
}
type scheduledResult struct {
	sequence int
	err      error
}

// safeRound 由定时任务的实际执行 goroutine 同步调用，隔离每一轮 panic 并返回可观察 error。
// recover 必须在 panic 展开的同一个 goroutine 内；outer main 的 recover 不能捕获这里的 panic。
// error 交给 RunSchedule 汇总，不吞掉、不只打印“成功结束”。这不处理 runtime fatal/os.Exit/kill。
func safeRound(ctx context.Context, sequence int, work func(context.Context, int) error) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("定时任务 #%d panic: %v", sequence, value)
		}
	}()
	return work(ctx, sequence)
}

// RunSchedule 由定时器 owner 同步调用。ticks 在生产环境来自真实 ticker.C；测试用手动 channel
// 只控制“下一次触发何时被 loop 接收”，不把这个辅助输入冒充 ticker 的丢 tick 行为。
// 最多接收八个触发，工作并发最多二个；ctx 是总上限。observed 必须由调用者持续接收，或者
// 至少提供容量八的缓冲。事件交付也监听 ctx，防止观察者提前退出把 loop 卡死。
// 本函数拥有启动的工作，返回前必须加入；work 自己也要观察 ctx/有可解除的等待。
func RunSchedule(parent context.Context, ticks <-chan time.Time, policy Policy, work func(context.Context, int) error, observed chan<- ScheduleEvent) (r ScheduleReport, resultErr error) {
	if (policy != Overlap && policy != Serial && policy != Skip) || work == nil {
		return r, errors.New("未知 policy 或 nil work")
	}
	r.Policy = policy
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	completed := make(chan scheduledResult, 8) // 最多八轮结果；即使 owner 提前停止收事件也能发布结果退出。
	var workers sync.WaitGroup
	active := 0 // 仅 loop goroutine 读写；不把这个普通变量传给并发工作修改。
	stopReading := false
	for !stopReading && r.Received < 8 {
		select {
		case <-ctx.Done():
			resultErr = ctx.Err()
			stopReading = true
		case item := <-completed:
			active--
			if item.err != nil {
				r.Errors = append(r.Errors, item.err)
			}
		case _, ok := <-ticks:
			// 两值接收中的 ok=false 表示手动测试输入关闭。真实 Ticker.Stop 不会得到 false。
			if !ok {
				stopReading = true
				continue
			}
			r.Received++
			action := "started"
			limit := 2
			if policy == Skip {
				limit = 1
			}
			if policy != Serial && active >= limit {
				r.Skipped++
				action = "skipped"
			} else {
				r.Started++
				sequence := r.Received // 当前轮的整数值；闭包不会读后来增加的 r.Received。
				if policy == Serial {
					// 真正同步执行，因此这一轮结束前 loop 无法继续读 ticker。
					if err := safeRound(ctx, sequence, work); err != nil {
						r.Errors = append(r.Errors, err)
					}
				} else {
					active++
					workers.Go(func() {
						err := safeRound(ctx, sequence, work)
						completed <- scheduledResult{sequence, err}
					})
				}
			}
			if observed != nil {
				select {
				case observed <- ScheduleEvent{r.Received, action}:
				case <-ctx.Done():
					resultErr = ctx.Err()
					stopReading = true
				}
			}
		}
	}
	// 关闭输入只停止新增触发，已启动轮次仍 drain。parent 取消则工作也收到 ctx.Done。
	// Wait 不能强杀不观察取消的 work；因此实验 work 的 gate 在 owner defer 中总会释放。
	workers.Wait()
	for range active {
		item := <-completed // 已 Wait，结果必已发布；每一项只消费一次，不等不存在的结果。
		if item.err != nil {
			r.Errors = append(r.Errors, item.err)
		}
	}
	return r, resultErr
}

// PolicyProbe 由 demo/测试调用，使用两次有屏障的触发比较重叠和跳过，再观察 serial。
// 完成通知用于 join loop，workStarted 用于业务开始；二者 owner 不同，不能相互替代。
func PolicyProbe(ctx context.Context, policy Policy) (r ScheduleReport, resultErr error) {
	ticks := make(chan time.Time, 2)
	observed := make(chan ScheduleEvent, 8)
	workStarted := make(chan int, 2)
	release := newGate()
	loopDone := make(chan struct{})
	var owner sync.WaitGroup
	var loopErr error
	var ownedReport ScheduleReport
	owner.Go(func() {
		ownedReport, loopErr = RunSchedule(ctx, ticks, policy, func(workCtx context.Context, sequence int) error {
			workStarted <- sequence
			select {
			case <-release.C:
				return nil
			case <-workCtx.Done():
				return workCtx.Err()
			}
		}, observed)
		close(loopDone)
	})
	// r 由 loop 写，调用方在 done 接收后才读取。提前失败也先放门再 join，不能返回仍被修改的 r。
	defer func() {
		if joinCleanup(&resultErr, release.release, loopDone, owner.Wait) {
			r = ownedReport
			resultErr = errors.Join(resultErr, loopErr)
		}
	}()
	ticks <- time.Now()
	select {
	case <-workStarted:
	case <-ctx.Done():
		return ScheduleReport{}, ctx.Err()
	}
	if policy == Serial {
		// 首轮 work 尚卡 gate，第二触发只在辅助 channel 中等待，loop 不会读它。
		ticks <- time.Now()
		release.release()
	} else {
		select {
		case <-observed: // 先确认第一 tick 已处理，不推断 goroutine 的偶然调度顺序。
		case <-ctx.Done():
			return ScheduleReport{}, ctx.Err()
		}
		ticks <- time.Now()
		select {
		case event := <-observed:
			if policy == Overlap && event.Action != "started" {
				return ScheduleReport{}, errors.New("重叠第二轮应启动")
			}
			if policy == Skip && event.Action != "skipped" {
				return ScheduleReport{}, errors.New("跳过第二轮应 skipped")
			}
		case <-ctx.Done():
			return ScheduleReport{}, ctx.Err()
		}
		if policy == Overlap {
			select {
			case <-workStarted:
			case <-ctx.Done():
				return ScheduleReport{}, ctx.Err()
			}
		}
		release.release()
	}
	close(ticks) // 仅辅助输入由本 owner 关闭；绝不 close 真正的 ticker.C。
	if err := await(ctx, loopDone, "定时策略 loop joined"); err != nil {
		return ScheduleReport{}, err
	}
	owner.Wait()
	return ownedReport, loopErr
}
