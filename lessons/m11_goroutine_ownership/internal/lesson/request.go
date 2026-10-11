package lesson

import (
	"context"
	"fmt"
	"sync"
)

// RequestTask 是请求 handler 必须保留的所有权句柄。
// cancel 只通知；done 表示结果与资源清理已发布；group 负责等待工作函数返回。
// result/outcome 由同一个 worker 写，owner 仅在 done/Wait 之后读取 outcome。
type RequestTask struct {
	started   chan struct{}
	sending   chan struct{}
	done      chan struct{}
	results   chan Receipt
	cancel    context.CancelFunc
	group     sync.WaitGroup
	outcome   Receipt
	delivered bool
}

// StartRequest 由请求 owner 调用，创建一个仅属于此请求的结果发送任务。
// tracker 在 go 前登记 O-101；buffer 为 0 演示会合，为 1 演示单结果邮箱。
// 返回指针避免复制内部 WaitGroup。parent 应来自请求作用域；不可把任务随手遗忘。
func StartRequest(parent context.Context, trace *Trace, tracker *Tracker, buffer int) *RequestTask {
	ctx, cancel := context.WithCancel(parent)
	task := &RequestTask{
		started: make(chan struct{}), sending: make(chan struct{}), done: make(chan struct{}),
		results: make(chan Receipt, buffer), cancel: cancel,
	}
	tracker.acquire("request-O-101")
	task.group.Go(func() {
		// defer 后注册先运行。结束时先释放令牌，再 close(done)；Go 的包装函数
		// 在本函数返回后减 WaitGroup 计数。Wait 才是本课的最后一层 join。
		defer close(task.done)
		defer func() {
			tracker.release("request-O-101")
			trace.Event("E03 request worker resources-released")
		}()
		close(task.started)
		task.outcome = Receipt{OrderID: "O-101", Amount: 200}
		trace.Event(fmt.Sprintf("E03 request worker before-send cap=%d", buffer))
		close(task.sending) // 只证明到达发送前边界，不声称已经进入 runtime 的阻塞状态。
		select {
		case task.results <- task.outcome:
			// buffer=0 只证明接收者已拿到值；buffer=1 可以只进入缓冲，
			// 两者都不能证明接收者已完成后续数据库写入。
			task.delivered = true
			trace.Event("E03 request worker send-accepted")
		case <-ctx.Done():
			// 无接收者且 cap=0 时发送不就绪；取消通知让这个分支可执行。
			// 不把通知当完成：清理 defer 与 done/Wait 仍在后面。
			task.outcome.Err = ctx.Err()
			trace.Event("E03 request worker cancellation-observed")
		}
	})
	return task
}

// Started/Sending/Done 把接收权限交给 owner，禁止外部关闭本 worker 的信号。
func (t *RequestTask) Started() <-chan struct{} { return t.started }
func (t *RequestTask) Sending() <-chan struct{} { return t.sending }
func (t *RequestTask) Done() <-chan struct{}    { return t.done }
func (t *RequestTask) Results() <-chan Receipt  { return t.results }

// Cancel 由请求 owner 调用；context 取消是协作通知，不是 Java Thread.interrupt 的等价实现。
// 此方法返回并不证明 worker 已观察、返回或释放令牌，必须继续 Join。
func (t *RequestTask) Cancel() { t.cancel() }

// Stop 供 owner 的 defer 收尾：发送取消通知后继续 join，不能只 Cancel 就返回。
// 此 worker 的唯一业务等待使用 select 观察 ctx，因此无需他人再关闭 results。
func (t *RequestTask) Stop() {
	t.cancel()
	t.group.Wait()
}

// Join 等待当前任务发布结果/清理，再等待工作函数返回。
// waitCtx 是观察者的时间预算；超时不取消 task，owner 应另行 Cancel 并收尾。
func (t *RequestTask) Join(waitCtx context.Context) (Receipt, bool, error) {
	if err := await(waitCtx, t.done, "request done"); err != nil {
		return Receipt{}, false, err
	}
	t.group.Wait()
	return t.outcome, t.delivered, nil
}

// RunNoReceiverFixes 展示无人接收结果的两个修复边界，正常路径不会留下 sender。
// 一项单结果允许缓冲；请求放弃则取消并 join。调用者检查两个返回事实。
// 返回参数 buffered/canceled/err 是本函数作用域里的具名变量；下方赋值用 = 更新它们，
// 裸 return 把当前三个值交回 caller。不能在内层用 := 意外遮蔽 err 再返回旧的 nil。
func RunNoReceiverFixes(ctx context.Context, trace *Trace) (buffered Receipt, canceled Receipt, err error) {
	tracker := NewTracker()
	mailbox := StartRequest(ctx, trace, tracker, 1)
	defer mailbox.Stop() // 任一错误返回也通知并 join；已完成时重复停止是安全的。
	if err = await(ctx, mailbox.Sending(), "mailbox sending"); err != nil {
		return
	}
	var delivered bool // 当前任务是否完成发送，与 receipt 是否算出来是两个事实。
	buffered, delivered, err = mailbox.Join(ctx)
	if err != nil {
		return
	}
	if err = require(delivered && len(mailbox.results) == 1 && buffered.Amount == 200, "单结果已入邮箱，尚无人读取"); err != nil {
		return
	}
	trace.Event("E03 owner joined buffered-task; mailbox-values=1; receiver-business未执行")
	// 本课最后由 owner 排空教学邮箱，防止误称“已经被业务消费”。
	<-mailbox.Results()
	unbuffered := StartRequest(ctx, trace, tracker, 0)
	defer unbuffered.Stop()
	if err = await(ctx, unbuffered.Sending(), "unbuffered sending"); err != nil {
		return
	}
	select {
	case <-unbuffered.Done():
		return buffered, canceled, fmt.Errorf("无接收者的 unbuffered 任务提前完成")
	default:
	}
	// 发送没有接收者，因此不能取得进展；Cancel 只改变 ctx.Done 的就绪状态。
	trace.Event("E03 owner abandoning-result; cancel-notified")
	unbuffered.Cancel()
	canceled, delivered, err = unbuffered.Join(ctx)
	if err != nil {
		return
	}
	active, resources := tracker.Counts()
	trace.Event(fmt.Sprintf("E03 owner joined canceled-task active=%d resources=%d delivered=%t", active, resources, delivered))
	err = require(!delivered && canceled.Err == context.Canceled && active == 0 && resources == 0, "取消发送者后真实收尾")
	return
}
