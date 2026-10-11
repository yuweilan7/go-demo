package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m23_gorm_semantics/gormcourse"
)

// main 是唯一进程入口：给整课一个上限，把输出交给 stdout。
// Run 自己拥有数据库、AMQP 和任务资源；它完成退出清理后 main 才决定退出码。
// os.Exit 不执行 defer，所以绝不能在 Run 的中途用它跳过资源清理。
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	err := gormcourse.Run(ctx, os.Stdout)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "M23 FAILED:", err)
		os.Exit(1)
	}
	fmt.Println("M23 PASS：SQL、事务、AMQP副作用与精确资源清理全部通过")
}
