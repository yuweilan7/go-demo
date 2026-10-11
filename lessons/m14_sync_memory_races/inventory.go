// Package lesson 用同一件库存展示同步范围与业务正确性。
// 正常包只含没有 data race 的代码；真正访问冲突被隔离在 faultcases 构建标签中。
package lesson

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// StockResult 是 owner 在所有购买者退出后收集的业务事实。
// Sold 是成功接受的订单数；不能只看 Remaining>=0，因为重复卖出也可能留下 0。
type StockResult struct {
	Initial   int64
	Remaining int64
	Sold      int64
}

// Conserved 由演示入口与测试调用，独立核对“初始=剩余+已卖且没有负库存”。
// 它不依赖某种锁或 CAS 的内部实现，因此能揭露原子 Load/Store 的业务错误。
func (r StockResult) Conserved() bool {
	return r.Remaining >= 0 && r.Sold >= 0 && r.Initial == r.Remaining+r.Sold
}

// SplitAtomicSale 由 E01 调用：两个购买者对同一个 atomic.Int64 各自先检查、后写回。
// ctx 属于调用者，函数拥有两个 goroutine、checked 屏障和 release 广播，返回前等待退出。
// 这段故意错误的业务代码仍没有 data race：每次访问库存都是合法的原子访问。
func SplitAtomicSale(ctx context.Context) (StockResult, error) {
	var remaining atomic.Int64
	remaining.Store(1)
	var sold atomic.Int64
	checked := make(chan struct{}, 2)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			// seen 是该 goroutine 的值副本。Load 返回后并不会替调用者锁住 remaining。
			seen := remaining.Load()
			checked <- struct{}{}
			select {
			case <-release:
				if seen > 0 {
					// 两个 seen 都是 1，因此两次 Store 都写 0，却各自接受了订单。
					remaining.Store(seen - 1)
					sold.Add(1)
				}
			case <-ctx.Done():
				// 取消只让尚在屏障处的购买者退出；owner 仍要 wg.Wait 才能安全收集结果。
			}
		})
	}
	for range 2 {
		select {
		case <-checked:
		case <-ctx.Done():
			close(release)
			wg.Wait()
			return StockResult{}, ctx.Err()
		}
	}
	// close 由唯一 owner 执行；它不等待购买者完成，只使两个接收分支都能继续。
	close(release)
	wg.Wait()
	return StockResult{1, remaining.Load(), sold.Load()}, nil
}

// Inventory 把关联业务字段与唯一一把锁放在同一个对象中。
// 字段不导出，调用者不能绕过方法直接改；对象创建后只传 *Inventory，禁止按值复制。
type Inventory struct {
	mu        sync.Mutex
	initial   int64
	remaining int64
	sold      int64
}

// NewInventory 由入口/测试创建本轮库存对象；返回指针，共享的是整件对象而不是值副本。
func NewInventory(initial int64) *Inventory {
	if initial < 0 {
		panic("教学输入错误：初始库存不能为负")
	}
	return &Inventory{initial: initial, remaining: initial}
}

// Buy 由每个购买者调用。检查、扣减、增加已卖数必须属于同一个临界区。
// 指针接收者 i 指向所有购买者共同的 Inventory，也共同使用 i.mu。
func (i *Inventory) Buy() bool {
	i.mu.Lock()         // 等待的是这件库存对象的同一把锁；别的购买者在这里排队。
	defer i.mu.Unlock() // Lock 成功后登记；包括售罄早返回在内，都先解锁再回到调用者。
	if i.remaining == 0 {
		return false
	}
	i.remaining--
	i.sold++
	return true
}

// Result 由 owner 或观察者调用，用同一把锁一次读取相互关联的两个字段。
// 如果只分别锁住每个 getter，中间可能有购买者修改，就不一定获得同一时刻的业务状态。
func (i *Inventory) Result() StockResult {
	i.mu.Lock()
	defer i.mu.Unlock()
	return StockResult{i.initial, i.remaining, i.sold}
}

// CASInventory 只保存一项原子业务状态：剩余库存。
// typed atomic 的零值可用，但首次使用后不能复制；并发接口也全部使用指针接收者。
type CASInventory struct {
	remaining atomic.Int64
}

// NewCASInventory 由 owner 建立独立库存；Store 发生在把指针交给购买者之前。
func NewCASInventory(initial int64) *CASInventory {
	if initial < 0 {
		panic("教学输入错误：初始库存不能为负")
	}
	i := &CASInventory{}
	i.remaining.Store(initial)
	return i
}

// Buy 把“仍是刚才看见的库存，才允许扣减”压成一次 CAS 状态转移。
// ctx 用来给重试上限；CAS 失败代表值已变，不代表网络或业务失败，应重新读再判断。
func (i *CASInventory) Buy(ctx context.Context) (bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		old := i.remaining.Load()
		if old == 0 {
			return false, nil
		}
		if i.remaining.CompareAndSwap(old, old-1) {
			// CAS 成功是本次售出的线性化点。这里无需再 Store，否则会覆盖别人后续的扣减。
			return true, nil
		}
	}
}

// Remaining 读取单个字段。它不是“库存+订单表”的跨对象事务。
func (i *CASInventory) Remaining() int64 { return i.remaining.Load() }

// SellWithMutex 由 demo/test 调用，owner 创建购买者并等待所有任务退出。
// 各购买者只通过 stock.Buy 修改共享库存；Wait 后 owner 再读取完整结果。
func SellWithMutex(initial int64, buyers int) StockResult {
	stock := NewInventory(initial)
	var wg sync.WaitGroup
	for range buyers {
		wg.Go(func() { stock.Buy() })
	}
	wg.Wait()
	return stock.Result()
}

// SellWithCAS 收集每个购买者的返回值，独立计算订单数，而不是从库存反推“卖了多少”。
// 若实现错误地把一次库存扣减报告成两次成功，这个统计与库存守恒断言会发现问题。
func SellWithCAS(ctx context.Context, initial int64, buyers int) (StockResult, error) {
	stock := NewCASInventory(initial)
	accepted := make([]bool, buyers)
	errs := make([]error, buyers)
	var wg sync.WaitGroup
	for id := range buyers {
		// Go 1.22+ 的循环变量逐次独立；仍显式把 id 用作该购买者唯一的结果槽位。
		wg.Go(func() { accepted[id], errs[id] = stock.Buy(ctx) })
	}
	wg.Wait()
	var sold int64
	for id, ok := range accepted {
		if errs[id] != nil {
			return StockResult{}, fmt.Errorf("购买者 %d: %w", id, errs[id])
		}
		if ok {
			sold++
		}
	}
	return StockResult{initial, stock.Remaining(), sold}, nil
}
