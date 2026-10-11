// demo 的正常入口拥有整个课程演示。危险入口始终经当前二进制的有界子进程执行。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m11_goroutine_ownership/internal/lesson"
)

// main 同步运行核心实验，再 supervision 四个故障。运行失败用非零退出表示。
// M11_INTERNAL_FAULT 仅由 parent 设置，避免公开 -fault 参数直接把正常进程挂住。
func main() {
	if fault := os.Getenv("M11_INTERNAL_FAULT"); fault != "" {
		if err := lesson.RunFault(fault, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return // early-main 在这里真实结束进程；不能 defer 等待故意遗忘的 worker。
	}
	fault := flag.String("fault", "", "只观察指定故障：early-main/no-join/no-receiver/self-wait（仍有界）")
	flag.Parse()
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// 正常演示中的 WaitGroup.Wait 没有 timeout API。这个进程级 watchdog 是最后边界，
	// 防止代码回归把演示永久挂住；正常结束由 main 通知并 join 此观察 goroutine。
	watchStop := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-watchStop:
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "M11 整体演示超过 20 秒：进程安全边界终止，不能视作正常清理")
			os.Exit(2)
		}
	}()
	defer func() { close(watchStop); <-watchDone }()
	if *fault != "" {
		result, checkErr := lesson.CheckFault(ctx, executable, nil, *fault)
		fmt.Printf("fault=%s exit=%d timeout=%t child-reaped=true\n%s", result.Name, result.ExitCode, result.TimedOut, result.Output)
		err = checkErr
	} else {
		err = runCore(ctx, executable)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "M11 FAIL:", err)
		// os.Exit 不执行 defer，因此先显式停止/join watchdog，cancel 释放 timer。
		close(watchStop)
		<-watchDone
		cancel()
		os.Exit(1)
	}
	fmt.Println("M11 PASS：结果、完成同步、服务作用域、资源清理和有界故障均已核对")
}

// runCore 是正常课程的 caller。每个调用返回时，该实验拥有的工作已被 join。
// E01–E04 是 10～20 分钟核心读课；E05 是同一责任模型的危险反例对照。
func runCore(ctx context.Context, executable string) error {
	trace := lesson.NewTrace(os.Stdout)
	if _, err := lesson.RunLifecycle(ctx, trace); err != nil {
		return err
	}
	for _, style := range []string{"classic", "go"} {
		if _, err := lesson.RunBatch(ctx, trace, style); err != nil {
			return err
		}
	}
	if _, err := lesson.RunLateAdd(ctx, trace); err != nil {
		return err
	}
	if _, _, err := lesson.RunNoReceiverFixes(ctx, trace); err != nil {
		return err
	}
	if _, err := lesson.RunServiceScope(ctx, trace); err != nil {
		return err
	}
	_, err := lesson.CheckAllFaults(ctx, executable, os.Stdout)
	return err
}
