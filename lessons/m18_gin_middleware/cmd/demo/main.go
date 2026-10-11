// demo 是整课正常入口；所有危险panic隔离到有预算的子进程，不终止此进程。
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m18_gin_middleware/internal/lab"
)

// main拥有整轮90秒工作预算，lab.Run拥有每个HTTP服务/任务的具体收尾。
// 不用log.Fatal跳过defer：run返回时资源已清理，然后main才选择退出码。
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	err := lab.Run(ctx, os.Stdout)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "M18失败:", err)
		os.Exit(1)
	}
}
