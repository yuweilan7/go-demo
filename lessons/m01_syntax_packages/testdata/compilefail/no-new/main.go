// 本文件是显式编译故障输入；testdata 被 go test ./... 忽略，只有脚本/命令指定时才编译。
package main

// main 没有外部副作用，编译器在第二个 := 拒绝它；修复对照见 README L08。
func main() {
	quantity := 2
	quantity := 3 // 错误：同一个块里没有任何新非空白变量，应写 quantity = 3。
	_ = quantity
}
