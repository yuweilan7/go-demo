// demo 是 M02 的正常入口。默认按 E01 到 E06 串行观察，不启动外部服务或后台任务。
package main

import (
	"flag"
	"fmt"
	"os"

	"go-demo/lessons/m02_values_methods/internal/lesson"
)

// main 由 Go 运行时调用。它选择实验并把 os.Stdout 交给教学层输出；标准输出归进程所有。
// -case 是实验筛选开关，flag.Parse 从命令行读取它；无参数时运行全部安全观察。
func main() {
	// := 在 main 的函数块内声明 experiment，类型由右边推断为 *string。
	// flag.String 返回 *string：flag 包解析后把选项写入这个 string，*experiment 读取最终值。
	experiment := flag.String("case", "all", "all/core/methods/allocation/clone/counter/nil")
	flag.Parse()
	if err := lesson.Run(os.Stdout, *experiment); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2) // 只有入口决定进程退出；业务层把未知实验名作为 error 返回。
	}
}
