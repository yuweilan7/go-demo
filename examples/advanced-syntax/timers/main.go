// timers 用短时间、可观察的实验演示 time.Timer 与 time.Ticker。
//
// 本示例所在模块声明 go 1.27，因此采用 Go 1.23 之后的 timer channel 语义：
// channel 是同步（无缓冲）的；Stop/Reset 返回后不会再收到旧配置产生的过期值。
package main

import (
	"fmt"
	"time"
)

func main() {
	demonstrateOneShotTimer()
	demonstrateStop()
	demonstrateReset()
	demonstrateAfterTimeout()
	demonstrateAfterFunc()
	demonstrateTicker()
	demonstrateSlowTickerReceiver()
	demonstrateWatcherCancellation()
}

func demonstrateOneShotTimer() {
	fmt.Println("=== 1. NewTimer：只触发一次 ===")

	const delay = 30 * time.Millisecond
	startedAt := time.Now()
	timer := time.NewTimer(delay)

	// NewTimer 不会创建一个“定时任务线程”。它登记一个一次性计时事件，
	// 到期后让 timer.C 可接收；当前 goroutine 在这里等待该事件。
	firedAt := <-timer.C
	fmt.Printf("设定=%v，实际等待约=%v，收到值类型=%T\n",
		delay, time.Since(startedAt).Round(time.Millisecond), firedAt)
	fmt.Println("接收一次后 timer 已过期；它不会像 Ticker 那样自动再次触发")
}

func demonstrateStop() {
	fmt.Println("\n=== 2. Timer.Stop：取消尚未发生的事件 ===")

	timer := time.NewTimer(100 * time.Millisecond)
	firstStop := timer.Stop()
	secondStop := timer.Stop()
	fmt.Printf("第一次 Stop=%t，第二次 Stop=%t\n", firstStop, secondStop)

	// true 表示这次调用确实把仍处于活动状态的 timer 停掉了。
	// false 表示调用时 timer 已过期或已经停止，不等于“Stop 执行失败”。
	// Stop 不会关闭 C；如果直接写 <-timer.C，这里会永久等待。
	select {
	case <-timer.C:
		panic("Go 1.23+ 中，Stop 返回后不应收到该 timer 的旧值")
	case <-time.After(15 * time.Millisecond):
		fmt.Println("Stop 后 C 没有值且没有被关闭；接收仍会阻塞")
	}
}

func demonstrateReset() {
	fmt.Println("\n=== 3. Timer.Reset：复用 timer 并重新计时 ===")

	timer := time.NewTimer(100 * time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	resetAt := time.Now()
	wasActive := timer.Reset(25 * time.Millisecond)
	<-timer.C
	fmt.Printf("活动中 Reset 返回=%t；新期限从 Reset 调用时重新计算，约 %v 后触发\n",
		wasActive, time.Since(resetAt).Round(time.Millisecond))

	// 上一次触发值已经接收，timer 此刻不再活动。
	// Reset 返回 false 描述的是“重置前的状态”，新的 15ms 计时仍然成功建立。
	wasActive = timer.Reset(15 * time.Millisecond)
	<-timer.C
	fmt.Printf("过期后 Reset 返回=%t，但 timer 已再次触发\n", wasActive)
}

func demonstrateAfterTimeout() {
	fmt.Println("\n=== 4. time.After + select：为等待设置超时 ===")

	// 容量为 1，保证主 goroutine 超时离开 select 后，工作 goroutine 仍能交付结果并退出。
	// 如果这里使用无缓冲 channel 且没人再接收，工作 goroutine 会卡在发送处。
	result := make(chan string, 1)
	go func() {
		time.Sleep(45 * time.Millisecond)
		result <- "任务结果"
	}()

	select {
	case value := <-result:
		fmt.Println("按时完成:", value)
	case <-time.After(15 * time.Millisecond):
		fmt.Println("等待超时")
	}

	// time.After 只让“等待者”结束等待，并不会取消上面的工作。
	// 生产代码若还要取消工作本身，通常要额外传递 context.Context 或取消信号。
	fmt.Println("超时后工作仍会完成:", <-result)
}

func demonstrateAfterFunc() {
	fmt.Println("\n=== 5. time.AfterFunc：到期后在独立 goroutine 调用函数 ===")

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})

	timer := time.AfterFunc(10*time.Millisecond, func() {
		close(started)
		<-release // 人为暂停回调，让主 goroutine 能观察 Stop 的语义。
		close(finished)
	})
	fmt.Printf("AfterFunc 返回的 Timer.C 是否为 nil: %t\n", timer.C == nil)

	<-started
	stopped := timer.Stop()
	fmt.Printf("回调已经开始后 Stop 返回=%t\n", stopped)

	// Stop 返回 false 时，回调已经由自己的 goroutine 启动。
	// Stop 不会等待该 goroutine 执行结束，需要调用方自己同步。
	select {
	case <-finished:
		panic("回调仍应被 release 阻塞")
	default:
		fmt.Println("Stop 已返回，但回调尚未结束")
	}

	close(release)
	<-finished
	fmt.Println("通过 channel 明确等待回调结束")
}

