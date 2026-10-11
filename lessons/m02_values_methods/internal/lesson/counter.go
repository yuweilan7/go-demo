package lesson

import "sync"

// Counter 是 E05 的修复对象：锁与被保护的计数保存在同一个不复制的实例里。
// 零值可用，所有读写方法都用 *Counter；没有返回 Counter 值或接收 Counter 值的 API。
// 它首次使用后不能复制；即使方法改对了，caller 写 copied := *counter 仍会破坏身份约束。
type Counter struct {
	mu    sync.Mutex
	value int
}

// Add 由实验入口或测试 worker 同步调用。参数 delta 是本次计入的订单数量。
// 接收者只复制 Counter 地址，所有 caller 竞争的是同一个 mu，修改的也是同一个 value。
// 契约要求接收者非 nil；方法不拥有 goroutine，caller 负责启动与等待 worker。
func (counter *Counter) Add(delta int) {
	counter.mu.Lock() // 其他 caller 已持有此 mu 时在此等待；这里没有复制锁，也没有换一把锁。
	// Lock 成功后立刻注册释放本次持有的锁。defer 的 receiver 在注册时求值。
	// 即使后面增加提前 return，函数退出仍会 Unlock；本函数内部不递归加同一把锁。
	defer counter.mu.Unlock()
	counter.value += delta
}

// Value 由实验入口和测试在需要快照时调用，仍然使用同一实例的 mu。
// 只读不等于可以使用值接收者：复制 Counter 会同时复制 sync.Mutex。
func (counter *Counter) Value() int {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	return counter.value // 返回的是 int 快照；defer 随后解锁，再把快照交给 caller。
}
