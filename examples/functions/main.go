// functions 用一个可运行的程序演示 Go 函数的核心语义：
// 定义与调用、多返回值、值传递、函数值（回调）以及闭包。
package main

import "fmt"

// operation 是一个“函数类型”：凡是参数和返回值形状为 func(int, int) int
// 的函数，都可以赋给 operation 类型的变量。
//
// 定义命名函数类型不是使用回调的必要条件，但它能表达业务含义，也能让较长的
// 函数签名更容易复用。
type operation func(int, int) int

// swap 展示多个返回值。返回值类型要放在参数列表之后；多个返回值用括号包住。
func swap(x, y string) (string, string) {
	return y, x
}

// max 展示普通的“接收参数并返回结果”的函数。
// Go 不要求先声明函数再调用，因此 main 写在它前面或后面都不影响调用。
func max(x, y int) int {
	if x > y {
		return x
	}
	return y
}

// incrementCopy 接收到的是调用方整数值的一份副本。
// 修改 n 只会修改这份副本，不会修改 main 中的原变量。
func incrementCopy(n int) {
	n++
	fmt.Printf("函数内副本 n = %d\n", n)
}

// incrementThroughPointer 的参数仍然是值传递：复制的是一个指针值。
// 两份指针值都指向同一个 int，所以通过副本解引用后，能修改调用方的那个 int。
// Go 没有 C++ 那种“引用传递”参数；这里也不应把指针参数误称为引用传递。
func incrementThroughPointer(n *int) {
	(*n)++
}

// changeFirst 接收的是 slice 值的一份副本。slice 值可理解为一个小描述符，
// 其中包含对底层数组的引用、长度和容量。复制描述符后，两个 slice 仍可指向
// 同一个底层数组，因此修改元素会被调用方看到。
func changeFirst(numbers []int) {
	numbers[0] = 99
}

// appendOne 特意返回新的 slice。append 可能复用原底层数组，也可能分配新数组，
// 而且函数内更新的只是 slice 描述符副本；调用方要稳定地获得新长度，就应接收返回值。
func appendOne(numbers []int, value int) []int {
	return append(numbers, value)
}

func add(x, y int) int {
	return x + y
}

// calculate 的第三个参数本身就是函数值，这就是常见的回调写法。
func calculate(x, y int, op operation) int {
	return op(x, y)
}

// newCounter 每调用一次，都会新建一个独立的变量 count，并返回一个函数值。
// 返回的函数引用了外层变量 count，所以它是闭包。
// 即使 newCounter 已经返回，只要闭包仍然可达，count 就会继续存活。
func newCounter() func() int {
	count := 0

	return func() int {
		count++
		return count
	}
}

// newCounterPair 返回两个闭包；它们来自同一次函数调用，捕获的是同一个 count 变量。
// 这个例子强调：闭包捕获的是变量本身，而不是创建闭包那一刻的数值快照。
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

func main() {
	fmt.Println("=== 1. 定义、调用与多个返回值 ===")
	first, second := swap("Google", "Hello")
	fmt.Printf("swap 后: %s %s\n", first, second)
	fmt.Printf("max(100, 200) = %d\n", max(100, 200))

	fmt.Println("\n=== 2. Go 的参数始终按值传递 ===")
	number := 100
	fmt.Printf("调用前 number = %d\n", number)
	incrementCopy(number)
	fmt.Printf("传 int 后 number = %d（原值不变）\n", number)
	incrementThroughPointer(&number)
	fmt.Printf("传 *int 后 number = %d（通过复制的指针修改同一对象）\n", number)

	numbers := []int{1, 2}
	changeFirst(numbers)
	fmt.Printf("修改 slice 元素后: %v（共享底层数组）\n", numbers)
	numbers = appendOne(numbers, 3)
	fmt.Printf("接收 append 返回值后: %v\n", numbers)

	fmt.Println("\n=== 3. 函数值与回调 ===")
	var op operation = add
	fmt.Printf("命名函数作为值: 3 + 4 = %d\n", calculate(3, 4, op))

	// 这是函数变量，也是匿名函数。按 Go 规范，函数字面量都是闭包；不过这里没有
	// 引用外层局部变量，所以它没有可观察的“捕获状态”。“匿名函数”描述的是写法，
	// “闭包”强调函数值和词法环境的绑定，不应简单地把两个概念画等号。
	multiply := func(x, y int) int {
		return x * y
	}
	fmt.Printf("匿名函数作回调: 3 * 4 = %d\n", calculate(3, 4, multiply))

	fmt.Println("\n=== 4. 闭包保存状态 ===")
	counterA := newCounter()
	fmt.Printf("counterA: %d %d %d\n", counterA(), counterA(), counterA())

	// 这是另一次 newCounter 调用，因此得到全新的 count，和 counterA 互不影响。
	counterB := newCounter()
	fmt.Printf("counterB: %d %d\n", counterB(), counterB())

	increment, current := newCounterPair()
	increment()
	increment()
	fmt.Printf("同一环境中的两个闭包共享 count: %d\n", current())
}
