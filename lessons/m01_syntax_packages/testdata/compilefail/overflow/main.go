// 显式故障输入：常量在编译期落入 uint8，超范围不能静默截断。
package main

// main 不会运行；应先检查真实输入范围，或在确需更大协议字段时改用更宽类型。
func main() {
	const input = 256
	var quantity uint8 = input // 错误：uint8 最大 255，常量无法表示。
	_ = quantity
}
