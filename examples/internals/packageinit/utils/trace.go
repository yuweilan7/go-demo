// Package utils 为初始化示例提供带全局序号的输出函数。
package utils

import "fmt"

// sequence 的零值是 0。包变量的存储会先被置为零值，随后才计算显式初始化表达式。
// 本例的初始化阶段由单个 goroutine 串行执行，因此不需要用锁保护它。
var sequence int

func init() {
	TraceEvent("utils: init（虽然被多个包导入，但整个程序中只初始化一次）")

	// init 中可以使用 go f() 启动新 goroutine；它可能和后续初始化代码并发执行。
	// 但是初始化框架本身仍按单 goroutine 串行调用各个 init，并等待当前 init 返回。
	// 本例不启动 goroutine，以便输出稳定、直观；真实代码也不应依赖 init 启动任务的完成时机。
}

// TraceValue 记录一个包级变量正在初始化，并原样返回 value，方便写在初始化表达式中。
func TraceValue(label string, value int) int {
	sequence++
	fmt.Printf("%02d  包变量  %-58s = %d\n", sequence, label, value)
	return value
}

// TraceEvent 记录 init 或 main 函数的执行时刻。
func TraceEvent(label string) {
	sequence++
	fmt.Printf("%02d  函数    %s\n", sequence, label)
}
