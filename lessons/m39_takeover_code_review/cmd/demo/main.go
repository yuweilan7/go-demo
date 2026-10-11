// demo是有界课程入口，不会创建常驻服务。run退出并完成所有defer之后main才退出。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m39_takeover_code_review/internal/review"
)

// run解析的故障选择只来自本机课程owner。bad仅供失败用例，不开放成外部用户配置；
// 维护入口Verify只读精确账本，不删除中间状态或重新派发业务。
func run() error {
	scenario := flag.String("scenario", "all", "all/helpers/isolation/transactions/contexts/offsets/pools/unknown/owners")
	candidate := flag.String("candidate", "fixed", "fixed或显式失败回归bad")
	verify := flag.String("verify-cleanup", "", "只读精确owned.json")
	flag.Parse()
	if *candidate != "fixed" && *candidate != "bad" {
		return fmt.Errorf("candidate必须fixed/bad")
	}
	if *candidate == "bad" && *scenario != "isolation" {
		return fmt.Errorf("bad仅用于isolation失败不变量")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	if *verify != "" {
		return review.Verify(ctx, *verify, os.Stdout)
	}
	return review.Run(ctx, *scenario, *candidate == "bad", os.Stdout)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "M39 FAILED:", err)
		os.Exit(1)
	}
}
