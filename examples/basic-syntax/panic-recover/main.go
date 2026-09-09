// panic-recover 演示 panic 的传播、defer 的执行，以及 recover 后程序从哪里继续。
package main

import "fmt"

func main() {
	fmt.Println("=== 1. 当前函数中捕获 panic ===")
	recoverInCurrentFunction()
	fmt.Println("main: recoverInCurrentFunction 已返回，main 继续执行")

	fmt.Println("\n=== 2. panic 沿调用栈传播 ===")
	level1()
	fmt.Println("main: level1 已返回，main 继续执行")

	fmt.Println("\n=== 3. recover 的直接调用规则 ===")
	demonstrateRecoverRules()
	fmt.Println("main: demonstrateRecoverRules 已返回")

	fmt.Println("\n=== 4. recover 不能跨 goroutine ===")
	recoverInsideGoroutine()
	fmt.Println("main: 子 goroutine 已结束，程序正常结束")
}

func recoverInCurrentFunction() {
	// defer 在函数退出前执行。函数因为 panic 开始退出时，这个延迟函数会运行。
	defer func() {
		if recoveredValue := recover(); recoveredValue != nil {
			fmt.Println("defer: 捕获到 panic:", recoveredValue)
		}
	}()

	fmt.Println("函数: panic 之前")
	panic("第一个示例中的故障")

	// recover 不会让控制流回到 panic 的下一行，因此这行永远不会执行。
	// fmt.Println("函数: panic 之后")
}

func level1() {
	fmt.Println("level1: 上半部分")
	level2()
	// level2 的延迟函数恢复了 panic，因此 level2 正常返回到它的调用者。
	// 对 level1 来说，level2() 这次调用已经结束，所以这里可以继续运行。
	fmt.Println("level1: 下半部分")
}

func level2() {
	defer func() {
		if recoveredValue := recover(); recoveredValue != nil {
			fmt.Println("level2 defer: 捕获到 panic:", recoveredValue)
		}
	}()

	fmt.Println("level2: 上半部分")
	level3()

	// level3 的 panic 会先结束 level3，再进入 level2 的 defer。
	// defer 成功 recover 后，level2 直接返回，不会从 level3() 后面恢复执行。
	// fmt.Println("level2: 下半部分")
}

func level3() {
	fmt.Println("level3: 上半部分")
	panic("level3 中的故障")
	// fmt.Println("level3: 下半部分") // 不可达
}

func demonstrateRecoverRules() {
	// 这一层负责最终捕获从 indirectRecover 向外传播的 panic，让示例能继续运行。
	defer func() {
		if recoveredValue := recover(); recoveredValue != nil {
			fmt.Println("外层 defer: 直接调用 recover，捕获到:", recoveredValue)
		}
	}()

	fmt.Println("普通代码中调用 recover，结果为:", recover())
	indirectRecover()

	// indirectRecover 的 panic 最终由本函数的 defer 捕获，所以本函数会直接返回。
	// fmt.Println("demonstrateRecoverRules: panic 之后")
}

func indirectRecover() {
	defer func() {
		// recover 必须由正在展开栈的延迟函数直接调用才有效。
		// 把它藏进普通辅助函数会返回 nil，不能终止 panic。
		fmt.Println("间接调用 recover，结果为:", callRecover())
	}()

	panic("间接 recover 无法捕获我")
}

func callRecover() any {
	return recover()
}

func recoverInsideGoroutine() {
	done := make(chan struct{})

	go func() {
		// defer 按后注册、先执行的顺序运行：先 recover，再关闭 done。
		defer close(done)
		defer func() {
			if recoveredValue := recover(); recoveredValue != nil {
				fmt.Println("子 goroutine: 在自己的 defer 中捕获到:", recoveredValue)
				// 实际服务中通常还要记录 runtime/debug.Stack() 返回的调用栈，
				// 只打印 recoveredValue 往往不足以定位 panic 的具体代码位置。
			}
		}()

		fmt.Println("子 goroutine: panic 之前")
		panic("子 goroutine 中的故障")
	}()

	// main 所在的 goroutine 无法 recover 子 goroutine 的 panic。
	// 需要隔离的 goroutine 应像上面一样，在自身入口设置恢复边界。
	<-done
}
