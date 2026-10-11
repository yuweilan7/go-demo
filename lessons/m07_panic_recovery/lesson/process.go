package lesson

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ChildCaseEnv 仅作为自启动程序的实验选择器，不包含秘密。
// cmd/demo 与 go test helper 都在业务入口前读取它，因此子进程不会递归再启动一批子进程。
const ChildCaseEnv = "M07_CHILD_CASE"

// ChildOutcome 保存被启动的实际可执行文件退出码，而不是 go run 包装命令的退出码。
// TimedOut 是 harness 失败，不算“预期崩溃”；Output 保留 stderr 中 panic/fatal 和清理哨兵。
type ChildOutcome struct {
	Name     string
	ExitCode int
	TimedOut bool
	Output   string
}

// RunChild 用同一个已编译二进制启动明确故障入口；prefix 是 test helper 的测试选择参数。
// 每个进程最多三秒，CommandContext 超时终止它并等待回收，不依赖 sleep 猜调度。
// panicnil 参数明确覆盖宿主配置，保证两个 nil 实验可对照。
func RunChild(executable string, prefix []string, name string, panicnil string) (ChildOutcome, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, prefix...)
	// 从继承环境删除同名项再加唯一值，避免 Windows 环境中重复键的优先级影响结果。
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, ChildCaseEnv) || strings.EqualFold(key, "GODEBUG") || strings.EqualFold(key, "GOTRACEBACK") {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.Env = append(command.Env, ChildCaseEnv+"="+name, "GOTRACEBACK=single", "GODEBUG=panicnil="+panicnil)
	output, runErr := command.CombinedOutput()
	result := ChildOutcome{Name: name, ExitCode: 0, TimedOut: ctx.Err() != nil, Output: string(output)}
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return result, fmt.Errorf("不能启动子进程 %s: %w", name, runErr)
		}
		result.ExitCode = exitErr.ExitCode()
	}
	return result, nil
}

// RunChildCase 只由显式子进程入口调用，绝不能直接从正常 demo 中调用 fatal/goroutine 分支。
// 输出哨兵用于断言实际清理顺序，最终退出码由父进程收集。
func RunChildCase(name string) {
	switch name {
	case "unrecovered":
		defer fmt.Println("CHILD_DEFER_RAN")
		panic("unrecovered-current-goroutine")
	case "goroutine":
		wrongParentRecovery()
	case "fatal":
		defer fmt.Println("FATAL_DEFER_RAN")
		defer func() { fmt.Printf("FATAL_RECOVER=%v\n", recover()) }()
		var mutex sync.Mutex
		// Unlock 一个从未 Lock 的 mutex 是标准库/runtime fatal，不是可恢复的 panic。
		// 不使用“并发 map 写”碰运气触发，也不使用死锁检测（testing 自身 timer 会改变它）。
		mutex.Unlock()
	case "exit":
		defer fmt.Println("EXIT_DEFER_RAN")
		fmt.Println("CALLING_OS_EXIT_17")
		os.Exit(17) // 直接结束进程，不展开 defer，recover 没有运行机会。
	case "nil":
		var value any
		func() {
			defer func() { value = recover() }()
			panic(nil)
		}()
		_, modern := value.(*runtime.PanicNilError)
		fmt.Printf("NIL_RECOVER_TYPE=%T MODERN=%t VALUE_NIL=%t\n", value, modern, value == nil)
	default:
		fmt.Printf("UNKNOWN_CHILD=%s\n", name)
		os.Exit(64)
	}
}

// wrongParentRecovery 是反例：recover 注册在 parent goroutine，崩溃发生在 child goroutine。
// parent 阻塞于 <-started 后再永久等 done；child panic 不会让 parent 的调用栈开始展开。
func wrongParentRecovery() {
	defer fmt.Println("PARENT_DEFER_RAN")
	defer func() {
		if recover() != nil {
			fmt.Println("PARENT_RECOVERED")
		}
	}()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer fmt.Println("WORKER_DEFER_RAN")
		close(started) // 通知 owner 已到故障入口；通知不是恢复，也不取消任何 goroutine。
		panic("unrecovered-child-goroutine")
	}()
	<-started
	<-done // 本反例没人关闭 done，进程将由 worker 的未恢复 panic 终止，而不是正常返回。
}

// CheckChild 由 demo/test 调用，验证真退出码、清理哨兵和类型；不只检查输出含 panic 字样。
func CheckChild(result ChildOutcome, legacyNil bool) error {
	if result.TimedOut {
		return fmt.Errorf("%s 超过三秒，未获得预期退出证据", result.Name)
	}
	expectedExit := map[string]int{"unrecovered": 2, "goroutine": 2, "fatal": 2, "exit": 17, "nil": 0}[result.Name]
	if result.ExitCode != expectedExit {
		return fmt.Errorf("%s 实际 exit=%d，预期=%d\n%s", result.Name, result.ExitCode, expectedExit, result.Output)
	}
	contains := func(text string) bool { return strings.Contains(result.Output, text) }
	var valid bool
	switch result.Name {
	case "unrecovered":
		valid = contains("panic: unrecovered-current-goroutine") && contains("CHILD_DEFER_RAN")
	case "goroutine":
		valid = contains("panic: unrecovered-child-goroutine") && contains("WORKER_DEFER_RAN") && !contains("PARENT_RECOVERED") && !contains("PARENT_DEFER_RAN")
	case "fatal":
		valid = contains("fatal error: sync: unlock of unlocked mutex") && !contains("FATAL_DEFER_RAN") && !contains("FATAL_RECOVER=")
	case "exit":
		valid = contains("CALLING_OS_EXIT_17") && !contains("EXIT_DEFER_RAN")
	case "nil":
		if legacyNil {
			valid = contains("NIL_RECOVER_TYPE=<nil> MODERN=false VALUE_NIL=true")
		} else {
			valid = contains("NIL_RECOVER_TYPE=*runtime.PanicNilError MODERN=true VALUE_NIL=false")
		}
	}
	if !valid {
		return fmt.Errorf("%s 缺少预期恢复/清理证据\n%s", result.Name, result.Output)
	}
	return nil
}
