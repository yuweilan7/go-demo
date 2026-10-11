// demo 是 M05 的唯一命令入口；默认串行执行全部有界实验。
// case 参数选择同一课程的阅读层次，不需要自行补代码或隐藏答案。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m05_errors_contracts/internal/course"
)

// main 是整个演示的 owner：解析输入、建立总截止时间，并在最终边界报告错误。
// Run 内部的 repository 不打印 error；真正失败只在这里给 stderr 和非零退出码。
func main() {
	choice := flag.String("case", "all", "all/core/io/cleanup/unknown/bad")
	flag.Parse()
	// *choice 解引用 flag.String 返回的指针，取用户选择的字符串。
	// 5 秒为网络扩展提供总上限；串行纯内存实验本身没有阻塞或无限重试。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := course.Run(ctx, os.Stdout, *choice)
	cancel() // 在可能调用 os.Exit 前显式释放 timer；os.Exit 不执行 main 的 defer。
	if err != nil {
		fmt.Fprintln(os.Stderr, "M05 验证失败:", err)
		os.Exit(1)
	}
}
