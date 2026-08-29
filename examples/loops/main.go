package main

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

func main() {
	basicLoops()
	rangeBasics()
	loopVariableAddresses()
	rangeLengthSnapshot()
	closureCapture()
	sliceDeletion()
	mapIteration()
	stringIteration()
}

func basicLoops() {
	fmt.Println("\n== 1. Go 的几种 for 写法 ==")

	// 这是唯一带分号的 for 形式。init、condition、post 都写在 for 这一行。
	// 注意：i++ 在 Go 中是语句，不是可以参与计算或赋值的表达式。
	for i := 0; i < 3; i++ {
		fmt.Printf("三段式: i=%d\n", i)
	}

	// Go 没有 while 关键字；只保留条件，就得到了 while 风格的循环。
	n := 0
	for n < 2 {
		fmt.Printf("条件式: n=%d\n", n)
		n++
	}

	// 不写条件就是无限循环。通常在循环体里用 break、return 或 panic 退出。
	// Go 也没有 do-while；需要“先执行一次再判断”时，可以采用这种结构。
	attempt := 0
	for {
		attempt++
		fmt.Printf("先执行后判断: attempt=%d\n", attempt)
		if attempt >= 2 {
			break
		}
	}

	// Go 1.22 起，range 还可以直接遍历整数 n，依次产生 0 到 n-1。
	for i := range 3 {
		fmt.Printf("整数 range: i=%d\n", i)
	}
}

func rangeBasics() {
	fmt.Println("\n== 2. range 的索引和值 ==")

	words := []string{"aa", "bb", "cc"}
	for i, word := range words {
		fmt.Printf("索引和值: i=%d word=%s\n", i, word)
	}

	// 只接收第一个结果时，不需要写下划线。
	for i := range words {
		fmt.Printf("只要索引: %d\n", i)
	}

	// 只要第二个结果时，必须用空白标识符 _ 丢弃第一个结果。
	for _, word := range words {
		fmt.Printf("只要值: %s\n", word)
	}
}

func loopVariableAddresses() {
	fmt.Println("\n== 3. 循环变量地址不等于元素地址 ==")

	array := [2]int{1, 2}
	// []*int 不是“指针数组”，而是“元素类型为 *int 的切片”：
	// [] 表示 slice，*int 表示每一项都是 int 的地址。若写 []int，就只能存 int 值，
	// append(copyPointers, &value) 会因 *int 不能当作 int 而编译失败。
	// 这里故意保存地址，是为了观察“循环变量副本”和“原数组元素”是否为同一位置；
	// 普通业务若只需要保存数值，直接使用 []int 更简单。
	copyPointers := make([]*int, 0, len(array))
	for _, value := range array {
		// 当前模块声明 go 1.27，因此采用 Go 1.22+ 语义：每轮声明一个新的 value。
		// 两次 &value 是不同地址，但它们指向的是数组元素的“副本”。
		copyPointers = append(copyPointers, &value)
	}
	fmt.Printf("副本指针解引用后的值: %d %d\n", *copyPointers[0], *copyPointers[1])
	fmt.Printf("比较值 (*p0 != *p1): %t；比较地址 (p0 != p1): %t\n",
		*copyPointers[0] != *copyPointers[1], copyPointers[0] != copyPointers[1])
	fmt.Printf("原元素地址与副本地址相同吗: &array[0] == p0 -> %t, &array[1] == p1 -> %t\n",
		&array[0] == copyPointers[0], &array[1] == copyPointers[1])
	fmt.Printf("实际地址: 原元素=%p %p；value 副本=%p %p\n",
		&array[0], &array[1], copyPointers[0], copyPointers[1])

	*copyPointers[0] = 100
	fmt.Printf("修改副本后，原数组不变: %v\n", array)

	originalPointers := make([]*int, 0, len(array))
	for i := range array {
		// 需要真正修改原元素时，应通过索引取得 &array[i]。
		originalPointers = append(originalPointers, &array[i])
	}
	*originalPointers[0] = 100
	fmt.Printf("通过元素地址修改后，原数组改变: %v\n", array)

	values := []int{1, 2, 3}
	for _, value := range values {
		// 你的理解是对的：value 只是本轮元素值的副本，修改它不会回写切片。
		value *= 10
	}
	fmt.Printf("修改 range 值副本: %v\n", values)
	for i := range values {
		values[i] *= 10
	}
	fmt.Printf("通过索引修改元素: %v\n", values)
}

