package main

import (
	"flag"
	"fmt"
	"os"

	"go-demo/lessons/m09_generics_reflection/internal/lesson"
)

// main 是读课入口。-part 选择分段；默认 core 已包含四个必需概念与故障/修复。
// library 扩展调用真实锁定版本的 backoff；all 将核心和真实库一次跑完。
func main() {
	part := flag.String("part", "core", "core|generics|options|iter|reflect|library|all")
	flag.Parse()
	if err := lesson.Run(os.Stdout, *part); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
