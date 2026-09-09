// 本示例集中讲解 Go 的变量声明、零值、短变量声明和生命周期。
//
// Go 标识符由 Unicode 字母、Unicode 数字和下划线组成，首字符必须是字母或下划线：
// name、user2、_cache、用户名都符合语法；2user 不符合语法，关键字 var、func 等也不能作名称。
// 虽然下划线可以出现在普通名称中，但 Go 社区通常使用 userName 这样的 mixedCaps 命名。
// 单独的 _ 是“空白标识符”，代表主动丢弃一个值，不能再把它读回来。
package main

import (
	"fmt"
	"math"
	"strconv"
)

// 包级变量有时被口语化地叫作“全局变量”，更准确的名称是 package-level variable。
// 它们的作用域和可见性仍然受包以及名称首字母大小写控制。
// 包级变量可以声明后暂时不使用；函数中的局部变量则必须被使用。
var packageMessage string

// var (...) 叫作分组声明。括号只是在源码中把几条声明排在一起，
// 不会像 Java class 或 C struct 那样创建一个新类型，也不会产生一个“组合对象”。
// 下面两项仍是两个完全独立的变量；拆成两行 var，程序语义完全一样。
// 分组在包级变量、const、type 以及 import 较多时可以让代码更整齐，
// 如果只有一两个互不相关的变量，分别声明通常反而更清楚。
var (
	packageCount int
	packageReady bool
)

// nextResult 模拟常见的“结果 + 错误”返回形式。
func nextResult() (int, error) {
	return 42, nil
}

// newCounterValue 直接返回 7 这个值。调用方会得到一份 int 值的副本。
// 如果调用方只需要结果，并不需要共享、修改函数里的那个变量，这种写法最简单。
func newCounterValue() int {
	local := 7
	return local
}

// newCounter 返回“指向 int 的指针”。返回类型中的 *int 是一个类型名称，意思是：
// 这个返回值不是 int 本身，而是能够找到某个 int 的地址。
func newCounter() *int {
	// local 是变量名，把它想成一个装着 int 的盒子；在表达式中写 local，读到的是值 7。
	local := 7

	// & 是“取地址”：&local 的类型是 *int，值是 local 所在的位置。
	// %p 专门用来显示地址；每次运行看到的十六进制地址可能不同，这是正常现象。
	fmt.Printf("newCounter 内部：local=%d(%T)，&local=%p(%T)\n",
		local, local, &local, &local)

	// Go 允许安全地返回局部变量地址，不会产生 C/C++ 中常见的悬空指针。
	// 编译器通过逃逸分析决定它实际放在哪里，垃圾回收器负责其生命周期，无需手动 free。
	return &local
}

func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}

