// demo 是本课正常入口：包含业务错误对照，但不包含任何真正的 data race 或永久阻塞。
package main

import (
	"context"
	"errors"
	"fmt"
	lesson "go-demo/lessons/m14_sync_memory_races"
	"os"
	"sync"
	"time"
)

// require 由 demo owner 调用，把教学观察变成会使命令失败的断言，而不是只打印“看起来正常”。
func require(ok bool, message string) error {
	if !ok {
		return fmt.Errorf("业务断言失败: %s", message)
	}
	return nil
}

// run 拥有本轮 ctx；每个实验在返回前等自己的 goroutine/HTTP 退出，再启动下个实验。
// 10 秒是整轮的取消上限，不是凭时间判断成功；所有先后关系由锁、屏障、原子操作保证。
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bad, err := lesson.SplitAtomicSale(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("E01 atomic Load/Store: initial=%d remaining=%d sold=%d conserved=%v（预期业务错误，无 data race）\n", bad.Initial, bad.Remaining, bad.Sold, bad.Conserved())
	if err := require(bad.Remaining == 0 && bad.Sold == 2 && !bad.Conserved(), "必须复现两次售出最后一件库存"); err != nil {
		return err
	}
	fixed := lesson.SellWithMutex(1, 8)
	cas, err := lesson.SellWithCAS(ctx, 1, 8)
	if err != nil {
		return err
	}
	fmt.Printf("E02 Mutex: %+v conserved=%v；CAS: %+v conserved=%v\n", fixed, fixed.Conserved(), cas, cas.Conserved())
	if err := require(fixed.Conserved() && cas.Conserved() && fixed.Sold == 1 && cas.Sold == 1, "Mutex/CAS 都只能接受一个订单"); err != nil {
		return err
	}

	rw, err := lesson.ObserveRW(ctx)
	if err != nil {
		return err
	}
	catalog := lesson.NewCatalog()
	input := []string{"phone", "case"}
	catalog.Set("sku-1", input)
	input[0] = "外部修改"
	read := catalog.Get("sku-1")
	read[0] = "读者修改"
	fmt.Printf("E03 RWMutex: readers=%d writer-before-release=%v final=%d；slice 隔离=%v\n", rw.ConcurrentReaders, rw.WriterBeforeRelease, rw.FinalValue, catalog.Get("sku-1"))
	if err := require(!rw.WriterBeforeRelease && rw.FinalValue == 1 && catalog.Get("sku-1")[0] == "phone", "读锁阻止写者，返回 slice 不泄漏别名"); err != nil {
		return err
	}

	snapshots := lesson.NewSnapshots(lesson.Config{Version: 1, Limits: map[string]int{"sku-1": 10}, Labels: []string{"v1"}})
	old := snapshots.Read()
	snapshots.Update(func(c *lesson.Config) { c.Version = 2; c.Limits["sku-1"] = 20; c.Labels[0] = "v2" })
	current := snapshots.Read()
	visible, err := lesson.Visibility(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("E04 snapshots: old=%+v new=%+v；atomic 发布普通字段=%q\n", old, current, visible)
	if err := require(old.Limits["sku-1"] == 10 && current.Limits["sku-1"] == 20 && visible == "sku-1:完整配置", "旧快照未改变且发布完整"); err != nil {
		return err
	}

	mapBad, err := lesson.SplitMapSale(ctx)
	if err != nil {
		return err
	}
	mapResult := lesson.ObserveMap()
	fmt.Printf("E05 sync.Map Load/Store: %+v conserved=%v；candidates=%d create-winners=%d pay-winners=%d state=%s；Range=%v mixed=%v（当前运行观察）\n", mapBad, mapBad.Conserved(), mapResult.Candidates, mapResult.CreateWinners, mapResult.PayWinners, mapResult.FinalState, mapResult.RangeObserved, mapResult.RangeMixed)
	if err := require(!mapBad.Conserved() && mapResult.CreateWinners == 1 && mapResult.PayWinners == 1 && mapResult.FinalState == "paid", "单命令正确性与业务组合错误分别成立"); err != nil {
		return err
	}
	once := lesson.ObserveOnce()
	fmt.Printf("E06 Once: %+v\n", once)
	if err := require(once.NormalCalls == 1 && once.AllReady && once.PanicRecovered && once.PanicInitCalls == 1 && once.ErrorInitCalls == 1 && once.CachedError && once.RetryCalls == 2 && once.RetryValue == "ready", "Once 不重试失败，显式策略只缓存成功"); err != nil {
		return err
	}
	pool := lesson.ObservePool()
	fmt.Printf("E07 Pool: dirty-reused=%v dirty-len=%d clean-len=%d New-before-GC=%d after-GC=%d（复用/丢弃均不保证）\n", pool.DirtyReused, pool.DirtyLength, pool.CleanLength, pool.NewBeforeGC, pool.NewAfterGC)
	if err := require(pool.CleanLength == 0 && (!pool.DirtyReused || pool.DirtyLength > 0), "干净 Pool 借出时无可见残留"); err != nil {
		return err
	}

	for _, hold := range []bool{true, false} {
		ioResult, ioErr := lesson.ObserveIO(ctx, hold)
		if ioErr != nil {
			return ioErr
		}
		fmt.Printf("E08 real HTTP: hold=%v observer-acquired=%v body=%q\n", hold, ioResult.ObserverAcquired, ioResult.Response)
		if err := require(ioResult.ObserverAcquired != hold && ioResult.Response == "priced", "锁内 I/O 阻挡观察者，复制后锁外 I/O 允许观察者"); err != nil {
			return err
		}
	}
	canceledIO, cancelErr := lesson.ObserveCanceledIO(ctx, true)
	fmt.Printf("E08 real HTTP cancel-at-gate: observer-acquired=%v error-is-canceled=%v（请求实际退出后返回）\n", canceledIO.ObserverAcquired, errors.Is(cancelErr, context.Canceled))
	if err := require(!canceledIO.ObserverAcquired && errors.Is(cancelErr, context.Canceled), "真实 HTTP 取消传播并收尾"); err != nil {
		return err
	}
	a, b := lesson.NewAccount(1, 100), lesson.NewAccount(2, 100)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { _, _ = lesson.Transfer(a, b, 1) })
		wg.Go(func() { _, _ = lesson.Transfer(b, a, 1) })
	}
	wg.Wait()
	fmt.Printf("E09 lock-order: a=%d b=%d total=%d\n", a.Balance(), b.Balance(), a.Balance()+b.Balance())
	if err := require(a.Balance()+b.Balance() == 200, "转账总额守恒"); err != nil {
		return err
	}
	fmt.Println("DEMO PASS: 业务错误被明确复现；所有修复业务断言通过；goroutine/HTTP 资源已收尾")
	return nil
}

// main 在 run 的 defer 已完成后才决定退出码；错误不会被打印后假装演示成功。
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
