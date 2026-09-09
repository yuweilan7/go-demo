package main

import (
	"go-demo/examples/internals/packageinit/package1"
	"go-demo/examples/internals/packageinit/utils"
	"go-demo/examples/internals/packageinit/zzside"
)

var MainValue2 = utils.TraceValue("main.MainValue2（b_second.go）", package1.V2+10)
var MainSide = utils.TraceValue("main.MainSide（b_second.go）", zzside.Value+1)

func init() {
	utils.TraceEvent("main: init #3（b_second.go）")
}
