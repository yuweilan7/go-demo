// demo 是 M07 的正常入口。故障在同一二进制的显式子进程入口发生，父进程仍能跑完全部对照。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"go-demo/lessons/m07_panic_recovery/lesson"
)

// main 先辨别自己是否被故障 harness 启动，再执行正常课程。
// --fault 方便单独复现；默认路径不能把 fatal 函数直接运行在父进程里。
func main() {
	fault := flag.String("fault", "", "显式故障入口：unrecovered/goroutine/fatal/exit/nil")
	flag.Parse()
	selected := os.Getenv(lesson.ChildCaseEnv)
	if *fault != "" {
		selected = *fault
	}
	if selected != "" {
		lesson.RunChildCase(selected)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "M07 验收失败:", err)
		os.Exit(1)
	}
}

// run 拥有每个有界实验的 context，收集事实并输出可与 README 对照的标记。
// 它没有恢复整个 main：harness/断言自身故障必须明确失败，不能吞掉后打印 PASS。
func run() error {
	fmt.Printf("M07 runtime=%s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Println("\n=== E01 三层调用链：error 与 panic 返回点 ===")
	for _, mode := range []string{"success", "error", "panic"} {
		outcome := lesson.RunChain(mode)
		fmt.Printf("mode=%s err=%v\n  %s\n", mode, outcome.Err, strings.Join(outcome.Events, " -> "))
		if mode == "panic" {
			var failure *lesson.PanicFailure
			if !errors.As(outcome.Err, &failure) || !strings.Contains(failure.Stack, "lesson.leaf") {
				return errors.New("E01 没有保留失败类型与 leaf 的现场栈")
			}
			fmt.Printf("task=%s stage=%s panic-type=%T\n现场栈：\n%s", failure.TaskID, failure.Stage, failure.Value, failure.Stack)
		}
	}
	fmt.Printf("defer-values=%v\n", lesson.DeferValues())
	fmt.Println("\n=== E02 recover 的直接调用规则 ===")
	fmt.Println(strings.Join(lesson.RecoverRules(), " -> "))
	runtimeFailure := lesson.RunRuntimePanic()
	if runtimeFailure == nil {
		return errors.New("运行时越界 panic 没有交付失败")
	}
	fmt.Printf("runtime-bounds-panic type=%T recovered=true err=%v\n", runtimeFailure.Value, runtimeFailure)
	fmt.Println("\n=== E03 恢复不等于撤销副作用 ===")
	for _, policy := range []string{"swallow", "commit-defer", "safe"} {
		outcome := lesson.RunEffects(policy)
		fmt.Printf("policy=%s external=%d staged=%d committed=%d rollback=%t err=%v\n  %s\n", policy, outcome.ExternalSent, outcome.Staged, outcome.Committed, outcome.RolledBack, outcome.Err, strings.Join(outcome.Events, " -> "))
	}
	fmt.Println("\n=== E04 worker 自己恢复，owner 等清理完成 ===")
	for _, repair := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		result, err := lesson.RunWorker(ctx, repair)
		cancel() // 当前实验已返回，不把 timer 活到整个 demo 结束。
		if err != nil {
			return err
		}
		fmt.Printf("repair=%t task=%s state=%s reserved=%d stack-saved=%t owner-joined=%t\n  %s\n", repair, result.TaskID, result.State, result.Reserved, result.Failure != nil && result.Failure.Stack != "", result.Joined, strings.Join(result.Events, " -> "))
	}
	fmt.Println("\n=== E05 真 HTTP/1.1：默认恢复、500、已发送的 200 前缀 ===")
	for _, mode := range []string{"builtin", "middleware", "partial"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		outcome, err := lesson.RunHTTP(ctx, mode)
		cancel()
		if err != nil {
			return err
		}
		fmt.Printf("mode=%s status=%d external=%d request-error=%q read-error=%q body=%q server-logged=%t stack-saved=%t partial-aborted=%t\n", mode, outcome.Status, outcome.ExternalSent, outcome.RequestErr, outcome.ReadErr, outcome.Body, outcome.ServerLogged, outcome.Failure != nil, outcome.PartialAborted)
	}
	fmt.Println("\n=== E06 真子进程：未恢复 panic、goroutine 边界、fatal、os.Exit、panic(nil) ===")
	executable, err := os.Executable() // go run 会执行已编译的临时程序；子进程直接启动该程序，拿实际退出码。
	if err != nil {
		return err
	}
	cases := []struct {
		name      string
		panicnil  string
		legacyNil bool
	}{{"unrecovered", "0", false}, {"goroutine", "0", false}, {"fatal", "0", false}, {"exit", "0", false}, {"nil", "0", false}, {"nil", "1", true}}
	for _, scenario := range cases {
		result, err := lesson.RunChild(executable, nil, scenario.name, scenario.panicnil)
		if err != nil {
			return err
		}
		if err := lesson.CheckChild(result, scenario.legacyNil); err != nil {
			return err
		}
		fmt.Printf("case=%s panicnil=%s exit=%d timeout=%t verified=true\n", scenario.name, scenario.panicnil, result.ExitCode, result.TimedOut)
		// 只打印诊断首行与哨兵；完整原始 Output 已先交给 CheckChild 核对。
		for _, line := range strings.Split(result.Output, "\n") {
			if strings.Contains(line, "panic:") || strings.Contains(line, "fatal error:") || strings.Contains(line, "_RAN") || strings.Contains(line, "NIL_RECOVER_") || strings.Contains(line, "CALLING_OS_EXIT") {
				fmt.Println("  " + strings.TrimSpace(line))
			}
		}
	}
	fmt.Println("\nM07 NORMAL_ENTRY_COMPLETED：正常入口已走完所有实验，子进程均回收。")
	return nil
}
