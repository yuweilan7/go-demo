// Package sideeffect 由 cmd/demo 的空白导入启用，仅演示 import 的初始化副作用。
package sideeffect

import "go-demo/lessons/m01_syntax_packages/startup/registry"

// init 自动执行一次；registry 是本包的依赖，所以其变量和 init 已在本函数前完成。
// “空白导入”丢弃包名，仍保留初始化；不要把这种隐含注册当作 Spring 式依赖注入。
func init() {
	registry.Register("demo-validator")
}
