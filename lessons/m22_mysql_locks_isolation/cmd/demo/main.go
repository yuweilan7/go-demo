// demo 是 M22 的正常入口；反例是有界实验，预期失败由独立业务断言确认。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m22_mysql_locks_isolation/lockcourse"
)

// main 拥有整课 timer，Run 拥有数据库、表和参与者。只有 Run 完成收尾后才
// os.Exit；Exit 不执行 defer，因此这里显式 cancel，不能让它跳过资源清理。
func main() {
	part := flag.String("part", "all", "all 或 stock（15分钟核心阅读实验）")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	err := lockcourse.Run(ctx, os.Stdout, *part)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "M22 FAILED:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "M22 实验及所有 owner 收尾完成")
}
