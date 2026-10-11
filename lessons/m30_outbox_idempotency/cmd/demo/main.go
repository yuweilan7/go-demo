package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m30_outbox_idempotency/internal/lab"
)

// execute是main的可清理边界。子进程只在父传入无秘密spec路径时运行一个窗口；
// 主进程3分钟业务预算，Run返回前已独立清理/join，main之后才决定退出码。
func execute() error {
	if spec := os.Getenv("M30_CHILD_SPEC"); spec != "" {
		return lab.Child(spec, os.Stdout)
	}
	registry := flag.String("recover-registry", "", "已结束失败run的resources.json，配合精确group")
	group := flag.String("recover-group", "", "实际失败日志中的本课精确group，原owner必须已退出")
	verify := flag.String("verify-cleanup", "", "只读逐项核验已结束run登记资源不存在")
	flag.Parse()
	if *verify != "" {
		return lab.VerifyRecordedAbsent(*verify, os.Stdout)
	}
	if *registry != "" || *group != "" {
		if *registry == "" || *group == "" {
			return fmt.Errorf("恢复必须同时提供registry和精确group")
		}
		return lab.RecoverRecordedGroup(*registry, *group, os.Stdout)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	return lab.Run(ctx, os.Stdout, lab.Launcher{})
}

// main只报告完成结果，不在中间层os.Exit跳过defer。实验中的强杀仅针对独立child。
func main() {
	if err := execute(); err != nil {
		fmt.Fprintln(os.Stderr, "M30_FAILED:", err)
		os.Exit(1)
	}
	fmt.Println("M30_PASS: 当前入口及其资源清理已完成，具体实验事实见上文")
}
