// goroutines 示例展示 Go 并发最重要的几个事实：
//  1. go 语句只负责启动任务，不负责等待任务；
//  2. goroutine 的实际执行顺序没有保证；
//  3. 等待一组任务完成应使用明确的同步手段，而不是猜一个 Sleep 时长；
//  4. goroutine 很轻量，但仍要限制对数据库、网络等有限资源的并发访问。
package main

import (
	"fmt"
	"runtime"
	"sync"
	"time"
)

func main() {
	fmt.Println("=== 1. go 语句启动任务后，调用者会继续执行 ===")
	demoGoStatement()

	fmt.Println("\n=== 2. WaitGroup 等待一组 goroutine 全部完成 ===")
	demoWaitGroup()

	fmt.Println("\n=== 3. 一次创建许多处于等待状态的 goroutine ===")
	demoManyGoroutines()

	fmt.Println("\n=== 4. goroutine 轻量，也仍然需要限制业务并发量 ===")
	demoWorkerPool()
}

func demoGoStatement() {
	workerStarted := make(chan struct{})
	releaseWorker := make(chan struct{})
	workerFinished := make(chan struct{})

	go func() {
		// close 可用作一次性的广播信号。这里通知 main：这个 goroutine 已经开始运行。
		close(workerStarted)

		// 接收操作会一直阻塞到 main 关闭 releaseWorker。
		// 这让示例能够确定地展示“goroutine 已启动，但调用者仍可继续”，而不是依赖
		// 调度器碰巧先运行哪一方。
		<-releaseWorker
		fmt.Println("worker: 收到放行信号，开始完成任务")
		close(workerFinished)
	}()

	// go 不是普通函数调用：main 不会自动等匿名函数执行完再走到下一行。
	// 这里显式接收信号，只是为了让演示输出稳定；它同时也是一次同步。
	<-workerStarted
	fmt.Println("main: worker 已启动但还被阻塞，main 仍可继续")

	close(releaseWorker)
	<-workerFinished
	fmt.Println("main: 已明确确认 worker 完成")
}

func demoWaitGroup() {
	var group sync.WaitGroup

	for _, name := range []string{"goroutine-1", "goroutine-2"} {
		// Go 1.25 起，WaitGroup.Go 会把“计数加一、启动 goroutine、结束时计数减一”
		// 组合起来。它适合当前模块声明的 Go 1.27。
		//
		// 需要兼容旧版 Go 时，可使用经典写法：
		//
		//     group.Add(1) // 必须在启动 goroutine 之前执行
		//     go func() {
		//         defer group.Done()
		//         printTask(name)
		//     }()
		//
		// 不要把 Add 放进新 goroutine；否则 main 可能先执行 Wait，并在计数仍为 0
		// 时直接返回。
		group.Go(func() {
			printTask(name)
		})
	}

	fmt.Println("main: 两个任务已提交，开始等待")
	group.Wait()
	fmt.Println("main: 两个任务都已完成")
}

func printTask(name string) {
	for step := 1; step <= 3; step++ {
		fmt.Printf("%s: 第 %d 步\n", name, step)

		// Sleep 在这里仅仅模拟任务耗时，让两份输出更容易交错。
		// 它不是等待 goroutine 完成的同步方案；真正负责等待的是 WaitGroup。
		time.Sleep(5 * time.Millisecond)
	}
}

func demoManyGoroutines() {
	const count = 1000

	before := runtime.NumGoroutine()
	releaseAll := make(chan struct{})

	var ready sync.WaitGroup
	var finished sync.WaitGroup
	ready.Add(count)
	finished.Add(count)

	for i := 0; i < count; i++ {
		go func() {
			// 先报告“我已经存在”，随后都停在同一个接收操作上。
			ready.Done()
			<-releaseAll
			finished.Done()
		}()
	}

	ready.Wait()
	during := runtime.NumGoroutine()
	fmt.Printf("一次启动 %d 个；NumGoroutine 的增量：%d\n", count, during-before)

	// goroutine 的初始栈通常很小并可增长，这是大量创建成为可能的重要原因。
	// 但具体初始字节数、栈上限和调度策略都属于运行时实现细节，不是语言规范承诺。
	close(releaseAll)
	finished.Wait()
	fmt.Println("关闭广播通道后，1000 个 goroutine 均已退出")
}

func demoWorkerPool() {
	const workerCount = 3
	const jobCount = 7

	jobs := make(chan int)
	var workers sync.WaitGroup

	for workerID := 1; workerID <= workerCount; workerID++ {
		id := workerID
		workers.Go(func() {
			// range 会持续接收任务；jobs 关闭且已有任务全部取完后，循环结束。
			for jobID := range jobs {
				fmt.Printf("worker-%d 处理 job-%d\n", id, jobID)
				time.Sleep(4 * time.Millisecond) // 仅模拟一次外部调用的耗时。
			}
		})
	}

	for jobID := 1; jobID <= jobCount; jobID++ {
		jobs <- jobID
	}
	close(jobs)
	workers.Wait()

	fmt.Printf("%d 个 worker 处理了 %d 个任务；同时处理任务的 goroutine 最多为 %d 个\n",
		workerCount, jobCount, workerCount)

	// 这里的 worker pool 不是为了复刻操作系统线程池，而是为业务资源提供背压：
	// 即使启动 goroutine 的成本较低，也不能无上限地同时请求数据库、第三方 API
	// 或打开文件。实际项目也常用带容量的 channel（信号量）实现同类限制。
}
