// Package package2 是 package1 的直接依赖。
package package2

import "go-demo/examples/internals/packageinit/utils"

// Value1 在文件名靠前的 a_first.go 中声明，却依赖 b_second.go 中的 Value2。
// 因而 Value1 暂时“不就绪”，Go 必须先初始化 Value2，再回来初始化 Value1。
// 这说明包变量不是机械地逐行执行：依赖关系优先于声明顺序。
var Value1 = utils.TraceValue("package2.Value1（a_first.go，依赖 Value2）", Value2-10)

// 同一个文件可以声明多个 init。它们不能带参数、不能返回值，也不能被手动调用。
func init() {
	utils.TraceEvent("package2: init #1（a_first.go）")
}

func init() {
	utils.TraceEvent("package2: init #2（a_first.go）")
}
