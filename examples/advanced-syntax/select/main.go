// select 示例演示：一个 goroutine 如何等待多个 channel 通信事件。
//
// 注意：Go 的 select 和操作系统的 select/poll/epoll 只是“多路等待”的思想相似。
// Go select 的操作对象是 channel；网络 fd 的高效等待由 Go runtime 的网络轮询器负责。
package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	// 空 select 的确会让当前 goroutine 永久阻塞。为了不破坏默认演示，
	// 只有显式传入 deadlock 参数时才运行它：
	//
	//     go run ./examples/advanced-syntax/select deadlock
	//
	// 当程序里所有 goroutine 都无法继续时，Go runtime 才会报告 deadlock；
	// 不是 select {} 语句自己主动抛出了异常。
	if len(os.Args) > 1 && os.Args[1] == "deadlock" {
		fmt.Println("即将进入空 select；这是故意触发的死锁演示")
		select {}
	}

	demonstrateBlockingSelect()
	demonstrateDefault()
	demonstrateMultipleReadyCases()
	demonstrateNilAndClosedChannels()
	demonstrateTimeout()
}

func demonstrateBlockingSelect() {
	fmt.Println("=== 1. 没有 default：等待任意一个 case 就绪 ===")

	ch1 := make(chan string)
	ch2 := make(chan string)
	selectStarted := make(chan struct{})
	selected := make(chan string)

	go func() {
		// select 不会再创建一个 goroutine。是这个 goroutine 自己同时挂在两个
		// channel 的等待队列上，直到其中一项通信能够进行。
		close(selectStarted)
		select {
		case value := <-ch1:
			selected <- "ch1: " + value
		case value := <-ch2:
			selected <- "ch2: " + value
		}
	}()

	<-selectStarted
	select {
	case result := <-selected:
		panic("两个输入 channel 都没有数据时，不应选中 case: " + result)
	default:
		fmt.Println("两个输入都未就绪，执行 select 的 goroutine 正在等待")
	}

	// 这次发送会与上面 select 中的 ch2 接收完成通信。
	ch2 <- "消息到达"
	fmt.Println("使 ch2 就绪后，select 返回 ->", <-selected)
}

func demonstrateDefault() {
	fmt.Println("\n=== 2. 有 default：只尝试一次，不等待 ===")

	in := make(chan int, 1)
	select {
	case value := <-in:
		fmt.Println("收到:", value)
	default:
		fmt.Println("in 暂时不可读，立即执行 default")
	}

	in <- 42
	select {
	case value := <-in:
		fmt.Println("放入数据后，接收 case 立即执行，value =", value)
	default:
		panic("channel 中已有数据，不应执行 default")
	}

	out := make(chan int, 1)
	out <- 1 // 缓冲区已满，暂时不能再发送。
	select {
	case out <- 2:
		panic("满缓冲 channel 的发送不应成功")
	default:
		fmt.Println("out 暂时不可写，发送 case 不会阻塞，而是执行 default")
	}
}

func demonstrateMultipleReadyCases() {
	fmt.Println("\n=== 3. 多个 case 同时就绪：伪随机选择一个 ===")

	ch1 := make(chan int, 1)
	ch2 := make(chan int, 1)
	ch1 <- 1
	ch2 <- 2

	const rounds = 10_000
	var selectedCh1, selectedCh2 int
	for range rounds {
		// 每一轮开始时两个 channel 中都有一个值，因此两个接收 case 都就绪。
		// 取出后马上放回，维持下一轮仍是“双就绪”状态。
		select {
		case value := <-ch1:
			selectedCh1++
			ch1 <- value
		case value := <-ch2:
			selectedCh2++
			ch2 <- value
		}
	}

	fmt.Printf("%d 轮选择结果：ch1=%d, ch2=%d\n", rounds, selectedCh1, selectedCh2)
	fmt.Println("结果通常接近各半，但 select 不保证轮流执行，也不承诺长期公平性")
}

func demonstrateNilAndClosedChannels() {
	fmt.Println("\n=== 4. nil channel 会禁用 case；关闭的 channel 永远可读 ===")

	left := make(chan string, 2)
	right := make(chan string, 2)
	left <- "L1"
	left <- "L2"
	right <- "R1"
	close(left)
	close(right)

	// 从已关闭且已取空的 channel 接收会立即返回元素类型零值和 ok=false。
	// 如果在循环 select 中保持这个 case，它会一直处于就绪状态，可能形成忙循环。
	// 把 channel 变量赋为 nil 后，对应 case 永远不就绪，相当于动态关闭该分支。
	for left != nil || right != nil {
		select {
		case value, ok := <-left:
			if !ok {
				fmt.Println("left 已关闭并取空：将 left 设为 nil，禁用此 case")
				left = nil
				continue
			}
			fmt.Println("从 left 收到", value)

		case value, ok := <-right:
			if !ok {
				fmt.Println("right 已关闭并取空：将 right 设为 nil，禁用此 case")
				right = nil
				continue
			}
			fmt.Println("从 right 收到", value)
		}
	}
}

func demonstrateTimeout() {
	fmt.Println("\n=== 5. select 实现超时等待 ===")

	work := make(chan string) // 本例没有发送者，work case 不会就绪。
	timer := time.NewTimer(20 * time.Millisecond)
	defer timer.Stop()

	select {
	case result := <-work:
		fmt.Println("任务完成:", result)
	case <-timer.C:
		fmt.Println("等待 20ms 后仍无结果，执行超时分支")
	}
}
