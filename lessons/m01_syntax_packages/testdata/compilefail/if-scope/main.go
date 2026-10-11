// 显式故障输入：正常入口不会导入 testdata。
package main

import "fmt"

// main 试图在 if 结束后读取其初始化变量 err，编译期便失败，业务代码不会开始执行。
func main() {
	if err := fmt.Errorf("数量为零"); err != nil {
		fmt.Println(err)
	}
	fmt.Println(err) // 错误：if 初始化变量的作用域已结束；应在 if 内处理或提前 var 声明。
}
