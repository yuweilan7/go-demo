// demo 是课程入口：构造具体依赖，按命名场景运行正常/故障/修复实验，再退出。
package main

import (
	"flag"
	"fmt"
	"os"

	"go-demo/lessons/m04_interfaces_nil/internal/adapter"
	"go-demo/lessons/m04_interfaces_nil/internal/lab"
)

// main 只负责进程边界：CLI 参数来自学习者，正常返回为退出码 0，失败打印后退出码 1。
// 所有资源清理都在 run 或被调用函数中完成；os.Exit 本身不会执行 main 的 defer。
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "demo failed:", err)
		os.Exit(1)
	}
}

// run 由 main 调用，负责本次实验的对象构造和场景分发，不创建全局 service locator。
// 文件路径相对当前工作目录；go -C 会先进入本模块，因此默认 testdata 能稳定定位。
func run() error {
	scenario := flag.String("scenario", "all", "all/core/nil/methods/any/embedding")
	dataPath := flag.String("data", "testdata/orders.json", "合成订单 JSON 的路径")
	flag.Parse()
	// flag.String 返回 *string；Parse 完成后解引用才得到命令行最终填写的值。
	repo, err := adapter.NewRepository(*dataPath)
	if err != nil {
		return err
	}
	// 本模块不需要注册 shutdown：仓储不持有打开文件，日志不启动后台 goroutine。
	switch *scenario {
	case "all":
		if err := lab.RunCore(os.Stdout, repo); err != nil {
			return err
		}
		lab.RunNil(os.Stdout, repo)
		if err := lab.RunMethods(os.Stdout, repo); err != nil {
			return err
		}
		lab.RunAny(os.Stdout, repo)
		return lab.RunEmbedding(os.Stdout, repo)
	case "core":
		return lab.RunCore(os.Stdout, repo)
	case "nil":
		lab.RunNil(os.Stdout, repo)
	case "methods":
		return lab.RunMethods(os.Stdout, repo)
	case "any":
		lab.RunAny(os.Stdout, repo)
	case "embedding":
		return lab.RunEmbedding(os.Stdout, repo)
	default:
		return fmt.Errorf("unknown scenario %q", *scenario)
	}
	return nil
}
