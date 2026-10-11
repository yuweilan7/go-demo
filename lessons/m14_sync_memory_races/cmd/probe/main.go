// probe 构建并监督独立故障进程，验证“预期失败”的诊断，而不是让正常 demo 陷入死锁。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// run 由探测 owner 调用，每次创建自己的 ctx/进程并同步 Wait。
// 超时是实验失败，不当成“已证明死锁”；正常预期要求真正收到 runtime/race/vet 诊断。
func run(limit time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	// race 第一处冲突即退出，避免重复报告和额外退出等待；这些变量仅影响此子进程。
	command.Env = append(os.Environ(), "GORACE=halt_on_error=1 exitcode=66 atexit_sleep_ms=0")
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return string(output), fmt.Errorf("子进程超过 %s 上限: %w", limit, ctx.Err())
	}
	return string(output), err
}

// fail 由 owner 调用，带上实际诊断后以失败退出；外层 defer 必须仍能执行临时目录清理。
func fail(message string, output string) error { return fmt.Errorf("%s\n%s", message, output) }

// probe 的临时目录由本次进程独占，defer RemoveAll 只删除 MkdirTemp 返回的已核对绝对路径。
// 不写根目录二进制，不修改共享 Go 缓存配置，也不在 fatal 子进程中指望 defer 清理。
func probe() (resultErr error) {
	module, err := os.ReadFile("go.mod")
	if err != nil || !strings.Contains(string(module), "module go-demo/lessons/m14_sync_memory_races") {
		return fmt.Errorf("请在本课 module 工作目录运行 go run ./cmd/probe")
	}
	temp, err := os.MkdirTemp("", "m14-fault-*")
	if err != nil {
		return err
	}
	tempRoot, err := filepath.Abs(os.TempDir())
	if err != nil || !filepath.IsAbs(temp) || filepath.Clean(filepath.Dir(temp)) != filepath.Clean(tempRoot) || !strings.HasPrefix(filepath.Base(temp), "m14-fault-") {
		return fmt.Errorf("临时目录不在预期临时根目录，拒绝递归清理: %q", temp)
	}
	// resultErr 是具名返回值，defer 在 probe 返回 main 前补上清理失败；不能静默声称清理完成。
	defer func() {
		if cleanupErr := os.RemoveAll(temp); cleanupErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("清理本次临时二进制: %w", cleanupErr))
		}
	}()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	plain := filepath.Join(temp, "fault"+suffix)
	traced := filepath.Join(temp, "fault-race"+suffix)
	for _, build := range []struct {
		path string
		race bool
	}{{plain, false}, {traced, true}} {
		args := []string{"build", "-tags=faultcases", "-o", build.path}
		if build.race {
			args = append(args, "-race")
		}
		args = append(args, "./cmd/fault")
		output, buildErr := run(60*time.Second, "go", args...)
		if buildErr != nil {
			return fail("故障入口构建失败: "+buildErr.Error(), output)
		}
	}
	for _, mode := range []string{"map", "copy-locked", "reentry", "rw-upgrade", "lock-order"} {
		output, childErr := run(5*time.Second, plain, mode)
		needle := "all goroutines are asleep - deadlock"
		if mode == "map" {
			needle = "concurrent map"
		}
		if childErr == nil || errors.Is(childErr, context.DeadlineExceeded) || !strings.Contains(output, needle) {
			return fail("未收到预期 runtime fatal: "+mode+"; err="+fmt.Sprint(childErr), output)
		}
		fmt.Printf("PASS fatal %-17s diagnostic=%q\n", mode, needle)
		fmt.Print(output) // 留下真实栈；日志可直接定位到 faults.go 的写入/等待点。
	}
	for _, mode := range []string{"map", "different-lock", "value-receiver", "copy-struct", "atomic-value-map", "slice", "visibility"} {
		output, childErr := run(5*time.Second, traced, mode)
		var exit *exec.ExitError
		if !errors.As(childErr, &exit) || exit.ExitCode() != 66 || !strings.Contains(output, "WARNING: DATA RACE") {
			return fail("未收到预期 race 报告: "+mode+"; err="+fmt.Sprint(childErr), output)
		}
		fmt.Printf("PASS race  %-17s diagnostic=%q\n", mode, "WARNING: DATA RACE")
		fmt.Print(output)
	}
	output, vetErr := run(30*time.Second, "go", "vet", "-tags=faultcases", "./internal/faults")
	if vetErr == nil || !strings.Contains(output, "copies lock value") {
		return fail("故障 vet 未识别复制锁: "+fmt.Sprint(vetErr), output)
	}
	fmt.Println("PASS vet   copylocks: expected rejection")
	fmt.Print(output)
	fmt.Println("PROBE PASS: 5 fatal + 7 race + copylocks；故障子进程均已等待退出，临时二进制由 owner 清理")
	return nil
}

// main 把实验错误交给终端；probe 先完成 defer 清理，再在本层决定非零退出码。
func main() {
	if err := probe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
