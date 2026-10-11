package lesson

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	m15 "go-demo/lessons/m15_structured_concurrency"
)

// testContext 由每个普通测试创建观察上限，Cleanup 归还其 timer；子测试不会共享已取消 ctx。
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestTimerContracts 检查状态返回值与回调真实归属，避免把“打印成功”当作独立断言。
// Stop/Reset 不断言毫秒精度；afterfunc 两轮必须都被真实加入而不是等待一个结果通知。
func TestTimerContracts(t *testing.T) {
	r, err := TimerProbe(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if !r.FirstStop || r.SecondStop || r.ResetStopped || !r.ResetActive {
		t.Fatalf("channel timer 状态错误: %+v", r)
	}
	if r.TimerChannelCap != 0 || r.TickerChannelCap != 0 || !r.StopDidNotClose {
		t.Fatalf("Go1.27 timer channel 契约错误: %+v", r)
	}
	if !r.AfterFuncChannelNil || r.CallbackStop || r.CallbackReset || r.CallbackRuns != 2 || !r.StopDidNotJoin {
		t.Fatalf("回调 Stop/Reset/join 错误: %+v", r)
	}
}

// TestRealTickerDropsTicks 用官方 synctest bubble 的虚拟时钟推进真实 time.NewTicker，
// 不用手动 tick channel 伪造丢 tick。100ms 是被测时间跨度，synctest 仅在所有 goroutine
// 已持久阻塞后自动推进时间；没有 Sleep 猜“worker 应该已经执行”。这仍不测试 Windows 时钟精度。
func TestRealTickerDropsTicks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		pause := time.NewTimer(100 * time.Millisecond)
		defer pause.Stop()
		<-pause.C
		first := <-ticker.C
		second := <-ticker.C
		// 十个周期未消费时，ticker 不交付十个排队任务；下一条时间戳已跨过被错过的周期。
		if gap := second.Sub(first); gap != 100*time.Millisecond {
			t.Fatalf("bubble 丢 tick 间隔=%v，预期100ms", gap)
		}
	})
}

