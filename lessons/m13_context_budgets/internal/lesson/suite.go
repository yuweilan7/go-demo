// Package lesson 提供可被 CLI 和测试共同执行的 M13 实验。
// 这些实验使用 Context 的公开契约和真实 loopback HTTP；不依赖全局端口或外部服务。
package lesson

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// trace 被同一实验的 owner、client goroutine 和 handler 共享。
// seq 和 writer 在同一把锁下使用，所以一行日志是完整事件；日志次序是观察，
// 真正证明先后的仍是下面各实验的 channel 屏障，而不是打印快慢。
type trace struct {
	mu    sync.Mutex
	start time.Time
	seq   int
	out   io.Writer
}

// event 由任意参与者同步调用。actor 是正在执行的函数角色，format 描述其状态。
// 指针接收者 t 指向共享 trace；不能复制带 Mutex 的值再各自锁一份。
func (t *trace) event(actor, format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	fmt.Fprintf(t.out, "%02d +%4dms %-14s %s\n", t.seq, time.Since(t.start).Milliseconds(), actor, fmt.Sprintf(format, args...))
}

// Run 由 main 或 TestScenarios 调用。ctx 是 caller 的总预算，out 是 caller 拥有的输出。
// scenario 选择固定实验；本函数不把 ctx 存进业务 struct，也不关闭 caller 的 writer。
func Run(ctx context.Context, out io.Writer, scenario string) error {
	experiments := []struct {
		name string
		fn   func(context.Context, *trace) error
	}{
		{"tree", runTree}, {"http", runHTTP}, {"budgets", runBudgets},
		{"traps", runTraps}, {"send", runSend}, {"lifecycle", runLifecycle},
	}
	matched := false
	for _, experiment := range experiments {
		if scenario != "all" && scenario != experiment.name {
			continue
		}
		matched = true
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprintf(out, "\n=== %s ===\n", experiment.name)
		// 这里派生出的 scope 是实验安全上限，和实验内部故意取消的 request 不同。
		// 即使危险函数忽略 request，它也有 owner 提供的独立收尾路径。
		scope, cancel := context.WithTimeout(ctx, 8*time.Second)
		t := &trace{start: time.Now(), out: out}
		err := experiment.fn(scope, t)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", experiment.name, err)
		}
		t.event("owner", "PASS %s；已等待本组所有自建任务/handler 完成", experiment.name)
	}
	if !matched {
		return fmt.Errorf("未知 scenario %q", scenario)
	}
	return nil
}

// wait 是 owner 等待一个一次性状态屏障的同步函数。
// signal 只能接收（<-chan），其关闭由生产该事实的参与者负责；本函数不抢关它。
// signal 完成和 scope 到期同时就绪时 select 不保证优先级，失败分支仍是有界退出。
func wait(ctx context.Context, signal <-chan struct{}, name string) error {
	select {
	case <-signal:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待 %s: %w", name, ctx.Err())
	}
}

// check 由每个实验在读到屏障或持锁快照后调用，用实际业务不变量决定成功。
// 格式化参数在调用时求值；本函数不会自动重试或改变错误结果。
func check(ok bool, format string, args ...any) error {
	if !ok {
		return fmt.Errorf("断言失败: "+format, args...)
	}
	return nil
}

// closed 只用于观察一个本来应继续等待的任务；default 表示此刻尚未关闭。
// 只有搭配明确屏障和无其他返回分支，才能用该观察证明“取消后仍未退出”。
func closed(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

// holdFor 表示可取消的定时等待，用来测排队时间或本实验的预算到期。
// 它不是用 Sleep 猜 goroutine 调度：caller 已用屏障确认状态才启动这段计时。
// timer 由本函数创建和 Stop；错误原样交回上层决定是否继续业务。
func holdFor(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// join 是 owner 的最终收尾等待，不能继续用可能已经取消的业务 ctx。
// 它只等已经被通知停止/放行的自建任务，最多 2 秒；不会重新执行业务。
// 这里的独立 Background 有明确 owner、deadline 和 cancel，所以不是传播断链反例。
func join(done <-chan struct{}, name string) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return wait(cleanupCtx, done, name)
}
