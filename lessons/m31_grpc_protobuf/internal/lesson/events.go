// Package lesson 把协议服务和实验协调放在同一模块内，真实请求仍必须经过 loopback TCP/gRPC。
package lesson

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// event 是一次可核对的状态变化；run 区分并发请求，stage 定位具体等待/副作用。
type event struct{ run, stage string }

// journal 由 harness 创建，并被实验、拦截器和 handler 共享。
// mutex 同时保护 events 和输出；slice append 不是并发安全操作，不能仅靠 RPC 的 context 保护它。
type journal struct {
	mu     sync.Mutex
	events []event
	out    io.Writer
}

// add 在当前 goroutine 同步记录事件，不创建日志 goroutine，因此不用额外等待后台输出。
func (j *journal) add(run, stage string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.events = append(j.events, event{run, stage})
	fmt.Fprintf(j.out, "[%s] %s\n", run, stage)
}

// stages 返回指定请求的独立 slice 副本，调用方遍历期间不会和其他 append 共享底层数组。
func (j *journal) stages(run string) []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	var result []string
	for _, e := range j.events {
		if e.run == run {
			result = append(result, e.stage)
		}
	}
	return result
}

// waitSignal 由实验 owner 调用；channel 关闭代表服务端到了屏障/退出，ctx 限制 owner 等待时间。
// <-chan 明确本函数不能发送/关闭该信号，关闭权保留在实际 handler。
func waitSignal(ctx context.Context, signal <-chan struct{}, name string) error {
	select {
	case <-signal:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待 %s: %w", name, ctx.Err())
	}
}

// check 统一把不变量失败变成 error 交给 Run 的调用者；不在 library 内 os.Exit，确保 defer 能收尾。
func check(ok bool, format string, args ...any) error {
	if !ok {
		return fmt.Errorf("断言失败: "+format, args...)
	}
	return nil
}
