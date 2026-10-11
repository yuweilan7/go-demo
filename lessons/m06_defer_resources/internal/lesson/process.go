package lesson

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ProcessCase 记录真正独立的子进程退出码、输出与清理标记，主实验进程不会自杀。
type ProcessCase struct {
	Mode       string
	ExitCode   int
	Output     string
	CleanupRan bool
}

// ProcessResult 同时保留普通 panic 展开和进程终止两个边界的证据。
type ProcessResult struct {
	PanicRecovered, PanicFileClosed, PanicAfterLine bool
	PanicContent                                    string
	Cases                                           []ProcessCase
	TempDir                                         string
	Removed                                         bool
}

// RunProcess 由 demo 调用，executable 是 os.Executable 获取的当前程序。
// 测试传入自身测试程序并用 prefix 只选 helper test；两种入口调用同一个 RunChild。
// 本函数拥有每个子进程，CombinedOutput 同步等待退出，5 秒超时会终止卡住的 child。
func RunProcess(t *Trace, executable string, prefix []string) (out ProcessResult, err error) {
	t.Event("E06", "普通 panic 与 os.Exit/log.Fatal 的故障边界")
	out.TempDir, err = os.MkdirTemp("", "m06-process-")
	if err != nil {
		return out, err
	}
	defer func() {
		removeErr := os.RemoveAll(out.TempDir)
		err = errors.Join(err, removeErr)
		out.Removed = removeErr == nil
		t.Event("E06.owner", "子进程均已被等待；临时目录删除=%t", out.Removed)
	}()
	panicPath := filepath.Join(out.TempDir, "panic.txt")
	out.PanicRecovered, out.PanicFileClosed, out.PanicAfterLine, err = panicBoundary(t, panicPath)
	if err != nil {
		return out, err
	}
	data, err := os.ReadFile(panicPath)
	if err != nil {
		return out, err
	}
	out.PanicContent = string(data)
	t.Event("E06.panic.caller", "recovered=%t file_closed=%t after_panic_line=%t content=%q",
		out.PanicRecovered, out.PanicFileClosed, out.PanicAfterLine, out.PanicContent)

	for _, mode := range []string{"normal", "exit", "fatal", "repaired"} {
		dir := filepath.Join(out.TempDir, mode)
		if err := os.Mkdir(dir, 0o700); err != nil {
			return out, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, executable, prefix...)
		// 这些变量只传给当前 child，不修改当前进程/全局环境，不影响其他课程。
		cmd.Env = append(os.Environ(), "M06_CHILD_MODE="+mode, "M06_CHILD_DIR="+dir)
		output, childErr := cmd.CombinedOutput()
		contextErr := ctx.Err()
		cancel() // child 已退出后释放 deadline timer；不是以取消替代等待退出。
		if contextErr != nil {
			return out, fmt.Errorf("%s 子进程超过 5 秒: %w", mode, contextErr)
		}
		code := 0
		if childErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(childErr, &exitErr) {
				return out, fmt.Errorf("启动 %s 子进程: %w", mode, childErr)
			}
			code = exitErr.ExitCode()
		}
		cleanup, markerErr := os.ReadFile(filepath.Join(dir, "cleanup.txt"))
		if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
			return out, markerErr
		}
		// 文件仅由 child 的延迟函数写入，且必须报告真实 Close 成功。
		// OS 在进程结束时回收句柄，不等于这段 Go 清理逻辑有运行。
		r := ProcessCase{Mode: mode, ExitCode: code, Output: strings.TrimSpace(string(output)), CleanupRan: string(cleanup) == "close_ok=true\n"}
		out.Cases = append(out.Cases, r)
		t.Event("E06."+mode, "exit_code=%d cleanup_ran=%t output=%q", code, r.CleanupRan, r.Output)
		payload, readErr := os.ReadFile(filepath.Join(dir, "payload.txt"))
		if readErr != nil || string(payload) != "before-exit\n" {
			return out, fmt.Errorf("%s 子进程真实写入不符: %w", mode, readErr)
		}
		wantCode := map[string]int{"normal": 0, "exit": 23, "fatal": 1, "repaired": 24}[mode]
		wantCleanup := mode == "normal" || mode == "repaired"
		if code != wantCode || r.CleanupRan != wantCleanup {
			return out, fmt.Errorf("%s 子进程退出/清理事实不符", mode)
		}
	}
	return out, nil
}

