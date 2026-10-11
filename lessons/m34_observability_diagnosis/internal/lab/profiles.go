package lab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"
)

const DigestIterations = 400000
const retainedBytes = 8 * 1024 * 1024

// Digest 由 CPU 请求和独立断言调用。两种版本用同一份合成输入与同一 checksum 公式。
// bad 每轮重新 hash 不变 payload；fixed 计算一次后复用。返回 calls 是实际 hash 调用数，
// 用于证明改动减少工作量；耗时是受本机并行构建影响的样本，不能作稳定 SLA 断言。
// checksum 是合成 uint64 累加，溢出按 Go 无符号整数规则回绕；两个版本保持同一公式。
func Digest(fixed bool, n int) (uint64, int) {
	payload := bytes.Repeat([]byte{0x34}, 4096)
	var checksum uint64
	if fixed {
		value := digestOnce(payload)
		for i := 0; i < n; i++ {
			checksum += value
		}
		return checksum, 1
	}
	for i := 0; i < n; i++ {
		checksum += digestOnce(payload)
	}
	return checksum, n
}

// digestOnce 单独留函数名供 CPU pprof -list 定位。noinline 是本课控制可读采样符号
// 的编译器指令，不是语言规则；真实业务通常应保留编译器正常优化后再解释内联栈。
//
//go:noinline
func digestOnce(payload []byte) uint64 {
	hash := sha256.Sum256(payload)
	return binary.LittleEndian.Uint64(hash[:8])
}

// Retainer 模拟只想缓存“订单凭证前 16 字节”的对象，Data 是 slice header 值。
// 它的 Data 指向底层数组；len 小不代表原始数组剩下的空间可以被 GC 单独回收。
type Retainer struct{ Data []byte }

// retainBad 由 heap 实验调用，返回原数组的视图；cap=8MiB 暴露了可达大数组。
// 即使写 full-slice expression [:16:16] 限制 cap，也仍引用同一个大数组，不能修复保留。
//
//go:noinline
func retainBad() []byte {
	data := make([]byte, retainedBytes)
	for i := range data {
		data[i] = byte(i % 251)
	}
	return data[:16]
}

// retainFixed 拷贝真正要保留的 16 字节，新数组与原数组没有别名。
// owner 把 Retainer.Data 换成它后，原大数组才失去这个引用；GC 是否归还 OS 是另一事实。
func retainFixed(header []byte) []byte {
	copyHeader := make([]byte, len(header))
	copy(copyHeader, header)
	return copyHeader
}

// writeProfile 保存当前进程 profile 快照，成功写入与 Close 都检查。
// heap/block/mutex 的二进制可给 go tool pprof；goroutine debug=2 是人工可读栈。
// block/mutex 的累计事件通常要在等待结束/解锁之后才出现，不能代替正在等待的栈。
func writeProfile(out, name, filename string, debug int) error {
	profile := pprof.Lookup(name)
	if profile == nil {
		return fmt.Errorf("profile %s unavailable in %s", name, runtime.Version())
	}
	file, err := os.Create(filepath.Join(out, filename))
	if err != nil {
		return err
	}
	return errors.Join(profile.WriteTo(file, debug), file.Close())
}

