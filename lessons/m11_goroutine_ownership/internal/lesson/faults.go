package lesson

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// FaultNames 返回副本；正常入口按此顺序观察四种不同的未完成责任。
func FaultNames() []string { return []string{"early-main", "no-join", "no-receiver", "self-wait"} }

// RunFault 只允许隔离子进程调用。它故意破坏 ownership，不能从正常进程直接调用。
// out 来自 child stdout；parent 用 CheckFault 总时限保证不会把自己永久挂住。
func RunFault(name string, out io.Writer) error {
	switch name {
	case "early-main":
		started := make(chan struct{})
		release := make(chan struct{})
		go func() {
			defer fmt.Fprintln(out, "EARLY_CLEANUP (不应出现)")
			close(started)
			<-release
			fmt.Fprintln(out, "EARLY_COMPLETED (不应出现)")
		}()
		<-started
		fmt.Fprintln(out, "EARLY_READY main-returning worker-not-finished")
		// 子进程 main 随即返回；Go 不等待 worker，不执行 worker 的 defer。
		return nil
	case "no-join":
		// 一个请求返回遗忘了 task 句柄；owner 丢掉停止/完成能力。
		// atomic 仅保护本实验登记数量，绝不拿 runtime.NumGoroutine 猜泄漏。
		var outstanding atomic.Int32
		started := make(chan struct{})
		gate := make(chan struct{})
		outstanding.Add(1)
		func() {
			go func() {
				defer outstanding.Add(-1)
				defer fmt.Fprintln(out, "NO_JOIN_CLEANUP (不应出现)")
				close(started)
				<-gate // gate 永远无人放行；请求作用域结束不会自动取消 goroutine。
			}()
		}() // 请求函数返回，goroutine 仍然存在。
		<-started
		fmt.Fprintf(out, "NO_JOIN_READY request-returned outstanding=%d stop-handle-lost\n", outstanding.Load())
		// 这个长 timer 只让模拟服务进程保持存活，防止 runtime 提前报告全局 deadlock。
		// 它不是为了等 worker，也没有用 Sleep 猜调度。parent 的 600ms 预算会终止 child。
		keepServiceAlive := time.NewTimer(time.Hour)
		defer keepServiceAlive.Stop()
		<-keepServiceAlive.C
		return nil
	case "no-receiver":
		attempting := make(chan struct{})
		results := make(chan Receipt) // cap=0，发送必须和接收会合。
		done := make(chan struct{})
		go func() {
			defer close(done)
			close(attempting)
			results <- Receipt{OrderID: "O-101", Amount: 200} // 错误：请求放弃了结果，没人接收。
			fmt.Fprintln(out, "SEND_COMPLETED (不应出现)")
		}()
		<-attempting
		fmt.Fprintln(out, "NO_RECEIVER_READY owner-waiting-done worker-sending-without-receiver")
		<-done // owner 等完成，但 worker 必须先发送；构成无法取得进展的等待关系。
		return nil
	case "self-wait":
		var group sync.WaitGroup
		started := make(chan struct{})
		group.Add(1)
		go func() {
			defer group.Done()
			close(started)
			group.Wait() // 错误：这项任务也在 counter 中，它等自己 Done，Done 又等它返回。
			fmt.Fprintln(out, "SELF_WAIT_COMPLETED (不应出现)")
		}()
		<-started
		fmt.Fprintln(out, "SELF_WAIT_READY task-waits-group-that-includes-itself")
		group.Wait()
		return nil
	default:
		return fmt.Errorf("未知故障 %q", name)
	}
}

// FaultResult 记录真实隔离结果；TimedOut 表示 parent 杀死并回收了 child，不能称为优雅清理。
type FaultResult struct {
	Name     string
	Output   string
	ExitCode int
	TimedOut bool
}

// CheckFault 由正常 cmd/demo 或测试调用。program 是当前可执行文件，args 是它的入口参数。
// exec.CommandContext 只启动这一层 child，不经 shell/go run 再生孙进程；Run 会等待进程回收。
func CheckFault(parent context.Context, program string, args []string, name string) (FaultResult, error) {
	budget := 3 * time.Second
	if name == "no-join" {
		budget = 600 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel() // 无论 child 正常/异常/超时都释放 parent 侧 timer。
	cmd := exec.CommandContext(ctx, program, args...)
	// 明确传入 child 的故障名字；不是把用户文本拼接成 shell 命令。
	cmd.Env = append(os.Environ(), "M11_INTERNAL_FAULT="+name)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	runErr := cmd.Run()
	result := FaultResult{Name: name, Output: output.String(), ExitCode: -1, TimedOut: ctx.Err() == context.DeadlineExceeded && parent.Err() == nil}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if parent.Err() != nil {
		return result, fmt.Errorf("故障观察者总预算到期: %w", parent.Err())
	}
	// 带 -race 的 child 输出不能被“本来就应失败”掩盖。若 detector 报告数据竞争，
	// 它是额外缺陷，必须交回 parent 失败，而非分类为预期阻塞/超时反例。
	if strings.Contains(result.Output, "WARNING: DATA RACE") || strings.Contains(result.Output, "race detected during execution") {
		return result, fmt.Errorf("故障 %s 的 child 出现非预期数据竞争：%s", name, result.Output)
	}
	var ok bool
	switch name {
	case "early-main":
		ok = runErr == nil && strings.Contains(result.Output, "EARLY_READY") && !strings.Contains(result.Output, "EARLY_CLEANUP") && !strings.Contains(result.Output, "EARLY_COMPLETED")
	case "no-join":
		ok = result.TimedOut && strings.Contains(result.Output, "NO_JOIN_READY request-returned outstanding=1") && !strings.Contains(result.Output, "NO_JOIN_CLEANUP")
	case "no-receiver":
		ok = runErr != nil && strings.Contains(result.Output, "NO_RECEIVER_READY") && !strings.Contains(result.Output, "SEND_COMPLETED")
	case "self-wait":
		ok = runErr != nil && strings.Contains(result.Output, "SELF_WAIT_READY") && !strings.Contains(result.Output, "SELF_WAIT_COMPLETED")
	}
	if !ok {
		return result, fmt.Errorf("故障 %s 未出现预期事实：exit=%d timeout=%t runErr=%v output=%q", name, result.ExitCode, result.TimedOut, runErr, result.Output)
	}
	return result, nil
}

// CheckAllFaults 由正常入口调用并把原始 child 输出交给读者。
// runtime deadlock 是当前实现的诊断；可移植验收依赖 marker、未完成和有界退出，不依赖固定栈文本。
func CheckAllFaults(ctx context.Context, program string, out io.Writer) ([]FaultResult, error) {
	var results []FaultResult
	for _, name := range FaultNames() {
		result, err := CheckFault(ctx, program, nil, name)
		results = append(results, result)
		fmt.Fprintf(out, "E05 fault=%s exit=%d timeout=%t child-reaped=true\n%s", name, result.ExitCode, result.TimedOut, result.Output)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}
