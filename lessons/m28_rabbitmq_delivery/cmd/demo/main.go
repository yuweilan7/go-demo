// demo 是课程唯一正常入口。所有故障都有隔离的 scenario 名称、超时和真实断言；
// “故障例子得到预期坏结果”才算实验通过，不把缺失 broker 当成跳过成功。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"go-demo/lessons/m28_rabbitmq_delivery/internal/lab"
)

// main 只负责命令/进程退出；run 返回后它才调用 os.Exit，避免跳过 run 的 defer。
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "M28 FAIL:", err)
		os.Exit(1)
	}
}

// run 是最上层 owner，接收 Ctrl+C 或 90s 总预算，并等待全部实验/清理返回。
// -only=core 给第一次阅读保留四个核心实验；默认 all 运行全部必需验收。
func run() error {
	only := flag.String("only", "all", "all/core/confirm/return/cancel/recovery/duplicate/retry/multiple/tag")
	flag.Parse()
	cfg, err := lab.LoadConfig()
	if err != nil {
		return err
	}
	interrupt, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(interrupt, 90*time.Second)
	defer cancel()
	return lab.Run(ctx, cfg, *only, os.Stdout)
}
