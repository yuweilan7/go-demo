package lesson

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testContext 给每个正常实验独立的截止时间；t.Cleanup 在测试结束时取消本次范围。
// 测试函数仍应等 worker 退出，不能把取消当成自动 join。
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestBusinessInvariant 用订单成功数与剩余库存独立核对，防止测试与实现共用同一错误推导。
// 反例预期违反守恒，修复必须同时卖满可用库存、没有超卖、没有负库存。
func TestBusinessInvariant(t *testing.T) {
	ctx := testContext(t)
	bad, err := SplitAtomicSale(ctx)
	if err != nil || bad.Sold != 2 || bad.Remaining != 0 || bad.Conserved() {
		t.Fatalf("原子组合反例: %+v err=%v", bad, err)
	}
	mapBad, err := SplitMapSale(ctx)
	if err != nil || mapBad.Sold != 2 || mapBad.Conserved() {
		t.Fatalf("sync.Map 组合反例: %+v err=%v", mapBad, err)
	}
	for _, initial := range []int64{0, 1, 7, 100} {
		mutex := SellWithMutex(initial, 200)
		cas, err := SellWithCAS(ctx, initial, 200)
		if err != nil {
			t.Fatal(err)
		}
		for name, got := range map[string]StockResult{"Mutex": mutex, "CAS": cas} {
			if !got.Conserved() || got.Sold != initial || got.Remaining != 0 {
				t.Fatalf("%s: %+v", name, got)
			}
		}
	}
}

// TestPointerReceiverRepair 将同一 Inventory 指针交给多者；检查实际销量而不只是跑过加锁代码。
func TestPointerReceiverRepair(t *testing.T) {
	stock := NewInventory(300)
	alias := stock // 复制指针，不复制 stock 所指的 Mutex/字段；两者共同调用一套锁协议。
	var wg sync.WaitGroup
	for range 150 {
		wg.Go(func() { stock.Buy() })
		wg.Go(func() { alias.Buy() })
	}
	wg.Wait()
	got := stock.Result()
	if !got.Conserved() || got.Sold != 300 {
		t.Fatalf("同对象指针接收者修复: %+v", got)
	}
}

// TestCancellation 表明取消是协作检查：已经取消的请求不会进入本课 CAS 业务转移。
// SplitAtomicSale 的 release 与 ctx 同时就绪时，不能承诺它必选取消，故不对那段错误代码假设零副作用。
func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stock := NewCASInventory(1)
	ok, err := stock.Buy(ctx)
	if ok || err != context.Canceled || stock.Remaining() != 1 {
		t.Fatalf("取消结果 ok=%v err=%v remaining=%d", ok, err, stock.Remaining())
	}
	if _, err := ObserveIO(ctx, true); err != context.Canceled {
		t.Fatalf("已取消 HTTP: %v", err)
	}
}

// TestRWAndSliceOwnership 同时验证读写互斥和输入/输出的底层数组隔离。
func TestRWAndSliceOwnership(t *testing.T) {
	got, err := ObserveRW(testContext(t))
	if err != nil || got.ConcurrentReaders != 2 || got.WriterBeforeRelease || got.FinalValue != 1 {
		t.Fatalf("RW: %+v err=%v", got, err)
	}
	c := NewCatalog()
	input := []string{"phone"}
	c.Set("sku-1", input)
	input[0] = "input mutation"
	output := c.Get("sku-1")
	output[0] = "output mutation"
	if c.Get("sku-1")[0] != "phone" {
		t.Fatal("底层数组别名泄漏")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				c.Set("sku-1", []string{"phone"})
			}
		})
		wg.Go(func() {
			for range 100 {
				if c.Get("sku-1")[0] != "phone" {
					t.Error("商品被外部别名修改")
				}
			}
		})
	}
	wg.Wait()
}

// TestSnapshotVersions 对每份读结果核对版本不变量，独立发现“版本号新、内容旧”的半发布。
// 并发读者只能看到某一完整版本；更新次数还检验 writerMu 是否避免丢失更新。
func TestSnapshotVersions(t *testing.T) {
	initial := Config{Version: 0, Limits: map[string]int{"sku-1": 0}, Labels: []string{"v0"}}
	s := NewSnapshots(initial)
	initial.Limits["sku-1"] = -1
	initial.Labels[0] = "外部输入"
	old := s.Read()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				s.Update(func(c *Config) {
					c.Version++
					c.Limits["sku-1"] = c.Version * 10
					c.Labels[0] = fmt.Sprintf("v%d", c.Version)
				})
			}
		})
		wg.Go(func() {
			for range 200 {
				c := s.Read()
				if c.Limits["sku-1"] != c.Version*10 || c.Labels[0] != fmt.Sprintf("v%d", c.Version) {
					t.Errorf("半发布快照: %+v", c)
				}
				c.Limits["sku-1"] = -2 // 读者修改自己的副本，不能破坏已发布版本。
			}
		})
	}
	wg.Wait()
	final := s.Read()
	if old.Version != 0 || old.Limits["sku-1"] != 0 || old.Labels[0] != "v0" {
		t.Fatalf("旧快照被修改: %+v", old)
	}
	if final.Version != 200 || final.Limits["sku-1"] != 2000 {
		t.Fatalf("writer 丢失更新: %+v", final)
	}
	visible, err := Visibility(testContext(t))
	if err != nil || visible != "sku-1:完整配置" {
		t.Fatalf("发布: %q err=%v", visible, err)
	}
}

