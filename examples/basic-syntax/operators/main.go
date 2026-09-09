// 本示例通过实际输出学习 Go 运算符，而不是只背运算符表。
//
// 运行方式：
//
//	go run ./examples/basic-syntax/operators
package main

import "fmt"

func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}

// traceBool 会在自己真的被调用时打印信息，用来观察 && 和 || 的短路行为。
func traceBool(label string, value bool) bool {
	fmt.Printf("计算了%s；", label)
	return value
}

func arithmeticOperators() {
	section("1. 算术运算符")

	a, b := 21, 10
	fmt.Printf("a=%d, b=%d\n", a, b)
	fmt.Printf("a+b=%d, a-b=%d, a*b=%d\n", a+b, a-b, a*b)

	// 两边都是整数时，/ 执行整数除法，小数部分直接舍去；它不是四舍五入。
	// 只有显式转换为浮点数后，结果才会保留小数。
	fmt.Printf("整数除法 a/b=%d；浮点除法 float64(a)/float64(b)=%.1f\n",
		a/b, float64(a)/float64(b))
	fmt.Printf("余数 a%%b=%d；负数余数 -a%%b=%d\n", a%b, -a%b)

	// Go 不会在 int 和 float64 之间做隐式数值转换。
	price := 2.5
	fmt.Printf("显式转换后 float64(a)+price=%.1f\n", float64(a)+price)
	// 下面这行不能编译：mismatched types int and float64
	// _ = a + price

	// ++ 和 -- 在 Go 中是“语句”，不是能产生值的表达式；只能写在变量后面。
	// 因而没有 ++a，也不能写 result := a++，这点与 Java/C++ 不同。
	a++
	fmt.Printf("执行 a++ 后 a=%d\n", a)
	a--
	fmt.Printf("执行 a-- 后 a=%d\n", a)
}

func comparisonOperators() {
	section("2. 比较运算符")

	a, b := 10, 20
	fmt.Printf("a=%d, b=%d：== %v，!= %v，< %v，<= %v，> %v，>= %v\n",
		a, b, a == b, a != b, a < b, a <= b, a > b, a >= b)

	// 字符串也支持有序比较，比较依据是 UTF-8 编码后的字节序列（按字典序比较）。
	fmt.Printf("字符串比较：%q < %q -> %v\n", "Go", "Java", "Go" < "Java")

	// Go 的比较结果是 bool，字面量写作小写 true/false。
	// slice、map 和 func 不能互相使用 == 比较，只能与 nil 比较。
	numbers := []int(nil)
	fmt.Printf("nil slice 只能与 nil 比较：numbers == nil -> %v\n", numbers == nil)
	// 下面这行不能编译：slice can only be compared to nil
	// _ = []int{1} == []int{1}
}

func logicalOperators() {
	section("3. 逻辑运算符与短路")

	a, b := true, false
	fmt.Printf("a=%v, b=%v：a&&b=%v，a||b=%v，!a=%v\n",
		a, b, a && b, a || b, !a)

	// &&：左边为 false 已足以确定结果，右边不会执行。
	fmt.Print("false && 右侧：")
	result := b && traceBool("右侧", true)
	fmt.Printf("结果=%v（没有看到“计算了右侧”）\n", result)

	// ||：左边为 true 已足以确定结果，右边同样不会执行。
	fmt.Print("true || 右侧：")
	result = a || traceBool("右侧", false)
	fmt.Printf("结果=%v（没有看到“计算了右侧”）\n", result)

	// 当左边无法决定结果时，右边才会执行。
	fmt.Print("true && 右侧：")
	result = a && traceBool("右侧", true)
	fmt.Printf("结果=%v\n", result)
}

