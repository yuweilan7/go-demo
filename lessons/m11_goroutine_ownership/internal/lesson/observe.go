// Package lesson 用合成订单演示 goroutine 的拥有者、等待边界和收尾协议。
// 本课没有数据库或网络客户端；资源计数表示本实验显式取得/释放的工作令牌。
package lesson

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// Trace 由实验入口创建并共享给所有参与者。mu 只保护日志序号与记录，
// 不能替代业务 channel 的完成协议；日志的先后也不是调度器的固定顺序。
type Trace struct {
	mu     sync.Mutex
	out    io.Writer
	events []string
}

// NewTrace 接收入口选择的输出位置；测试传 io.Discard，演示传 os.Stdout。
// 返回指针让参与者共享同一个锁与事件列表，禁止复制正在使用的 Trace。
func NewTrace(out io.Writer) *Trace {
	if out == nil {
		out = io.Discard
	}
	return &Trace{out: out}
}

// Event 由 caller/worker 在有业务意义的状态变化处调用。
// defer Unlock 在函数返回时释放锁；持锁的代码不等待业务 channel，避免锁顺序循环。
func (t *Trace) Event(event string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
	fmt.Fprintf(t.out, "%02d %s\n", len(t.events), event)
}

// Events 给断言返回 slice 的独立副本，避免测试持有内部底层数组。
func (t *Trace) Events() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.events...)
}

// Tracker 记录本实验拥有的业务 goroutine 与资源令牌。
// 它没有观察整个 Go runtime；零值表示“登记过的任务均已清理”，不是进程中零 goroutine。
type Tracker struct {
	mu        sync.Mutex
	active    map[string]bool
	resources int
}

// NewTracker 由 owner 创建；同一指针传给它管理的工作函数。
func NewTracker() *Tracker { return &Tracker{active: make(map[string]bool)} }

// acquire 必须由 owner 在 go 语句前调用，登记预期工作及它的一个合成令牌。
// 因而即使 worker 尚未获得运行机会，owner 也知道还有一项责任未结束。
func (t *Tracker) acquire(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active[id] {
		panic("教学程序内部错误：重复登记 " + id)
	}
	t.active[id] = true
	t.resources++
}

// release 仅由相应 worker 的退出清理调用一次；owner 不能替 worker 假装已退出。
func (t *Tracker) release(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.active, id)
	t.resources--
}

// Counts 由 owner/测试在同步边界后读取；锁保护的是这两项观测值。
func (t *Tracker) Counts() (active, resources int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.active), t.resources
}

// await 是实验驱动器的有界接收，不会改变被观察任务的状态。
// ctx 到期仅使观察者返回 error；调用者仍必须放行/取消任务并等待其清理。
func await(ctx context.Context, signal <-chan struct{}, name string) error {
	select {
	case <-signal:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待 %s: %w", name, ctx.Err())
	}
}

// require 把正常演示的业务不变量转成 error，交回 cmd/demo 决定进程结果。
// 测试还会独立核对返回值与实际事件，不能仅依据这条 PASS 日志。
func require(ok bool, message string) error {
	if !ok {
		return fmt.Errorf("断言失败：%s", message)
	}
	return nil
}
