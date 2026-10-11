package lesson

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// LifecycleReport 是 E01 在 done 同步以后读取的快照。
// Value/Cleaned 在 worker 内写入；caller 绝不在接收 done 以前读取它们。
type LifecycleReport struct {
	Value             int
	Cleaned           bool
	UnfinishedAtStart bool
}

// RunLifecycle 由正常入口/测试同步调用，是三个信号的 owner。
// started 由 worker 关闭，release 由本函数关闭，done 由 worker 最后一个 defer 关闭。
// 返回值交给入口与独立测试；go 调用本身没有 Future，也拿不到 worker 的 return 值。
func RunLifecycle(ctx context.Context, trace *Trace) (LifecycleReport, error) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var result LifecycleReport
	var group sync.WaitGroup
	// group 跟踪 worker 最后一个 defer 的 Done。done 表示结果与清理已发布；
	// Done/Wait 也不是“runtime 已回收栈”的证明，worker 仍可能处于返回的尾部。
	group.Add(1)
	go chooseWorker(trace, &group)(evaluateArgument(trace), started, release, done, &result)
	trace.Event("E01 caller go-returned (没有隐式 join)")
	// 不猜哪个 goroutine 先被调度；只在明确收到 started 以后陈述 worker 已运行。
	if err := await(ctx, started, "E01 started"); err != nil {
		close(release)
		group.Wait()
		return result, err
	}
	select {
	case <-done:
		return result, fmt.Errorf("E01 worker 未放行却报告完成")
	default:
		// worker 必须等待尚未关闭的 release，故此处未完成是机制保证而非偶然调度。
		result.UnfinishedAtStart = true
	}
	trace.Event("E01 caller observed-started; done=false; release 尚未关闭")
	close(release)
	trace.Event("E01 caller release-notified")
	// release-notified 的日志可能晚于 worker observed-release：close 已先发生，
	// 日志锁不会保证 caller 在 close 后一定先取得运行机会。
	if err := await(ctx, done, "E01 done"); err != nil {
		group.Wait()
		return result, err
	}
	group.Wait()
	trace.Event(fmt.Sprintf("E01 caller joined value=%d cleaned=%t", result.Value, result.Cleaned))
	return result, require(result.Value == 200 && result.Cleaned && result.UnfinishedAtStart, "E01 结果、清理和启动状态")
}

// chooseWorker 在 caller 中求值，返回的函数值才是 go 语句的被调用函数。
// 这里把选择函数和实际函数体分开，避免把实参求值误认为 worker 已运行。
func chooseWorker(trace *Trace, group *sync.WaitGroup) func(int, chan<- struct{}, <-chan struct{}, chan<- struct{}, *LifecycleReport) int {
	trace.Event("E01 caller function-value-evaluated")
	return func(amount int, started chan<- struct{}, release <-chan struct{}, done chan<- struct{}, result *LifecycleReport) int {
		// amount 是 caller 求值后传入的 int 副本；result 是同一报告的指针别名。
		// 参数 channel 方向限制本函数的操作：started/done 仅发送/关闭，release 仅接收。
		defer group.Done()
		defer func() {
			close(done)
			trace.Event("E01 worker done-notified (结果与清理已发布)")
		}()
		defer func() {
			// defer 后注册先执行：先完成清理，再 close(done)/记通知日志，最后 Done。
			result.Cleaned = true
			trace.Event("E01 worker cleanup-written")
		}()
		trace.Event(fmt.Sprintf("E01 worker body-start amount=%d", amount))
		close(started)
		<-release // worker 的真正业务等待点；只有 owner 的 close(release) 能放行。
		trace.Event("E01 worker observed-release")
		result.Value = amount
		trace.Event("E01 worker result-written")
		return 999 // go 语句会丢弃这个返回值，caller 收到的是显式发布的 result.Value。
	}
}

// evaluateArgument 是实参表达式，在 caller 中运行完后才会创建新 goroutine。
func evaluateArgument(trace *Trace) int {
	trace.Event("E01 caller argument-evaluated O-101 units=2 amount=200")
	return 2 * 100
}

var ErrPriceUnavailable = errors.New("O-102 的价格不可用")

// Receipt 把每项任务自己的业务结果与错误一起保存，WaitGroup 本身不携带它们。
type Receipt struct {
	OrderID string
	Amount  int
	Err     error
}

