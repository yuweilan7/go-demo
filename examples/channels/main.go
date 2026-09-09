// channels 用可观察的输出演示 channel 的阻塞、关闭、方向限制和同步语义。
package main

import (
	"fmt"
	"sync"
)

func main() {
	demonstrateDeclarations()
	demonstrateUnbufferedChannel()
	demonstrateBufferedChannel()
	demonstrateClosedChannel()
	demonstrateRangeAndDirection()
	demonstrateParallelSum()
	demonstrateSemaphore()
}

func demonstrateDeclarations() {
	fmt.Println("=== 1. 声明、初始化与 nil channel ===")

	// 只声明而没有 make：channel 的零值是 nil。
	// 对 nil channel 直接发送或接收会永久阻塞；close(nilChannel) 会 panic。
	var nilChannel chan int
	fmt.Printf("nil channel: value=%v, len=%d, cap=%d\n", nilChannel, len(nilChannel), cap(nilChannel))

	// 原材料中的“var ch [size]chan int”不是容量声明，而是 channel 数组。
	// 下面得到 3 个彼此独立的 channel 变量，并且每个元素初始都是 nil。
	var channelArray [3]chan int
	fmt.Printf("[3]chan int: 数组长度=%d，第一个元素是否为 nil=%t\n", len(channelArray), channelArray[0] == nil)

	// 容量只能在 make 时指定。make(chan int) 等价于 make(chan int, 0)。
	buffered := make(chan int, 3)
	fmt.Printf("make(chan int, 3): len=%d, cap=%d\n", len(buffered), cap(buffered))

	// 用带 default 的 select 可以“探测”当前操作是否会立即完成，而不会真的卡住。
	select {
	case nilChannel <- 1:
		fmt.Println("不可能到达：向 nil channel 发送成功")
	default:
		fmt.Println("向 nil channel 发送当前无法进行；直接发送会永久阻塞")
	}
}

func demonstrateUnbufferedChannel() {
	fmt.Println("\n=== 2. 无缓冲 channel：发送和接收会合 ===")

	ch := make(chan string)
	sendStarted := make(chan struct{})
	sendReturned := make(chan struct{})

	go func() {
		close(sendStarted)
		ch <- "hand-off" // 没有接收者时，这条发送语句不可能完成。
		close(sendReturned)
	}()

	<-sendStarted
	select {
	case <-sendReturned:
		panic("无缓冲 channel 的发送不应在接收前完成")
	default:
		fmt.Println("主 goroutine 接收前：发送方尚不能完成发送")
	}

	message := <-ch
	<-sendReturned
	fmt.Printf("主 goroutine 接收到 %q 后：发送方才可继续\n", message)
}

func demonstrateBufferedChannel() {
	fmt.Println("\n=== 3. 有缓冲 channel：未满时发送可立即完成，满后阻塞 ===")

	ch := make(chan int, 2)
	ch <- 10
	ch <- 20
	fmt.Printf("连续发送 10、20，无接收者也已完成：len=%d, cap=%d\n", len(ch), cap(ch))

	thirdSendStarted := make(chan struct{})
	thirdSendReturned := make(chan struct{})
	go func() {
		close(thirdSendStarted)
		ch <- 30 // 缓冲区已满，要等接收方腾出一个位置。
		close(thirdSendReturned)
	}()

	<-thirdSendStarted
	select {
	case <-thirdSendReturned:
		panic("满缓冲 channel 的发送不应在出现空位前完成")
	default:
		fmt.Println("缓冲区已满：发送 30 的 goroutine 还不能继续")
	}

	first := <-ch
	<-thirdSendReturned
	second, third := <-ch, <-ch
	fmt.Printf("取出 %d 后出现空位；随后依次取出 %d、%d\n", first, second, third)
}