// TestMapSingleKey 核对选出一位创建者和付款者，而不依赖具体哪个 goroutine 获胜。
// Range 混合不是每版必现的 API 保证，测试只要求两条预先存在的 key 各访问一次。
func TestMapSingleKey(t *testing.T) {
	r := ObserveMap()
	if r.Candidates != 8 || r.CreateWinners != 1 || r.PayWinners != 1 || r.FinalState != "paid" {
		t.Fatalf("单 key 协议: %+v", r)
	}
	if len(r.RangeObserved) != 2 || r.RangeVisits != 2 {
		t.Fatalf("Range 遗漏 key: %+v", r.RangeObserved)
	}
	for _, key := range []string{"left", "right"} {
		if r.RangeFinal[key] != 2 {
			t.Fatalf("Range callback 的写入未完成: %+v", r.RangeFinal)
		}
		if r.RangeObserved[key] != 1 && r.RangeObserved[key] != 2 {
			t.Fatalf("非法 Range 版本: %+v", r.RangeObserved)
		}
	}
}

// TestOnceAndPool 检查库契约与重置策略，故意不要求 Pool 复用同一指针或 GC 恰好丢弃多少对象。
func TestOnceAndPool(t *testing.T) {
	r := ObserveOnce()
	if r.NormalCalls != 1 || !r.AllReady || !r.PanicRecovered || r.PanicInitCalls != 1 || r.ErrorInitCalls != 1 || !r.CachedError || r.RetryCalls != 2 || r.RetryValue != "ready" {
		t.Fatalf("Once: %+v", r)
	}
	p := ObservePool()
	if p.CleanLength != 0 || (p.DirtyReused && p.DirtyLength == 0) {
		t.Fatalf("Pool 重置策略: %+v", p)
	}
}

// TestRealHTTPAndTransfer 通过真实 loopback HTTP 检查 I/O 等待期间的锁范围，再检查总账守恒。
// 不用吞吐/耗时判断正确性：响应门固定等待窗口，TryLock 给出直接观察。
func TestRealHTTPAndTransfer(t *testing.T) {
	for _, hold := range []bool{true, false} {
		r, err := ObserveIO(testContext(t), hold)
		if err != nil || r.ObserverAcquired == hold || r.Response != "priced" {
			t.Fatalf("真实 HTTP: %+v err=%v", r, err)
		}
	}
	for _, hold := range []bool{true, false} {
		r, err := ObserveCanceledIO(testContext(t), hold)
		if !errors.Is(err, context.Canceled) || r.ObserverAcquired == hold {
			t.Fatalf("真实请求取消路径: %+v err=%v", r, err)
		}
	}
	a, b := NewAccount(1, 1000), NewAccount(2, 1000)
	var wg sync.WaitGroup
	for range 200 {
		wg.Go(func() {
			if ok, err := Transfer(a, b, 1); !ok || err != nil {
				t.Errorf("a→b: ok=%v err=%v", ok, err)
			}
		})
		wg.Go(func() {
			if ok, err := Transfer(b, a, 1); !ok || err != nil {
				t.Errorf("b→a: ok=%v err=%v", ok, err)
			}
		})
	}
	wg.Wait()
	if a.Balance() != 1000 || b.Balance() != 1000 || a.Balance()+b.Balance() != 2000 {
		t.Fatal("双向转账余额不守恒")
	}
	if _, err := Transfer(a, a, 1); err == nil {
		t.Fatal("同账户转账应拒绝")
	}
}

// mustPanic 由类型契约测试同步调用，recover 只覆盖本次函数调用所在 goroutine。
// 若 API 没有拒绝错误输入，测试失败；捕获的是预期普通 panic，不冒充 fatal 可恢复。
func mustPanic(t *testing.T, call func()) {
	t.Helper()
	defer func() {
		got := recover()
		if got == nil {
			t.Error("错误输入没有触发预期 panic")
		} else {
			t.Logf("捕获预期 panic: %v", got)
		}
	}()
	call()
}

// TestAtomicValueTypeContract 实际核对容器动态类型/可比较性，不从内存布局推断 API。
// nil interface 与 typed nil 不同：Store(nil) 禁止，Store((*Config)(nil)) 本身可以，但本课 Read 不接受它。
func TestAtomicValueTypeContract(t *testing.T) {
	var empty atomic.Value
	if empty.Load() != nil {
		t.Fatal("零值 Value.Load 应为 nil")
	}
	mustPanic(t, func() { empty.Store(nil) })
	empty.Store(1)
	mustPanic(t, func() { empty.Store("不同的动态类型") })
	var typedNil atomic.Value
	typedNil.Store((*Config)(nil))
	if typedNil.Load().(*Config) != nil {
		t.Fatal("typed nil 指针值应保留")
	}
	var mapValue atomic.Value
	m := map[string]int{"sku-1": 1}
	mapValue.Store(m) // map 可以 Store/Load；容器不负责 map 内容的后续访问。
	mustPanic(t, func() { mapValue.CompareAndSwap(m, map[string]int{"sku-1": 2}) })
	var syncMap sync.Map
	syncMap.Store("sku-1", m)
	mustPanic(t, func() { syncMap.CompareAndSwap("sku-1", m, map[string]int{"sku-1": 2}) })
}
