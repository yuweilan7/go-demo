package main

import "fmt"

// init 是一个特殊函数：不能由我们传入参数，也不需要手动调用。
// Go 会在进入 main 之前自动执行它。
//
// init 适合完成少量、必要的初始化。真实项目中不要把大量业务逻辑藏在这里，
// 否则程序的执行顺序会变得难以理解和测试。
func init() {
	fmt.Println("[启动顺序] lifecycle.go 的 init() 先执行")
}
