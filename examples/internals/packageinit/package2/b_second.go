package package2

import "go-demo/examples/internals/packageinit/utils"

// Value2 没有依赖尚未初始化的同包变量，所以它比源码位置更早的 Value1 先执行。
var Value2 = utils.TraceValue("package2.Value2（b_second.go，先满足依赖）", 30)

// 当前 go 工具链通常按文件名词法序把文件交给编译器，所以这个 init 位于
// a_first.go 的两个 init 之后。但语言规范真正描述的是“交给编译器的文件顺序”，
// 并只建议构建系统采用文件名词法序；业务逻辑不应依赖跨文件 init 的文件名排序。
func init() {
	utils.TraceEvent("package2: init #3（b_second.go）")
}