func rangeLengthSnapshot() {
	fmt.Println("\n== 4. range 切片时，迭代长度在开始时确定 ==")

	numbers := []int{1, 2, 3}
	iterations := 0
	for i := range numbers {
		// range 开始时复制了切片描述符，并据其初始长度决定迭代 3 次。
		// append 会改变 numbers，但不会让当前循环继续遍历新追加的元素。
		numbers = append(numbers, i)
		iterations++
	}
	fmt.Printf("迭代次数=%d，循环结束后的切片=%v\n", iterations, numbers)
}

func closureCapture() {
	fmt.Println("\n== 5. Go 1.22+ 的闭包捕获循环变量 ==")

	functions := make([]func() int, 0, 3)
	for i := 0; i < 3; i++ {
		// Go 1.22+ 中，由 for 初始化子句声明的 i 每轮都是新变量。
		// 因此三个闭包分别捕获 0、1、2，不再都观察到循环结束后的 3。
		functions = append(functions, func() int { return i })
	}

	results := make([]int, 0, len(functions))
	for _, function := range functions {
		results = append(results, function())
	}
	fmt.Printf("三个闭包的结果: %v\n", results)

	// 为了直观看懂旧问题，下面故意把变量声明在循环外：整个循环始终复用 sharedI。
	// 闭包捕获的是“变量”而不是创建闭包当时的整数快照；等闭包执行时，循环已经结束，
	// sharedI 已经变成 3，所以三个闭包都返回 3。这也模拟了 Go 1.21 及更早版本的常见陷阱。
	sharedFunctions := make([]func() int, 0, 3)
	sharedI := 0
	for ; sharedI < 3; sharedI++ {
		sharedFunctions = append(sharedFunctions, func() int { return sharedI })
	}
	sharedResults := make([]int, 0, len(sharedFunctions))
	for _, function := range sharedFunctions {
		sharedResults = append(sharedResults, function())
	}
	fmt.Printf("故意捕获循环外的同一个变量: %v\n", sharedResults)

	// Go 1.22 以前，for/range 用 := 声明的循环变量也会被各轮复用，因此容易出现上述结果。
	// 当时的通用修复是在循环体内写 current := i，再捕获 current。
	// Go 1.22+ 改为每轮产生新变量；阅读旧教程时知道这一背景即可，新代码不要主动模拟旧写法。
}

func sliceDeletion() {
	fmt.Println("\n== 6. 遍历时删除切片元素 ==")

	numbers := []int{1, 2, 2, 3}
	for i, value := range numbers {
		fmt.Printf("本轮开始: i=%d, value=%d, 当前 numbers=%v\n", i, value, numbers)
		if value == 2 {
			// 删除元素会把后面的元素左移，但 range 的下一轮仍会让 i 加一，
			// 所以两个相邻的 2 中，第二个会被跳过。
			numbers = append(numbers[:i], numbers[i+1:]...)
			fmt.Printf("删除下标 %d 后: numbers=%v\n", i, numbers)
		}
	}
	fmt.Printf("边遍历边删除，漏掉一个 2: %v\n", numbers)

	source := []int{1, 2, 2, 3}
	// source[:0] 等价于 source[0:0]：创建一个 len=0、但仍从 source 下标 0 开始，
	// 且共享其底层数组和容量的空窗口。它常用于“不要旧逻辑元素，但复用旧存储”的原地过滤。
	filtered := source[:0]
	fmt.Printf("过滤前: source=%v；filtered=%v, len=%d, cap=%d\n",
		source, filtered, len(filtered), cap(filtered))
	for _, value := range source {
		if value != 2 {
			// 把要保留的元素依次写回原底层数组。写入位置不会超过当前读取位置，
			// 所以不会破坏尚未读取的元素，但原来的 source 内容会被改写。
			filtered = append(filtered, value)
		}
	}
	fmt.Printf("原地过滤后: filtered=%v；source 底层内容也变为=%v\n", filtered, source)
	fmt.Printf("二者共享起始元素地址: %t\n", &filtered[0] == &source[0])
}

