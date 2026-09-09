package main

import (
	"go-demo/examples/internals/packageinit/package1"
	"go-demo/examples/internals/packageinit/utils"
)

var MainValue1 = utils.TraceValue("main.MainValue1（a_first.go）", package1.V1+10)

// 即使 init 写在文件名靠前的 a_first.go 中，它也要等待 main 包所有包变量完成。
// 所以 b_second.go 中的 MainValue2 和 MainSide 都会在本函数之前初始化。
func init() {
	utils.TraceEvent("main: init #1（a_first.go）")
}

func init() {
	utils.TraceEvent("main: init #2（a_first.go）")
}
