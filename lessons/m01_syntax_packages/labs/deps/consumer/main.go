package main

import (
	"fmt"

	"example.com/m01/quote"
)

// main 由 consumer 实验调用，只有 rules 路径能解析时才执行。
// 坏版在构建阶段失败，打印语句根本不会运行；修复版显示跨模块调用的实际结果。
func main() {
	fmt.Println("[L07 consumer]", quote.Describe("ORD-100"))
}
