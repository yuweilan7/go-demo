package lab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestService 为每个测试创建真实独立 loopback 服务和本地 exporter。
// Cleanup 按后注册先执行：driver 取消/释放/join → Service HTTP/audit → SDK → ctx。
// t.Fatal 会执行已经登记的 Cleanup，不能只依赖成功路径中的 take/Release。
// 没有调用远程系统，没有 mock 替代协议；这里不测试采样耗时与 CPU profile 排名。
func newTestService(t *testing.T, variant string) (context.Context, *Service, *Telemetry, *driverOwner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	telemetry, err := NewTelemetry(io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := telemetry.Provider.Shutdown(cleanupCtx); err != nil {
			t.Error(err)
		}
	})
	service, err := NewService(ctx, variant, telemetry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})
	drivers := &driverOwner{}
	t.Cleanup(func() {
		if err := drivers.Close(); err != nil {
			t.Error(err)
		}
	})
	return ctx, service, telemetry, drivers
}

// TestFixedOrderCompletesWhileOtherOrderStillInIO 的断言是先后关系与权威状态，
// 而不是“耗时小于 X”。holder 的真实下游门仍关闭时，不相关订单必须完成。
func TestFixedOrderCompletesWhileOtherOrderStillInIO(t *testing.T) {
	ctx, s, _, drivers := newTestService(t, "fixed")
	holder := drivers.launch(ctx, s, "test-holder", "hold")
	if err := s.Await(ctx, "test-holder:down_started"); err != nil {
		t.Fatal(err)
	}
	fast := request(ctx, s, "test-fast", "normal")
	if err := expectReply(fast, 200, Success); err != nil {
		t.Fatal(err)
	}
	if !s.Committed("test-fast") || s.Committed("test-holder") {
		t.Fatal("unrelated order independence violated")
	}
	s.Release("test-holder")
	result, err := take(ctx, holder)
	if err != nil {
		t.Fatal(err)
	}
	if err := expectReply(result, 200, Success); err != nil {
		t.Fatal(err)
	}
	if !s.Committed("test-holder") {
		t.Fatal("holder did not commit after release")
	}
}

// TestCancelBoundaryKeepsUnknownSeparate 同样取消两个真实网络请求，但验证副作用
// 完全不同。派发前 canceled 没有 dependency 样本；响应丢失的写请求已经提交。
func TestCancelBoundaryKeepsUnknownSeparate(t *testing.T) {
	ctx, s, _, drivers := newTestService(t, "fixed")
	pre := drivers.launch(ctx, s, "test-pre", "cancel")
	if err := s.Await(ctx, "test-pre:queue_entered"); err != nil {
		t.Fatal(err)
	}
	pre.Cancel()
	result, err := take(ctx, pre)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("caller error=%v", result.Err)
	}
	if err := s.Await(ctx, "test-pre:finished"); err != nil {
		t.Fatal(err)
	}
	counts, inFlight, waiters, samples := s.Metrics.Snapshot()
	if counts[Canceled] != 1 || inFlight != 0 || waiters != 0 || samples != 0 || s.Committed("test-pre") {
		t.Fatal("pre-dispatch cancellation crossed side-effect boundary")
	}
	post := drivers.launch(ctx, s, "test-post", "unknown")
	if err := s.Await(ctx, "test-post:committed"); err != nil {
		t.Fatal(err)
	}
	post.Cancel()
	result, err = take(ctx, post)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("caller error=%v", result.Err)
	}
	if err := s.Await(ctx, "test-post:finished"); err != nil {
		t.Fatal(err)
	}
	counts, inFlight, waiters, samples = s.Metrics.Snapshot()
	if counts[Unknown] != 1 || counts[DependencyError] != 0 || !s.Committed("test-post") || inFlight != 0 || waiters != 0 || samples != 1 {
		t.Fatal("unknown write result incorrectly declared failed")
	}
}

