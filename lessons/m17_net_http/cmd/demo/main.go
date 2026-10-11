// demo 是课程唯一正常入口；故障由 lab 中有上限的协议实验触发，进程仍应正常退出。
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m17_net_http/internal/lab"
)

// main 拥有整轮实验的截止时间。Run 同步阻塞当前 goroutine，内部的 server/client
// goroutine 都由相应实验等待退出；Go 不要求 async 关键字，也不靠 main 返回等待后台任务。
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := lab.Run(ctx, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "M17 FAIL:", err)
		os.Exit(1)
	}
}
