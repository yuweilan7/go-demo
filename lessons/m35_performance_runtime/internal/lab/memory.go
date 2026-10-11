package lab

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"strings"
	"time"
)

const MiB = uint64(1 << 20)

// MemorySample 同时保留不同口径，防止把 HeapAlloc 当成进程总内存。
// RuntimeBytes 是 GOMEMLIMIT 对应的 Sys-HeapReleased；WorkingSet 是 OS 观测。
// Windows 的 WorkingSet 与 Linux RSS 都是驻留视角，但不保证所有平台语义完全相同。
type MemorySample struct {
	HeapAlloc, RuntimeBytes, StackInuse, HeapGoal uint64
	WorkingSet                                    uint64
	WorkingSetError                               string
	NumGC                                         uint32
}

// SampleMemory 在实验主 goroutine 上读取。ReadMemStats 给 Go 运行时账本快照，
// OSWorkingSet 在后面读取，所以两个数不属于完全相同的瞬间，不能做逐字节等式。
func SampleMemory() MemorySample {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	samples := []metrics.Sample{{Name: "/gc/heap/goal:bytes"}}
	metrics.Read(samples)
	result := MemorySample{HeapAlloc: stats.HeapAlloc, RuntimeBytes: stats.Sys - stats.HeapReleased,
		StackInuse: stats.StackInuse, NumGC: stats.NumGC}
	if samples[0].Value.Kind() == metrics.KindUint64 {
		result.HeapGoal = samples[0].Value.Uint64()
	}
	if value, err := OSWorkingSet(); err == nil {
		result.WorkingSet = value
	} else {
		// 平台测量不可用不伪造为已测到零。Go 堆实验还能完成，错误原样交给报告。
		result.WorkingSetError = err.Error()
	}
	return result
}

// OSWorkingSet 只读当前进程的 OS 内存口径，不使用 unsafe 做数据零拷贝。
// Windows 通过系统自带 PowerShell 的 Get-Process 查询本进程整数 PID；
// context 负责最多等 10 秒，CommandContext.Run/Output 会等待并回收该子进程。
func OSWorkingSet() (uint64, error) {
	if runtime.GOOS == "windows" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		query := "(Get-Process -Id " + strconv.Itoa(os.Getpid()) + ").WorkingSet64"
		output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", query).Output()
		if err != nil {
			return 0, fmt.Errorf("Windows WorkingSet 查询: %w", err)
		}
		return strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	}
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/self/status")
		if err != nil {
			return 0, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "VmRSS:" {
				kb, err := strconv.ParseUint(fields[1], 10, 64)
				return kb * 1024, err
			}
		}
	}
	return 0, fmt.Errorf("%s 尚未实现 OS 驻留内存采样", runtime.GOOS)
}

