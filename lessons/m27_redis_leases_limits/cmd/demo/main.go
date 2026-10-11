package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m27_redis_leases_limits/internal/lesson"
)

// main 是本课全部资源的最上层 owner。入口读私有配置，建立整课预算，
// 同步等待 Run 完成；Run 返回时，任务、专属键/表、客户连接池均已收尾。
// os.Exit 放在 runMain 返回之后，避免它跳过持有资源函数中的 defer。
func main() {
	if err := runMain(); err != nil {
		fmt.Fprintln(os.Stderr, "M27 FAIL:", err)
		os.Exit(1)
	}
}

// runMain 的 scenario 由命令行给出；all 是完整课程，core 是 10～20 分钟
// 阅读/运行主线。程序实际运行只需几秒，剩余时间用于对照状态与源码。
func runMain() error {
	index := flag.String("index", lesson.DefaultIndex(), "安装者的只读服务索引路径")
	scenario := flag.String("scenario", "all", "all 或 core；core=过期旧写与SQL fencing")
	flag.Parse()
	config, err := lesson.LoadConfig(*index)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel() // owner 正常结束也要释放整课 deadline 的 timer。
	report, err := lesson.Run(ctx, config, *scenario, os.Stdout)
	if err != nil {
		return err
	}
	fmt.Printf("M27 PASS assertions=%d tasks=%d/%d cleaned_keys=%d tables=%d sockets=%d/%d\n",
		report.Assertions, report.TasksJoined, report.TasksStarted,
		len(report.Keys), len(report.Tables), report.SocketsClosed, report.SocketsOpened)
	return nil
}
