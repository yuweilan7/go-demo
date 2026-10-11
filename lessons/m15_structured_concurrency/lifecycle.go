// Package lesson 把一次批量 SKU 读取的任务、预算、结果和收尾放在同一个 owner 范围。
// 本课没有数据库写入；价格是整数分，成功价格不能按完成顺序配回另一个 SKU。
package lesson

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const cleanupBudget = 2 * time.Second

var (
	ErrUnavailable      = errors.New("SKU 暂无价格") // 可允许部分失败的业务事实。
	ErrBackend          = errors.New("价格服务故障")   // 应使整批停止的基础设施错误。
	ErrNotStarted       = errors.New("任务尚未派发")
	ErrClosed           = errors.New("pool 已停止接纳")
	ErrQueueFull        = errors.New("pool 暂无接纳位置")
	ErrResponseTooLarge = errors.New("价格响应超过4096字节")
)

// Row 是一个稳定 SKU 的终态。它只含值字段，传递时不会共享价格对象的可变指针。
// Err 的动态值仍可能是指针错误对象；本课只读取它，不修改第三方错误。
type Row struct {
	ID    string
	Price int64
	Err   error
}
type Fetch func(context.Context, string) (int64, error)

// Gate 是实验 owner 的一次性广播门。指针接收者共享同一个 Once/channel，不能复制。
// Release 只解除等待；它没有“等待任务完成”的含义，join 必须另做。
type Gate struct {
	once   sync.Once
	signal chan struct{}
	C      <-chan struct{}
}

func NewGate() *Gate     { signal := make(chan struct{}); return &Gate{signal: signal, C: signal} }
func (g *Gate) Release() { g.once.Do(func() { close(g.signal) }) }

// Await 由 owner 等待可观察边界；ctx 限制的是这次观察，不会强杀被观察的工作。
// <-chan 只把接收权传入，调用者不能通过这个参数关闭别人拥有的 channel。
func Await(ctx context.Context, done <-chan struct{}, label string) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待 %s: %w", label, ctx.Err())
	}
}

// Cleanup 在业务 ctx 已取消时仍建立独立两秒收尾预算。stop 必须先取消/解除所有 gate，
// joined 必须来自真正 Wait 的完成通知；若收尾失败，errors.Join 同时保留主错误与清理错误。
// 它不能把超时包装成成功。所有本课工作均有可解除的等待，因此正常/提前失败都能加入。
func Cleanup(primary *error, stop func(), joined <-chan struct{}, actualWait func()) {
	stop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := Await(ctx, joined, "独立清理 join"); err != nil {
		*primary = errors.Join(*primary, err)
		return
	}
	actualWait() // 通知前的 Wait 已完成；再加入发布通知的 owner goroutine，不能拿业务结果替代。
}

// Tracker 只统计本课实际进入/离开业务函数的工作，不使用 NumGoroutine 猜泄漏。
// mu 保护关联字段的同一快照；active 与 max 是活跃工作数，units 是加权资源占用。
type Tracker struct {
	mu                                              sync.Mutex
	active, max, units, maxUnits, started, finished int
}
type Counts struct{ Active, Max, Units, MaxUnits, Started, Finished int }

// Enter由正在进入业务函数的worker调用；返回一次性的离开函数，必须恰好调用一次。
// units是本项值副本，t是共享Tracker指针；互斥锁保护所有统计字段关联变化。
func (t *Tracker) Enter(units int) func() {
	t.mu.Lock()
	t.active++
	t.units += units
	t.started++
	if t.active > t.max {
		t.max = t.active
	}
	if t.units > t.maxUnits {
		t.maxUnits = t.units
	}
	t.mu.Unlock()
	// 返回的闭包由当前工作 defer 调用，读取的是同一个 t 指针与本次 units 值副本。
	return func() { t.mu.Lock(); t.active--; t.units -= units; t.finished++; t.mu.Unlock() }
}

// Counts由owner/测试读取同一时刻的统计副本；锁内整体快照不能拆成六次无锁getter。
func (t *Tracker) Counts() Counts {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Counts{t.active, t.max, t.units, t.maxUnits, t.started, t.finished}
}

// validate 把重复 ID、无限输入和非法限额挡在任何 goroutine 启动前。
// append 创建新的底层数组：owner 不再借用 caller 可能复用的输入 slice。
func validate(ids []string, limit int) ([]string, error) {
	if limit < 1 || limit > 8 || len(ids) > 128 {
		return nil, errors.New("实验限额：limit=1..8，SKU<=128")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			return nil, fmt.Errorf("SKU ID 空或重复: %q", id)
		}
		seen[id] = true
	}
	return append([]string(nil), ids...), nil
}

// PanicError 表示任务边界隔离了可恢复的 panic。recover 必须在发生 panic 的 goroutine 内。
// 它不拦截 runtime fatal、os.Exit 或无限阻塞；是否隔离是本课的显式业务策略。
type PanicError struct{ Value any }

func (e *PanicError) Error() string { return fmt.Sprintf("任务 panic: %v", e.Value) }

// invoke由batch/worker同步调用Fetch，在当前goroutine的调用栈中隔离任务panic。
// 具名price/err是返回给上层的实际变量；recover写它们后直接返回，不恢复panic点后的执行。
// Fetch内部已经注册的defer先展开，再运行这里的recover；没有启动另一个goroutine。
func invoke(ctx context.Context, id string, fetch Fetch) (price int64, err error) {
	defer func() {
		if value := recover(); value != nil {
			price = 0
			err = &PanicError{Value: value}
		}
	}()
	return fetch(ctx, id)
}
