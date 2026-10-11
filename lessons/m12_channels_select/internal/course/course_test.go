package course

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// testContext 由每个测试建立有界业务范围；Cleanup 注册到测试退出，防止断言 Fatal 后漏掉 timer。
// 并发先后仍由 channel 屏障建立，3 秒只是在代码回归后把永久等待转成明确失败。
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestStateTable 独立列出规范预期，不用生产函数的布尔值当自身证明。
// closed 中仍有真实零值时 ok=true，与 closed/drained 的 ok=false 是两个不同事实。
func TestStateTable(t *testing.T) {
	expected := []StateRow{
		{"nil", "would block", "would block", "panic: close of nil channel"},
		{"open unbuffered empty", "would block", "would block", "closed successfully"},
		{"open buffered empty cap=1", "would block", "accepted", "closed successfully"},
		{"open buffered zero partial", "value=0 ok=true", "accepted", "closed successfully"},
		{"open buffered zero full", "value=0 ok=true", "would block", "closed successfully"},
		{"closed buffered zero", "value=0 ok=true", "panic: send on closed channel", "panic: close of closed channel"},
		{"closed drained", "value=0 ok=false", "panic: send on closed channel", "panic: close of closed channel"},
	}
	actual := ChannelStates(nil)
	if !slices.Equal(actual, expected) {
		t.Fatalf("状态表不同: actual=%+v expected=%+v", actual, expected)
	}
	drained := DrainClosed(nil)
	if len(drained) != 4 || drained[0].Value != 0 || !drained[0].OK || drained[1].Value != 7 || !drained[1].OK || drained[2].Value != 0 || drained[2].OK || drained[3].OK {
		t.Fatalf("close 丢弃了缓冲值或零值结束被混淆: %+v", drained)
	}
}