func demonstrateClosedChannel() {
	fmt.Println("\n=== 4. close、合法零值与 comma-ok ===")

	ch := make(chan int, 2)
	ch <- 0 // 这是发送方真的发送的合法零值。
	ch <- 7
	close(ch)

	// close 表示“以后不会再发送”，并不会丢掉缓冲区中已有的数据。
	// ok 只有在 channel 已关闭且缓冲区已取空时才是 false。
	for i := 1; i <= 3; i++ {
		value, ok := <-ch
		fmt.Printf("第 %d 次接收: value=%d, ok=%t\n", i, value, ok)
	}

	// 以下错误留在注释里，保证示例本身可以运行：
	// close(ch) // panic: close of closed channel
	// ch <- 8   // panic: send on closed channel
	// 再次执行 <-ch 不会 panic，也不会阻塞，仍会立刻得到 0, false。
}

func demonstrateRangeAndDirection() {
	fmt.Println("\n=== 5. for range 与单向 channel ===")

	ch := make(chan int, 2)
	go produce(ch) // 双向 chan int 可以收窄为函数要求的只发送 chan<- int。

	values := collect(ch) // 同一个双向 channel 也可以收窄为只接收 <-chan int。
	fmt.Println("range 收到全部值并在 channel 关闭、排空后结束:", values)
}

// chan<- int 把函数的能力限制为“只能发送”。这是编译期约束，不是新建了一个 channel。
func produce(out chan<- int) {
	// 通常由发送方关闭 channel，因为发送方知道何时不会再产生数据。
	defer close(out)
	for _, value := range []int{1, 2, 0} {
		out <- value
	}

	// value := <-out // 编译错误：cannot receive from send-only channel
}

// <-chan int 把函数的能力限制为“只能接收”。
func collect(in <-chan int) []int {
	var values []int
	for value := range in {
		values = append(values, value)
	}

	// in <- 1 // 编译错误：cannot send to receive-only channel
	return values
}

type partialResult struct {
	part string
	sum  int
}

func demonstrateParallelSum() {
	fmt.Println("\n=== 6. goroutine 通过 channel 交付结果 ===")

	numbers := []int{7, 2, 8, -9, 4, 0}
	results := make(chan partialResult, 2)
	middle := len(numbers) / 2

	// Go 不建立“父 goroutine / 子 goroutine”的语言级生命周期关系。
	// 这里是 main goroutine 启动两个工作 goroutine，再明确接收两个结果。
	go sum("左半段", numbers[:middle], results)
	go sum("右半段", numbers[middle:], results)

	first, second := <-results, <-results
	fmt.Printf("先完成=%s(%d)，后完成=%s(%d)，总和=%d\n",
		first.part, first.sum, second.part, second.sum, first.sum+second.sum)
	fmt.Println("哪一段先到没有保证；channel 保证的是结果安全交付，不是调度顺序")
}

func sum(part string, numbers []int, out chan<- partialResult) {
	total := 0
	for _, number := range numbers {
		total += number
	}
	out <- partialResult{part: part, sum: total}
}

func demonstrateSemaphore() {
	fmt.Println("\n=== 7. 容量为 1 的 channel 充当信号量 ===")

	guard := make(chan struct{}, 1)
	var waitGroup sync.WaitGroup
	counter := 0

	for i := 0; i < 100; i++ {
		waitGroup.Add(1)
		go increment(guard, &counter, &waitGroup)
	}

	// WaitGroup 是“等待所有任务结束”的协议；Sleep 不是。
	// time.Sleep(2) 只睡 2 纳秒，即使写成 2*time.Second 也只是猜测调度时间。
	waitGroup.Wait()
	fmt.Println("100 个 goroutine 完成后，counter =", counter)
	fmt.Println("只保护共享内存时通常优先 sync.Mutex；channel 更适合传值、交接所有权或限制并发数")
}

func increment(guard chan struct{}, counter *int, waitGroup *sync.WaitGroup) {
	defer waitGroup.Done()

	// 容量为 1：第一个 goroutine 放入令牌后，其他发送者会阻塞。
	// struct{} 不携带数据，也无需分配一个 bool 值；这里关心的只有令牌本身。
	guard <- struct{}{}
	defer func() { <-guard }()

	(*counter)++
}
