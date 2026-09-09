// closurestate 用一个计数器解释：闭包保存的不是某个数值快照，
// 而是可以继续访问创建它时的局部变量。
package main

import "fmt"

// newCounter 可以想成一个“计数器工厂”。
//
// 每调用一次 newCounter，都会发生两件事：
//  1. 创建一个全新的 count 变量；
//  2. 返回一个能够继续访问这个 count 的函数。
//
// newCounter 返回以后，它的普通调用过程已经结束了。但是，返回的函数仍然需要
// count，所以 count 的生命周期会延长。至于编译器最终把它放在栈上还是堆上，
// 是实现细节；理解闭包时，只要把它想成函数随身带着一个“装 count 的小盒子”。
func newCounter() func() int {
	count := 0

	return func() int {
		count++
		return count
	}
}

// newCounterPair 这一次只创建一个 count，却返回两个函数。
// 两个函数都引用这一次调用里的同一个 count，所以它们共享状态：
// increment 负责修改，current 负责读取。
func newCounterPair() (increment func(), current func() int) {
	count := 0

	increment = func() {
		count++
	}
	current = func() int {
		return count
	}
	return increment, current
}

// Counter 展示生产代码中更直白的另一种写法。
// 当状态和操作逐渐增多时，给状态一个名字并写成结构体，通常比返回多个闭包更容易维护。
// 这个简单版本不是并发安全的；多个 goroutine 同时使用时还要增加同步保护。
type Counter struct {
	count int
}

func (c *Counter) Increment() int {
	c.count++
	return c.count
}

func (c *Counter) Current() int {
	return c.count
}

func main() {
	fmt.Println("=== 1. 一个闭包一直使用同一个 count ===")
	counterA := newCounter() // 创建盒子 A，里面的 count 初始为 0。
	fmt.Printf("counterA 第 1 次调用: %d\n", counterA())
	fmt.Printf("counterA 第 2 次调用: %d\n", counterA())
	fmt.Printf("counterA 第 3 次调用: %d\n", counterA())

	fmt.Println("\n=== 2. 再调用一次工厂，就得到另一个 count ===")
	counterB := newCounter() // 创建盒子 B；它和盒子 A 没有关系。
	fmt.Printf("counterB 第 1 次调用: %d\n", counterB())
	fmt.Printf("counterB 第 2 次调用: %d\n", counterB())
	fmt.Printf("counterA 再调用一次: %d（A 仍保存着自己的状态）\n", counterA())

	fmt.Println("\n=== 3. 同一次调用返回的两个闭包共享一个 count ===")
	increment, current := newCounterPair() // 只创建一个盒子，两把“钥匙”都能访问它。
	fmt.Printf("刚创建时 current(): %d\n", current())
	increment()
	fmt.Printf("increment() 1 次后 current(): %d\n", current())
	increment()
	fmt.Printf("increment() 2 次后 current(): %d\n", current())

	fmt.Println("\n=== 4. 状态较多时，结构体通常更好读 ===")
	counter := &Counter{}
	fmt.Printf("结构体计数器: %d %d，当前值: %d\n",
		counter.Increment(), counter.Increment(), counter.Current())
}