func main() {
	section("1. 分组声明、var 与零值")

	// packageMessage 是单独声明的；packageCount 和 packageReady 写在 var (...) 中。
	// 运行时看不出“组”的存在，因为组只负责整理源码，三个变量依旧各自有名称、类型和值。
	fmt.Printf("单独声明：packageMessage=%q；分组声明：packageCount=%d, packageReady=%v\n",
		packageMessage, packageCount, packageReady)

	// 完整形式是 var 名称 类型 = 初始值；有初始值时也可以省略显式类型。
	var greeting string = "Hello"
	var leftNumber, rightNumber int = 1, 2

	// 只声明、不初始化时，不会得到 Java 局部变量的“尚未初始化”状态，
	// 而是立刻取得该 Go 类型的零值。
	var integer int
	var decimal float64
	var complexNumber complex128
	var enabled bool
	var text string

	fmt.Println(greeting)
	fmt.Println(leftNumber, rightNumber)
	// %v 使用默认格式；%q 给字符串加引号，因此能清楚看到它是空字符串。
	fmt.Printf("int=%v float64=%v complex128=%v bool=%v string=%q\n",
		integer, decimal, complexNumber, enabled, text)

	section("1.1 数值类型：int、float 与 complex")

	// Go 的 int 不是固定宽度：只能保证它是 32 位或 64 位，取决于目标平台。
	// 当前程序通过 strconv.IntSize 把实际位数打印出来。
	// Java long 对应的 Go 类型是固定 64 位的 int64，而不是 complex128。
	fmt.Printf("当前平台 int：%d 位，范围 [%d, %d]\n",
		strconv.IntSize, int(math.MinInt), int(math.MaxInt))
	fmt.Printf("固定宽度 int64（最接近 Java long）：范围 [%d, %d]\n",
		int64(math.MinInt64), int64(math.MaxInt64))

	// int8/int16/int32/int64 永远是相应的位数；前面的数字不是“容量提示”，而是类型契约。
	fmt.Printf("固定宽度整数：int8=[%d,%d]，int16=[%d,%d]，int32=[%d,%d]\n",
		int8(math.MinInt8), int8(math.MaxInt8),
		int16(math.MinInt16), int16(math.MaxInt16),
		int32(math.MinInt32), int32(math.MaxInt32))

	// float32/float64 都是 IEEE 754 浮点数，类似 Java 的 float/double。
	// “最大范围”和“精度”是两回事：float64 范围更大，也能保留更多有效数字。
	fmt.Printf("float32：最大有限值约 %.1e，约 7 位十进制有效数字\n", float32(math.MaxFloat32))
	fmt.Printf("float64：最大有限值约 %.1e，约 15~16 位十进制有效数字\n", float64(math.MaxFloat64))

	// complex128 确实是复数：总计 128 位，由两个 float64 组成，分别保存实部和虚部。
	// complex64 则由两个 float32 组成。Java 标准语言没有内建复数类型，通常要使用类或第三方库。
	complexNumber = complex(3, 4) // 也可以写成：complexNumber = 3 + 4i
	fmt.Printf("complexNumber=%v(%T)，实部=%.1f，虚部=%.1f\n",
		complexNumber, complexNumber, real(complexNumber), imag(complexNumber))

	section("2. 可以为 nil 的类型")

	var pointer *int
	var numbers []int
	var scores map[string]int
	var jobs chan int
	var transform func(string) int
	// error 是接口类型；这里展示的是“零值接口”，所以它等于 nil。
	// 如果接口里装入一个类型化的 nil 指针，接口本身反而可能不等于 nil。
	var problem error

	fmt.Printf("pointer=%v slice=%v map=%v chan=%v func=%v error=%v\n",
		pointer == nil,
		numbers == nil,
		scores == nil,
		jobs == nil,
		transform == nil,
		problem == nil,
	)

	// “零值都是 nil”只说明它们初始时的比较结果，并不意味着使用方式相同：
	//   - nil slice 可以读取、取 len、遍历和 append；
	//   - nil map 可以读取，但写入会 panic，写入前通常要 make；
	//   - 对 nil channel 发送或接收会永久阻塞；
	//   - 调用 nil func 或解引用 nil 指针会 panic。
	numbers = append(numbers, 10)
	fmt.Printf("append 到 nil slice 后：numbers=%v, len=%d\n", numbers, len(numbers))
	fmt.Printf("读取 nil map 中不存在的键：score=%d\n", scores["Go"])

	section("3. var、:= 与 =")

	// var 是完整的声明关键字：它能用在包级或函数内，也允许只声明而不初始化。
	// 这里没有显式写 bool，类型由右侧表达式推断。
	var inferred = true

	// := 才是“短变量声明”；注意不是单独的 =。
	// 它把“声明新局部变量 + 根据右侧推断类型 + 初始化”合成一步，只允许在函数体中。
	// 对单个变量来说，name := "Gopher" 大致等价于 var name = "Gopher"。
	// 包级不能写 packageName := "Go"，必须写 var packageName = "Go"。
	name := "Gopher"
	age, city := 18, "Shanghai"
	fmt.Printf("inferred=%v(%T), name=%q(%T), age=%d(%T), city=%q\n",
		inferred, inferred, name, name, age, age, city)

	// 教程常把规则简化成“:= 左边不能出现已声明变量”，这并不准确。
	// 在同一代码块中，只要至少有一个非空白标识符是新变量，就允许混合声明和赋值：
	// age 已存在，language 是新变量，所以这一行合法。
	age, language := 20, "Go"
	fmt.Printf("混合使用 := 后：age=%d, language=%q\n", age, language)

	// 如果取消下一行注释，左侧没有任何新变量，编译器会报告：
	// no new variables on left side of :=
	// age, language := 21, "Golang"

	// = 是普通赋值：它不会声明变量，左边的名称必须已经存在。
	// 两个名称都已存在时，应使用 =，这和 Java 中给已有变量重新赋值是同一个概念。
	age, language = 21, "Golang"
	fmt.Printf("使用 = 重新赋值后：age=%d, language=%q\n", age, language)

	section("4. 多重赋值、交换与空白标识符")

	left, right := 5, 7
	fmt.Printf("交换前：left=%d, right=%d\n", left, right)

	// 多重赋值会先求出右侧全部值，再把它们赋给左侧，因此交换时不需要临时变量。
	// 语言规则并不是简单要求两个变量类型相同，而是要求每个右值都可赋给对应的左值。
	left, right = right, left
	fmt.Printf("交换后：left=%d, right=%d\n", left, right)

	value, err := nextResult()
	fmt.Printf("接收两个返回值：value=%d, err=%v\n", value, err)

	// _ 是空白标识符，不是真正可读取的“只写变量”。
	// 它表示这里有意忽略第二个返回值。
	valueOnly, _ := nextResult()
	fmt.Printf("用 _ 丢弃 error：valueOnly=%d\n", valueOnly)

	section("5. := 最常见的坑：变量遮蔽")

	status := "outer"
	fmt.Printf("进入内层前：status=%q，变量地址=%p\n", status, &status)
	{
		// 这里是新的词法代码块，所以 := 声明了一个全新的内层 status。
		// 它与外层变量名称相同，但地址不同；在这个块中，外层名称暂时被“遮住”。
		status := "inner"
		fmt.Printf("内层：status=%q，变量地址=%p\n", status, &status)
	}
	fmt.Printf("离开内层后：外层 status 仍为 %q，地址仍是 %p\n", status, &status)

	{
		// 这里使用 =，没有声明新的 status，因此修改的是外层变量。
		status = "outer changed"
	}
	fmt.Printf("内层使用 = 后：外层 status=%q\n", status)

	// Java 也有“遮蔽”这个概念，例如方法参数/局部变量可以遮蔽同名字段，可用 this.status 访问字段。
	// 但 Java 不允许在同一个方法的重叠局部作用域中再次声明同名局部变量；Go 允许内层块这样做。
	// Go 常见作用域还包括函数体以及 if、for、switch 的块；离开块后，内层名称就失效。

	section("6. 局部变量必须使用")

	// 如果取消下一行注释，编译器会报告 declared and not used: unused；
	// 仅仅再次给它赋值仍不够，程序必须读取这个局部变量的值。
	// var unused string
	fmt.Println("当前示例中的每个局部变量都被读取，因此可以正常编译。")

	section("7. 指针：local、&local 与 *counter")

	// 不用指针的版本：counterValue 的类型就是 int，值是复制出来的 7。
	counterValue := newCounterValue()
	fmt.Printf("值版本：counterValue=%d(%T)\n", counterValue, counterValue)

	// counter 保存 newCounter 中 local 的地址，所以 counter 的类型是 *int。
	counter := newCounter()
	fmt.Printf("指针版本：counter=%p(%T)，*counter=%d\n", counter, counter, *counter)

	// 表达式中的 * 是“解引用”：沿着地址找到那个 int。它既能读取，也能修改目标值。
	*counter = 8
	fmt.Printf("执行 *counter = 8 后：counter 仍是地址 %p，目标值变为 %d\n", counter, *counter)

	// 复制指针只会复制地址，不会复制目标 int；两个指针会找到同一个值。
	alias := counter
	*alias = 9
	fmt.Printf("alias=%p，修改 *alias 后，*counter=%d，*alias=%d\n",
		alias, *counter, *alias)

	// 普通 Go 指针比 C/C++ 指针受限得多：不能做指针算术，也不需要手动释放。
	// 常见用途是让函数修改调用方的数据、共享同一个对象身份，或避免复制较大的 struct。
	// int 这样的小值如果只需要返回结果，优先采用上面的值版本，完全可以不用指针。

	section("8. 作用域与生命周期不是一回事")

	// 作用域回答“源码中的哪些位置能使用这个名称”；
	// 生命周期回答“运行期间这个值需要存活多久”。newCounter 的 local 名称已离开作用域，
	// 但 counter 仍指向它，因此对应的 int 值必须继续存活；Go 编译器和垃圾回收器会保证这一点。
	fmt.Printf("包级变量的零值：packageMessage=%q, packageCount=%d, packageReady=%v\n",
		packageMessage, packageCount, packageReady)
}
