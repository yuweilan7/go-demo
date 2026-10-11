// demo 是 M06 的可运行入口。正常启动只运行有界实验；会结束进程的操作
// 都放在它自己启动的子进程里，因此学习者不必手工中断或清理后台服务。
package main

import (
	"flag"
	"fmt"
	"os"

	"go-demo/lessons/m06_defer_resources/internal/lesson"
)

// main 先识别父进程设置的私有实验环境，再处理学习者的 -only 参数。
// 资源真正的 owner 在 lesson 各函数内；run 返回时清理已经完成，main 才决定退出码。
func main() {
	if mode := os.Getenv("M06_CHILD_MODE"); mode != "" {
		if err := lesson.RunChild(mode, os.Getenv("M06_CHILD_DIR")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}

	only := flag.String("only", "all", "all/core/semantics/returns/files/buffers/ownership/process")
	flag.Parse()
	if err := run(*only); err != nil {
		// 这里的 Exit 在 run 完整返回之后。run 和其下层的 defer 已经执行，
		// 与在资源拥有者内部直接 log.Fatal 的危险版形成对照。
		fmt.Fprintln(os.Stderr, "实验失败:", err)
		os.Exit(1)
	}
}

// run 负责选择实验并把未预期的 I/O/断言失败交回 main。
// 危险版预期产生的 error 会由对应实验识别，不能被误当成课程本身失败。
func run(only string) error {
	trace := lesson.NewTrace(os.Stdout)
	valid := map[string]bool{"all": true, "core": true, "semantics": true, "returns": true,
		"files": true, "buffers": true, "ownership": true, "process": true}
	if !valid[only] {
		return fmt.Errorf("未知 -only=%q", only)
	}
	selected := func(name string) bool {
		return only == "all" || only == name || (only == "core" && name != "ownership" && name != "process")
	}
	if selected("semantics") {
		lesson.RunSemantics(trace)
	}
	if selected("returns") {
		lesson.RunReturns(trace)
	}
	if selected("files") {
		if _, err := lesson.RunFiles(trace); err != nil {
			return err
		}
	}
	if selected("buffers") {
		if _, err := lesson.RunBuffers(trace); err != nil {
			return err
		}
	}
	if selected("ownership") {
		if _, err := lesson.RunOwnership(trace); err != nil {
			return err
		}
	}
	if selected("process") {
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("取得当前实验程序路径: %w", err)
		}
		if _, err := lesson.RunProcess(trace, executable, nil); err != nil {
			return err
		}
	}
	trace.Event("DONE", "所选实验完成；真实临时目录均已清理")
	return trace.Err()
}