// RunBatch 由入口调用，style 为 classic 或 go，使用同样的三份合成订单比较 API。
// 本函数拥有任务、release 和 WaitGroup；每项任务只写 results[id]，互不写同一元素。
func RunBatch(ctx context.Context, trace *Trace, style string) ([]Receipt, error) {
	if style != "classic" && style != "go" {
		return nil, fmt.Errorf("未知 WaitGroup 风格 %q", style)
	}
	inputs := []int{2, 3, 5}
	results := make([]Receipt, len(inputs))
	started := make(chan struct{}, len(inputs))
	release := make(chan struct{})
	var group sync.WaitGroup // 只在此处声明，不在返回值、参数或 struct 副本中复制它。
	for id, units := range inputs {
		work := func() {
			// Go 1.27 的 range 声明变量每轮独立；闭包捕获本轮 id、units。
			// started 的容量等于任务数，发送只表示进入 body，不代表订单处理完成。
			started <- struct{}{}
			<-release
			results[id] = Receipt{OrderID: fmt.Sprintf("O-%d", 101+id)}
			if id == 1 {
				results[id].Err = ErrPriceUnavailable
				trace.Event("E02 " + style + " O-102 business-error (不取消兄弟)")
				return
			}
			results[id].Amount = units * 100
			trace.Event("E02 " + style + " " + results[id].OrderID + " result-written")
		}
		if style == "classic" {
			// counter 从 0 增加必须发生在 Wait 之前。Add 在 go 前完成，
			// 避免 worker 尚未运行时 owner 在 counter=0 上提前通过 Wait。
			group.Add(1)
			go func() {
				defer group.Done() // 正常 return/当前 goroutine 栈展开时减一次；不传 error。
				work()
			}()
		} else {
			// WaitGroup.Go 自己 Add 并创建 goroutine，f 返回时移除任务。
			// f 不得 panic；本 work 用普通 error 值表达业务失败，未抛 panic。
			group.Go(work)
		}
	}
	for range inputs {
		if err := await(ctx, started, "E02 body-start"); err != nil {
			close(release)
			group.Wait()
			return nil, err
		}
	}
	trace.Event("E02 " + style + " caller observed-all-started; release-notified")
	close(release)
	// 已放行，work 只做有界纯计算与日志；不存在未解除的业务等待。
	// Wait 无超时参数，若未来 work 加入 I/O，必须给 I/O 自身设置取消/超时。
	group.Wait()
	trace.Event(fmt.Sprintf("E02 %s caller joined amounts=[%d %d %d] errors=1", style, results[0].Amount, results[1].Amount, results[2].Amount))
	return results, require(results[0].Amount == 200 && results[2].Amount == 500 && errors.Is(results[1].Err, ErrPriceUnavailable), "E02 错误记录与兄弟完成")
}

// RunLateAdd 用屏障确定复现旧写法的提前 Wait，随后由实验 owner 完整收尾。
// 这不是一个供业务使用的 API：addAllowed 只为把错误的顺序固定下来。
func RunLateAdd(ctx context.Context, trace *Trace) (bool, error) {
	addAllowed := make(chan struct{})
	added := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var group sync.WaitGroup
	go func() {
		defer close(done)
		<-addAllowed
		group.Add(1) // 错误位置：go 已发生，owner 可以先在 counter=0 时调用 Wait。
		close(added)
		defer group.Done()
		<-release
	}()
	group.Wait() // addAllowed 尚未关闭，所以 counter 必定仍为 0，Wait 立即返回。
	unfinished := false
	select {
	case <-done:
	default:
		unfinished = true
	}
	trace.Event(fmt.Sprintf("E02 late-add caller Wait-returned; task-unfinished=%t", unfinished))
	// 后续仅是实验救援：禁止业务把提前 Wait 解释成成功。
	close(addAllowed)
	if err := await(ctx, added, "late-add 已登记"); err != nil {
		close(release)
		<-done
		return unfinished, err
	}
	close(release)
	group.Wait()
	if err := await(ctx, done, "late-add 救援完成"); err != nil {
		return unfinished, err
	}
	trace.Event("E02 late-add harness released-and-joined")
	return unfinished, require(unfinished, "late Add 未被误当成完成")
}
