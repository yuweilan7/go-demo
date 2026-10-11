// demo 是唯一进程入口：加载只读接入索引，把总预算传给课程 owner。
// 本目录是独立 module；请运行整个 package，而不是只运行某个内部源码文件。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"go-demo/lessons/m24_schema_queries_migrations/internal/course"
)

// main 不拥有数据库内部资源。Run 负责连接、表、任务和清理；它返回后才可退出。
// os.Exit 不执行 defer，所以把有 defer 的工作放在 execute，等其返回再调用 Exit。
func main() {
	if err := execute(); err != nil {
		fmt.Fprintln(os.Stderr, "M24 未通过：", err)
		os.Exit(1)
	}
}

// execute 用信号 ctx 通知正在运行的 SQL，然后由 Run 的退出路径等待 worker 和清理表。
// 90 秒是整课预算，DDL 等待另有会话 2 秒上限；cancel 是通知，不是等待任务完成。
func execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	config, err := course.LoadConfig(course.ServiceIndexPath())
	if err != nil {
		return err
	}
	return course.Run(ctx, config, os.Stdout)
}