// waitingStack 在进程本地读取真实 goroutine 栈，用实际符号确认已进入等待。
// signal(queue_entered) 只证明即将 Lock；此循环才确认同一 goroutine 的栈同时含
// badCheckout 与 lockSlow。Gosched 让出执行机会，不用于把某段 Sleep 当成功证据。
func waitingStack(ctx context.Context, function, waitSymbol string) ([]byte, error) {
	for {
		var snapshot bytes.Buffer
		if err := pprof.Lookup("goroutine").WriteTo(&snapshot, 2); err != nil {
			return nil, err
		}
		for _, stack := range strings.Split(snapshot.String(), "\n\n") {
			if strings.Contains(stack, function) && strings.Contains(stack, waitSymbol) {
				return snapshot.Bytes(), nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("stack did not reach %s/%s: %w", function, waitSymbol, ctx.Err())
		default:
			runtime.Gosched()
		}
	}
}

// heapExperiment 保存同一 owner 替换缓存前后的样本，并核对业务内容相等。
// MemProfileRate 在 Run 启动阶段设为 1，只在这个有界实验使用全部分配采样；GC 强制
// 完成后再写 profile，KeepAlive 明确保证 owner 在最后一次采样之后仍被视为存活。
func heapExperiment(out string) error {
	cache := &Retainer{Data: retainBad()}
	before := append([]byte(nil), cache.Data...)
	if len(cache.Data) != 16 || cap(cache.Data) != retainedBytes {
		return errors.New("bad retention shape changed")
	}
	runtime.GC()
	if err := writeProfile(out, "heap", "heap-bad.prof", 0); err != nil {
		return err
	}
	cache.Data = retainFixed(cache.Data)
	if !bytes.Equal(cache.Data, before) || cap(cache.Data) != 16 {
		return errors.New("fixed retention changed receipt")
	}
	runtime.GC()
	if err := writeProfile(out, "heap", "heap-fixed.prof", 0); err != nil {
		return err
	}
	runtime.KeepAlive(cache)
	return os.WriteFile(filepath.Join(out, "heap-shape.txt"), []byte(fmt.Sprintf("same receipt bytes=true\nbad len=16 cap=%d\nfixed len=16 cap=16\nprofile is allocation-site evidence, not an object reference graph or RSS promise\n", retainedBytes)), 0600)
}

// reachableGate 故意模拟程序还保存着 channel，却忘记按业务生命周期发停止通知。
// leak detector 的可达性分析会认为将来仍可能唤醒，所以业务泄漏不能只靠该 profile 排除。
var reachableGate chan struct{}

// unreachableWaiter 的 channel 只保存在永远阻塞的 goroutine 栈里，任何可运行 owner
// 都无法关闭/发送它。错误被隔离在有界子进程：不能回收此 goroutine，最终进程退出清理。
func unreachableWaiter(started chan<- struct{}) { gate := make(chan struct{}); close(started); <-gate }

// reachableWaiter 的退出通知与 done 都由调用方拥有；坏版本暂时不通知，修复后 close
// gate 并等 done。关闭只是广播，done 才确认 worker 的 defer 已执行完成。
func reachableWaiter(gate <-chan struct{}, started chan<- struct{}, done chan<- struct{}) {
	defer close(done)
	close(started)
	<-gate
}

// LeakChild 由 cmd/demo 的显式子进程入口调用，不创建 HTTP 服务。每种 profile 会触发
// runtime 自己的泄漏检查。对不可达等待要求检出；对可达等待记录盲区，然后由 owner 清理。
// 不能把 reported=0 翻译为“没有泄漏”：这里只检验 Go 1.27 当前实现的这两类条件。
func LeakChild(kind string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan struct{})
	done := make(chan struct{})
	function := "unreachableWaiter"
	if kind == "unreachable" {
		go unreachableWaiter(started)
	} else if kind == "reachable" {
		reachableGate = make(chan struct{})
		go reachableWaiter(reachableGate, started, done)
		function = "reachableWaiter"
	} else {
		return errors.New("invalid leak child kind")
	}
	select {
	case <-started:
	case <-ctx.Done():
		return ctx.Err()
	}
	stack, err := waitingStack(ctx, function, "chan receive")
	if err != nil {
		return err
	}
	profile := pprof.Lookup("goroutineleak")
	if profile == nil {
		return errors.New("Go 1.27 goroutineleak unavailable")
	}
	var leaks bytes.Buffer
	// Go 1.27 的 writeGoroutineLeak(debug>=2) 会输出全部栈，包括未泄漏的。
	// 因此不能对 debug=2 做“含函数名=被检出”的判断；debug=1 才只列泄漏记录。
	if err := profile.WriteTo(&leaks, 1); err != nil {
		return err
	}
	reported := strings.Contains(leaks.String(), function)
	fmt.Printf("kind=%s runtime=%s live_wait_stack=true detected_target=%t leak_count=%d debug=1\n", kind, runtime.Version(), reported, profile.Count())
	fmt.Printf("--- goroutineleak actual text ---\n%s\n", leaks.String())
	if kind == "unreachable" {
		if !reported {
			return errors.New("unreachable waiter not detected in this sample")
		}
		fmt.Println("unreachable child exits; no parent-owned goroutine or listener remains")
	} else {
		if reported {
			return errors.New("reachable sample no longer demonstrates documented blind spot")
		}
		// 存活栈独立证明 worker 仍然阻塞，即便泄漏 profile 没给出它。
		for _, block := range strings.Split(string(stack), "\n\n") {
			if strings.Contains(block, function) {
				fmt.Printf("--- live waiting stack ---\n%s\n", block)
			}
		}
		close(reachableGate)
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		reachableGate = nil
		fmt.Println("owner repair: close(gate) then await(done); worker exit confirmed")
	}
	return nil
}
