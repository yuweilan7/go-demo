package main

import (
	"fmt"

	"example.com/m01/quote"
	"example.com/m01/receipt"
)

// main 由模块实验运行，直接 import 两个包，二者间接使用同一个 rules 模块。
// quote/receipt 返回实际执行的本地 rules 字符串，与 go list -m 的选择结果一起观察。
func main() {
	fmt.Println("[L07 quote]", quote.Describe("ORD-100"))
	fmt.Println("[L07 receipt]", receipt.Describe("ORD-100"))
}
