// demo 是 M08 的核心实验入口，三个实验都使用同一份合成订单契约。
// 所有处理同步发生在 main goroutine；无网络、数据库、后台任务和需要关闭的连接。
package main

import (
	"fmt"
	"os"
	"runtime"
)

// main 只安排实验顺序并处理不可预期失败；每个危险反例在自己的函数里观察并修复。
// run 返回错误时入口打印后退出，避免把失败误报为实验完成。
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "M08 失败:", err)
		os.Exit(1)
	}
}

// run 是完整调用链的 owner，不启动 goroutine，也没有独立运行的实验进程。
// 每个子函数返回后才能进入下一段；最后一行只表示全部本地实验正常返回。
func run() error {
	fmt.Printf("M08 runtime=%s contract=order.v1\n", runtime.Version())
	if err := jsonDemo(); err != nil {
		return fmt.Errorf("E01 JSON: %w", err)
	}
	if err := moneyDemo(); err != nil {
		return fmt.Errorf("E02 金额: %w", err)
	}
	if err := timeDemo(); err != nil {
		return fmt.Errorf("E03 时间: %w", err)
	}
	fmt.Println("\nM08 核心实验完成：副作用仅为本进程的订单值变化。")
	return nil
}
