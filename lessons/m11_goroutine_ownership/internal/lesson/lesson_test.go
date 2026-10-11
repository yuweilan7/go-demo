package lesson

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// testContext 为每个独立实验设观察上限；Cleanup 取消观察者 timer。
// 工作任务的 owner 仍通过 Stop/Shutdown join，不能用观察者超时冒充任务已退出。
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// needSignal 在屏障接收失败时使当前测试停止；调用它之前已经注册 owner 的清理。
func needSignal(t *testing.T, ctx context.Context, signal <-chan struct{}, name string) {
	t.Helper()
	if err := await(ctx, signal, name); err != nil {
		t.Fatal(err)
	}
}

// eventIndex 仅供独立顺序断言；找不到目标也会失败，避免缺日志被当作通过。
func eventIndex(t *testing.T, events []string, fragment string) int {
	t.Helper()
	for i, event := range events {
		if strings.Contains(event, fragment) {
			return i
		}
	}
	t.Fatalf("缺少事件 %q，实际 %v", fragment, events)
	return -1
}

// TestLifecyclePublication 检查业务金额与 happens-before 链。
// 不断言 go-returned 与 body-start 的偶然先后，也不拿准确耗时断言调度顺序。
func TestLifecyclePublication(t *testing.T) {
	trace := NewTrace(io.Discard)
	report, err := RunLifecycle(testContext(t), trace)
	if err != nil {
		t.Fatal(err)
	}
	if report.Value != 200 || !report.Cleaned || !report.UnfinishedAtStart {
		t.Fatalf("需要订单 O-101 的业务结果 200 与清理完成，实际 %+v", report)
	}
	events := trace.Events()
	before := []string{"function-value-evaluated", "argument-evaluated", "body-start", "result-written", "cleanup-written", "done-notified", "caller joined"}
	for i := 1; i < len(before); i++ {
		if eventIndex(t, events, before[i-1]) >= eventIndex(t, events, before[i]) {
			t.Fatalf("同步链必须保证 %q 在 %q 之前，实际 %v", before[i-1], before[i], events)
		}
	}
}

// TestWaitGroupKeepsBusinessErrorsOutsideGroup 分别测试两种 API 的同一业务不变量。
// 若有人误把 O-102 error 当作自动取消兄弟，成功金额总和 700 的断言会失败。
func TestWaitGroupKeepsBusinessErrorsOutsideGroup(t *testing.T) {
	for _, style := range []string{"classic", "go"} {
		t.Run(style, func(t *testing.T) {
			results, err := RunBatch(testContext(t), NewTrace(io.Discard), style)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 3 {
				t.Fatalf("每项任务都应有单独结果，实际 %v", results)
			}
			total, failures := 0, 0
			for _, receipt := range results {
				if receipt.Err != nil {
					failures++
					if receipt.OrderID != "O-102" || !errors.Is(receipt.Err, ErrPriceUnavailable) {
						t.Fatalf("错误应归属 O-102，实际 %+v", receipt)
					}
				} else {
					total += receipt.Amount
				}
			}
			if total != 700 || failures != 1 || results[0].OrderID != "O-101" || results[2].OrderID != "O-103" {
				t.Fatalf("两项成功合计 700、一项独立失败；实际 %v", results)
			}
		})
	}
}

// TestLateAddWaitIsNotJoin 用未关闭的屏障确保计数仍为 0，复现提前 Wait。
// 同时要求实验救援已经 join，避免测试本身制造遗留任务。
func TestLateAddWaitIsNotJoin(t *testing.T) {
	trace := NewTrace(io.Discard)
	unfinished, err := RunLateAdd(testContext(t), trace)
	if err != nil || !unfinished {
		t.Fatalf("提前 Wait 应看见未完成任务，unfinished=%t err=%v", unfinished, err)
	}
	events := trace.Events()
	if eventIndex(t, events, "Wait-returned") >= eventIndex(t, events, "released-and-joined") {
		t.Fatalf("救援必须发生在提前 Wait 后，实际 %v", events)
	}
}

