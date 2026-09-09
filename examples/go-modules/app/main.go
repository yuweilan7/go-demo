package main

import (
	"fmt"
	"runtime/debug"

	"example.com/modulelab/greeting"
)

func main() {
	fmt.Println("== 1. 包调用链 ==")
	fmt.Println(greeting.Hello("Gopher"))
	fmt.Println("app -> greeting -> punctuation")

	fmt.Println("\n== 2. 二进制中记录的模块信息 ==")
	printBuildModules()

	fmt.Println("\n== 3. 关键观察 ==")
	fmt.Println("标准库 fmt、runtime/debug 不会出现在 go.mod 中")
	fmt.Println("本地 replace 的模块不会写入 go.sum，因为没有下载内容需要校验")
}

// printBuildModules 读取 Go 写进当前可执行程序的构建信息。
// 它让“模块图”不只停留在 go.mod 文本里：程序运行时也能观察到主模块、依赖版本和替换路径。
func printBuildModules() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Println("当前二进制没有可读取的 Go 构建信息")
		return
	}

	fmt.Printf("main: %s\n", info.Main.Path)
	for _, dependency := range info.Deps {
		fmt.Printf("dependency: %s %s", dependency.Path, dependency.Version)
		if dependency.Replace != nil {
			fmt.Printf(" => %s", dependency.Replace.Path)
		}
		fmt.Println()
	}
}
