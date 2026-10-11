//go:build faultcases

// fault 是显式故障入口，不能直接替代 demo。cmd/probe 建立有截止时间的子进程调用它。
package main

import (
	"fmt"
	"go-demo/lessons/m14_sync_memory_races/internal/faults"
	"os"
)

// main 只负责参数校验和故障派发。runtime fatal 不能由 recover 转成普通 error。
func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "用法：fault <模式>，推荐通过 cmd/probe 运行")
		os.Exit(2)
	}
	if err := faults.Run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
