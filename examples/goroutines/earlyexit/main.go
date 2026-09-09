// earlyexit 用确定的方式演示：main 返回时，进程直接结束，Go 不会自动等待
// 其他 goroutine。运行它时，只会看到 main 的一行输出。
package main

import "fmt"

func main() {
	workerStarted := make(chan struct{})
	neverReleased := make(chan struct{})

	go func() {
		close(workerStarted)

		// main 永远不会向这个通道发送值，也不会关闭它，所以 worker 会一直等待。
		<-neverReleased
		fmt.Println("worker: 这一行不会执行")
	}()

	// 先确认 worker 确实已经启动，排除“可能还没得到调度”这个偶然因素。
	<-workerStarted
	fmt.Println("main: 现在返回；尚未完成的 worker 会随进程一起结束")

	// main 返回不会触发“等待所有 goroutine”的隐式流程。若任务必须完成，应该使用
	// WaitGroup、channel 或更高层的生命周期管理，而不是随意 Sleep 一段时间。
}
