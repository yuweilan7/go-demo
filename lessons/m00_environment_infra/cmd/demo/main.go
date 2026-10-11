// package main 声明一个可执行包。main.go 与 label.go 一起构成这个目录的包，
// 文件名不是包身份：go run ./cmd/demo 会选整个包，go run main.go 只选指定文件。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m00_environment_infra/internal/environment"
)

// main 是 Go 运行时调用的程序入口。execute 返回前已经执行所有 defer；
// 此时再 os.Exit 把 0/1 交给 PowerShell，不会绕过 execute 内部的资源清理。
func main() {
	os.Exit(execute())
}

// execute 由 main 同步调用。它拥有整个演示的超时范围，把结果写到终端。
// flag 的参数来自命令行；默认运行只读本机 Go 信息，不连接服务器。
// 只有显式 -tcp 才检查索引登记的 loopback 端口，而且不会发送鉴权或业务命令。
func execute() int {
	tcp := flag.Bool("tcp", false, "额外做索引中 Redis/PG 的有界 TCP 检查")
	index := flag.String("index", "infra/endpoints.json", "无凭据的端点索引路径")
	flag.Parse()

	// := 在 main 当前作用域声明 ctx、cancel。ctx 是这次演示共享的取消范围，
	// cancel 是由 main 持有的清理函数。到期通知不能撤销已经完成的网络握手。
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel() // 无论成功还是返回错误，都释放本次 deadline 的 timer。

	if err := run(ctx, *tcp, *index); err != nil {
		// run 已返回，内部连接/子进程已收尾。return 1 时先执行本函数的
		// defer cancel，再回到 main 的 os.Exit，保留错误状态和清理顺序。
		fmt.Fprintln(os.Stderr, "演示失败：", err)
		return 1
	}
	return 0
}

// run 由 main 同步调用：前半段读取编译事实与当前 Go 命令的 module；
// 后半段按 tcp 开关决定是否连接现有 loopback 端口，错误沿返回值回到 main。
// ctx 由 main 创建和取消，本函数不另起后台任务，也不接管共享 SSH 隧道。
func run(ctx context.Context, tcp bool, index string) error {
	fmt.Println(lessonLabel())
	if err := environment.Describe(ctx, os.Stdout); err != nil {
		return err
	}
	if !tcp {
		fmt.Println("[边界] 未连接中间件；需要 TCP 检查时加 -tcp，需要协议鉴权时运行 scripts/check-infra.ps1。")
		return nil
	}
	endpoints, err := environment.ReadEndpoints(index)
	if err != nil {
		return err
	}
	for _, endpoint := range endpoints {
		if !endpoint.ReadOnlyTCP {
			fmt.Printf("[跳过] %s: %s\n", endpoint.ID, endpoint.State)
			continue
		}
		// 每个探测同步完成后才探下一个：最多两个连接、单次最多两秒，
		// 不创建无上限 goroutine。返回的 Evidence 只包含它实际证明的层次。
		evidence := environment.ProbeTCP(ctx, endpoint.Address)
		fmt.Printf("[TCP] %s address=%s connected=%t authenticated=%t business=%t detail=%s\n",
			endpoint.ID, endpoint.Address, evidence.Connected, evidence.Authenticated,
			evidence.Business, evidence.Detail)
	}
	return nil
}
