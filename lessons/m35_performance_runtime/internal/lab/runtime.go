package lab

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"
)

// StackResult 的字节数是 runtime 当前观测，不能作为 goroutine 栈大小的常量。
// Checksum 来自实际递归帧，证明工作发生过；只对它和 barrier 关系做稳定断言。
type StackResult struct {
	Before, Deep, After uint64
	Checksum            uint64
}

// StackDemo 由入口调用，拥有 4 个 worker 的停止门 release 和完成等待 wg。
// 参数 ctx 限制 owner 等待 ready 的时间；即使超时，owner 也先释放并等待全部退出。
func StackDemo(ctx context.Context) (result StackResult, err error) {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	result.Before = stats.StackInuse
	ready := make(chan struct{}, 4)
	release := make(chan struct{})
	checksums := make(chan uint64, 4)
	var wg sync.WaitGroup
	for range 4 {
		// Add 在启动前执行。每个 worker 仅返回一个整数，channel 容量可容纳全部结果，
		// 因而 owner 在超时清理时先 wg.Wait 不会卡在“没人接收结果”的发送上。
		wg.Add(1)
		go func() {
			defer wg.Done()
			checksums <- stackFrames(64, ready, release)
		}()
	}
	// 无论正常还是错误都由唯一 owner 关闭 release、等待 goroutine，再读取最终统计。
	// 这里没有其他 closer，因此不会出现重复 close。
	defer func() {
		close(release)
		wg.Wait()
		close(checksums)
		for checksum := range checksums {
			result.Checksum += checksum
		}
		runtime.ReadMemStats(&stats)
		result.After = stats.StackInuse
	}()
	for range 4 {
		select {
		case <-ready:
			// 每条信号都来自递归最深处，发送之后 worker 等 release；
			// 收齐 4 条才测 Deep，避免用 sleep 猜测栈是否已经增长。
		case <-ctx.Done():
			return result, fmt.Errorf("等待递归屏障: %w", ctx.Err())
		}
	}
	runtime.ReadMemStats(&stats)
	result.Deep = stats.StackInuse
	return result, nil
}

// stackFrames 由每个 worker 递归调用；每帧的数组必须活到递归返回之后。
// noinline 仅用于让递归栈实验的源码结构可观测，不能用这组编译选项评估生产性能。
//
//go:noinline
func stackFrames(depth int, ready chan<- struct{}, release <-chan struct{}) uint64 {
	var frame [2048]byte
	frame[0] = byte(depth)
	frame[len(frame)-1] = byte(depth + 1)
	var child uint64
	if depth == 0 {
		ready <- struct{}{}
		// 当前 goroutine 在 channel 接收处等待，不是在忙循环里等待。
		// 这让 M/P 可以运行其他就绪 G；具体被哪个 OS 线程接手不属于业务协议。
		<-release
	} else {
		child = stackFrames(depth-1, ready, release)
	}
	answer := child + uint64(frame[0]) + uint64(frame[len(frame)-1])
	// 保活到此处，使编译器不能在最深处等待期间丢掉前面各帧数组。
	// KeepAlive 不延长到函数返回之后，不锁住对象，也不建立 goroutine 间同步。
	runtime.KeepAlive(&frame)
	return answer
}

// CounterResult 用总数验证行为，用 LockCalls 表达实际锁获取次数。
// Elapsed 是这一次环境下的 wall time；它不能代替多样本 benchmark。
type CounterResult struct {
	Total, LockCalls int
	Elapsed          time.Duration
}

// CounterDemo 是锁定位实验：4 个 worker 各处理 50,000 条订单，只需要最终总数。
// batch=1 每条都改共享计数；batch=100 在本 worker 合并后再加，是单一批量策略改动。
// 如果业务需要每一条的即时可见进度，批量方案就改变契约，不能这样替换。
func CounterDemo(ctx context.Context, batch int) (CounterResult, error) {
	if batch < 1 || batch > 1000 {
		return CounterResult{}, fmt.Errorf("batch 必须在 [1,1000]，得到 %d", batch)
	}
	const perWorker = 50_000
	var total, calls int
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	begin := time.Now()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // 所有 worker 使用 owner 关闭的同一个开始门。
			for completed := 0; completed < perWorker; {
				// 每一批前检查取消。取消是通知，owner 仍需 wg.Wait 才能确认退出。
				if ctx.Err() != nil {
					return
				}
				n := min(batch, perWorker-completed)
				mu.Lock() // 锁仅保护共享 total/calls；不会在锁内格式化、I/O 或等待 channel。
				total += n
				calls++
				mu.Unlock()
				completed += n
			}
		}()
	}
	close(start)
	wg.Wait() // 返回前所有写入者已退出，可直接读取 total/calls。
	result := CounterResult{Total: total, LockCalls: calls, Elapsed: time.Since(begin)}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("计数取消且全部 worker 已退出: %w", err)
	}
	return result, nil
}
