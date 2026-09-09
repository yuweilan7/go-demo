// sync-primitives 用可运行的现象演示 Go 中常用的并发同步工具。
//
// 最重要的心智模型是：
//   - channel 传递数据和所有权，也可以顺便传递“完成”信号；
//   - WaitGroup 只等待一组任务结束；
//   - Mutex/RWMutex 保护共享状态及其不变量；
//   - atomic 适合一个简单状态，或一次替换一份不可变快照；
//   - sync.Map 和 sync.Pool 是针对特定工作负载设计的工具，不是默认选择。
package main

import "fmt"

func main() {
	section("1. channel 与 WaitGroup：等待任务完成")
	demoCompletion()

	section("2. sync.Once：第一次需要时只初始化一次")
	demoOnce()

	section("3. sync.Mutex：保护共享状态")
	demoMutex()

	section("4. sync.RWMutex：读可并发，写独占")
	demoRWMutex()

	section("5. sync.Map：为特定并发访问模式优化的 map")
	demoSyncMap()

	section("6. sync/atomic：原子计数与不可变快照")
	demoAtomic()

	section("7. sync.Pool：临时对象的机会性复用")
	demoPool()
}

func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}
