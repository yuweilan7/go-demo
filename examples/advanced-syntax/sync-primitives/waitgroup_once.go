package main

import (
	"fmt"
	"sync"
)

func demoCompletion() {
	const taskCount = 4

	// channel 也能表达“一个任务已完成”，而且不必像原材料所说的那样把缓冲区
	// 开成任务数量。这里是容量为 0 的无缓冲 channel；每次发送与一次接收会合。
	// 若还要传回计算结果、错误或所有权，channel 通常很自然。
	done := make(chan struct{})
	channelResults := make([]int, taskCount)
	for id := 0; id < taskCount; id++ {
		go func(id int) {
			channelResults[id] = id * id
			// 完成信号放在任务逻辑的最后；main 收到全部信号后再读取结果。
			done <- struct{}{}
		}(id)
	}
	for i := 0; i < taskCount; i++ {
		<-done
	}
	fmt.Printf("channel：cap(done)=%d，结果=%v\n", cap(done), channelResults)

	// WaitGroup 不传递业务数据，它只跟踪“还有多少任务没有返回”。Go 1.25 起，
	// WaitGroup.Go 会把增加计数、启动 goroutine 和任务返回时减计数组合在一起。
	// 本项目声明 Go 1.27，因此优先使用这个不易漏掉 Done 的写法。
	var wg sync.WaitGroup
	waitGroupResults := make([]string, taskCount)
	for id := 0; id < taskCount; id++ {
		id := id
		wg.Go(func() {
			// 每个 goroutine 只写自己的切片元素，没有两个任务访问同一元素。
			waitGroupResults[id] = fmt.Sprintf("task-%d 完成", id)
		})
	}
	wg.Wait()
	fmt.Printf("WaitGroup：Wait 返回后，结果=%v\n", waitGroupResults)

	// 旧代码或需要手动跟踪非 goroutine 任务时，等价模式如下：
	//
	//     wg.Add(1) // 必须在启动 goroutine 之前增加计数
	//     go func() {
	//         defer wg.Done()
	//         work()
	//     }()
	//     wg.Wait()
	//
	// 计数降到负数会 panic；WaitGroup 第一次使用后也不能复制。
	// 传给 WaitGroup.Go 的函数不得 panic；需要恢复时应在函数内部妥善处理。
}

type appConfig struct {
	Environment string
	Workers     int
}

func demoOnce() {
	var once sync.Once
	var config *appConfig
	initializeCalls := 0

	const callers = 8
	results := make([]*appConfig, callers)
	var wg sync.WaitGroup
	for id := 0; id < callers; id++ {
		id := id
		wg.Go(func() {
			once.Do(func() {
				initializeCalls++
				config = &appConfig{Environment: "dev", Workers: 4}
			})
			// Do 返回时，初始化函数的写入对本次调用可见。
			results[id] = config
		})
	}
	wg.Wait()

	allSame := true
	for _, result := range results[1:] {
		if result != results[0] {
			allSame = false
		}
	}
	fmt.Printf("并发调用=%d，初始化执行=%d 次，所有调用者拿到同一指针=%v，配置=%+v\n",
		callers, initializeCalls, allSame, *config)

	// Once 保证的是：对这一个 Once，传入的函数至多执行一次。它并不负责缓存变量的
	// 生命周期，config 能被后续代码使用是因为程序仍然持有它的引用。
	// 与 init 不同：init 在包初始化期间、main 之前执行；Once.Do 可以把工作推迟到
	// 首次真正需要它时。若传入函数发生 panic，Once 仍把该次调用视为已经执行过。
}