func bitwiseOperators() {
	section("4. 位运算符")

	// 使用 uint8 是为了固定为 8 位，让二进制输出和溢出行为容易观察。
	var a uint8 = 60 // 0011 1100
	var b uint8 = 13 // 0000 1101
	fmt.Printf("a=%3d (%08b)，b=%3d (%08b)\n", a, a, b, b)
	fmt.Printf("a & b =%3d (%08b)：两位都为 1 才是 1\n", a&b, a&b)
	fmt.Printf("a | b =%3d (%08b)：至少一位为 1 就是 1\n", a|b, a|b)
	fmt.Printf("a ^ b =%3d (%08b)：两位不同才是 1\n", a^b, a^b)
	fmt.Printf("a &^ b=%3d (%08b)：把 b 中为 1 的位从 a 清掉\n", a&^b, a&^b)
	fmt.Printf("^a     =%3d (%08b)：一元 ^ 翻转 a 的每一位\n", ^a, ^a)
	fmt.Printf("a << 2 =%3d (%08b)\n", a<<2, a<<2)
	fmt.Printf("a >> 2 =%3d (%08b)\n", a>>2, a>>2)

	// “左移 n 位等于乘 2^n”只在结果没有超出类型宽度时成立。
	// a 是 uint8，a<<3 的数学结果为 480；只保留低 8 位后得到 224。
	fmt.Printf("uint8 的 a << 3=%d（数学结果 480 已超出 uint8）\n", a<<3)
	// “右移 n 位等于除 2^n”也要谨慎理解，尤其是负的有符号整数。
}

func assignmentOperators() {
	section("5. 赋值与复合赋值")

	value := 21
	fmt.Printf("初始 value=%d\n", value)
	value += 9
	value *= 2
	value /= 5
	value %= 7
	fmt.Printf("依次执行 +=9、*=2、/=5、%%=7 后 value=%d\n", value)

	flags := uint8(0b0011)
	flags |= 0b0100  // 设置某些位
	flags &^= 0b0001 // 清除某些位；原教程遗漏了 &^=
	flags ^= 0b0010  // 翻转某些位
	flags <<= 1
	flags >>= 1
	fmt.Printf("位复合赋值后的 flags=%04b\n", flags)

	// x op= y 通常可理解为 x = x op y，但左侧 x 在复合赋值中只求值一次。
	// 当左侧含函数调用、索引等副作用时，这个差异很重要。
	numbers := []int{10, 20}
	indexCalls := 0
	nextIndex := func() int {
		indexCalls++
		return 0
	}
	numbers[nextIndex()] += 5
	fmt.Printf("numbers[nextIndex()] += 5 -> numbers=%v，nextIndex 调用 %d 次\n",
		numbers, indexCalls)

	// 赋值在 Go 中也是语句，不会返回一个值，因此不能像某些语言那样链式赋值。
	// 下面这行不能编译：unexpected = at end of statement
	// left = right = 1
}

func pointerOperators() {
	section("6. 取地址 & 与解引用 *")

	value := 4
	pointer := &value
	fmt.Printf("value=%d(%T)，&value=%p(%T)\n", value, value, &value, &value)
	fmt.Printf("pointer=%p(%T)，*pointer=%d\n", pointer, pointer, *pointer)

	// 表达式里的 *pointer 表示“沿地址找到目标值”，它既能读取，也能作为赋值左侧。
	*pointer = 9
	fmt.Printf("执行 *pointer=9 后：value=%d，*pointer=%d\n", value, *pointer)

	// 注意同一符号会因位置不同而含义不同：
	//   &value 是一元取地址；a & b 是二元按位与。
	//   *pointer 是一元解引用；a * b 是二元乘法；*int 则是类型“指向 int 的指针”。
	// 与 C/C++ 不同，普通 Go 代码不能对指针做 pointer++ 之类的指针算术。
}

func precedence() {
	section("7. 优先级与括号")

	// 同一优先级按从左到右结合，所以 20/5*2 是 (20/5)*2。
	fmt.Printf("20 / 5 * 2=%d；20 / (5 * 2)=%d\n", 20/5*2, 20/(5*2))

	// Go 把 << 放在与 * 相同的最高二元优先级组里，这与 Java 不同。
	// 因此 2 + 3<<1 会解析成 2 + (3<<1)，而不是 (2+3)<<1。
	fmt.Printf("2 + 3<<1=%d；(2 + 3)<<1=%d\n", 2+3<<1, (2+3)<<1)

	// && 的优先级高于 ||。
	left, middle, right := true, false, false
	fmt.Printf("true || false && false=%v；(true || false) && false=%v\n",
		left || middle && right, (left || middle) && right)

	// 原教程列的是五档二元运算符优先级，不是 Go 中“所有运算符”的完整表。
	// 实际开发中，如果读者需要停下来计算结合方式，就用括号表达意图。
}

func main() {
	arithmeticOperators()
	comparisonOperators()
	logicalOperators()
	bitwiseOperators()
	assignmentOperators()
	pointerOperators()
	precedence()
}