func mapIteration() {
	fmt.Println("\n== 7. map 的遍历顺序与遍历中增删 ==")

	scores := map[string]int{"banana": 2, "apple": 1, "cherry": 3}
	fmt.Print("一次未排序的 range: ")
	for key, value := range scores {
		fmt.Printf("%s=%d ", key, value)
	}
	fmt.Println()

	// Go 规范只说 map 的迭代顺序“未指定”，不能依赖它；
	// “随机”只是常见的口语化说法，不是一个均匀随机的语言保证。
	keys := make([]string, 0, len(scores))
	for key := range scores {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Print("排序键后的稳定输出: ")
	for _, key := range keys {
		fmt.Printf("%s=%d ", key, scores[key])
	}
	fmt.Println()

	mutable := map[string]int{"a": 1, "b": 2, "c": 3}
	visited := make([]string, 0, len(mutable))
	visitedC := false
	visitedD := false
	for key := range mutable {
		visited = append(visited, key)
		if key == "c" {
			visitedC = true
		}
		if key == "d" {
			visitedD = true
		}
		if key == "a" {
			// 单个 goroutine 在遍历中增删 map 不是“未定义行为”：
			// 尚未访问就被删除的 c 不会再出现；新加的 d 可能出现，也可能不出现。
			delete(mutable, "c")
			mutable["d"] = 4
		}
	}
	fmt.Printf("本次访问到的键（顺序未指定）: %v\n", visited)
	fmt.Printf("本轮是否访问 c=%v，是否访问新增的 d=%v，迭代次数=%d（不保证固定为 3）\n",
		visitedC, visitedD, len(visited))

	finalKeys := make([]string, 0, len(mutable))
	for key := range mutable {
		finalKeys = append(finalKeys, key)
	}
	sort.Strings(finalKeys)
	fmt.Printf("增删后的最终键: %v\n", finalKeys)
}

func stringIteration() {
	fmt.Println("\n== 8. 字符串：字节与 rune ==")

	// Go string 本质是不可变的字节序列，通常约定其中放 UTF-8，但它也能容纳任意字节。
	// ASCII 字符占 1 个 UTF-8 字节；“世”“界”各占 3 字节；“😀”占 4 字节。
	text := "Go世界😀"
	fmt.Printf("%q: 字节数=%d，Unicode 码点数=%d\n",
		text, len(text), utf8.RuneCountInString(text))

	// range 返回的 index 是当前 rune 起始处的“字节偏移”，不是第几个字符。
	// value 的类型是 rune（int32 的别名），代表一个 Unicode 码点。
	for index, value := range text {
		fmt.Printf("rune: byteIndex=%d value=%c codePoint=%U\n", index, value, value)
	}

	// []rune 会先按 UTF-8 解码，得到码点切片；此时下标才是第几个 rune。
	// 对日常文本逐码点处理，这通常比按字节索引直观。
	runes := []rune(text)
	fmt.Printf("转成 []rune: 长度=%d，内容=%q，第 3 个 rune=%c\n", len(runes), runes, runes[2])

	// 普通索引 text[i] 返回单个 byte（uint8），不是一个 Unicode 字符。
	// 如果协议、文件格式或网络数据要求逐字节处理，就使用这种方式。
	fmt.Print("逐字节的十六进制: ")
	for i := 0; i < len(text); i++ {
		fmt.Printf("%02x ", text[i])
	}
	fmt.Println()

	// Java 的 char 是一个 UTF-16 code unit（16 位编码单元），并不保证是完整字符；
	// “世”能放进一个 char，但 U+1F600“😀”会被拆成两个代理 char。
	// Go 的 rune 能保存一个完整 Unicode code point，所以“😀”是 1 个 rune、4 个 UTF-8 byte。
	// 进一步说，人眼看到的一个字形还可能由多个 rune 组合；rune 也不总等于“视觉字符”。
}
