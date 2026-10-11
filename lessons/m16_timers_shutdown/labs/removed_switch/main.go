// 此隔离 module 是故意的编译反例：Go1.27 已删除恢复旧异步 timer channel 的设置。
// 正常课程不导入它；运行 go -C labs/removed_switch run . 应在检查 go.mod 时失败。
package main

import "fmt"

func main() {
	fmt.Println("如果到达这里，必须重新核对实际 Go 版本和 removed setting 的契约")
}
