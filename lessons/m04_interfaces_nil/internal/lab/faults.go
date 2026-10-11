// Package lab 放显式教学实验；业务 usecase 从不依赖这里的危险写法或 recover。
package lab

import "fmt"

// ObservePanic 只由故障实验和断言调用，action 是 caller 给出的一个有限、同步操作。
// 具名结果 message 初始为空；发生 panic 时，defer 改写它，再返回实验入口打印事实。
// 本函数没有重试，没有 goroutine，也不会把业务 panic 伪装成正常业务结果。
func ObservePanic(action func()) (message string) {
	// 注册清理位置时不执行函数体。action 正常返回或 panic 展开当前调用栈时才执行它。
	// recover 必须在同一 goroutine 的这个 deferred 函数中直接调用，才能截住当前 panic。
	defer func() {
		if cause := recover(); cause != nil {
			message = fmt.Sprint(cause)
		}
	}()
	action()
	// panic 时执行不会到达这一行；被恢复后的 ObservePanic 直接返回到它的 caller。
	// action 里 panic 后的语句也不会恢复执行。
	return ""
}