// TestStopOnlyAndRepair 分别加入有救援的坏 worker 与可取消 worker，不能遗留故障 goroutine。
func TestStopOnlyAndRepair(t *testing.T) {
	ctx := testContext(t)
	observed, err := StopOnlyProbe(ctx)
	if err != nil || !observed {
		t.Fatalf("Stop 未结束 worker 反例: observed=%t err=%v", observed, err)
	}
	if err := CancelTickerProbe(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestPolicies 断言两个受控触发的业务执行数量。serial 的辅助触发源可以保存两次输入，
// 真实 ticker 没有这个可靠队列保证，丢 tick 的独立证据见上一个 TestRealTickerDropsTicks。
func TestPolicies(t *testing.T) {
	for _, policy := range []Policy{Overlap, Serial, Skip} {
		t.Run(string(policy), func(t *testing.T) {
			r, err := PolicyProbe(testContext(t), policy)
			if err != nil {
				t.Fatal(err)
			}
			wantStarted, wantSkipped := 2, 0
			if policy == Skip {
				wantStarted, wantSkipped = 1, 1
			}
			if r.Received != 2 || r.Started != wantStarted || r.Skipped != wantSkipped || len(r.Errors) != 0 {
				t.Fatalf("策略终态错误: %+v", r)
			}
		})
	}
}

// TestScheduledPanic 建立两个触发：第一轮 panic，第二轮正常。safeRound 必须在发生 panic 的
// 执行 goroutine 里恢复，并把一条 error 交给 owner，不能把后台任务失败吞成 nil。
func TestScheduledPanic(t *testing.T) {
	ticks := make(chan time.Time, 2)
	ticks <- time.Now()
	ticks <- time.Now()
	close(ticks)
	r, err := RunSchedule(testContext(t), ticks, Overlap, func(_ context.Context, sequence int) error {
		if sequence == 1 {
			panic("扫描器 panic")
		}
		return nil
	}, nil)
	if err != nil || r.Started != 2 || len(r.Errors) != 1 {
		t.Fatalf("定时 panic 丢失: report=%+v err=%v", r, err)
	}
}

// TestOverlapHasBoundedCapacity 把八个触发送到同一个loop，并把已经开始的两轮卡住。
// 当active=2且没有结果可领取时，其余六轮必须skip；它检查过载上限，不靠随机并发峰值猜策略。
func TestOverlapHasBoundedCapacity(t *testing.T) {
	ctx := testContext(t)
	ticks := make(chan time.Time, 8)
	for range 8 {
		ticks <- time.Now()
	}
	close(ticks)
	observed := make(chan ScheduleEvent, 8)
	release := newGate()
	done := make(chan struct{})
	var owner sync.WaitGroup
	var report ScheduleReport
	var runErr error
	owner.Go(func() {
		report, runErr = RunSchedule(ctx, ticks, Overlap, func(ctx context.Context, _ int) error {
			select {
			case <-release.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, observed)
		close(done)
	})
	defer func() {
		var cleanupErr error
		joinCleanup(&cleanupErr, release.release, done, owner.Wait)
		if cleanupErr != nil {
			t.Error(cleanupErr)
		}
	}()
	for range 8 {
		select {
		case <-observed:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	release.release()
	if err := await(ctx, done, "八触发loop joined"); err != nil {
		t.Fatal(err)
	}
	owner.Wait()
	if runErr != nil || report.Received != 8 || report.Started != 2 || report.Skipped != 6 {
		t.Fatalf("重叠上限错误: %+v err=%v", report, runErr)
	}
}

// TestRealPeriodicProducer 让Service自己的真实ticker至少接纳一轮扫描；started屏障证明
// 已进业务，不使用Sleep估计。停止入口后放门、join，再核对每个已接纳periodic Ticket终态。
func TestRealPeriodicProducer(t *testing.T) {
	ctx := testContext(t)
	started, release := newGate(), newGate()
	s, err := NewService(ctx, 1, 1, time.Millisecond, func(ctx context.Context, _ string) (int64, error) {
		started.release()
		select {
		case <-release.C:
			return 100, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}, func() error { return nil }, release.release)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		release.release()
		cleanCtx, cancel := context.WithTimeout(context.Background(), CleanupBudget)
		defer cancel()
		if _, err := s.Shutdown(cleanCtx); err != nil {
			t.Error(err)
		}
	}()
	if err := await(ctx, started.C, "真实周期任务started"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var caller sync.WaitGroup
	var report ShutdownReport
	var shutdownErr error
	caller.Go(func() { report, shutdownErr = s.Shutdown(ctx); close(done) })
	defer func() {
		var cleanupErr error
		joinCleanup(&cleanupErr, release.release, done, caller.Wait)
		if cleanupErr != nil {
			t.Error(cleanupErr)
		}
	}()
	if err := await(ctx, s.EntranceStopped(), "真实生产者入口停止"); err != nil {
		t.Fatal(err)
	}
	release.release()
	if err := await(ctx, done, "真实生产者停机完成"); err != nil {
		t.Fatal(err)
	}
	caller.Wait()
	if shutdownErr != nil || len(report.FinalRows) < 1 || len(report.FinalRows) > 2 || !report.ProducerJoined || !report.WorkersJoined || !report.ClientClosed {
		t.Fatalf("真实周期producer终态: %+v err=%v", report, shutdownErr)
	}
	for _, row := range report.FinalRows {
		if row.ID != "periodic" || row.Price != 100 || row.Err != nil {
			t.Fatalf("periodic Ticket终态丢失: %+v", row)
		}
	}
}

// TestCoreRealHTTPDrain 独立核对响应金额、账本和真实连接终态，HTTP200/compile 都不够。
// 最终两个 Ticket 按接纳 ID 对应金额，handler、连接、worker、producer、client 全部回收。
func TestCoreRealHTTPDrain(t *testing.T) {
	r, err := CoreDrain(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"A": 12500, "B": 1800}
	if r.Amount != 14300 || !reflect.DeepEqual(r.Backend.Committed, want) {
		t.Fatalf("业务账本/金额错误: %+v", r)
	}
	rows := make(map[string]int64)
	for _, row := range r.Shutdown.FinalRows {
		if row.Err != nil {
			t.Fatal(row.Err)
		}
		rows[row.ID] = row.Price
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("Ticket 对应金额错误: %v", rows)
	}
	if r.Backend.Accepted != 2 || r.Backend.Closed != 2 || r.Backend.ActiveHandlers != 0 {
		t.Fatalf("真实HTTP资源未收尾: %+v", r.Backend)
	}
	if !r.FullRejected || !r.ClosedRejected || !r.EarlyCloseRejected || !r.ResultWaitCanceled {
		t.Fatalf("业务所有权边界缺失: %+v", r)
	}
	if !r.Shutdown.ProducerJoined || !r.Shutdown.WorkersJoined || !r.Shutdown.ClientClosed || r.Shutdown.Err != nil {
		t.Fatalf("服务终态错误: %+v", r.Shutdown)
	}
}

// TestShutdownPaths 在同一固定A/B数据上检查六种业务路径，deadline快照保留当时unknown，
// 最终清理成功也保留主错误；panic 则是单项业务失败而不自动取消兄弟（M15 failFast=false）。
func TestShutdownPaths(t *testing.T) {
	for _, name := range []string{"idle", "running", "full", "timeout", "canceled-budget", "panic"} {
		t.Run(name, func(t *testing.T) {
			r, err := ServiceProbe(testContext(t), name)
			if err != nil {
				t.Fatal(err)
			}
			if !r.ClosedRejected || !r.RepeatSame || !r.Shutdown.ProducerJoined || !r.Shutdown.WorkersJoined || !r.Shutdown.ClientClosed {
				t.Fatalf("停机资源/幂等性错误: %+v", r)
			}
			if name == "idle" {
				if len(r.Shutdown.FinalRows) != 0 {
					t.Fatalf("空闲不应凭空生结果: %+v", r)
				}
				return
			}
			if !r.FullRejected || len(r.Shutdown.FinalRows) != 2 {
				t.Fatalf("已接纳两项终态丢失: %+v", r)
			}
			if name == "timeout" || name == "canceled-budget" {
				wantErr := context.DeadlineExceeded
				if name == "canceled-budget" {
					wantErr = context.Canceled
				}
				if !errors.Is(r.Shutdown.Err, wantErr) || !reflect.DeepEqual(r.Shutdown.UnknownAtDeadline, []string{"A", "B"}) || r.Shutdown.ActiveAtDeadline != 1 || r.Shutdown.QueueAtDeadline != 1 {
					t.Fatalf("超预算当时事实/主错丢失: %+v", r.Shutdown)
				}
				for _, row := range r.Shutdown.FinalRows {
					// 已运行A的业务函数返回ctx.Err()，取消类型是Canceled；尚未运行B由M15
					// worker读取context.Cause()，保留触发Abort的drain超时原因，两者不能混同。
					rowErr := wantErr
					if row.ID == "A" {
						rowErr = context.Canceled
					}
					if !errors.Is(row.Err, rowErr) {
						t.Fatalf("取消类型/原因不符合所属分支: %+v", row)
					}
				}
			} else if name == "panic" {
				for _, row := range r.Shutdown.FinalRows {
					var target *m15.PanicError
					if !errors.As(row.Err, &target) {
						t.Fatalf("panic终态丢失: %+v", row)
					}
				}
			} else {
				if r.Shutdown.Err != nil || r.Shutdown.FinalRows[0].Price != 12500 || r.Shutdown.FinalRows[1].Price != 1800 {
					t.Fatalf("drain 业务结果错误: %+v", r.Shutdown)
				}
			}
		})
	}
}

// TestParentCancelIsNotJoin 验证父取消、Result、最终加入三个直接接口，不用goroutine数量猜泄漏。
func TestParentCancelIsNotJoin(t *testing.T) {
	if err := ParentCancelProbe(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

// TestConcurrentShutdown 同时十六个 caller 请求同一次关闭。closeClient必须只调用一次，
// 所有caller加入后统计才被读取；它检查sync.Once所有权而不是希望调用恰好串行。
func TestConcurrentShutdown(t *testing.T) {
	ctx := testContext(t)
	closeCalls := 0
	s, err := NewService(ctx, 1, 1, time.Hour, func(context.Context, string) (int64, error) { return 1, nil }, func() error { closeCalls++; return nil }, func() {})
	if err != nil {
		t.Fatal(err)
	}
	var callers sync.WaitGroup
	errorsOut := make(chan error, 16)
	for range 16 {
		callers.Go(func() { _, err := s.Shutdown(ctx); errorsOut <- err })
	}
	callers.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	if closeCalls != 1 {
		t.Fatalf("客户端关闭次数=%d", closeCalls)
	}
	if err := await(ctx, s.Done(), "并发shutdown全部资源"); err != nil {
		t.Fatal(err)
	}
}

// TestCleanupErrorsArePreserved 提前取消drain预算再让closeClient报告指定错误，独立清理仍已
// join worker/producer，但不能关闭最终Done。两条错误原因要同时保留，不能覆盖主错或伪装成功。
func TestCleanupErrorsArePreserved(t *testing.T) {
	ctx := testContext(t)
	closeErr := errors.New("实验客户端 Close 报错")
	s, err := NewService(ctx, 1, 1, time.Hour, func(context.Context, string) (int64, error) { return 1, nil }, func() error { return closeErr }, func() {})
	if err != nil {
		t.Fatal(err)
	}
	bad, cancel := context.WithCancel(ctx)
	cancel()
	_, _ = s.Shutdown(bad)
	r, err := s.Shutdown(ctx)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, closeErr) || !r.ProducerJoined || !r.WorkersJoined || r.ClientClosed {
		t.Fatalf("主/清理错误或资源状态错误: %+v err=%v", r, err)
	}
	select {
	case <-s.Done():
		t.Fatal("客户端Close失败时Done不能伪装资源全释放")
	default:
	}
	// 此处Close是辅助故障函数，没有实际socket资源；所有实际goroutine已join，无遗留泄漏。
}

// TestIndependentJoinError 只用永不关闭的信号检查join预算错误合并；没有创建被遗弃goroutine。
func TestIndependentJoinError(t *testing.T) {
	primary := errors.New("原始业务失败")
	err := primary
	stopped, actuallyWaited := false, false
	joined := joinCleanup(&err, func() { stopped = true }, make(chan struct{}), func() { actuallyWaited = true })
	if joined || !stopped || actuallyWaited || !errors.Is(err, primary) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误链=%v", err)
	}
}

// TestRealHTTPAbortUnknown 验证真实网络中的期限边界：到期时不能预言远端提交，随后账本
// 查询给出当前进程已确认事实。B一直没开始所以不会出现B账本，A最多提交一次；
// A若成功响应则必须对应实际金额，A若取消则允许远端仍有提交，不写错误的“取消即回滚”断言。
func TestRealHTTPAbortUnknown(t *testing.T) {
	r, err := HTTPAbortProbe(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(r.Shutdown.Err, context.DeadlineExceeded) || !reflect.DeepEqual(r.Shutdown.UnknownAtDeadline, []string{"A", "B"}) || !r.Shutdown.WorkersJoined || !r.Shutdown.ClientClosed {
		t.Fatalf("真实超时/最终收尾错误: %+v", r)
	}
	if r.Backend.Accepted != 1 || r.Backend.Closed != 1 || r.Backend.ActiveHandlers != 0 {
		t.Fatalf("真实HTTP Abort资源错误: %+v", r.Backend)
	}
	for id, amount := range r.Backend.Committed {
		if id != "A" || amount != 12500 {
			t.Fatalf("超时后实际账本不符合已派发范围: %v", r.Backend.Committed)
		}
	}
	for _, row := range r.Shutdown.FinalRows {
		if row.ID == "B" && !errors.Is(row.Err, context.DeadlineExceeded) {
			t.Fatalf("B未开始取消原因: %+v", row)
		}
		if row.ID == "A" && row.Err == nil && (row.Price != 12500 || r.Backend.Committed["A"] != 12500) {
			t.Fatalf("A成功终态需对应账本: %+v ledger=%v", row, r.Backend.Committed)
		}
	}
}

// TestHTTPRejectsBadBusinessBody 用httptest.NewServer的真实TCP请求辅助检查adapter的语义边界。
// 它不替代Core的真实A/B账本、drain和资源顺序验收；每次请求都读取/关闭Body并关闭client/server。
func TestHTTPRejectsBadBusinessBody(t *testing.T) {
	for _, body := range []string{`{"id":"B","amount":12500}`, `{"id":"A","amount":0}`, `{"id":"A","amount":12500}{"extra":1}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			client := NewOrderClient(server.URL)
			defer func() {
				if err := client.Close(); err != nil {
					t.Error(err)
				}
			}()
			if _, err := client.Fetch(testContext(t), "A"); err == nil {
				t.Fatal("200下的错误业务响应必须拒绝")
			}
		})
	}
}
