// demo 是课程入口：解析实验编号，把控制权交给 lesson.Run，最后处理返回的错误。
// 全课同步执行，没有 goroutine、外部连接或后台工作，main 返回便结束本次实验。
package main

import (
	"flag"
	"fmt"
	"os"

	"go-demo/lessons/m03_collections_aliasing/internal/lesson"
)

// main 由 Go runtime 调用，读取本次实验选择；所有业务步骤由 Run 同步完成后返回。
// stdout/stderr 是进程提供的输出端点，本入口不关闭它们，也不持有待收尾的业务连接。
func main() {
	// flag.String 返回 *string；Parse 把命令行的 -case 值写到它指向的变量。
	// 默认 all 包含安全观察、逻辑错误及修复；真正 panic 只有显式 nil-map 入口。
	caseName := flag.String("case", "all", "all/core/slices/cache/collections/loops/text/nil-map")
	flag.Parse()
	if err := lesson.Run(os.Stdout, *caseName); err != nil {
		// Run 的未知编号错误、业务断言错误或 Writer 错误沿普通返回值到这里。
		// 此时 Run 已返回，没有仍需 main 等待的任务；非零退出让脚本能识别失败。
		fmt.Fprintln(os.Stderr, "M03 失败:", err)
		os.Exit(1)
	}
}