// TestReceiverAndMailbox 是真实 channel 通信断言，不只是看 worker 打印了“完成”。
// 第一个分支检查会合交付；第二个检查无人接收时一个结果仍可进入容量 1 邮箱。
func TestReceiverAndMailbox(t *testing.T) {
	ctx := testContext(t)
	tracker := NewTracker()
	task := StartRequest(ctx, NewTrace(io.Discard), tracker, 0)
	defer task.Stop()
	var received Receipt
	select {
	case received = <-task.Results():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	receipt, delivered, err := task.Join(ctx)
	if err != nil || !delivered || received.OrderID != "O-101" || received.Amount != 200 || receipt.Err != nil {
		t.Fatalf("接收者的真实值与发送完成应一致，received=%+v receipt=%+v delivered=%t err=%v", received, receipt, delivered, err)
	}
	buffered, canceled, err := RunNoReceiverFixes(ctx, NewTrace(io.Discard))
	if err != nil || buffered.Amount != 200 || canceled.Err != context.Canceled {
		t.Fatalf("两种无人接收修复结果，buffered=%+v canceled=%+v err=%v", buffered, canceled, err)
	}
}

// TestRequestOwnersReleaseEveryRegisteredTask 重复登记/取消/Join 32 次。
// 只检查本测试实际拥有的任务和令牌，避免一次 NumGoroutine 读数误判后台 runtime 工作。
func TestRequestOwnersReleaseEveryRegisteredTask(t *testing.T) {
	ctx := testContext(t)
	tracker := NewTracker()
	for range 32 {
		task := StartRequest(ctx, NewTrace(io.Discard), tracker, 0)
		// 小作用域的 defer 保证这次迭代出错也收尾，而不是积累 32 个结束时才清理的句柄。
		func() {
			defer task.Stop()
			needSignal(t, ctx, task.Sending(), "请求到达发送边界")
			active, resources := tracker.Counts()
			if active != 1 || resources != 1 {
				t.Fatalf("请求 owner 应知道一项未完成责任，active=%d resources=%d", active, resources)
			}
			task.Cancel()
			receipt, delivered, err := task.Join(ctx)
			if err != nil || receipt.Err != context.Canceled || delivered {
				t.Fatalf("无接收者只能在取消分支结束，receipt=%+v delivered=%t err=%v", receipt, delivered, err)
			}
			active, resources = tracker.Counts()
			if active != 0 || resources != 0 {
				t.Fatalf("Join 后任务/令牌归零，active=%d resources=%d", active, resources)
			}
		}()
	}
}

// TestRequestEndDoesNotEndService 独立控制请求结束与服务放行。
// 请求返回后 job.done 仍未关闭；业务记录只在服务 owner 放行后出现。
func TestRequestEndDoesNotEndService(t *testing.T) {
	ctx := testContext(t)
	tracker := NewTracker()
	service := NewService(NewTrace(io.Discard), tracker)
	defer service.Shutdown()
	requestCtx, requestCancel := context.WithCancel(ctx)
	defer requestCancel()
	release := make(chan struct{})
	job, err := HandleRequest(requestCtx, service, "O-101", release)
	if err != nil {
		t.Fatal(err)
	}
	needSignal(t, ctx, job.Started(), "服务已接纳并开始 job")
	requestCancel()
	select {
	case <-job.done:
		t.Fatal("请求取消不应结束 service context；release 尚未关闭")
	default:
	}
	if len(service.Ledger()) != 0 {
		t.Fatal("服务尚未放行，不应写入业务记录")
	}
	close(release)
	receipt, err := job.Result(ctx)
	if err != nil || receipt.Err != nil || receipt.Amount != 200 {
		t.Fatalf("请求结束后服务应可完成，receipt=%+v err=%v", receipt, err)
	}
	service.Shutdown()
	active, resources := tracker.Counts()
	if ledger := service.Ledger(); len(ledger) != 1 || ledger[0] != "O-101" || active != 0 || resources != 0 {
		t.Fatalf("业务唯一记录与最终清理，ledger=%v active=%d resources=%d", ledger, active, resources)
	}
}

// TestServiceShutdownFinishesRunningAndQueuedJobs 检查关停的遗漏窗口。
// 第一项卡在 release、第二项真实排队、第三项因满队列拒绝；Shutdown 要结束两项已接纳工作。
func TestServiceShutdownFinishesRunningAndQueuedJobs(t *testing.T) {
	ctx := testContext(t)
	tracker := NewTracker()
	service := NewService(NewTrace(io.Discard), tracker)
	defer service.Shutdown()
	neverRelease := make(chan struct{})
	first := NewBackgroundJob("O-101", neverRelease)
	second := NewBackgroundJob("O-102", neverRelease)
	if err := service.Submit(ctx, first); err != nil {
		t.Fatal(err)
	}
	needSignal(t, ctx, first.Started(), "第一项已运行")
	if err := service.Submit(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := service.Submit(ctx, NewBackgroundJob("O-103", neverRelease)); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("不能无限接纳，满队列应拒绝，实际 %v", err)
	}
	service.Shutdown() // 停止通知在 release 仍未关闭时发出，running 必然选择取消。
	for _, job := range []*BackgroundJob{first, second} {
		receipt, err := job.Result(ctx)
		if err != nil || receipt.Err != context.Canceled {
			t.Fatalf("每项已接纳任务都应有结束结果，order=%s receipt=%+v err=%v", job.orderID, receipt, err)
		}
	}
	if err := service.Submit(ctx, NewBackgroundJob("O-104", neverRelease)); !errors.Is(err, ErrServiceStopped) {
		t.Fatalf("Shutdown 后拒绝新的 admission，实际 %v", err)
	}
	active, resources := tracker.Counts()
	if len(service.Ledger()) != 0 || active != 0 || resources != 0 {
		t.Fatalf("未放行订单不得写入，且 owner 已清理，active=%d resources=%d ledger=%v", active, resources, service.Ledger())
	}
}

// TestFaultChild 是当前测试二进制的子进程入口；普通 go test 运行时跳过。
// child os.Exit 模拟真正 main 返回，不能让 testing 框架帮我们等待后台任务。
func TestFaultChild(t *testing.T) {
	name := os.Getenv("M11_INTERNAL_FAULT")
	if name == "" {
		t.Skip("仅由 TestIsolatedFaults 的有界 child 调用")
	}
	if err := RunFault(name, os.Stdout); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// TestIsolatedFaults 用同一个二进制复现故障；每次 cmd.Run 已经回收 child。
// 禁用 child testing 的总 timer，便于观察 runtime deadlock；parent 仍保有 3s/600ms 上限。
func TestIsolatedFaults(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, name := range FaultNames() {
		t.Run(name, func(t *testing.T) {
			result, err := CheckFault(ctx, executable, []string{"-test.run=^TestFaultChild$", "-test.timeout=0"}, name)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("真实 child：exit=%d timeout=%t output=%s", result.ExitCode, result.TimedOut, strings.Split(result.Output, "\n")[0])
		})
	}
}
