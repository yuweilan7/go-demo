package main

import "fmt"

// section 输出醒目的章节标题，让运行结果本身也能充当学习笔记。
//
// section 以小写字母开头，因此不能被其他包使用；但同一个 main 包中的
// main.go 可以调用它。这说明 Go 的小写名称是“包内可见”，不是只在当前文件可见。
//
// output.go 不需要再定义 func main()：main 是整个可执行包的入口，不是每个文件的入口。
// 如果同一个 package main 中再定义一个同名 main 函数，反而会因为重复定义而编译失败。
func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}