// AffinityDescription 由入口输出构建环境。Windows 的 ProcessorAffinity 是允许
// OS 线程在哪些 CPU 上调度的位图，不代表这些 CPU 当前没有其他进程争抢。
func AffinityDescription() string {
	if runtime.GOOS != "windows" {
		return "本入口未读取 affinity；Linux 容器还需检查 cpu.max/CPU affinity"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	query := "(Get-Process -Id " + strconv.Itoa(os.Getpid()) + ").ProcessorAffinity.ToInt64()"
	output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", query).Output()
	if err != nil {
		return "查询失败: " + err.Error()
	}
	return "Windows CPU affinity mask=" + strings.TrimSpace(string(output)) + "; Linux cgroup quota 不适用，Windows Job CPU 限制未测"
}

// RetentionResult 由独立子进程交给父入口。LogicalBytes 只有 128 字节，
// HeapAlloc 却能因对象别名保留大数组；profile 再定位实际分配函数。
type RetentionResult struct {
	Variant                string
	LogicalBytes, Checksum uint64
	Before, After          MemorySample
}

// RetentionWork 仅由 retention 子进程调用，固定创建 8 个 4MiB 的响应体。
// keep 是当前调用唯一的长期持有者。不同 variant 使用相同数据并执行同一个 GC。
// after 回调在 keep 仍存活时写 heap profile，防止 profile 看到提前回收后的状态。
func RetentionWork(variant string, after func() error) (RetentionResult, error) {
	if variant != "alias" && variant != "cap" && variant != "clone" {
		return RetentionResult{}, fmt.Errorf("retention variant 必须是 alias/cap/clone")
	}
	runtime.GC()
	result := RetentionResult{Variant: variant, Before: SampleMemory()}
	keep := make([][]byte, 0, 8)
	for i := 0; i < 8; i++ {
		keep = append(keep, retainOne(variant, byte(i+1)))
	}
	runtime.GC() // 只为建立观测边界，业务请求不应逐条强制 GC。
	result.After = SampleMemory()
	for i, header := range keep {
		if len(header) != 16 {
			return result, fmt.Errorf("保留长度错误: %d", len(header))
		}
		for _, b := range header {
			if b != byte(i+1) {
				return result, fmt.Errorf("保留内容错误: 第 %d 个 body", i)
			}
			result.Checksum += uint64(b)
		}
		result.LogicalBytes += uint64(len(header))
	}
	if after != nil {
		if err := after(); err != nil {
			return result, err
		}
	}
	// keep 本来只在前面循环使用；放到 profile 后保活，明确定义所测生命周期。
	runtime.KeepAlive(keep)
	return result, nil
}

// retainOne 返回后不保留完整 body 的 slice 描述符，但 alias/cap 返回值仍指向它。
// noinline 令 heap profile 的分配归属容易定位；这里只测保留，不比较其 CPU 性能。
//
//go:noinline
func retainOne(variant string, marker byte) []byte {
	body := make([]byte, 4*MiB)
	// 触碰每一页让 OS 驻留视角也可观察；前 16 字节表达业务 header 的合成内容。
	for i := 0; i < len(body); i += 4096 {
		body[i] = marker
	}
	for i := range 16 {
		body[i] = marker
	}
	switch variant {
	case "alias":
		return HeaderAlias(body)
	case "cap":
		return HeaderCapTrap(body)
	default:
		return HeaderClone(body)
	}
}

// GCResult 的中途快照 WorkMemory 在强制终态 GC 前读取；After 是规范化终态。
// WorkNumGC 只统计分配工作期间的周期，不能用多个独立进程的绝对 NumGC 比较。
type GCResult struct {
	GOGC                    int
	Limit                   int64
	LiveBytes, ChurnBytes   uint64
	WorkNumGC               uint32
	WorkTotalAlloc, PauseNS uint64
	WorkElapsedNS           int64
	WorkMemory, After       MemorySample
	Checksum                uint64
}

// GCWork 在专属子进程调整 GC 参数，创建 12MiB 必须存活的数据，再分配 96MiB。
// debug 参数是进程级，不是请求级；defer 恢复旧值，父进程与其他模块不受影响。
// 数据规模、循环数和 owner 超时共同限定实验，绝不通过耗尽系统内存来证明软限制。
func GCWork(gogc int, limit int64) (GCResult, error) {
	if gogc < 1 || gogc > 1000 || limit < int64(8*MiB) {
		return GCResult{}, fmt.Errorf("有界 GC 实验要求 GOGC=[1,1000]、limit>=8MiB")
	}
	previousGC := debug.SetGCPercent(gogc)
	previousLimit := debug.SetMemoryLimit(limit)
	defer debug.SetGCPercent(previousGC)
	defer debug.SetMemoryLimit(previousLimit)
	live := make([][]byte, 12)
	for i := range live {
		live[i] = make([]byte, MiB)
		for j := 0; j < len(live[i]); j += 4096 {
			live[i][j] = byte(i + 1)
		}
	}
	runtime.GC()
	var before, work runtime.MemStats
	runtime.ReadMemStats(&before)
	result := GCResult{GOGC: gogc, Limit: limit, LiveBytes: 12 * MiB, ChurnBytes: 96 * MiB}
	// ring 只保留最近 16 个 64KiB 块，避免“为演示 GC”把所有临时分配都保留下来。
	// make 的块被放入跨迭代 slice，因此明确需要实际分配；不会成为无作用空工作。
	ring := make([][]byte, 16)
	begin := time.Now()
	for i := 0; i < 1536; i++ {
		block := make([]byte, 64<<10)
		block[0], block[len(block)-1] = byte(i), byte(i>>8)
		ring[i%len(ring)] = block
		result.Checksum += uint64(block[0]) + uint64(block[len(block)-1])
	}
	result.WorkElapsedNS = time.Since(begin).Nanoseconds()
	runtime.ReadMemStats(&work)
	result.WorkNumGC = work.NumGC - before.NumGC
	result.WorkTotalAlloc = work.TotalAlloc - before.TotalAlloc
	result.PauseNS = work.PauseTotalNs - before.PauseTotalNs
	result.WorkMemory = SampleMemory()
	runtime.GC()
	result.After = SampleMemory()
	// 终态包含业务 live 和固定大小 ring；这两项持有到观测后才能比较口径。
	runtime.KeepAlive(live)
	runtime.KeepAlive(ring)
	return result, nil
}
