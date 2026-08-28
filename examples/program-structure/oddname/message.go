// 这个目录故意叫 oddname，包却故意叫 classroom。
// 这用来证明“目录名和包名承担不同职责”：
//
//   - 目录路径决定别人从哪里导入它：go-demo/examples/program-structure/oddname
//   - package 声明决定这个包在 Go 代码中的默认名称：classroom
//
// 二者技术上可以不同，但会增加理解成本，正常项目通常让它们保持一致。
package classroom

// Explain 返回这个反例想说明的规则。
// Explain 大写开头，因此导入该包后可以从包外调用。
func Explain() string {
	return "目录 oddname 中声明了 package classroom：可以编译，但命名一致更清晰。"
}
