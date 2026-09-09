// package main 表示：这个目录中的这些 Go 文件共同组成一个“可执行包”。
//
// program-structure 是目录名，也是导入路径的一部分；它不会自动变成包名。
// 而且连字符“-”不能出现在 Go 标识符中，所以 package program-structure 本身就是非法语法。
// 真正让这个包成为可执行程序的是“package main”与下面的“func main()”。
// 文件名叫 main.go 只是常见约定，不是语言强制要求。
package main

import (
	"fmt"

	// 导入路径由 go.mod 中的“module go-demo”加上目录路径组成。
	"go-demo/examples/basic-syntax/program-structure/mymath"

	// 目录名是 oddname，但里面声明的是 package classroom。
	// 所以调用时使用 classroom.Explain，而不是 oddname.Explain。
	// 这种写法技术上可行，但真实项目通常让目录名和包名保持一致。
	"go-demo/examples/basic-syntax/program-structure/oddname"
)

// main 是可执行程序的入口。
// 如果程序里存在 init，所有需要执行的 init 都会先于 main 执行。
func main() {
	section("1. Hello World：包、导入和 main 函数")

	// fmt 来自 Go 标准库，Println 会在最后自动换行。
	fmt.Println("Hello, World!")

	section("2. 变量、常量、语句和表达式")

	// var 可以明确写出变量类型。
	var name string = "小明"

	// 在函数内部，:= 会根据右侧的值自动推断变量类型。
	age := 18
	isLearning := true

	// const 声明常量。常量在运行过程中不能再次赋值。
	const language = "Go"

	// age + 1 是表达式：它会计算并产生一个值。
	// nextYearAge := age + 1 是一条声明语句：它把结果保存到新变量中。
	nextYearAge := age + 1

	fmt.Printf("name=%q，类型=%T\n", name, name)
	fmt.Printf("age=%d，类型=%T\n", age, age)
	fmt.Printf("isLearning=%t，类型=%T\n", isLearning, isLearning)
	fmt.Printf("常量 language=%s，明年年龄=%d\n", language, nextYearAge)

	// 没有显式赋初值的变量会得到该类型的零值。
	var count int
	var message string
	var ready bool
	fmt.Printf("零值对比：int=%d，string=%q，bool=%t\n", count, message, ready)

	section("3. fmt.Print、Println、Printf 对比")

	// Print 不会自动换行；下面两次调用会出现在同一行。
	fmt.Print("Print 第一次")
	fmt.Print(" + Print 第二次（手动用 \\n 换行）\n")

	// Println 自动换行，并在多个参数之间添加空格。
	fmt.Println("Println 自动分隔参数：", name, age)

	// Printf 按格式模板输出，%s、%d、%t 分别对应字符串、整数和布尔值。
	fmt.Printf("Printf 精确控制格式：姓名=%s，年龄=%d，正在学习=%t\n", name, age, isLearning)

	// Println 也能直接使用切片等变量，并采用默认格式。
	numbers := []int{1, 2, 3}
	fmt.Println("Println 输出切片：", numbers)

	section("4. 多文件组成同一个包")

	// section 定义在 output.go 中，main.go 仍然可以直接调用它。
	// 因为 main.go、output.go、lifecycle.go 都在同一个目录且都是 package main。
	//
	// 它们依然是三个独立源文件，并没有在磁盘上或语义上变成一个大文件。
	// 编译器只是把同一目录、同一 package 的可参与构建文件收集起来，作为一个包编译，
	// 所以这些文件中的包级函数、变量、常量和类型可以互相访问。
	fmt.Println("main.go 可以调用 output.go 中小写开头的 section 函数。")
	fmt.Println("源文件视角：main.go、output.go、lifecycle.go 是 3 个独立文件。")
	fmt.Println("编译视角：它们共同组成 1 个 package main。")
	fmt.Println("入口视角：整个 package main 只需要 1 个 func main()。")
	fmt.Println("命令对比：go run main.go 只取指定文件；go run . 会构建当前整个包。")

	section("5. 自定义包与大小写导出规则")

	// Add 和 Sub 首字母大写，所以可以被 main 包调用。
	fmt.Printf("mymath.Add(8, 3) = %d\n", mymath.Add(8, 3))
	fmt.Printf("mymath.Sub(8, 3) = %d\n", mymath.Sub(8, 3))

	// abs 首字母小写，main 包不能直接调用 mymath.abs(-8)。
	// 但同属 mymath 包的 AddAbsolute 可以调用它。
	fmt.Printf("mymath.AddAbsolute(-8, 3) = %d（包内调用了未导出的 abs）\n", mymath.AddAbsolute(-8, 3))

	// 如果取消下一行注释，编译器会报错：name abs not exported by package mymath。
	// fmt.Println(mymath.abs(-8))

	section("6. 文件夹名与包名可以不同，但不推荐")

	// 导入目录叫 oddname，但代码中的包声明是 package classroom。
	// 默认选择器来自包声明，因此这里写 classroom.Explain()。
	fmt.Println(classroom.Explain())

	section("7. 本例最重要的规则")
	fmt.Println("① package main + func main：组成可执行程序")
	fmt.Println("② 同一普通目录中的 Go 文件必须声明同一个包")
	fmt.Println("③ 大写开头的名称可被其他包使用；小写开头的名称仅包内可见")
	fmt.Println("④ 导入本项目包时，路径从 go.mod 的 module 名开始")
	fmt.Println("⑤ 推荐用 go run ./examples/basic-syntax/program-structure 运行整个包")
}
