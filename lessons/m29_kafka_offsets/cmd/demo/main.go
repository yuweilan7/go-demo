// M29 的命令入口只负责参数、总预算和退出码；Kafka/文件的 owner 在 course.Run 中。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go-demo/lessons/m29_kafka_offsets/internal/course"
)

// main 由 Go 运行时调用。execute 先返回并执行完 defer，再将结果交给 os.Exit，
// 因而失败退出不会绕过客户端、goroutine 和文件的清理。
func main() { os.Exit(execute()) }

// execute 读取调用者的命令行。默认 core 连接真实 Kafka；offline 仅演示连续水位，
// 不会把纯 Go 输出称为 Kafka 验收。总预算限制实验，清理另取独立预算。
func execute() int {
	// flag.String/Duration返回指针：Parse以后通过*mode/*budget读取调用者填写的值。
	// :=的变量只在execute作用域；context.Context接口值交给Run，共享同一取消通知。
	mode := flag.String("mode", "core", "core：真实 Kafka；offline：纯 Go 水位；broker-fault：受安装者协调的故障")
	index := flag.String("index", course.DefaultIndex(), "无秘密服务索引；也可用 GOLEARN_SERVICE_INDEX")
	budget := flag.Duration("budget", 2*time.Minute, "实验总预算，不含独立清理预算")
	control := flag.String("control", "", "broker-fault 的独占控制目录；安装者写 stopped/resumed 标记")
	flag.Parse()
	if *budget <= 0 {
		fmt.Fprintln(os.Stderr, "budget 必须为正")
		return 1
	}
	// NotifyContext 的 stop 释放 signal 注册；cancel 释放本次 deadline timer。
	// 两个通知都不等于 join：Run 的各资源 owner 会另行等待 Done。
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, *budget)
	defer cancel()
	if err := course.Run(ctx, *mode, *index, *control, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "M29 失败（清理错误也会合并到这里）：", err)
		return 1
	}
	return 0
}
