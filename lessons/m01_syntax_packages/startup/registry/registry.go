// Package registry 只记录教学启动事件，不提供真实业务依赖容器。
package registry

import "fmt"

// names 的初始化发生在本包 init 前。initialize 的输出使该时机可观察。
// 这是包级状态，进程仅有这一份；本课 main 后不并发写它。
var names = initialize()

// initialize 由运行时的包变量初始化过程调用；返回本包持有的名称 slice。
func initialize() []string {
	fmt.Println("[L00 init] registry 变量初始化")
	return []string{}
}

// init 由 Go 启动过程调用，不能像普通函数一样手动调用或传入参数。
func init() {
	fmt.Println("[L00 init] registry.init")
}

// Register 仅由 sideeffect.init 调用，记录一次 import 引发的副作用。
// 不去配置数据库或创建后台任务；业务 Service 仍由 New 显式传依赖。
func Register(name string) {
	names = append(names, name)
	fmt.Printf("[L00 init] sideeffect 注册 %q\n", name)
}

// Names 由 main 变量初始化和运行入口读取；副本不允许 caller 改写本包状态。
func Names() []string {
	return append([]string(nil), names...)
}