func demonstrateTicker() {
	fmt.Println("\n=== 6. NewTicker：周期性产生 tick ===")

	const period = 15 * time.Millisecond
	ticker := time.NewTicker(period)
	startedAt := time.Now()

	for sequence := 1; sequence <= 3; sequence++ {
		tickAt := <-ticker.C
		fmt.Printf("tick #%d：计划时间距开始约 %v\n",
			sequence, tickAt.Sub(startedAt).Round(time.Millisecond))
	}
	ticker.Stop()

	// Ticker.Stop 没有返回值，也不会关闭 C。若它关闭 C，接收方会不断读到 time.Time
	// 的零值，反而容易把“channel 已关闭”误判为一次真正的 tick。
	select {
	case <-ticker.C:
		panic("Go 1.23+ 中，Ticker.Stop 返回后不应收到旧 tick")
	case <-time.After(20 * time.Millisecond):
		fmt.Println("Ticker 已停止；C 保持打开但不再产生 tick")
	}
}

func demonstrateSlowTickerReceiver() {
	fmt.Println("\n=== 7. 慢消费者：Ticker 会丢弃 tick，不会无限排队 ===")

	const period = 10 * time.Millisecond
	ticker := time.NewTicker(period)
	time.Sleep(55 * time.Millisecond) // 故意错过大约 5 个周期。

	readyTicks := receiveReadyTicks(ticker.C)
	ticker.Stop()
	fmt.Printf("暂停约 55ms（周期 %v）后，立即可接收的 tick 数=%d\n", period, readyTicks)
	fmt.Println("Ticker 表示周期信号，不是每次执行都必须保留的任务队列；接收太慢时会调整间隔或丢弃 tick")
}

func receiveReadyTicks(ticks <-chan time.Time) int {
	count := 0
	for {
		select {
		case <-ticks:
			count++
		default:
			return count
		}
	}
}

func demonstrateWatcherCancellation() {
	fmt.Println("\n=== 8. 可停止的周期任务：close(done) 发出取消信号 ===")

	done := make(chan struct{})
	events := make(chan int)
	stopped := make(chan struct{})

	go watchEvery(12*time.Millisecond, done, events, stopped)
	for range 3 {
		fmt.Printf("观察到 watcher 第 %d 次执行\n", <-events)
	}

	// 只关闭一次即可广播取消；不需要先发送 struct{} 再 close。
	close(done)
	<-stopped
	fmt.Println("watcher 已停止并完成资源清理")
}

func watchEvery(period time.Duration, done <-chan struct{}, events chan<- int, stopped chan<- struct{}) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	defer close(stopped)

	count := 0
	for {
		select {
		case <-ticker.C:
			count++
			// 交付观察结果时也监听 done，避免调用方取消后 goroutine 卡在发送处。
			select {
			case events <- count:
			case <-done:
				return
			}
		case <-done:
			return
		}
	}
}