// TestTraceParentAndAsyncLink 检查真实 HTTP header 传播，SDK provider 相同也不保证
// 请求链路自动连接；没有 Inject/Extract，remote parent 断言会失败。
func TestTraceParentAndAsyncLink(t *testing.T) {
	ctx, s, telemetry, _ := newTestService(t, "fixed")
	result := request(ctx, s, "test-trace", "normal")
	if err := expectReply(result, 200, Success); err != nil {
		t.Fatal(err)
	}
	if err := s.Await(ctx, "test-trace:finished"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	} // join 确保 audit.End 已执行，不能只等 audit signal。
	edges, links, err := validateSpans(telemetry.capture.snapshot())
	if err != nil || edges != 2 || links != 1 {
		t.Fatalf("edges=%d links=%d err=%v", edges, links, err)
	}
}

// TestListenerOwnership 检查三个真实 listener 的生命周期，第二次 Close 必须幂等。
// 仅看 in-flight=0 不证明监听 socket 已关闭，所以这里真正尝试重新 Dial。
func TestListenerOwnership(t *testing.T) {
	_, s, _, _ := newTestService(t, "fixed")
	addresses := []string{s.URL, s.DebugURL, s.downURL}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		connection, err := net.DialTimeout("tcp", strings.TrimPrefix(address, "http://"), 200*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			t.Fatalf("owned listener remains reachable: %s", address)
		}
	}
}

// TestDigestAndReceiptCopies 用独立 hash 公式算出预期值；并检查复制后的 receipt
// 不再与源数组别名。该测试证明业务不变量与引用边界，不承诺任何性能倍数。
func TestDigestAndReceiptCopies(t *testing.T) {
	hash := sha256.Sum256(bytes.Repeat([]byte{0x34}, 4096))
	want := binary.LittleEndian.Uint64(hash[:8]) * uint64(7)
	for _, fixed := range []bool{false, true} {
		got, calls := Digest(fixed, 7)
		wantCalls := 7
		if fixed {
			wantCalls = 1
		}
		if got != want || calls != wantCalls {
			t.Fatalf("fixed=%t value=%d calls=%d", fixed, got, calls)
		}
	}
	large := retainBad()
	copied := retainFixed(large)
	if len(large) != 16 || cap(large) != retainedBytes || !bytes.Equal(large, copied) || cap(copied) != 16 {
		t.Fatal("receipt shape/content mismatch")
	}
	old := copied[0]
	large[0]++
	if copied[0] != old {
		t.Fatal("fixed receipt still aliases large backing array")
	}
}

// TestHistogramNoSampleAndFiniteLabels 确认初始没有 dependency 样本，指标也不带 ID。
// bucket 是累计值，因此 5ms 和 50ms 的两个样本对 le=0.01/0.05/+Inf 分别是 1/2/2。
func TestHistogramNoSampleAndFiniteLabels(t *testing.T) {
	m := NewMetrics("fixed")
	var empty bytes.Buffer
	if err := m.WritePrometheus(&empty); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty.String(), "m34_dependency_seconds_count{variant=\"fixed\",operation=\"checkout\"} 0") {
		t.Fatal("missing zero-sample fact")
	}
	m.dependency(5 * time.Millisecond)
	m.dependency(50 * time.Millisecond)
	var output bytes.Buffer
	if err := m.WritePrometheus(&output); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"le=\"0.01\"} 1", "le=\"0.05\"} 2", "le=\"+Inf\"} 2"} {
		if !strings.Contains(output.String(), "m34_dependency_seconds_bucket{variant=\"fixed\",operation=\"checkout\","+line) {
			t.Fatalf("bucket mismatch: %s", line)
		}
	}
	for _, forbidden := range []string{"request_id", "user_id", "order_id", "trace_id"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("high-cardinality label %s", forbidden)
		}
	}
	if validMode("user-input-as-label") || safeID("contains\nsecret") {
		t.Fatal("unbounded mode or unsafe log field accepted")
	}
}

