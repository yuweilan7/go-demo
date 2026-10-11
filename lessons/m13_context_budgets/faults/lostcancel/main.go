//go:build lostcancel

// lostcancel 是显式静态故障入口，仅 go vet -tags=lostcancel 会纳入本目录。
// 正常 demo/test/vet 不编译它；它专门展示完全丢弃 cancel 的诊断。
package main

import (
	"context"
	"fmt"
	"time"
)

// main 的错误是把取消权丢到 _：业务已经结束，child/timer 却等一分钟或父取消。
// 不用它证明具体内存字节数/goroutine 数，Go 内部实现可能变；正常反例由 traps 的
// recovery owner 保留 cancel，并观察迟释放范围后明确回收。
func main() {
	ctx, _ := context.WithTimeout(context.Background(), time.Minute)
	fmt.Println("故意忘 cancel，业务已结束但 ctx.Err 仍为", ctx.Err())
	// 修复应为 ctx, cancel := ...；取得后立即 defer cancel()，成功路径也会释放。
}
