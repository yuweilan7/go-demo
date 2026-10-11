// demo 是 M21 唯一正常入口：故障是有界实验，任何断言失败都会返回非零。
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m21_transactions_defer_panic/txcourse"
)

// main 只拥有整个实验预算；Run 拥有池、表、任务和代理，并在返回前收尾。
// os.Exit 不执行 defer，所以 cancel 在判断失败之前显式调用。
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	err := txcourse.Run(ctx, os.Stdout)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "M21 FAILED:", err)
		os.Exit(1)
	}
	fmt.Println("M21_COMPLETED: 业务 SELECT、通信故障与最终清理全部通过")
}
