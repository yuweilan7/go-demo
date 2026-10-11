package lesson

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// task 保存一个已创建 goroutine 的最终值和 error。done 由任务入口的 defer 关闭，
// owner 等到 done 后才能读 value/err；只取消 ctx 或看到中间 result 均不足以安全读取。
type task struct {
	done   chan struct{}
	value  string
	err    error
	joined bool
}

// taskGroup 是每个并发实验的局部 owner，最多创建2个或8个任务，不无限扩 worker。
// cancel 是停止通知；releases 解除本课 gate；forceClose 只关闭本课独占连接以救援 I/O。
type taskGroup struct {
	ctx        context.Context
	cancel     context.CancelFunc
	tasks      []*task
	releases   []func()
	forceClose func() error
	report     *Report
}

// newTaskGroup 被实验函数调用，parent 是本次请求总预算；返回的指针由该函数独占管理。
func newTaskGroup(parent context.Context, report *Report) *taskGroup {
	ctx, cancel := context.WithCancel(parent)
	return &taskGroup{ctx: ctx, cancel: cancel, report: report}
}

// start 在启动前登记任务；即使下一行实验就失败，finish 仍会收尾全部已登记的任务。
// fn 取得共享 ctx，但 task 的结果槽仅由自身写入；done close 与 owner receive 建立同步。
func (g *taskGroup) start(fn func(context.Context) (string, error)) *task {
	t := &task{done: make(chan struct{})}
	g.tasks = append(g.tasks, t)
	g.report.TasksStarted++
	go func() {
		defer close(t.done) // 真正执行到函数退出才发布 Done；不提前把结果发送当 join。
		t.value, t.err = fn(g.ctx)
	}()
	return t
}

// wait 只等待已创建任务退出，不发停止。caller 在正常步骤可用请求预算，
// finish 则总用独立新预算，因此请求已经失败不会立即跳过 join。
func (g *taskGroup) wait(ctx context.Context) error {
	for _, t := range g.tasks {
		select {
		case <-t.done:
			if !t.joined {
				t.joined = true
				g.report.TasksJoined++
			}
		case <-ctx.Done():
			return fmt.Errorf("owner 等待真实 Done：%w", ctx.Err())
		}
	}
	return nil
}

// finish 由实验 defer 调用。参数是 caller 的具名 error 地址，清理错误加入同一返回链。
// 顺序是 cancel -> release 所有 gate -> 新预算 join -> 必要时 Close 独占 client -> 再 join。
// socket I/O 不一定被 cancel 中断，所以 forceClose 与实际 Done 必须独立存在。
func (g *taskGroup) finish(retErr *error) {
	g.cancel()
	for _, release := range g.releases {
		release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	err := g.wait(ctx)
	cancel()
	if err != nil {
		*retErr = errors.Join(*retErr, err)
		if g.forceClose != nil {
			*retErr = errors.Join(*retErr, g.forceClose())
		}
		rescue, rescueCancel := context.WithTimeout(context.Background(), 3*time.Second)
		*retErr = errors.Join(*retErr, g.wait(rescue))
		rescueCancel()
	}
}

// onceRelease 创建一个幂等的 gate 释放函数：正常路径与 defer 可以都调用，不会重复 close panic。
func onceRelease(gate chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

// await 在当前具体屏障上等待，caller 可以辨认是哪项状态没有出现。
// <-chan 只允许本函数接收；channel 的 close 权限/生命周期仍属于创建它的 owner。
func await(ctx context.Context, signal <-chan struct{}, label string) error {
	select {
	case <-signal:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待 %s：%w", label, ctx.Err())
	}
}