// TestRendezvousAndRange 调用真实 goroutine 屏障反例和修复，而不是用 time.Sleep 后读一个标志。
func TestRendezvousAndRange(t *testing.T) {
	ctx := testContext(t)
	if err := Rendezvous(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := RangeNeedsClose(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

// assertOrders 用输入业务数据作为独立 oracle：每个 ID 恰好出现一次，金额保存，非法金额被拒绝。
// 按 map 比较集合，不把某次 worker 调度/输出顺序写成课程契约。
func assertOrders(t *testing.T, actual []Result, expected []Order) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("消费条数=%d，期待=%d", len(actual), len(expected))
	}
	byID := make(map[string]Order, len(expected))
	for _, order := range expected {
		byID[order.ID] = order
	}
	seen := make(map[string]bool, len(actual))
	for _, result := range actual {
		order, exists := byID[result.Order.ID]
		if !exists || seen[result.Order.ID] || order != result.Order {
			t.Fatalf("未知/重复/损坏的订单结果: %+v", result)
		}
		seen[result.Order.ID] = true
		if order.AmountCents <= 0 {
			if !errors.Is(result.Err, ErrInvalidOrder) {
				t.Fatalf("非法订单 %s 没保留拒绝原因: %v", order.ID, result.Err)
			}
		} else if result.Err != nil {
			t.Fatalf("合法订单 %s 被错误拒绝: %v", order.ID, result.Err)
		}
	}
}

// TestNormalAndEmptyMultiProducer 将零容量与有限缓冲分别运行；1/3/8 个 worker 都写同一 Results。
// Collect 返回后再实际接收 Results 得到 ok=false，验证唯一关闭者确实关闭且没有未交付的尾值。
func TestNormalAndEmptyMultiProducer(t *testing.T) {
	for _, workers := range []int{1, 3, 8} {
		for _, capacity := range []int{0, 1} {
			for _, empty := range []bool{false, true} {
				name := fmt.Sprintf("workers=%d/buffer=%d/empty=%t", workers, capacity, empty)
				t.Run(name, func(t *testing.T) {
					ctx := testContext(t)
					input := SampleOrders()
					if empty {
						input = nil
					}
					pipeline, err := Start(ctx, input, Options{Workers: workers, JobsBuffer: capacity, ResultsBuffer: capacity, FailAt: -1}, nil)
					if err != nil {
						t.Fatal(err)
					}
					report := Collect(ctx, pipeline, 0, nil)
					if report.Err != nil || report.Completion.Produced != len(input) || report.Completion.Validated != len(input) || report.Completion.WorkersExited != workers || report.Completion.SourceErr != nil || report.Completion.Canceled {
						t.Fatalf("正常终态不正确: %+v", report)
					}
					assertOrders(t, report.Results, input)
					if _, ok := <-pipeline.Results; ok {
						t.Fatal("owner 返回后 Results 尚有尾值/仍打开")
					}
				})
			}
		}
	}
}

// TestSourceError 不把来源失败误判为“关闭等于成功”。检查已交付前缀完整、终态 error 链未丢。
// failAt=len 模拟最后一个订单也交付之后来源才报错；仍然不能向调用者返回 nil。
func TestSourceError(t *testing.T) {
	input := SampleOrders()
	for _, failAt := range []int{0, 3, len(input)} {
		t.Run(fmt.Sprintf("failAt=%d", failAt), func(t *testing.T) {
			report := Run(testContext(t), input, Options{Workers: 3, JobsBuffer: 1, ResultsBuffer: 1, FailAt: failAt}, nil)
			if !errors.Is(report.Err, ErrSource) || !errors.Is(report.Completion.SourceErr, ErrSource) || report.Completion.WorkersExited != 3 || report.Completion.Produced != failAt || report.Completion.Validated != failAt || report.Completion.Canceled {
				t.Fatalf("来源错误被丢弃或没有排空先前订单: %+v", report)
			}
			assertOrders(t, report.Results, input[:failAt])
		})
	}
}

// TestEarlyConsumerStop 刻意使用无缓冲 Results：停止消费后没有“额外缓冲恰好藏住了结果”的机会。
// 仅断言两条唯一的输入订单、完整 join 与关闭，不断言哪位 worker 先到或源端提前跑到了第几单。
func TestEarlyConsumerStop(t *testing.T) {
	ctx := testContext(t)
	pipeline, err := Start(ctx, SampleOrders(), Options{Workers: 3, JobsBuffer: 1, ResultsBuffer: 0, FailAt: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	report := Collect(ctx, pipeline, 2, nil)
	if !errors.Is(report.Err, ErrEarlyStop) || len(report.Results) != 2 || !report.Completion.Canceled || report.Completion.WorkersExited != 3 {
		t.Fatalf("主动停止未按协议完成: %+v", report)
	}
	if report.Results[0].Order.ID == report.Results[1].Order.ID {
		t.Fatal("提前停止拿到了重复订单")
	}
	for _, result := range report.Results {
		if !slices.Contains(SampleOrders(), result.Order) {
			t.Fatalf("结果不是输入子集: %+v", result)
		}
	}
	if _, ok := <-pipeline.Results; ok {
		t.Fatal("无接收者时仍等待发送，或多生产者尚未完成关闭")
	}
}

// TestEarlyStopDoesNotLoseSourceError 构造两种错误同处一批次。source 的 FailAt=1 在第一个
// jobs 交付后必定生成 ErrSource，consumer 恰好只取一条；Join 必须保留两种业务事实。
func TestEarlyStopDoesNotLoseSourceError(t *testing.T) {
	report := Run(testContext(t), SampleOrders(), Options{Workers: 1, JobsBuffer: 0, ResultsBuffer: 0, FailAt: 1, StopAfter: 1}, nil)
	if !errors.Is(report.Err, ErrEarlyStop) || !errors.Is(report.Err, ErrSource) || report.Completion.WorkersExited != 1 {
		t.Fatalf("提前停止覆盖了来源错误: %+v", report)
	}
}

// TestSaturationResume 验证冻结消费者后的真实饱和与恢复；业务 oracle 仍是六个原始订单。
func TestSaturationResume(t *testing.T) {
	report := Saturation(testContext(t), nil)
	if report.Err != nil || report.Completion.WorkersExited != 1 || report.Completion.Produced != 6 || report.Completion.Validated != 6 {
		t.Fatalf("背压恢复错误: %+v", report)
	}
	assertOrders(t, report.Results, SampleOrders())
}

// TestSaturationCancelAtSend 使用结果发送失败与来源第三单实际交付通知作为饱和屏障。
// 仅收到过 jobs/results 失败通知不够：旧 jobs 通知可能过时，第三次成功发送才能补齐事实。
// owner 全程不读 Results，直接取消并等待终态；若 send 没有取消分支，Wait 会超时失败。
func TestSaturationCancelAtSend(t *testing.T) {
	ctx := testContext(t)
	blocked := make(chan string, 8)
	produced := make(chan int, 8)
	pipeline, err := Start(ctx, SampleOrders(), Options{Workers: 1, JobsBuffer: 1, ResultsBuffer: 1, FailAt: -1, Blocked: blocked, Produced: produced}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pipeline.Cancel()
	jobsSeen, resultsSeen, accepted := false, false, 0
	for !jobsSeen || !resultsSeen || accepted < 3 {
		select {
		case count := <-produced:
			accepted = count
		case stage := <-blocked:
			jobsSeen = jobsSeen || stage == "jobs"
			resultsSeen = resultsSeen || stage == "results"
		case <-ctx.Done():
			t.Fatal("没有观察到稳定饱和: ", ctx.Err())
		}
	}
	if jobs, results := pipeline.Queues(); jobs != 1 || results != 1 {
		t.Fatalf("消费者冻结时未饱和: jobs=%d results=%d", jobs, results)
	}
	pipeline.Cancel()
	completion, err := pipeline.Wait(ctx)
	if err != nil || !completion.Canceled || completion.WorkersExited != 1 || !errors.Is(completion.CancelErr, context.Canceled) {
		t.Fatalf("不接收结果时，取消未解开等待: %+v err=%v", completion, err)
	}
	// 先前已进入 buffer 的 O-01 不会被取消自动删除；唯一关闭者关闭后仍可真实排空。
	var remaining []Result
	for result := range pipeline.Results {
		remaining = append(remaining, result)
	}
	if len(remaining) != 1 || remaining[0].Order != SampleOrders()[0] {
		t.Fatalf("取消误当作自动清空/多交付: %+v", remaining)
	}
}

// TestExternalCancellation 验证调用者已取消和已过截止时间都保留原始 error 类型。
// 取消在 Start 前发生，所有发送前都检查 Err，所以应当没有订单产生。
func TestExternalCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			expected := context.Canceled
			if deadline {
				ctx, cancel = context.WithDeadline(context.Background(), time.Unix(0, 0))
				expected = context.DeadlineExceeded
			} else {
				ctx, cancel = context.WithCancel(context.Background())
				cancel()
			}
			defer cancel()
			report := Run(ctx, SampleOrders(), Options{Workers: 3, FailAt: -1}, nil)
			if !errors.Is(report.Err, expected) || len(report.Results) != 0 || report.Completion.WorkersExited != 3 || report.Completion.Produced != 0 {
				t.Fatalf("外部取消类型/退出丢失: %+v", report)
			}
		})
	}
}

