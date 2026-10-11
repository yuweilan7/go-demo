// nilpanic 是明确的故障入口：只有显式运行这个命令才会产生未恢复的 nil 字段访问 panic。
package main

import "go-demo/lessons/m02_values_methods/internal/lesson"

// main 不设置 recover，因此 SetStatus 中的 panic 展开到运行时并以非零退出码结束进程。
// 它不创建资源或 goroutine；demo 的 E06 有同一错误的有界恢复版本和 nil 守卫修复。
func main() {
	var missing *lesson.Order // 指针零值为 nil，静态类型依然是 *Order。
	missing.SetStatus("paid") // 可以进入指针接收者方法；方法内部解引用 nil 时 panic。
}
