package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m09_generics_reflection/internal/compileprobe"
)

// main 是显式编译边界入口；退出码 0 表示接受/拒绝均符合预期，不表示坏片段被接受。
// 全程最多 60 秒，每个编译 8 秒；Run 拥有并清理模块 .local 内的临时目录。
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := compileprobe.Run(ctx, ".local", os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