// TestFaultsAndCleanup 断言错误窗口的副作用和最后收尾，而不是只找日志里的 PASS 文案。
func TestFaultsAndCleanup(t *testing.T) {
	ctx := testContext(t)
	blocked, err := BlockedConsumer(ctx, nil)
	if err != nil || !blocked.BlockedBeforeRescue || blocked.RescuedOrder != "O-02" || !blocked.ProducerExited {
		t.Fatalf("错误裸发送/救援没有成立: %+v err=%v", blocked, err)
	}
	message, err := ReceiverCloses(ctx, nil)
	if err != nil || !strings.Contains(message, "send on closed channel") {
		t.Fatalf("接收者关闭反例没有真正触发 panic: %s err=%v", message, err)
	}
}

// TestSelectAndMemory 验证固定工作量、消息集合与同步后的字段值；ready case 比例永不成为断言。
func TestSelectAndMemory(t *testing.T) {
	ctx := testContext(t)
	selected, err := SelectCases(ctx, nil)
	slices.Sort(selected.Merged)
	if err != nil || selected.DefaultHits != 100_000 || selected.ClosedHits != 100_000 || !slices.Equal(selected.Merged, []int{11, 22}) || selected.LeftReady+selected.RightReady != 4_096 || selected.SentAfterCancel+selected.ChoseCancel != 4_096 {
		t.Fatalf("select 状态/固定次数不成立: %+v err=%v", selected, err)
	}
	memory, err := MemoryOwnership(ctx, nil)
	if err != nil || memory != (MemoryReport{CopiedAmount: 100, OriginalAmount: 9, ReturnedAmount: 200, FinalCount: 6}) {
		t.Fatalf("值副本/所有权/同步后的业务值不成立: %+v err=%v", memory, err)
	}
}

