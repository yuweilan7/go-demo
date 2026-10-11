// Package course 把 M12 的实验组织为可重复调用的函数，供 demo 与独立断言共用。
package course

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// Trace 由实验入口创建，多参与者共用同一个指针。mu 同时保护日志编号和输出，
// 使一行日志不会被其他 goroutine 切开；编号只表示记录先后，不创造业务优先级。
// writer 可为 nil；测试仍执行真实 channel 协议，只省略控制台输出。
type Trace struct {
	mu     sync.Mutex
	writer io.Writer
	seq    int
}

// NewTrace 把入口选择的输出位置交给 Trace 管理；它不拥有、也不关闭 writer。
func NewTrace(writer io.Writer) *Trace { return &Trace{writer: writer} }

// Log 由任意实验参与者调用。format/args 说明当前事实，actor/code 对应 README 时间线。
// 接收者 *Trace 是共享对象；加锁后生成编号，defer 在本次 Log 返回时解锁。
// ...any 接收任意数量的格式参数并组成 slice，args... 在调用 Sprintf 时再把这些实参展开。
func (t *Trace) Log(actor, code, format string, args ...any) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	if t.writer != nil {
		fmt.Fprintf(t.writer, "%03d %-12s %-20s %s\n", t.seq, actor, code, fmt.Sprintf(format, args...))
	}
}

// await 供实验 owner 等待一个不携带业务值的屏障。关闭该 channel 的参与者发出通知，
// 收到通知后 owner 才进入下一步。ctx 是兜底截止时间，不用于猜测调度先后。
func await(ctx context.Context, signal <-chan struct{}, label string) error {
	select {
	case <-signal:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待 %s: %w", label, ctx.Err())
	}
}

// cleanupAwait 给实验退出路径建立独立且有限的等待。业务 ctx 已取消时也要等待参与者
// 观察取消、执行 defer、公布结束；所以使用新的有界 context，而不是已 ready 的业务 Done。
func cleanupAwait(signal <-chan struct{}, label string) error {
	cleanup, cancel := context.WithTimeout(context.Background(), CleanupLimit)
	defer cancel()
	return await(cleanup, signal, label)
}
