// Package zzside 是 main 的另一个直接依赖，与 package1 之间没有依赖关系。
package zzside

import "go-demo/examples/internals/packageinit/utils"

var Value = utils.TraceValue("zzside.Value（main 的独立依赖分支）", 100)

func init() {
	utils.TraceEvent("zzside: init")
}