// TestStructuredLogKeepsMissingObservationAndPanicStack 检查真实 handler 的 JSON，
// 从退出事件验证“没调用依赖”是 null+false，并从 panic 事件验证真实定位栈已保存。
// 读取 buffer 前先 Close join；否则 logger 正在写时读取 bytes.Buffer 也会产生数据竞争。
func TestStructuredLogKeepsMissingObservationAndPanicStack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	var logs bytes.Buffer
	telemetry, err := NewTelemetry(&logs, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := telemetry.Provider.Shutdown(cleanupCtx); err != nil {
			t.Error(err)
		}
	})
	s, err := NewService(ctx, "fixed", telemetry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	drivers := &driverOwner{}
	t.Cleanup(func() {
		if err := drivers.Close(); err != nil {
			t.Error(err)
		}
	})
	done := drivers.launch(ctx, s, "test-log-cancel", "cancel")
	if err := s.Await(ctx, "test-log-cancel:queue_entered"); err != nil {
		t.Fatal(err)
	}
	done.Cancel()
	if _, err := take(ctx, done); err != nil {
		t.Fatal(err)
	}
	if err := s.Await(ctx, "test-log-cancel:finished"); err != nil {
		t.Fatal(err)
	}
	if result := request(ctx, s, "test-log-panic", "panic"); result.Err != nil || result.Status != 500 {
		t.Fatal("panic response mismatch")
	}
	if err := errors.Join(drivers.Close(), s.Close()); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&logs)
	missingSeen, stackSeen := false, false
	for {
		var event map[string]any
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if event["msg"] == "request.finished" && event["request_id"] == "test-log-cancel" {
			if event["dependency_observed"] != false || event["dependency_ms"] != nil || event["outcome"] != "canceled" {
				t.Fatal("missing dependency was logged as measured zero")
			}
			missingSeen = true
		}
		if event["msg"] == "request.panic" {
			stack, ok := event["stack"].(string)
			if !ok || !strings.Contains(stack, "(*Service).checkout") || event["outcome"] != "internal_error" {
				t.Fatal("panic event lacks actual source stack")
			}
			stackSeen = true
		}
	}
	if !missingSeen || !stackSeen {
		t.Fatal("expected structured observations missing")
	}
}

// joinObservedContext 仅用于断言：当 owner 的 select 实际求值 Done 时关闭屏障。
// 它沿用真实有界 context；不会伪造取消、用 sleep 猜调度或改生产代码加测试开关。
type joinObservedContext struct {
	context.Context
	once     sync.Once
	observed chan struct{}
}

