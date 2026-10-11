// demo 是本课唯一入口；资源 owner 在 Run 中，main 必须等 Run 收尾后才 os.Exit。
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m26_cache_consistency/cachecourse"
)

// main 由 Go runtime 调用。总预算限制业务实验；Run 的清理另用 Background 预算，
// 所以业务超时不会使 DROP、DEL 和 join 一开始就因已取消的 ctx 失败。
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	err := cachecourse.Run(ctx, os.Stdout)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "M26 FAILED:", err)
		os.Exit(1)
	}
	fmt.Println("M26 COMPLETE：真实断言与精确清理完成；一致性范围见 README")
}
