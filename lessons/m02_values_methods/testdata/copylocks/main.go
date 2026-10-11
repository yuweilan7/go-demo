// 这个目录是显式故障入口。Go 的 ./... 默认忽略 testdata，因此不会让正常 vet 必然失败。
// 需要单独执行 go run ./testdata/copylocks 和 go vet -copylocks ./testdata/copylocks。
package main

import (
	"fmt"
	"sync"
)

// Counter 是反例，和 internal/lesson 的修复 Counter 使用相同业务含义。
// 同步对象并不会因为嵌入 struct 就获得 Java 对象那种不可随赋值复制的身份。
type Counter struct {
	mu    sync.Mutex
	value int
}

// Add 由本文件 main 调用。错误就在值接收者：每次调用复制 mu 和 value。
// 锁保护的是本次接收者副本；即使按 Lock/Unlock 成对使用，也没有修改 caller 的计数。
// vet 的 copylocks 分析器会报告 passes lock by value；换成 *Counter 并统一读方法可修复。
func (counter Counter) Add(delta int) {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	counter.value += delta
	fmt.Printf("[BAD value-receiver] local=%d local-lock=%p\n", counter.value, &counter.mu)
}

// SharedCounter 的 count 指针在副本之间共享，mu 却是各自的锁值。
// 这更危险：不同锁可以同时允许写同一个 int，本例用串行/非阻塞观察，不制造数据竞争。
type SharedCounter struct {
	mu    sync.Mutex
	count *int
}

// main 是单 goroutine 的故障观察 owner，所有锁均在退出前释放，没有 sleep 或无限等待。
// copiedAfterUse 那一行故意违反 sync 的首次使用后禁止复制契约：结果只记录当前实现观察。
func main() {
	var broken Counter
	broken.Add(1)
	broken.Add(1)
	fmt.Printf("[BAD state] caller=%d expected=2 caller-lock=%p\n", broken.value, &broken.mu)
	if broken.value != 0 {
		panic("实验前提失效：值接收者竟修改了 caller")
	}

	original := SharedCounter{count: new(int)}
	original.mu.Lock()
	original.mu.Unlock()       // original.mu 已经首次使用；解锁不使它重新成为可复制对象。
	copiedAfterUse := original // vet 会报告 assignment copies lock value。
	original.mu.Lock()
	// TryLock 非阻塞，因此即使未来实现表现不同也不会卡住；false 同样是可观察结果。
	copyAcquired := copiedAfterUse.mu.TryLock()
	if copyAcquired {
		*copiedAfterUse.count = 9 // 只有本 goroutine 写入，无竞争；原锁没能约束副本锁。
		copiedAfterUse.mu.Unlock()
	}
	original.mu.Unlock()
	fmt.Printf("[BAD identity] shared-count=%t distinct-lock=%t copy-acquired-while-original-held=%t count=%d\n",
		original.count == copiedAfterUse.count, &original.mu != &copiedAfterUse.mu, copyAcquired, *original.count)
	// 不把 TryLock 的当前结果当语言保证：此处已经违反同步对象的使用契约。
	// 状态断言只证明两个不同的锁存储配上同一 count；安全修复是共享一个 *Counter。
	if original.count != copiedAfterUse.count || &original.mu == &copiedAfterUse.mu {
		panic("未复现共享数据与不同锁的对象关系")
	}
}