func (c *joinObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func awaitTestSignal(t *testing.T, ctx context.Context, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// TestDriverOwnerWaitsAfterWorkDeadline 把清理故意卡在独立 gate。
// 请求结果/工作 deadline 已经发生，owner 已实际进入 join，仍不允许 owner 返回；
// 测试释放 gate 后，worker 执行完 cleanup 才关闭 Done，owner 才能带原失败返回。
func TestDriverOwnerWaitsAfterWorkDeadline(t *testing.T) {
	testCtx, cancelTest := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancelTest)
	workCtx, cancelWork := context.WithTimeout(context.Background(), 20*time.Millisecond)
	t.Cleanup(cancelWork)
	drivers := &driverOwner{}
	workCanceled, allowResult := make(chan struct{}), make(chan struct{})
	cleanupEntered, finishCleanup, releaseCalled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var finishOnce, releaseOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(finishCleanup) }) }
	// 即使下面 t.Fatal，测试自己也必须最终放行，并用新的预算加入所有已启动任务。
	t.Cleanup(func() {
		finish()
		if err := drivers.Close(); err != nil {
			t.Error(err)
		}
	})
	d := drivers.start(workCtx, "held-cleanup", func(ctx context.Context) Result {
		<-ctx.Done()
		close(workCanceled)
		<-allowResult
		return Result{Err: ctx.Err()}
	}, func() { close(cleanupEntered); <-finishCleanup }, func() {
		releaseOnce.Do(func() { close(allowResult); close(releaseCalled) })
	})
	awaitTestSignal(t, testCtx, workCanceled)
	if _, err := take(workCtx, d); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("work result wait must return its expired budget: %v", err)
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancelCleanup)
	observed := &joinObservedContext{Context: cleanupCtx, observed: make(chan struct{})}
	ownerReturned := make(chan error, 1)
	ownerDone := make(chan struct{})
	go func() {
		defer close(ownerDone)
		ownerReturned <- errors.Join(workCtx.Err(), drivers.closeWithContext(observed))
	}()
	t.Cleanup(func() {
		finish()
		joinCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		select {
		case <-ownerDone:
		case <-joinCtx.Done():
			t.Error("test owner did not exit:", joinCtx.Err())
		}
	})
	awaitTestSignal(t, testCtx, observed.observed)
	awaitTestSignal(t, testCtx, releaseCalled)
	awaitTestSignal(t, testCtx, cleanupEntered)
	for i := 0; i < 2; i++ {
		result, err := take(testCtx, d)
		if err != nil || !errors.Is(result.Err, context.DeadlineExceeded) {
			t.Fatalf("repeatable result %d: result=%v error=%v", i, result.Err, err)
		}
	}
	select {
	case <-d.Done():
		t.Fatal("Done closed while worker cleanup gate remains closed")
	default:
	}
	select {
	case err := <-ownerReturned:
		t.Fatalf("owner returned before actual driver Done: %v", err)
	default:
	}
	finish()
	awaitTestSignal(t, testCtx, d.Done())
	select {
	case err := <-ownerReturned:
		if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "cleanup incomplete") {
			t.Fatalf("original failure must remain, cleanup must finish: %v", err)
		}
	case <-testCtx.Done():
		t.Fatal(testCtx.Err())
	}
	// 多次读取结果和 Done 都可重复观察，之前的 take 没有消费 join 证明。
	awaitTestSignal(t, testCtx, d.Done())
	awaitTestSignal(t, testCtx, ownerDone)
}

// TestDriverCleanupTimeoutReportsIncomplete 验证清理期限不能被写成“join 完成”。
// 超时保留原失败和清理 DeadlineExceeded；测试最后亲自放行并再次加入，避免
// 为展示泄漏而把本测试进程留下一个坏 goroutine。
func TestDriverCleanupTimeoutReportsIncomplete(t *testing.T) {
	testCtx, cancelTest := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancelTest)
	workCtx, cancelWork := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancelWork)
	drivers := &driverOwner{}
	entered, finishCleanup := make(chan struct{}), make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(finishCleanup) }) }
	t.Cleanup(func() {
		finish()
		if err := drivers.Close(); err != nil {
			t.Error(err)
		}
	})
	d := drivers.start(workCtx, "timeout-cleanup", func(ctx context.Context) Result {
		return Result{Err: ctx.Err()}
	}, func() { close(entered); <-finishCleanup }, nil)
	awaitTestSignal(t, testCtx, entered)
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelCleanup()
	original := errors.New("independent experiment failure")
	err := errors.Join(original, workCtx.Err(), drivers.closeWithContext(cleanupCtx))
	if !errors.Is(err, original) || !errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(err.Error(), "driver timeout-cleanup cleanup incomplete") {
		t.Fatalf("original and explicit incomplete cleanup error must both remain: %v", err)
	}
	select {
	case <-d.Done():
		t.Fatal("cleanup timeout falsely closed driver Done")
	default:
	}
	finish()
	if err := drivers.Close(); err != nil {
		t.Fatal(err)
	}
	awaitTestSignal(t, testCtx, d.Done())
}
