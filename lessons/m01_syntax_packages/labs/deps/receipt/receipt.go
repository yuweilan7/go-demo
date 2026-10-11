// Package receipt 是 app 的另一个直接依赖，对 rules 声明更高的 v1.2.0 最低要求。
package receipt

import "example.com/m01/rules"

// Describe 由 app 调用，与 quote 一起读 rules；不会各自私有加载不同版本的同路径模块。
func Describe(id string) string {
	return id + " receipt via " + rules.Describe()
}
