// Package greeting 是 app 直接使用的依赖模块。
package greeting

import "example.com/modulelab/punctuation"

// Hello 返回一条完整问候语。
//
// 从 app 的视角看，greeting 是直接依赖；punctuation 是传递依赖。
// 注意：Go 源码 import 的是“包路径”，而 go.mod require 的是“模块路径”。
// 这个实验中每个模块恰好只有一个包，所以两种路径长得相同，但概念并不相同。
func Hello(name string) string {
	return punctuation.Finish("你好，" + name)
}
