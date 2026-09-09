// Package package1 依赖 package2，因此 package2 必须完整初始化后才能轮到本包。
package package1

import (
	"go-demo/examples/internals/packageinit/package2"
	"go-demo/examples/internals/packageinit/utils"
)

var V1 = utils.TraceValue("package1.V1", package2.Value1+10)
var V2 = utils.TraceValue("package1.V2", package2.Value2+10)

func init() {
	utils.TraceEvent("package1: init")
}
