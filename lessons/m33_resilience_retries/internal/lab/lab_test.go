package lab

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"
)

// testScope为每个测试创建有限观察ctx；取消观察者并不取消fixture，t.Cleanup另管资源owner。
func testScope(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// testFixture创建真实网络资源。后注册Task清理先执行，t.Fatal也先cancel/join任务再关闭server。
func testFixture(t *testing.T) (*Fixture, *Client, *Trace) {
	t.Helper()
	trace := &Trace{out: io.Discard, start: time.Now()}
	f, err := newFixture(trace)
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(4)
	t.Cleanup(func() {
		c.Close()
		if err := f.close(); err != nil {
			t.Error(err)
		}
		if c.bodies.Load() != 0 {
			t.Errorf("response body=%d", c.bodies.Load())
		}
	})
	return f, c, trace
}

// ownTask测试owner的清理登记，必须紧跟startTask；即便后续t.Fatal，也解除gate并独立等待Done。
func ownTask(t *testing.T, task *Task, unblock func()) {
	t.Helper()
	t.Cleanup(func() {
		if err := task.StopAndJoin(unblock); err != nil {
			t.Error(err)
		}
	})
}

// TestAllScenarios复跑真实网络课程链；它补充回归，下面各测试再直接读取独立业务事实。
func TestAllScenarios(t *testing.T) {
	if err := Run(testScope(t), io.Discard, "all"); err != nil {
		t.Fatal(err)
	}
}

// TestTotalDeadlineAndAttemptLimit独立构造始终失败的真实HTTP，不只匹配demo的PASS字符串。
// 相同绝对deadline/真实调用数是机制证据；不对机器调度做精确毫秒断言。
func TestTotalDeadlineAndAttemptLimit(t *testing.T) {
	ctx := testScope(t)
	f, c, trace := testFixture(t)
	spec := Spec{Method: "GET", URL: f.URL + "/always503", Idempotent: true}
	_, s, err := Retry(ctx, c, spec, Policy{Total: 100 * time.Millisecond, MaxAttempts: 10, Base: 80 * time.Millisecond, Cap: 160 * time.Millisecond}, trace)
	if !errors.Is(err, context.DeadlineExceeded) || len(s.Attempts) >= 10 || f.count("/always503") != len(s.Attempts) {
		t.Fatalf("err=%v attempts=%d handlers=%d", err, len(s.Attempts), f.count("/always503"))
	}
	for _, a := range s.Attempts {
		if !a.Deadline.Equal(s.Deadline) {
			t.Fatalf("deadline被重置: %v != %v", a.Deadline, s.Deadline)
		}
	}
	before := f.count("/always503")
	_, s, err = Retry(ctx, c, spec, Policy{Total: time.Second, MaxAttempts: 2}, trace)
	if statusCode(err) != 503 || len(s.Attempts) != 2 || f.count("/always503")-before != 2 {
		t.Fatalf("max attempts失效: %v %+v", err, s)
	}
}

// TestCancelAndEarlyReturn在真实慢handler entered后模拟caller主动早退；不等待Result便执行清理。
// 独立断言Task.Done、handler.Done、槽/body/任务计数，证明取消通知本身不足以完成收尾。
func TestCancelAndEarlyReturn(t *testing.T) {
	ctx := testScope(t)
	f, c, trace := testFixture(t)
	g := f.gate("early-return")
	task := startTask(ctx, func(work context.Context) taskResult {
		r, s, err := Retry(work, c, Spec{Method: "GET", URL: f.URL + "/slow?gate=early-return", Idempotent: true}, Policy{Total: 2 * time.Second, MaxAttempts: 3}, trace)
		return taskResult{Reply: r, Stats: s, Err: err}
	}, nil)
	ownTask(t, task, g.Open)
	if err := wait(ctx, g.Entered, "真实handler entered"); err != nil {
		t.Fatal(err)
	}
	if err := task.StopAndJoin(g.Open); err != nil {
		t.Fatal(err)
	}
	if err := join(g.Done, "handler Done"); err != nil {
		t.Fatal(err)
	}
	if !closed(task.Done) || ownedTasks.Load() != 0 || f.count("/slow") != 1 {
		t.Fatalf("早返回未join: tasks=%d calls=%d", ownedTasks.Load(), f.count("/slow"))
	}
	result, err := receive(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("取消原因丢失: %v", result.Err)
	}
}

// TestResultIsNotDone制造真实HTTP结果已到、附加清理仍卡gate。owner先消费result，不能据此宣称退出。
// barrier未开放时Task.Done不可关闭；解除后独立join再读取cleanupErr，保留主/清理两项error。
func TestResultIsNotDone(t *testing.T) {
	ctx := testScope(t)
	f, c, trace := testFixture(t)
	cleanupGate := newGate()
	cleanupCause := errors.New("合成清理失败")
	task := startTask(ctx, func(work context.Context) taskResult {
		r, s, err := Retry(work, c, Spec{Method: "GET", URL: f.URL + "/healthy", Idempotent: true}, Policy{Total: time.Second, MaxAttempts: 1}, trace)
		return taskResult{Reply: r, Stats: s, Err: err}
	}, func() error {
		close(cleanupGate.Entered)
		// 附加清理有自己有限预算，故意不继承已取消工作ctx；所有早退由owner Open解除。
		if err := join(cleanupGate.Release, "附加清理gate"); err != nil {
			return err
		}
		return cleanupCause
	})
	// 此测试预期清理error，不用ownTask把期望error当测试失败；仍在任何t.Fatal路径解除并join。
	t.Cleanup(func() {
		cleanupGate.Open()
		err := task.StopAndJoin(nil)
		if !errors.Is(err, cleanupCause) {
			t.Errorf("清理原因丢失: %v", err)
		}
	})
	result, err := receive(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if err := wait(ctx, cleanupGate.Entered, "真实请求返回后的清理等待"); err != nil {
		t.Fatal(err)
	}
	task.cancel()
	if closed(task.Done) || ownedTasks.Load() != 1 {
		t.Fatal("result/cancel被当作join")
	}
	mainCause := errors.New("caller提前返回原因")
	// 另一个被当前测试拥有的owner任务真正调用StopAndJoin；仍关闭的gate强制它等worker清理。
	owner := startTask(ctx, func(context.Context) taskResult {
		return taskResult{Err: errors.Join(mainCause, task.StopAndJoin(nil))}
	}, nil)
	t.Cleanup(func() {
		cleanupGate.Open()
		if err := owner.StopAndJoin(nil); err != nil {
			t.Error(err)
		}
	})
	if err := wait(ctx, task.JoinWaiting, "owner正在独立join"); err != nil {
		t.Fatal(err)
	}
	if closed(owner.Done) || closed(task.Done) {
		t.Fatal("gate未开放却提前宣称owner/worker完成")
	}
	cleanupGate.Open()
	ownerResult, err := receive(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.StopAndJoin(nil); err != nil {
		t.Fatal(err)
	}
	combined := ownerResult.Err
	if !errors.Is(combined, mainCause) || !errors.Is(combined, cleanupCause) || !closed(task.Done) || ownedTasks.Load() != 0 {
		t.Fatalf("原因/Done不完整: %v", combined)
	}
}

// TestUnknownWriteReconciliation独立沿真实POST -> EOF -> GET查账验证UNKNOWN不是操作未执行。
func TestUnknownWriteReconciliation(t *testing.T) {
	ctx := testScope(t)
	f, c, trace := testFixture(t)
	spec := Spec{Method: "POST", URL: f.URL + "/lost-order", Key: "test-stable", Body: []byte(`{"sku":"test-sku","qty":4}`), Replayable: true, Idempotent: true}
	_, _, err := Retry(ctx, c, spec, Policy{Total: time.Second, MaxAttempts: 1}, trace)
	var unknown *Unknown
	if !errors.As(err, &unknown) || f.Ledger.snapshot() != 1 || f.count("/lost-order") != 1 {
		t.Fatalf("UNKNOWN边界: %v rows=%d", err, f.Ledger.snapshot())
	}
	r, _, err := Retry(ctx, c, Spec{Method: "GET", URL: f.URL + "/status?key=test-stable", Idempotent: true}, Policy{Total: time.Second, MaxAttempts: 1}, trace)
	if err != nil || r.Receipt.ID != 1 || r.Receipt.Order != (Order{SKU: "test-sku", Qty: 4}) {
		t.Fatalf("查询未确认业务: %+v %v", r, err)
	}
	spec.URL = f.URL + "/orders"
	r, _, err = Retry(ctx, c, spec, Policy{Total: time.Second, MaxAttempts: 1}, trace)
	if err != nil || !r.Receipt.Duplicate || r.Receipt.ID != 1 || f.Ledger.snapshot() != 1 {
		t.Fatalf("重放重复写: %+v %v rows=%d", r, err, f.Ledger.snapshot())
	}
}

// TestUntrustedSuccessfulWriteResponses独立读实际账本和handler次数，不拿客户端error当commit证据。
// stable key/Replayable/Idempotent全部满足时仍不自动重放损坏201；随后GET同key确认原操作。
func TestUntrustedSuccessfulWriteResponses(t *testing.T) {
	for _, fault := range []struct {
		path  string
		cause error
	}{{"/corrupt-order", ErrInvalidOrderResponse}, {"/large-order", ErrResponseTooLarge}, {"/no-receipt-order", ErrInvalidReceipt}, {"/mismatched-order", ErrInvalidReceipt}} {
		t.Run(fault.path, func(t *testing.T) {
			ctx := testScope(t)
			f, c, trace := testFixture(t)
			spec := Spec{Method: "POST", URL: f.URL + fault.path, Key: "untrusted-stable", Body: []byte(`{"sku":"untrusted-test","qty":5}`), Replayable: true, Idempotent: true}
			_, s, err := Retry(ctx, c, spec, Policy{Total: time.Second, MaxAttempts: 4}, trace)
			var unknown *Unknown
			if !errors.As(err, &unknown) || !errors.Is(err, fault.cause) || len(s.Attempts) != 1 || s.Attempts[0].Status != 201 || f.count(fault.path) != 1 || f.Ledger.snapshot() != 1 {
				t.Fatalf("UNKNOWN/no replay边界 err=%v attempts=%+v calls=%d rows=%d", err, s.Attempts, f.count(fault.path), f.Ledger.snapshot())
			}
			if fault.path == "/corrupt-order" {
				var syntax *json.SyntaxError
				if !errors.As(err, &syntax) {
					t.Fatalf("坏JSON的原始原因未保留: %v", err)
				}
			}
			r, _, queryErr := Retry(ctx, c, Spec{Method: "GET", URL: f.URL + "/status?key=untrusted-stable", Idempotent: true}, Policy{Total: time.Second, MaxAttempts: 1}, trace)
			if queryErr != nil || r.Receipt.ID != 1 || r.Receipt.Order != (Order{SKU: "untrusted-test", Qty: 5}) || f.Ledger.snapshot() != 1 || f.count(fault.path) != 1 || f.count("/orders") != 0 {
				t.Fatalf("原key对账不可信: %+v %v rows=%d post=%d", r, queryErr, f.Ledger.snapshot(), f.count(fault.path))
			}
		})
	}
}

// TestEmptyBodyWriteCannotBypassKey真实handler从默认参数commit，证明空body绝不等于无副作用。
// 无key必须加ErrUnsafeRetry且只1次；有key仍先把UNKNOWN交回caller，由原key GET对账，不盲POST。
func TestEmptyBodyWriteCannotBypassKey(t *testing.T) {
	for _, key := range []string{"", "empty-stable"} {
		t.Run("key="+key, func(t *testing.T) {
			ctx := testScope(t)
			f, c, trace := testFixture(t)
			_, s, err := Retry(ctx, c, Spec{Method: "POST", URL: f.URL + "/empty-lost-order", Key: key, Replayable: true, Idempotent: true}, Policy{Total: time.Second, MaxAttempts: 3}, trace)
			var unknown *Unknown
			if !errors.As(err, &unknown) || len(s.Attempts) != 1 || f.count("/empty-lost-order") != 1 || f.Ledger.snapshot() != 1 {
				t.Fatalf("空body被盲重放: %v attempts=%d calls=%d rows=%d", err, len(s.Attempts), f.count("/empty-lost-order"), f.Ledger.snapshot())
			}
			if key == "" {
				if !errors.Is(err, ErrUnsafeRetry) {
					t.Fatalf("空body未要求key: %v", err)
				}
				return
			}
			r, _, queryErr := Retry(ctx, c, Spec{Method: "GET", URL: f.URL + "/status?key=" + key, Idempotent: true}, Policy{Total: time.Second, MaxAttempts: 1}, trace)
			if queryErr != nil || r.Receipt.ID != 1 || r.Receipt.Order != (Order{SKU: "server-default-m33", Qty: 1}) || f.count("/empty-lost-order") != 1 || f.Ledger.snapshot() != 1 {
				t.Fatalf("空body原key对账失败: %+v %v", r, queryErr)
			}
		})
	}
}

// TestKnownWriteRejectionsStayDefinite确认修复没有把所有POST错误都变UNKNOWN。
// BAD_ORDER422/OUT_OF_STOCK422在本课handler契约中均发生于commit前，合法状态/code保持确定拒绝。
func TestKnownWriteRejectionsStayDefinite(t *testing.T) {
	ctx := testScope(t)
	f, c, trace := testFixture(t)
	for _, rejection := range []struct{ path, body, code string }{{"/orders", `{"sku":"bad","qty":0}`, "BAD_ORDER"}, {"/reject", `{"sku":"known","qty":1}`, "OUT_OF_STOCK"}} {
		_, s, err := Retry(ctx, c, Spec{Method: "POST", URL: f.URL + rejection.path, Key: "reject-stable", Body: []byte(rejection.body), Replayable: true, Idempotent: true}, Policy{Total: time.Second, MaxAttempts: 3}, trace)
		var unknown *Unknown
		var rejected *HTTPError
		if errors.As(err, &unknown) || !errors.As(err, &rejected) || rejected.Status != 422 || rejected.Code != rejection.code || len(s.Attempts) != 1 || f.count(rejection.path) != 1 || f.Ledger.snapshot() != 0 {
			t.Fatalf("有效业务拒绝被误判: err=%v calls=%d rows=%d", err, f.count(rejection.path), f.Ledger.snapshot())
		}
	}
}

// TestConcurrentIdempotency同时释放八个真实POST，每个Task各有ctx/Done；ledger锁内效果只能一次。
// 不复制整套M17项目，延续其接口约定并新增“幂等key绑定payload”的独立断言。
func TestConcurrentIdempotency(t *testing.T) {
	ctx := testScope(t)
	f, c, trace := testFixture(t)
	start := newGate()
	tasks := make([]*Task, 0, 8)
	for range 8 {
		task := startTask(ctx, func(work context.Context) taskResult {
			if err := wait(work, start.Release, "并发POST起跑gate"); err != nil {
				return taskResult{Err: err}
			}
			r, s, err := Retry(work, c, Spec{Method: "POST", URL: f.URL + "/orders", Key: "parallel-key", Body: []byte(`{"sku":"parallel","qty":2}`), Replayable: true, Idempotent: true}, Policy{Total: 2 * time.Second, MaxAttempts: 1}, trace)
			return taskResult{Reply: r, Stats: s, Err: err}
		}, nil)
		ownTask(t, task, start.Open)
		tasks = append(tasks, task)
	}
	start.Open()
	duplicates := 0
	for _, task := range tasks {
		r, err := receive(ctx, task)
		if err != nil {
			t.Fatal(err)
		}
		if err = task.StopAndJoin(start.Open); err != nil {
			t.Fatal(err)
		}
		if r.Err != nil || r.Reply.Receipt.ID != 1 {
			t.Fatalf("并发回执: %+v", r)
		}
		if r.Reply.Receipt.Duplicate {
			duplicates++
		}
	}
	if f.Ledger.snapshot() != 1 || f.count("/orders") != 8 || duplicates != 7 {
		t.Fatalf("rows=%d calls=%d duplicates=%d", f.Ledger.snapshot(), f.count("/orders"), duplicates)
	}
	_, s, err := Retry(ctx, c, Spec{Method: "POST", URL: f.URL + "/orders", Key: "parallel-key", Body: []byte(`{"sku":"parallel","qty":3}`), Replayable: true, Idempotent: true}, Policy{Total: time.Second, MaxAttempts: 3}, trace)
	if statusCode(err) != http.StatusConflict || len(s.Attempts) != 1 || f.Ledger.snapshot() != 1 {
		t.Fatalf("key参数冲突应拒绝: %v", err)
	}
}

// TestBodyReplayAndRetryAfter独立看handler保存的字节，避免只在客户端对同一Spec做镜像比较。
// HTTP-date/非法值为辅助纯函数测试，真正Retry-After=1由实际server/client及attempt时间验证。
func TestBodyReplayAndRetryAfter(t *testing.T) {
	ctx := testScope(t)
	f, c, trace := testFixture(t)
	spec := Spec{Method: "GET", URL: f.URL + "/replay", Body: []byte("stock-query"), Replayable: true, Idempotent: true}
	_, _, err := Retry(ctx, c, spec, Policy{Total: time.Second, MaxAttempts: 2}, trace)
	bodies := f.bodyCopies("/replay")
	if err != nil || len(bodies) != 2 || bodies[0] != "stock-query" || bodies[1] != "stock-query" {
		t.Fatalf("真实body=%v err=%v", bodies, err)
	}
	spec.Replayable = false
	_, s, err := Retry(ctx, c, spec, Policy{Total: time.Second, MaxAttempts: 2}, trace)
	if !errors.Is(err, ErrBodyNotReplayable) || len(s.Attempts) != 1 || f.count("/replay") != 3 {
		t.Fatalf("不可重放仍尝试: %v %+v", err, s)
	}
	_, s, err = Retry(ctx, c, Spec{Method: "GET", URL: f.URL + "/retry-after", Idempotent: true}, Policy{Total: 2 * time.Second, MaxAttempts: 2}, trace)
	if err != nil || f.count("/retry-after") != 2 || s.Attempts[1].Start.Sub(s.Attempts[0].Start) < time.Second {
		t.Fatalf("提前重试: %+v %v", s, err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if retryAfter(now.Add(2*time.Second).Format(http.TimeFormat), now) != 2*time.Second || retryAfter("bad", now) != 0 || retryAfter("-1", now) != 0 {
		t.Fatal("Retry-After辅助解析错误")
	}
}

// TestHalfOpenFailureAndWindow独立验证库窗口清计数和半开失败再打开，补充demo的半开成功路径。
// timer起点是已确认的失败返回/open，不是sleep猜并发；全部调用仍是实际网络。
func TestHalfOpenFailureAndWindow(t *testing.T) {
	ctx := testScope(t)
	f, c, trace := testFixture(t)
	cb := breaker(trace, nil, 50*time.Millisecond, 50*time.Millisecond)
	bad := Spec{Method: "GET", URL: f.URL + "/always503", Idempotent: true}
	if _, err := throughBreaker(ctx, cb, c, bad, trace); err == nil {
		t.Fatal("503应失败")
	}
	if err := pause(ctx, 60*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if cb.State() != gobreaker.StateClosed || cb.Counts().TotalFailures != 0 {
		t.Fatal("fixed窗口应清零")
	}
	for range 2 {
		_, _ = throughBreaker(ctx, cb, c, bad, trace)
	}
	if cb.State() != gobreaker.StateOpen {
		t.Fatal("连续2故障应open")
	}
	if err := pause(ctx, 60*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := throughBreaker(ctx, cb, c, bad, trace); err == nil {
		t.Fatal("half-open失败应返回503")
	}
	if cb.State() != gobreaker.StateOpen || f.count("/always503") != 4 {
		t.Fatalf("half-open失败未重开 calls=%d", f.count("/always503"))
	}
}

// TestAlreadyCanceledAndBusinessRejection验证已取消入口不派发、422只一次、不污染breaker故障统计。
func TestAlreadyCanceledAndBusinessRejection(t *testing.T) {
	ctx := testScope(t)
	f, c, trace := testFixture(t)
	dead, cancel := context.WithCancel(ctx)
	cancel()
	_, s, err := Retry(dead, c, Spec{Method: "GET", URL: f.URL + "/healthy", Idempotent: true}, Policy{Total: time.Second, MaxAttempts: 3}, trace)
	if !errors.Is(err, context.Canceled) || len(s.Attempts) != 0 || f.count("/healthy") != 0 {
		t.Fatal("已取消仍派发")
	}
	cb := breaker(trace, nil, time.Second, time.Second)
	for range 3 {
		_, err = throughBreaker(ctx, cb, c, Spec{Method: "GET", URL: f.URL + "/reject", Idempotent: true}, trace)
		if statusCode(err) != 422 {
			t.Fatal(err)
		}
	}
	if cb.State() != gobreaker.StateClosed || cb.Counts().TotalFailures != 0 || f.count("/reject") != 3 {
		t.Fatal("明确业务拒绝不应被算依赖故障")
	}
}

// TestDocumentationEntry检查core可直接运行且包含业务事实日志，不将文本作为上述副作用验收的替代。
func TestDocumentationEntry(t *testing.T) {
	var output strings.Builder
	if err := Run(testScope(t), &output, "core"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "UNKNOWN") || !strings.Contains(output.String(), "rows=3") {
		t.Fatal("教学入口未展示未知写和修复事实")
	}
}
