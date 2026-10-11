// demo 是课程进程入口；所有业务实验同步返回以后才决定退出码，避免 os.Exit 跳过资源 defer。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m33_resilience_retries/internal/lab"
)

// main 拥有整轮 watchdog 的 cancel；Run 内每个实验还拥有自己的请求预算与清理预算。
// scenario=core 运行适合 10～20 分钟讲解的重试/订单主线，all 再展开治理实验。
func main() {
	scenario := flag.String("scenario", "all", "all/core/amplify/breaker/isolation/limits")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err := lab.Run(ctx, os.Stdout, *scenario)
	cancel() // Run 已经 join 自建任务；这里释放总 watchdog，cancel 本身不是 join。
	if err != nil {
		fmt.Fprintln(os.Stderr, "M33 FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("M33 PASS：真实调用、副作用与各 owner 的清理断言全部完成")
}
