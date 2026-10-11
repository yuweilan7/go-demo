// fault 是显式错误入口；默认demo通过父进程预算监督它，不直接用于长期服务。
package main

import (
	"fmt"
	"os"

	"go-demo/lessons/m18_gin_middleware/internal/lab"
)

// main只接受两个故障模式，具体panic在RunChild创建的新goroutine里发生。
// runtime会异常终止此子进程；父owner的Command.Run负责等待并验证退出，不留下后台进程。
func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "用法: fault goroutine-panic|copy-write")
		os.Exit(1)
	}
	if err := lab.RunChild(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
