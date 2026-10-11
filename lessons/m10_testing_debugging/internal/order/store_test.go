package order_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go-demo/lessons/m10_testing_debugging/internal/order"
)

// newStore 是当前测试 goroutine 调用的夹具工厂；Helper 使失败报告指向调用它的测试行。
// 每次调用都创建新指针和新 map，不借共享全局订单降低代码量。
func newStore(t *testing.T) *order.Store {
	t.Helper()
	store, err := order.NewStore("order-42", 1299)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestRejectedPaymentLeavesBusinessUnchanged 从业务契约断言错误后的事实，不镜像 ApplyPayment 的 if 顺序。
func TestRejectedPaymentLeavesBusinessUnchanged(t *testing.T) {
	store := newStore(t)
	_, err := store.ApplyPayment(order.Payment{ID: "order-42", Cents: 1300})
	if !errors.Is(err, order.ErrAmountMismatch) {
		t.Fatalf("err=%v; want amount mismatch", err)
	}
	assertPending(t, store) // 只断言返回 error 不足够；错误版本正是“返回 error 但状态已经 paid”。
}

// assertPending 由拒绝支付和并行隔离测试调用，证明未入账、未完成状态，而非只证明锁没有 race。
func assertPending(t *testing.T, store *order.Store) {
	t.Helper()
	got, err := store.View("order-42")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "pending" || got.CreditedCents != 0 || got.CreditCount != 0 {
		t.Fatalf("rejected payment mutated business: %+v; want pending, credit=0, count=0", got)
	}
}

// TestPaymentCreditsExactlyOnce 给同一笔金额重放两次，证明本内存规则中的入账守恒和次数唯一。
// 它不证明网络中断恢复、实库唯一键、broker ack 或不同幂等键的业务规则。
func TestPaymentCreditsExactlyOnce(t *testing.T) {
	store := newStore(t)
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := store.ApplyPayment(order.Payment{ID: "order-42", Cents: 1299}); err != nil {
			t.Fatalf("attempt=%d: %v", attempt, err)
		}
	}
	got, err := store.View("order-42")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "paid" || got.CreditedCents != 1299 || got.CreditCount != 1 {
		t.Fatalf("snapshot=%+v; want paid, credited 1299 once", got)
	}
}

// TestMissingOrderDoesNotChangeExistingOrder 证明不存在 id 的失败不会误伤已有订单。
func TestMissingOrderDoesNotChangeExistingOrder(t *testing.T) {
	store := newStore(t)
	if _, err := store.ApplyPayment(order.Payment{ID: "other", Cents: 1299}); !errors.Is(err, order.ErrNotFound) {
		t.Fatalf("err=%v; want order not found", err)
	}
	assertPending(t, store)
}

// TestParallelIsolatedFixtures 是共享夹具故障的修复版。t.Parallel 在父函数返回后放行子测试。
// 两个用例故意使用同一个订单 id 和文件名，但独占 Store 与 TempDir，互不覆盖业务数据。
func TestParallelIsolatedFixtures(t *testing.T) {
	for _, name := range []string{"pay", "reject"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newStore(t)
			dir := t.TempDir() // testing 创建独占目录，并在本子测试和其后代完成后递归清理。
			if name == "pay" {
				if _, err := store.ApplyPayment(order.Payment{ID: "order-42", Cents: 1299}); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := store.ApplyPayment(order.Payment{ID: "order-42", Cents: 1300}); !errors.Is(err, order.ErrAmountMismatch) {
					t.Fatalf("err=%v; want rejection", err)
				}
				assertPending(t, store)
			}
			value, err := store.View("order-42")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "receipt.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved order.Snapshot
			if err := json.Unmarshal(actual, &saved); err != nil {
				t.Fatal(err)
			}
			wantState, wantCents, wantCount := "pending", int64(0), 0
			if name == "pay" {
				wantState, wantCents, wantCount = "paid", 1299, 1
			}
			if saved.ID != "order-42" || saved.State != wantState || saved.CreditedCents != wantCents || saved.CreditCount != wantCount {
				t.Fatalf("persisted receipt=%+v; want state=%s credit=%d count=%d", saved, wantState, wantCents, wantCount)
			}
			t.Logf("exclusive receipt=%s state=%s count=%d", path, saved.State, saved.CreditCount)
		})
	}
}

// TestCleanupLIFOAndTempDirRemoval 由测试 runner 调用，实际观察子测试退出后清理已完成。
// child 的回调在正常返回或 Fatal 路径也会执行；callback 不是每个 goroutine 的自动 finally。
func TestCleanupLIFOAndTempDirRemoval(t *testing.T) {
	var calls []string
	var dir string
	t.Run("child", func(t *testing.T) {
		dir = t.TempDir()
		t.Cleanup(func() { calls = append(calls, "first") })
		t.Cleanup(func() { calls = append(calls, "last") })
	}) // 非 parallel 的 child 完成及 Cleanup 执行后，Run 才返回。
	if len(calls) != 2 || calls[0] != "last" || calls[1] != "first" {
		t.Fatalf("cleanup order=%v; want [last first]", calls)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("TempDir still present or unexpected stat error: %s err=%v", dir, err)
	}
}

// TestEnvironmentRestored 使用串行 child 改进程环境；不调用 Parallel，因为环境不是请求专属数据。
func TestEnvironmentRestored(t *testing.T) {
	const key = "GO_DEMO_M10_TEST_MODE"
	before, existed := os.LookupEnv(key)
	t.Run("serial_environment", func(t *testing.T) {
		t.Setenv(key, "isolated") // Setenv 自动用 Cleanup 恢复原值，但这段期间仍影响整个进程。
		if got := os.Getenv(key); got != "isolated" {
			t.Fatalf("env=%q; want isolated", got)
		}
	})
	after, exists := os.LookupEnv(key)
	if after != before || exists != existed {
		t.Fatalf("environment not restored: before=%q/%t after=%q/%t", before, existed, after, exists)
	}
}
