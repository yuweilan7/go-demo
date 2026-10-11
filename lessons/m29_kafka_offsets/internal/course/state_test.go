package course

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWatermarkGap 验证独立安全事实：高offset完成、重复完成都不能越过低offset缺口。
// 这是纯Go状态测试；broker的实际next offset由显式真实集成实验另行读取验证。
func TestWatermarkGap(t *testing.T) {
	w := NewWatermark(40)
	for _, offset := range []int64{42, 41, 42} {
		if next, advanced := w.Complete(offset); next != 40 || advanced {
			t.Fatalf("缺口未补就前移: next=%d", next)
		}
	}
	if next, advanced := w.Complete(40); next != 43 || !advanced {
		t.Fatalf("连续完成应前移到43: %d", next)
	}
	if next, advanced := w.Complete(41); next != 43 || advanced {
		t.Fatal("旧完成不能回退水位")
	}
}

// TestCleanupErrorsRemainVisible 保证预期业务失败不能掩盖join/Close失败。
// 同时保留三种身份，故障扩展才能拒绝把资源泄漏当作“produce确实失败”的PASS。
func TestCleanupErrorsRemainVisible(t *testing.T) {
	primary := errInjected
	cleanup := errors.New("synthetic close failure")
	combineCleanup(&primary, cleanup)
	if !errors.Is(primary, errInjected) || !errors.Is(primary, cleanup) || !errors.Is(primary, errCleanup) {
		t.Fatalf("错误聚合丢掉身份: %v", primary)
	}
	clean := errInjected
	combineCleanup(&clean, nil)
	if clean != errInjected {
		t.Fatal("nil清理不得改变预期故障的直接身份")
	}
}

// testLedger 由文件语义/生命周期测试借用，返回指针别名与独占路径。
// t.Cleanup注册关闭文件；调用者若再启动任务，必须后注册任务停止，LIFO让任务先join。
func testLedger(t *testing.T) (*Ledger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "business.jsonl")
	l, err := OpenLedger(path, &Trace{out: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Errorf("关闭测试业务文件: %v", err)
		}
	})
	return l, path
}

// TestLedgerDedupeAndCancellation 读取真正写出的JSONL，验证稳定event_id两次尝试只有一次
// applied；取消后的第三次调用不能留下文件/内存副作用。此证据仅是本机文件，不是SQL事务。
func TestLedgerDedupeAndCancellation(t *testing.T) {
	l, path := testLedger(t)
	e := Effect{Scope: "orders", Owner: "test", EventID: "stable-1", Topic: "test", Partition: 0, Offset: 7}
	for i := 0; i < 2; i++ {
		if err := l.Apply(context.Background(), e, true); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Apply(ctx, e, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("预期取消: %v", err)
	}
	a, b := l.Counts("orders", "stable-1", "")
	if a != 2 || b != 1 {
		t.Fatalf("attempts=%d effects=%d", a, b)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("磁盘应恰好2条尝试: %s", data)
	}
	var first, second Effect
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[1], &second); err != nil {
		t.Fatal(err)
	}
	if !first.Applied || second.Applied || first.EventID != second.EventID {
		t.Fatal("磁盘幂等事实错误")
	}
}

