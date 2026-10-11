// demo 是 M13 的进程入口：它拥有整批实验的总期限和退出码。
// 所有业务函数在 internal/lesson；这个入口不创建脱离 owner 的后台任务。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m13_context_budgets/internal/lesson"
)

// main 由 Go runtime 调用。scenario 来自命令行，输出交给 stdout。
// 发生失败时先让 Run 返回并执行各实验 defer，再设置非零退出码。
func main() {
	scenario := flag.String("scenario", "all", "all、tree、http、budgets、traps、send、lifecycle")
	flag.Parse()
	// Background 是进程 owner 的起点，不是每一层业务重新获取预算的工具。
	// 整批最多 40 秒；Run 内还给每组实验 8 秒 watchdog，正常总耗时约几秒。
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	err := lesson.Run(ctx, os.Stdout, *scenario)
	// os.Exit 不执行 defer，因此明确在它之前释放主入口持有的 timer。
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "M13 FAILED:", err)
		os.Exit(1)
	}
	fmt.Println("M13 PASS：业务事实、错误分类和 owner 收尾断言全部成立")
}
