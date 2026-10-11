// demo 是 M38 的组合根：正常模式拥有整场实验，child 模式只拥有一个故障窗口。
// 两种模式调用的订单业务都是 M37 的公开库，进程隔离不会生成第二套业务实现。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"go-demo/lessons/m38_failure_recovery_lab/internal/lab"
)

// main 仅在 run 的全部 defer 已执行后决定退出码。os.Exit 不执行 defer，因此不能
// 将它放在取得连接/进程的工作函数里；被父进程真实 Kill 的 child 是明确例外。
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "M38 FAILED:", err)
		os.Exit(1)
	}
}

// run 的参数来自 CLI。SignalContext 是通知范围，Timeout 是总工作预算；真正退出
// 还由 lab 的 owner 以新的 Background 清理预算等待，不能用 signal 代替 join。
func run() error {
	scenario := flag.String("scenario", "all", "all/core/kills/proxies")
	child := flag.String("child", "", "可信父进程传入的 child spec 路径")
	verify := flag.String("verify-cleanup", "", "只读核验本课精确 manifest")
	flag.Parse()
	life, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(life, 240*time.Second)
	defer cancel()
	if *child != "" {
		return lab.Worker(ctx, *child, os.Stdin, os.Stdout)
	}
	if *verify != "" {
		return lab.Verify(ctx, *verify, os.Stdout)
	}
	return lab.Run(ctx, *scenario, os.Stdout)
}
