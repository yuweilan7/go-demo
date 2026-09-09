// Package punctuation 是实验中的“传递依赖”。
// app 不直接导入这个包，而是通过 greeting 间接使用它。
package punctuation

// Finish 为一句话补上中文感叹号。
// 这里故意保持实现简单，让注意力集中在模块依赖关系，而不是业务逻辑上。
func Finish(sentence string) string {
	return sentence + "！"
}
