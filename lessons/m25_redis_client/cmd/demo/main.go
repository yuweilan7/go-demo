// M25 的进程入口只负责配置、总预算和退出码；Redis 的生命周期由 lesson.Run 拥有。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m25_redis_client/internal/lesson"
)

// main 由 Go runtime 调用。把可能失败且需要 defer 收尾的工作放入 execute，
// 等 execute 的 defer 全部执行后才 os.Exit；在资源函数里 os.Exit 会跳过 defer。
func main() {
	if err := execute(); err != nil {
		fmt.Fprintln(os.Stderr, "M25 未完成：", err)
		os.Exit(1)
	}
}

// execute 的参数来自 flag/env，返回错误给 main 决定退出码。
// 它拥有总 ctx 的 cancel；Run 拥有 client、精确键清单和所有实验任务。
func execute() error {
	index := flag.String("index", lesson.DefaultIndex(), "无秘密服务索引；也可设 GOLEARN_SERVICE_INDEX")
	part := flag.String("part", "all", "core=键/TTL/并发/原子边界；all=再加认证/连接池/响应故障")
	reportPath := flag.String("report", ".local/latest.json", "无秘密运行报告路径")
	flag.Parse()
	if *part != "core" && *part != "all" {
		return fmt.Errorf("part 必须是 core 或 all")
	}
	// LoadConfig 在创建 client 或连接之前检查 READY 与显式 loopback，失败不拨号。
	// := 在 execute 作用域声明 cfg 和 err；下方 if 中的 err 则只在那个 if 的作用域存在。
	cfg, err := lesson.LoadConfig(*index)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel() // 释放总预算计时器；这句通知不等于任何 goroutine 的 join。
	report, runErr := lesson.Run(ctx, cfg, os.Stdout, *part == "all")
	// Run 返回时已经执行精确键清理和 Close/join；失败报告也必须保留真实收尾结果。
	writeErr := lesson.WriteReport(*reportPath, report)
	if runErr != nil {
		return fmt.Errorf("运行/报告未完成：%w", errors.Join(runErr, writeErr))
	}
	return writeErr
}
