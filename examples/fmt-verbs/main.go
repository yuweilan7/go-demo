// 本示例集中演示 fmt.Printf 中常见的格式化占位符（Go 通常称它们为 verbs）。
package main

import "fmt"

type Student struct {
	Name  string
	Score int
}

func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}

func main() {
	section("1. %s、%q、%v：同一个字符串的不同展示方式")

	text := "Go\t\"fmt\"\n下一行"
	// %s：把字符串内容直接输出，所以制表符和换行符会真的生效。
	fmt.Printf("%%s  -> %s\n", text)
	// %q：输出带双引号、经过 Go 转义的字符串，适合观察空白符和特殊字符。
	fmt.Printf("%%q  -> %q\n", text)
	// %v：使用该值的默认格式；对 string 来说通常看起来和 %s 一样。
	fmt.Printf("%%v  -> %v\n", text)
	// %#v：输出接近 Go 源代码的表示，调试复杂值时很有用。
	fmt.Printf("%%#v -> %#v\n", text)

	section("2. 为什么原数组和切片使用 %q")

	words := [4]string{"aa", "", "a\nb", "cc"}
	// 对数组或切片使用 %v 时，字符串元素不带引号，空字符串和换行符不容易看清。
	fmt.Printf("%%v  -> %v\n", words)
	// 对字符串数组或切片使用 %q，会让每个字符串元素带引号并显示转义字符。
	// 因此空字符串会明确显示为 ""，而换行符会明确显示为 \n。
	fmt.Printf("%%q  -> %q\n", words)
	fmt.Printf("%%#v -> %#v\n", words)

	section("3. 类型、布尔值和整数")

	count := 42
	exists := true
	// %T 打印实参的类型；注意 T 必须大写。
	fmt.Printf("%%T：count 的类型=%T，words 的类型=%T\n", count, words)
	// %d 是十进制整数；%t 专门打印 bool；%v 则可以按默认格式打印二者。
	fmt.Printf("%%d=%d，%%t=%t，两个 %%v=%v/%v\n", count, exists, count, exists)
	// 整数也能按二进制、八进制、十六进制和 Unicode 字符打印。
	fmt.Printf("%%b=%b，%%o=%o，%%x=%x，%%X=%X，%%c=%c\n",
		count, count, count, count, count)

	section("4. 结构体、指针和浮点数")

	student := Student{Name: "lisi", Score: 96}
	// %v 只打印字段值；%+v 额外打印字段名；%#v 更接近 Go 源代码形式。
	fmt.Printf("%%v=%v\n", student)
	fmt.Printf("%%+v=%+v\n", student)
	fmt.Printf("%%#v=%#v\n", student)
	// %p 打印指针保存的地址。
	fmt.Printf("%%p=%p\n", &student)
	// %f 默认保留 6 位小数；%.2f 表示小数点后保留 2 位。
	price := 12.3456
	fmt.Printf("%%f=%f，%%.2f=%.2f\n", price, price)

	section("5. 宽度、补零和真正的百分号")

	// %5d 表示至少占 5 个字符并右对齐；%05d 用 0 补齐；%-5d 左对齐。
	fmt.Printf("右对齐=[%5d]，补零=[%05d]，左对齐=[%-5d]\n", 42, 42, 42)
	// %% 不消费参数，只输出一个普通的百分号。
	fmt.Printf("完成度=100%%\n")
}
