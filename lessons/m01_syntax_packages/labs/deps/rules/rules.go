// Package rules 是合成传递依赖，源码由当前 main module 的本地 replace 提供。
package rules

// Describe 由 quote/receipt 同步调用，不读配置、不启动任务。
// 字符串强调实际源码是本地替换物；它不能独立证明发布标签的内容，版本选择看 go list。
func Describe() string {
	return "local-rules-source"
}