// TestCanceledGateHasNoLateEffect 用未释放gate挂起真实worker，取消并join后再释放gate。
// channel已经没有接收任务，通知不能“复活”已结束goroutine；文件必须仍然不存在业务写入。
func TestCanceledGateHasNoLateEffect(t *testing.T) {
	l, path := testLedger(t)
	gate := make(chan struct{})
	w := StartWorker(context.Background(), gate, func(ctx context.Context) error {
		return l.Apply(ctx, Effect{Scope: "revoke", EventID: "old", Owner: "old-generation"}, false)
	})
	t.Cleanup(func() {
		if err := w.Stop(); err != nil {
			t.Errorf("停止worker: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-w.Started:
	case <-ctx.Done():
		t.Fatal("worker未发布开始状态")
	}
	w.Cancel()
	if err := w.ResultBeforeDeadline(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("预期取消结果: %v", err)
	}
	if err := w.Stop(); err != nil {
		t.Fatal(err)
	}
	close(gate)
	if a, _ := l.Counts("revoke", "old", ""); a != 0 {
		t.Fatal("旧owner出现迟到副作用")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) != 0 {
		t.Fatalf("取消后文件应为空 err=%v data=%s", err, data)
	}
}

// TestWorkerPanicAndDone 验证panic仅在其所在goroutine入口recover，再通过error给owner；
// owner还必须join Done，不能因为拿到panic error就认为所有defer已完成。
func TestWorkerPanicAndDone(t *testing.T) {
	w := StartWorker(context.Background(), nil, func(context.Context) error { panic("synthetic-worker") })
	t.Cleanup(func() {
		if err := w.Stop(); err != nil {
			t.Errorf("join: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := w.ResultBeforeDeadline(ctx); err == nil || !strings.Contains(err.Error(), "synthetic-worker") {
		t.Fatalf("panic没有转成结果: %v", err)
	}
	if err := w.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Done:
	default:
		t.Fatal("Stop返回后Done必须已关闭")
	}
}

// TestResultDoesNotMeanJoined 刻意让任务先交结果，再在cleanup gate等待。
// 断言读result时done仍未关闭，再由owner cancel/unblock并用新预算join，给出确定性反例。
func TestResultDoesNotMeanJoined(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan int, 1)
	done := make(chan struct{})
	cleanupGate := make(chan struct{})
	var once sync.Once
	stop := func() {
		cancel()
		once.Do(func() { close(cleanupGate) })
		if err := joinDone(done, "result-vs-done test"); err != nil {
			t.Errorf("join: %v", err)
		}
	}
	go func() {
		defer close(done)
		result <- 7
		select {
		case <-cleanupGate:
		case <-ctx.Done():
		}
	}()
	t.Cleanup(stop)
	budget, budgetCancel := context.WithTimeout(context.Background(), time.Second)
	defer budgetCancel()
	select {
	case <-result:
	case <-budget.Done():
		t.Fatal("任务未发送结果")
	}
	select {
	case <-done:
		t.Fatal("cleanup gate未解除时任务不应退出")
	default:
	}
	stop()
}

// TestPendingAndScopeGuard 使用无秘密JSON，只验证READY/loopback边界在资源创建前拒绝。
// 它没有Kafka mock，也不把配置测试当成broker鉴权/业务证据。
func TestPendingAndScopeGuard(t *testing.T) {
	for _, tc := range []struct{ name, data string }{
		{"pending", `{"services":{"kafka":{"state":"PENDING"}}}`},
		{"non-loopback", `{"services":{"kafka":{"state":"READY","host":"example.com","port":9092,"server_version":"4.1"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "index.json")
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadConfig(path); err == nil {
				t.Fatal("非法端点不应通过guard")
			}
		})
	}
}

// TestRealKafkaCore 只有明确GOLEARN_KAFKA_INTEGRATION=1才运行，调用与CLI相同的真实
// Kafka路径。默认测试明确SKIP；启用时不mock producer/consumer，也不会跳过实验错误。
func TestRealKafkaCore(t *testing.T) {
	if os.Getenv("GOLEARN_KAFKA_INTEGRATION") != "1" {
		t.Skip("真实Kafka集成未启用；默认测试只覆盖Go/文件语义")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var output bytes.Buffer
	if err := Run(ctx, "core", DefaultIndex(), "", &output); err != nil {
		t.Fatalf("真实Kafka实验失败: %v\n%s", err, output.String())
	}
	for _, proof := range []string{"E1_PASS", "E2_PASS", "scope=duplicate", "scope=idempotent", "scope=unordered.bad", "scope=unordered.watermark", "E5_PASS", "CORE_ASSERTIONS_PASS"} {
		if !strings.Contains(output.String(), proof) {
			t.Fatalf("缺少真实路径证据%s", proof)
		}
	}
	t.Log(output.String())
}
