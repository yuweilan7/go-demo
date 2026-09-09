package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// safeCounter 把锁和它保护的状态放在一起。方法使用指针接收者，避免复制 Mutex。
// 含有 Mutex/RWMutex 的值第一次使用后不能复制；复制会连锁状态一起复制，可能导致
// 永久阻塞。原材料中的“复制已加锁 Mutex”示例属于这个问题，并非普通的漏 Unlock。
type safeCounter struct {
	mu    sync.Mutex
	value int
}

func (counter *safeCounter) increment() {
	counter.mu.Lock()
	defer counter.mu.Unlock()

	// value++ 是“读、加一、写回”的复合操作，本身不是原子的。Mutex 把整个临界区
	// 串行化。如果还要同时维护多个字段，应在同一把锁下维护它们的不变量。
	counter.value++
}

func (counter *safeCounter) load() int {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	return counter.value
}

func demoMutex() {
	const increments = 1000
	var counter safeCounter
	var wg sync.WaitGroup

	for i := 0; i < increments; i++ {
		wg.Go(counter.increment)
	}
	wg.Wait()

	fmt.Printf("加锁累加：期望=%d，实际=%d\n", increments, counter.load())

	// 故意不加锁执行并发 value++ 会产生 data race，错误结果虽然常见，却不保证
	// 每次都出现。因此可运行示例不靠“碰巧算错”证明竞态，而会用 -race 验证本目录。
	// 解锁未锁定的 Mutex 会触发运行时错误；漏掉 Unlock 则常令其他 goroutine 永久阻塞。
	// 多把锁必须采用统一获取顺序，避免 A 等 B、B 又等 A 的循环等待。
}

func demoRWMutex() {
	const readerCount = 3

	var rw sync.RWMutex
	var value int
	var activeReaders atomic.Int64
	var maxReaders atomic.Int64

	readersReady := make(chan struct{}, readerCount)
	releaseReaders := make(chan struct{})
	writerAttempted := make(chan struct{})
	writerFinished := make(chan struct{})

	var wg sync.WaitGroup
	for i := 0; i < readerCount; i++ {
		wg.Go(func() {
			rw.RLock()
			current := activeReaders.Add(1)
			updateMaximum(&maxReaders, current)
			readersReady <- struct{}{}

			// 所有 reader 都持有读锁，直到 main 关闭 releaseReaders。
			<-releaseReaders
			activeReaders.Add(-1)
			rw.RUnlock()
		})
	}

	// 确认三个 reader 都已经取得读锁，再启动 writer。
	for i := 0; i < readerCount; i++ {
		<-readersReady
	}
	wg.Go(func() {
		close(writerAttempted)
		rw.Lock()
		value++
		rw.Unlock()
		close(writerFinished)
	})
	<-writerAttempted

	// reader 尚未释放 RLock，因此 writer 在语言库语义上不可能已完成写入。
	writerCompletedWhileReading := false
	select {
	case <-writerFinished:
		writerCompletedWhileReading = true
	default:
	}

	close(releaseReaders)
	wg.Wait()
	fmt.Printf("同时持有读锁的最大数量=%d；读锁持有期间 writer 完成=%v；最终值=%d\n",
		maxReaders.Load(), writerCompletedWhileReading, value)

	// RWMutex 允许多个 reader，或一个 writer；读写不能同时持锁，两个 writer 也不能。
	// 它不支持从 RLock 升级成 Lock，也不支持从 Lock 降级成 RLock。
	// “读多写少”只是候选场景，不代表 RWMutex 必然比 Mutex 快；应以基准测试为准。
}

func updateMaximum(maximum *atomic.Int64, candidate int64) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}
