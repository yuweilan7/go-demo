package main

import (
	"fmt"

	"go-demo/examples/internals/packageinit/utils"
)

// 本示例的依赖图：
//
//	main ──> package1 ──> package2 ──> utils
//	  └────> zzside ─────────────────> utils
//
// “最深层包先初始化”适合帮助理解一条依赖链，但它不是完整算法。
// 精确规则是：先按 import path 排好全部包；每一步选择第一个“所有依赖均已初始化”的包。
// 因此 package1 与 zzside 这种互不依赖的分支，不按 main.go 中 import 的书写顺序决定先后。
func main() {
	utils.TraceEvent("main: main")

	fmt.Println("\n最终值：")
	fmt.Printf("MainValue1=%d, MainValue2=%d, MainSide=%d\n", MainValue1, MainValue2, MainSide)

	fmt.Println("\n从上面的序号可以直接观察到：")
	fmt.Println("1. 被导入的包先于导入它的包初始化；每个包只初始化一次。")
	fmt.Println("2. 每个包先完成全部包变量，再依次执行 init。")
	fmt.Println("3. 包变量有依赖时，依赖关系优先于源码声明顺序。")
	fmt.Println("4. 所有包初始化完毕后，才调用 main.main。")
	fmt.Println("5. init 可以启动 goroutine，但不要假定该 goroutine 会在后续 init 或 main 前完成。")
}
