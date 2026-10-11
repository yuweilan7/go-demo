package main

import (
	"fmt"

	"go-demo/lessons/m01_syntax_packages/startup/registry"
	_ "go-demo/lessons/m01_syntax_packages/startup/sideeffect"
)

// 包变量先于本包 init；被导入的 sideeffect 已初始化，所以可以读取注册结果。
// 这里的声明依赖用于教学输出，不用它隐式创建 Service 或仓库。
var startupNames = describeStartup()

// describeStartup 由本包变量初始化调用，返回一个独立的名称快照。
func describeStartup() []string {
	names := registry.Names()
	fmt.Printf("[L00 init] main 变量初始化：names=%v\n", names)
	return names
}

// init 在 main 之前执行，文件名 startup.go 本身不是业务顺序契约。
func init() {
	fmt.Println("[L00 init] main.init")
}
