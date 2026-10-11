// demo 是本课唯一正常入口。它读取安装者的私有索引，再执行真实 MySQL 实验。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"go-demo/lessons/m20_sql_pools/internal/course"
)

// main 只决定进程退出码；execute 先返回，里面注册的 defer 才能全部执行。
// 若在持有 Rows/Tx 的函数中直接 os.Exit，Go 不会替我们执行那些 defer。
func main() {
	if err := execute(); err != nil {
		fmt.Fprintln(os.Stderr, "M20 未通过：", err)
		os.Exit(1)
	}
}

// execute 拥有整次课程的取消范围。Ctrl+C 关闭 signalCtx.Done，75 秒是全程上限；
// Run 的独立清理预算不会从这个可能已经取消的 ctx 派生。
func execute() error {
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, 75*time.Second)
	defer cancel()
	config, err := course.LoadConfig(course.ServiceIndexPath())
	if err != nil {
		return err
	}
	return course.Run(ctx, config, os.Stdout)
}