// panicBoundary 是当前 goroutine 的恢复边界，由 RunProcess 同步调用。
// childOwnedPanic 先执行自己的 Close defer，再向这一层传播 panic；本层的 defer
// 直接调用 recover 才能恢复。恢复后整个函数返回，绝不跳回 panic 下一行继续。
func panicBoundary(t *Trace, path string) (recovered, closed, after bool, err error) {
	var file *os.File
	var cleanupErr error
	defer func() {
		if value := recover(); value != nil {
			recovered = value == "订单处理 panic"
			t.Event("E06.panic.boundary", "直接 recover 得到 %q", value)
		}
		if file != nil {
			// 检查原对象上的 I/O，不假定各平台 File.Stat 的关闭错误类别相同。
			_, readErr := file.Read(make([]byte, 1))
			closed = errors.Is(readErr, os.ErrClosed)
		}
		// panic 会中断下面 childOwnedPanic 的普通返回赋值。不能只让下层改自己的
		// 具名 err 再指望它传上来；共享观察槽保留清理错误，由本恢复边界合并。
		err = errors.Join(err, cleanupErr)
	}()
	err = childOwnedPanic(t, path, &file, &after, &cleanupErr)
	return recovered, closed, after, err
}

// childOwnedPanic 拥有一个真实文件；outFile 把同一指针交给恢复边界检查最终状态。
// after 是 caller 专属观察值，不涉及并发，用来证明 panic 后普通语句没有运行。
// cleanupErr 是恢复边界提供的错误槽；panic 中断普通返回时仍能由上层读取清理失败。
func childOwnedPanic(t *Trace, path string, outFile **os.File, after *bool, cleanupErr *error) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	*outFile = f
	defer func() {
		*cleanupErr = f.Close()
		t.Event("E06.panic.resource", "栈展开时 Close close_ok=%t", *cleanupErr == nil)
	}()
	if _, err = f.WriteString("before-panic\n"); err != nil {
		return err
	}
	t.Event("E06.panic.body", "文件已写入，触发普通 panic")
	triggerPanic()
	// triggerPanic 不会正常返回。下面是实际业务“继续写入”的语句，但本次调用不执行。
	// 恢复后 caller 检查 after 仍为 false，文件也只包含 before-panic。
	*after = true
	_, err = f.WriteString("after-panic\n")
	return err
}

// triggerPanic 由资源拥有者调用，代表业务深处产生的普通 panic。
// 单独函数便于看到调用点：recover 在外层恢复，不会回到这个调用点的下一句。
func triggerPanic() { panic("订单处理 panic") }

// RunChild 只由父进程设置了 M06_CHILD_MODE 的独立进程入口调用。
// repaired 的资源函数先返回 ErrBusiness、执行 defer，外层再 Exit；退出码仍表示失败。
// normal 直接返回。exit/fatal 在资源函数内部结束进程，父进程负责等待和清理目录。
func RunChild(mode, dir string) error {
	if dir == "" {
		return errors.New("child 缺少专属临时目录")
	}
	if mode != "normal" && mode != "exit" && mode != "fatal" && mode != "repaired" {
		return fmt.Errorf("未知 child 模式 %q", mode)
	}
	err := processOwnedFile(mode, dir)
	if mode == "repaired" && errors.Is(err, ErrBusiness) {
		fmt.Println("owner-returned-before-exit")
		os.Exit(24)
	}
	return err
}

// processOwnedFile 拥有 payload 文件；真实 Close 成功后才写 cleanup.txt 标记。
// os.Exit/log.Fatal 的危险版没有执行这段 defer，不能指望它 Flush、删锁或写业务收尾。
func processOwnedFile(mode, dir string) (err error) {
	f, err := os.Create(filepath.Join(dir, "payload.txt"))
	if err != nil {
		return err
	}
	defer func() {
		closeErr := f.Close()
		markerErr := os.WriteFile(filepath.Join(dir, "cleanup.txt"), []byte(fmt.Sprintf("close_ok=%t\n", closeErr == nil)), 0o600)
		err = errors.Join(err, closeErr, markerErr)
	}()
	if _, err = f.WriteString("before-exit\n"); err != nil {
		return err
	}
	switch mode {
	case "exit":
		fmt.Println("exit-body")
		os.Exit(23)
	case "fatal":
		// 私有 Logger 不带墙钟时间，让本地证据可读；Fatal 打印后内部 os.Exit(1)。
		log.New(os.Stdout, "", 0).Fatal("fatal-body")
	case "repaired":
		return ErrBusiness
	}
	fmt.Println("normal-body")
	return nil
}
