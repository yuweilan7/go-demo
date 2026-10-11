package lesson

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// outcome 是一次工作返回的业务结果。Result channel 中能读到它，只说明 fn 已返回；
// wrapper 的附加 cleanup 还可能在等门或失败，所以 Finished 是另一个独立事实。
type outcome struct {
	Value int64
	Err   error
}

// task 只由 group 的 owner 读取 joined/result，worker 只写 cleanupErr。
// close(finished) 与 owner 的接收建立同步，之后才允许读取 cleanupErr。
type task struct {
	name       string
	result     chan outcome
	finished   chan struct{}
	cleanupErr error
	joined     *outcome
}

// group 拥有自己启动的 goroutine 和停止范围。releases 解除所有本课屏障；
// forceClose 只救援本课独占 socket/client，不涉及共享服务/隧道。
type group struct {
	ctx        context.Context
	cancel     context.CancelFunc
	tasks      []*task
	releases   []func()
	forceClose func() error
	report     *Report
}

// newGroup 由单个实验创建；返回指针共享的是同一组资源，不能把它当工作线程拷贝。
func newGroup(parent context.Context, report *Report) *group {
	ctx, cancel := context.WithCancel(parent)
	return &group{ctx: ctx, cancel: cancel, report: report}
}

// start 先登记后启动，保证下一行提前失败时 finish 仍知道要等谁。
// fn 的 ctx 来自本组；cleanup 属于 goroutine wrapper，返回错误由 owner 通过 join 收取。
func (g *group) start(name string, fn func(context.Context) outcome, cleanup func() error) *task {
	t := &task{name: name, result: make(chan outcome, 1), finished: make(chan struct{})}
	g.tasks = append(g.tasks, t)
	g.report.TasksStarted++
	go func() {
		defer close(t.finished) // 最早注册、最后执行；全部wrapper清理之后才能宣布结束。
		if cleanup != nil {
			defer func() { t.cleanupErr = cleanup() }()
		}
		t.result <- fn(g.ctx) // 缓冲1允许交付结果；本行后面的defer尚未执行。
	}()
	return t
}

// join 只能由 owner 调用。先等 Finished，再消费/缓存 Result，重复 join 仍检查完成。
// ctx 是等待预算；不能把一个已取消的工作ctx当作收尾预算。
func (g *group) join(ctx context.Context, t *task) (outcome, error) {
	if err := await(ctx, t.finished, t.name+" Finished"); err != nil {
		return outcome{}, err
	}
	if t.joined == nil {
		select {
		case value := <-t.result:
			t.joined = &value
			g.report.TasksJoined++
		default:
			return outcome{}, errors.New("Finished已关闭但Result缺失")
		}
	}
	return *t.joined, t.cleanupErr
}

// finish 在 start 后立即 defer。cancel只发通知，release才解除屏障，join才证明退出。
// 工作结果已由正常路径join处理则不重复加预期错误；提前失败未读结果则保留其原因。
// errors.Join 同时保留主错误、未处理工作错误、wrapper清理错误与join超时。
func (g *group) finish(retErr *error) {
	g.cancel()
	for _, release := range g.releases {
		release()
	}
	joinAll := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		var combined error
		for _, t := range g.tasks {
			wasJoined := t.joined != nil
			result, err := g.join(ctx, t)
			combined = errors.Join(combined, err)
			if !wasJoined {
				combined = errors.Join(combined, result.Err)
			}
		}
		return combined
	}
	first := joinAll()
	*retErr = errors.Join(*retErr, first)
	if first != nil && g.forceClose != nil {
		*retErr = errors.Join(*retErr, g.forceClose(), joinAll())
	}
}

// onceRelease 让正常路径和defer都能放门；close同一个channel两次会panic，sync.Once
// 把关闭权限集中到这个函数里，而不是让每个worker自行关闭共享门。
func onceRelease(ch chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// await 在调用者goroutine中等待一个明确屏障；<-chan表示只能接收，不能关闭或发送。
// 两个case都就绪时select没有“优先取消”的承诺，可能选择屏障；后续I/O仍接收ctx。
func await(ctx context.Context, ch <-chan struct{}, label string) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待%s: %w", label, ctx.Err())
	}
}

// observe 反复读取真实状态，predicate成立才越过屏障；ticker只是观测节奏，不把
// “等了若干毫秒”当作Redis过期/任务到达等待点的证据。timer由当前函数Stop。
func observe(ctx context.Context, label string, predicate func() (bool, error)) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		ok, err := predicate()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("观察%s: %w", label, ctx.Err())
		}
	}
}
