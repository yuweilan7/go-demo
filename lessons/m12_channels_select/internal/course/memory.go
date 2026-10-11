package course

import (
	"context"
	"errors"
)

// MemoryReport 保留三个可以由断言核对的事实：值副本不受修改影响、移交指针按协议修改、
// close 之前的写入在接收关闭通知之后可见。本课不依赖 runtime 内部锁的具体实现。
type MemoryReport struct {
	CopiedAmount   int
	OriginalAmount int
	ReturnedAmount int
	FinalCount     int
}

// MemoryOwnership 由 demo/测试调用。每个共享对象都有明确的通信边界，避免把
// “channel 本身并发安全”误读成“channel 里传的指针对象自动线程安全”。
func MemoryOwnership(ctx context.Context, trace *Trace) (MemoryReport, error) {
	return memoryOwnership(ctx, trace, nil)
}

// memoryHooks 仅供本包取消测试设置真实屏障，不是课程业务配置。测试拥有并创建这些 channel；
// 最终发布任务关闭 finalStarted，owner 的 defer 关闭 cleanupStarted，测试关闭 releaseFinal。
// 默认公开入口传 nil，最终发布直接进行，不增加正常流程中的等待。
type memoryHooks struct {
	finalStarted   chan<- struct{}
	releaseFinal   <-chan struct{}
	cleanupStarted chan<- struct{}
}

// memoryOwnership 是 MemoryOwnership 的实际调用链；tests 仅给最后的发布工作段加屏障。
// report/err 为具名返回值，所有 defer 执行完以后才返回，业务取消与清理错误都保留在 err 中。
func memoryOwnership(ctx context.Context, trace *Trace, hooks *memoryHooks) (report MemoryReport, err error) {
	values := make(chan Order, 1)
	original := Order{ID: "O-01", AmountCents: 100}
	values <- original       // 复制 string/int 值，发送返回也不需要接收者。
	original.AmountCents = 9 // 改的是 original 的独立字段，不是缓冲区里的 Order 值。
	copied := <-values
	report.CopiedAmount, report.OriginalAmount = copied.AmountCents, original.AmountCents
	trace.Log("memory", "value_copy", "收到 amount=%d，原变量后来改成 %d", copied.AmountCents, original.AmountCents)

	// 指针只复制地址。这里采用独占所有权移交：main 发出后直到 returnPointer 收到才再触碰对象。
	pointer := &Order{ID: "O-01", AmountCents: 100}
	toWorker, returnPointer := make(chan *Order), make(chan *Order)
	done := make(chan struct{})
	defer func() { err = errors.Join(err, cleanupAwait(done, "指针所有权 worker 清理")) }()
	go func() {
		defer close(done)
		select {
		case owned := <-toWorker:
			owned.AmountCents = 200
			select {
			case returnPointer <- owned:
			case <-ctx.Done():
			}
		case <-ctx.Done():
		}
	}()
	select {
	case toWorker <- pointer:
		// send 返回后继续写 pointer.AmountCents 就会破坏所有权；cmd/fault pointer-race 真正复现。
	case <-ctx.Done():
		return report, ctx.Err()
	}
	select {
	case owned := <-returnPointer:
		report.ReturnedAmount = owned.AmountCents // worker 写 -> 回传 send -> 此接收 -> owner 读，建立同步。
		trace.Log("memory", "pointer_returned", "同一个地址已回传，owner 此时再读 amount=%d", report.ReturnedAmount)
	case <-ctx.Done():
		return report, ctx.Err()
	}
	if err := await(ctx, done, "指针 worker 退出"); err != nil {
		return report, err
	}

	finished := make(chan struct{})
	finalCount := 0
	var publicationErr error // 仅 publisher 写；owner 在 finished 关闭同步之后才读取。
	// 先登记最后工作段的独立清理，再启动它。即使下面 await 的业务 ctx 已取消，
	// 本 defer 仍用新建的有界清理 ctx 等待 finished，不能只等早已结束的指针 worker done。
	defer func() {
		if hooks != nil {
			close(hooks.cleanupStarted) // 测试可以观察：业务等待已返回，owner 正进入最终发布清理。
		}
		cleanupErr := cleanupAwait(finished, "最终值发布任务清理")
		err = errors.Join(err, cleanupErr) // 不覆盖 await 返回的 context.Canceled/DeadlineExceeded。
		if cleanupErr == nil {
			// 收到 finished 后才能安全读取 publisher 写的字段；取消结果也携带已确认的最终状态。
			report.FinalCount = finalCount
			err = errors.Join(err, publicationErr)
			trace.Log("memory", "final_publish_joined", "独立清理已收到 finished，finalCount=%d；保留业务错误=%v", finalCount, err)
		}
	}()
	go func() {
		defer close(finished) // 无论发布完成或测试屏障超时，均公布本工作段没有后续写入。
		if hooks != nil {
			close(hooks.finalStarted)
			// 故障夹具暂时冻结发布。它不用已取消的业务 ctx，否则取消立即放行，
			// 便不能证明 owner 确实等这项工作；独立上限确保测试屏障也不会永久挂起。
			gateCtx, cancel := context.WithTimeout(context.Background(), CleanupLimit)
			defer cancel()
			publicationErr = await(gateCtx, hooks.releaseFinal, "测试放行最终发布")
			if publicationErr != nil {
				return
			}
		}
		finalCount = 6 // 写发生在 deferred close(finished) 前，接收关闭通知后可见。
	}()
	if err := await(ctx, finished, "close 发布最终值"); err != nil {
		return report, err
	}
	report.FinalCount = finalCount
	trace.Log("memory", "close_visibility", "收到关闭通知后读 finalCount=%d；goroutine 自然退出本身没有此同步保证", finalCount)
	return report, nil
}
