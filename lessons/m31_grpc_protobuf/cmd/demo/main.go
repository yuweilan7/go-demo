// demo 在一个进程中拥有真实 TCP gRPC server/client；二者由协议连接，不直接调用业务 handler 冒充 RPC。
package main

import (
	"context"
	"fmt"
	"os"

	"go-demo/lessons/m31_grpc_protobuf/internal/lesson"
)

// main 是整个实验的最终 owner；Run 内部先清理连接/listener/goroutine，再把结果交回这里决定进程退出码。
func main() {
	// := 在 main 的当前作用域声明 report 和 err；Run 是同步调用，返回时其清理 defer 已执行。
	report, err := lesson.Run(context.Background(), os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Printf("[E99] ASSERT connection_released=%t final_stock=%d\n", report.ConnectionReleased, report.FinalStock)
}
