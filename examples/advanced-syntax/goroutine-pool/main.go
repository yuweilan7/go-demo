// goroutine-pool 用可观察的输出演示固定 worker、有限队列、背压和可靠退出。
package main

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

func main() {
	fmt.Println("=== 固定大小的 goroutine pool ===")
	demonstrateConcurrencyLimit()
	demonstrateBackpressure()
	demonstrateErrorsAndShutdown()
}

func demonstrateConcurrencyLimit() {
	fmt.Println("\n--- 1. capacity 限制同时执行的任务数 ---")

	const (
		capacity  = 3
		taskCount = 8
	)
	pool := mustNewPool(capacity, taskCount)

	var active atomic.Int64
	var maxActive atomic.Int64
	started := make(chan struct{}, taskCount)
	release := make(chan struct{})
	tasks := make([]*Task, 0, taskCount)

	for i := 1; i <= taskCount; i++ {
		taskNumber := i
		task := NewTask(fmt.Sprintf("task-%d", taskNumber), func() error {
			current := active.Add(1)
			updateMaximum(&maxActive, current)
			defer active.Add(-1)

			started <- struct{}{}
			<-release // 先不让任务结束，以便稳定观察到 3 个 worker 同时忙碌。
			return nil
		})

		mustSubmit(pool, task)
		tasks = append(tasks, task)
	}

	// 前 3 个任务占满全部 worker；其余任务只能留在 jobs channel 中等待。
	for i := 0; i < capacity; i++ {
		<-started
	}
	fmt.Printf("已提交 %d 个任务，worker=%d；释放前同时执行=%d，其余任务在队列中等待\n",
		taskCount, pool.Capacity(), active.Load())

	close(release)
	pool.CloseAndWait()
	for _, task := range tasks {
		if err := task.Wait(); err != nil {
			panic(err)
		}
	}
	fmt.Printf("全部完成；观测到的最大并发数=%d（不会超过 capacity=%d）\n",
		maxActive.Load(), pool.Capacity())
}

func demonstrateBackpressure() {
	fmt.Println("\n--- 2. worker 忙且队列满时，Submit 产生背压 ---")

	pool := mustNewPool(2, 1)
	releaseWorkers := make(chan struct{})
	started := make(chan struct{}, 2)

	// 两个任务分别占住两个 worker。
	for i := 1; i <= 2; i++ {
		taskNumber := i
		mustSubmit(pool, NewTask(fmt.Sprintf("blocking-%d", taskNumber), func() error {
			started <- struct{}{}
			<-releaseWorkers
			return nil
		}))
	}
	<-started
	<-started

	// queueSize=1，因此第三个任务可以进入唯一的缓冲位，Submit 仍会立即返回。
	queued := NewTask("queued", func() error { return nil })
	mustSubmit(pool, queued)

	// 第四个任务既没有空闲 worker 可接手，也没有队列位置可用，所以提交方会阻塞。
	fourth := NewTask("backpressured", func() error { return nil })
	submitStarted := make(chan struct{})
	submitReturned := make(chan error, 1)
	go func() {
		close(submitStarted)
		submitReturned <- pool.Submit(fourth)
	}()
	<-submitStarted

	// 这里的短计时器只用于“观察当前仍阻塞”，不用于等待任务生命周期结束。
	select {
	case err := <-submitReturned:
		panic(fmt.Sprintf("满队列上的 Submit 不应提前返回: %v", err))
	case <-time.After(100 * time.Millisecond):
		fmt.Println("100ms 后第四次 Submit 仍未返回：背压已出现")
	}

	close(releaseWorkers)
	if err := <-submitReturned; err != nil {
		panic(err)
	}
	pool.CloseAndWait()

	if err := fourth.Wait(); err != nil {
		panic(err)
	}
	fmt.Println("worker 完成旧任务并取走队列任务后，第四次 Submit 才返回")
}

func demonstrateErrorsAndShutdown() {
	fmt.Println("\n--- 3. 错误、panic 与关闭协议 ---")

	pool := mustNewPool(1, 3)
	tasks := []*Task{
		NewTask("success", func() error { return nil }),
		NewTask("failure", func() error { return errors.New("模拟业务错误") }),
		NewTask("panic", func() error { panic("模拟意外") }),
	}

	for _, task := range tasks {
		mustSubmit(pool, task)
	}
	pool.CloseAndWait() // 取代 time.Sleep：返回时所有已接收任务都已完成。

	for _, task := range tasks {
		if err := task.Wait(); err != nil {
			fmt.Printf("%-7s -> error: %v\n", task.Name(), err)
			continue
		}
		fmt.Printf("%-7s -> success\n", task.Name())
	}

	err := pool.Submit(NewTask("too-late", func() error { return nil }))
	fmt.Printf("关闭后拒绝新任务: errors.Is(err, ErrPoolClosed)=%t\n", errors.Is(err, ErrPoolClosed))
}

func updateMaximum(maximum *atomic.Int64, candidate int64) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}

func mustNewPool(capacity, queueSize int) *Pool {
	pool, err := NewPool(capacity, queueSize)
	if err != nil {
		panic(err)
	}
	return pool
}

func mustSubmit(pool *Pool, task *Task) {
	if err := pool.Submit(task); err != nil {
		panic(err)
	}
}