// TestLimits 防止错误参数创建无限工作或负容量 channel panic。Start 失败时不能启动 goroutine。
func TestLimits(t *testing.T) {
	for _, options := range []Options{{Workers: 0, FailAt: -1}, {Workers: 9, FailAt: -1}, {Workers: 1, JobsBuffer: -1, FailAt: -1}, {Workers: 1, ResultsBuffer: 17, FailAt: -1}, {Workers: 1, FailAt: 7}, {Workers: 1, FailAt: -2}, {Workers: 1, FailAt: -1, StopAfter: -1}} {
		if pipeline, err := Start(testContext(t), SampleOrders(), options, nil); err == nil || pipeline != nil {
			t.Fatalf("错误参数没有提前拒绝: %+v", options)
		}
	}
	if pipeline, err := Start(testContext(t), make([]Order, 129), Options{Workers: 1, FailAt: -1}, nil); err == nil || pipeline != nil {
		t.Fatal("订单上限没有生效")
	}
}

// TestMemoryFinalPublisherCancellation 冻结最后的 publisher，取消业务 ctx，再观察 owner 的
// 最终清理入口。只有放行 publisher、它写6并关闭 finished 之后，函数才能返回完整结果。
// 不使用 sleep/NumGoroutine，也不把“收到完成通知”扩大为“runtime 已回收 goroutine 栈”。
func TestMemoryFinalPublisherCancellation(t *testing.T) {
	observationCtx := testContext(t)
	businessCtx, cancelBusiness := context.WithCancel(context.Background())
	started, release, cleanupStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	returned := make(chan struct {
		report MemoryReport
		err    error
	}, 1)
	released, resultRead := false, false
	defer func() {
		// Fatal/Goexit 也执行这个 defer：解除测试屏障并等待 owner 最后的返回，避免断言失败泄漏。
		cancelBusiness()
		if !released {
			close(release)
		}
		if !resultRead {
			cleanup, cancel := context.WithTimeout(context.Background(), CleanupLimit)
			defer cancel()
			select {
			case <-returned:
			case <-cleanup.Done():
				t.Errorf("取消测试的 owner 收尾未完成: %v", cleanup.Err())
			}
		}
	}()
	go func() {
		report, err := memoryOwnership(businessCtx, nil, &memoryHooks{finalStarted: started, releaseFinal: release, cleanupStarted: cleanupStarted})
		returned <- struct {
			report MemoryReport
			err    error
		}{report, err}
	}()
	if err := await(observationCtx, started, "最后 publisher 已到达发布前屏障"); err != nil {
		t.Fatal(err)
	}
	cancelBusiness()
	if err := await(observationCtx, cleanupStarted, "owner 已观察业务取消并进入最终清理"); err != nil {
		t.Fatal(err)
	}
	select {
	case premature := <-returned:
		resultRead = true
		t.Fatalf("publisher 尚未放行，owner 已提前返回: %+v err=%v", premature.report, premature.err)
	default:
		// 此处不是猜 owner 已挂起：release 未关闭，publisher 不能完成，finished 不能 ready。
		t.Log("业务取消已被 owner 观察；最终 publisher 未放行，owner 尚不能完成返回")
	}
	close(release)
	released = true
	select {
	case outcome := <-returned:
		resultRead = true
		if !errors.Is(outcome.err, context.Canceled) || outcome.report != (MemoryReport{CopiedAmount: 100, OriginalAmount: 9, ReturnedAmount: 200, FinalCount: 6}) {
			t.Fatalf("业务取消被覆盖或最终发布未被确认: %+v err=%v", outcome.report, outcome.err)
		}
		t.Log("放行后最终值6已发布；独立清理完成并返回，原始 context.Canceled 仍在错误链中")
	case <-observationCtx.Done():
		t.Fatal("放行后 owner 未完成: ", observationCtx.Err())
	}
}
