package course

import (
	"context"
	"errors"
	"fmt"
)

// SelectReport 是有界 select 实验的事实集合。Counts 不拿来断言比例、轮流或“哪个 case 优先”。
type SelectReport struct {
	DefaultHits     int
	ClosedHits      int
	Merged          []int
	LeftReady       int
	RightReady      int
	SentAfterCancel int
	ChoseCancel     int
}

// SelectCases 由 demo/测试运行：先复现 bounded 忙循环，再显示关闭分支禁用和双 ready 选择。
// 循环上限决定实验的工作量，ctx 只保护后面的真正等待，不用 Sleep 猜测数据何时到达。
func SelectCases(ctx context.Context, trace *Trace) (report SelectReport, err error) {
	report = SelectReport{}
	empty := make(chan int)
	for index := 0; index < 100_000; index++ {
		select {
		case <-empty:
			return report, fmt.Errorf("没有发送者的 open channel 不应收到数据")
		default:
			report.DefaultHits++ // 无等待，重复检查；放进无限 for 就会一直占用执行机会。
		}
	}
	trace.Log("select", "default_spin", "empty 未改变，仍跑完 %d 次 default；修复是阻塞等待", report.DefaultHits)

	started, done := make(chan struct{}), make(chan struct{})
	defer func() { err = errors.Join(err, cleanupAwait(done, "select 接收者清理")) }()
	go func() {
		defer close(done)
		close(started)
		select {
		case value := <-empty:
			trace.Log("select-reader", "blocking_receive", "没有 default，真正等到 value=%d", value)
		case <-ctx.Done():
			trace.Log("select-reader", "cancel_observed", "%v", ctx.Err())
		}
	}()
	if err := await(ctx, started, "阻塞接收前屏障"); err != nil {
		return report, err
	}
	select {
	case empty <- 9:
	case <-ctx.Done():
		return report, ctx.Err()
	}
	if err := await(ctx, done, "阻塞接收者退出"); err != nil {
		return report, err
	}

	closed := make(chan int)
	close(closed)
	for index := 0; index < 100_000; index++ {
		select {
		case _, ok := <-closed:
			if ok {
				return report, fmt.Errorf("closed/drained 必须是 ok=false")
			}
			report.ClosedHits++ // 这里即使没有 default 也不等待，closed 的接收永远 ready。
		default:
			return report, fmt.Errorf("closed 接收永远 ready，不能进入 default")
		}
	}
	trace.Log("select", "closed_spin", "closed/drained 立刻返回 ok=false %d 次", report.ClosedHits)

	left, right := make(chan int, 1), make(chan int, 1)
	left <- 11
	right <- 22
	close(left)
	close(right)
	// left/right 是当前 goroutine 的 channel 变量，赋 nil 只禁用这个 select 的 case。
	// 原 channel 对象仍然 closed；其他持有者的变量不会被一起变成 nil。
	for left != nil || right != nil {
		select {
		case value, ok := <-left:
			if !ok {
				left = nil
				trace.Log("select", "disable_left", "left=nil，下一轮此 case 永远不 ready")
				continue
			}
			report.Merged = append(report.Merged, value)
			trace.Log("select", "merge_value", "left=%d", value)
		case value, ok := <-right:
			if !ok {
				right = nil
				trace.Log("select", "disable_right", "right=nil，下一轮此 case 永远不 ready")
				continue
			}
			report.Merged = append(report.Merged, value)
			trace.Log("select", "merge_value", "right=%d", value)
		}
	}

	left, right = make(chan int, 1), make(chan int, 1)
	left <- 1
	right <- 2
	for index := 0; index < 4_096; index++ {
		// 每次选择后把值放回，保证下一轮两个 case 都 ready；书写顺序没有业务优先级。
		select {
		case value := <-left:
			report.LeftReady++
			left <- value
		case value := <-right:
			report.RightReady++
			right <- value
		}
	}
	trace.Log("select", "both_ready", "left=%d right=%d；只能断言总次数=4096", report.LeftReady, report.RightReady)

	canceled, cancel := context.WithCancel(ctx)
	cancel() // 在每轮 select 之前 Done 就已关闭，不是“运行期间才可能取消”。
	out := make(chan int, 1)
	for index := 0; index < 4_096; index++ {
		select {
		case out <- index:
			report.SentAfterCancel++
			<-out // 腾出空位，使下一轮 send 仍 ready。
		case <-canceled.Done():
			report.ChoseCancel++
		}
	}
	trace.Log("select", "cancel_not_priority", "已 cancel 后仍选发送=%d，选取消=%d；不是固定比例或严格停止承诺", report.SentAfterCancel, report.ChoseCancel)
	return report, nil
}
