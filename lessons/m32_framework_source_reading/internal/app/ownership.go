package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-kratos/kratos/v3/transport"
	"net/url"
	"sync"
)

// activeOwner 是 transport 请求的实际函数账本；它观察 outer HTTP filter/GRPC interceptor 的退出。
// 封账后不允许新请求进入，Wait 的 idle 关闭才能证明所有已登记 handler 都到了末尾。
type activeOwner struct {
	mu     sync.Mutex
	count  int
	sealed bool
	idle   chan struct{}
}

// newActiveOwner创建“暂无请求”的账本：idle立即关闭，使未Start的Root也能直接完成等待。
// 第一个请求进入时会换成未关闭的新idle；这不是关闭后再复用同一个channel。
func newActiveOwner() *activeOwner {
	idle := make(chan struct{})
	close(idle)
	return &activeOwner{idle: idle}
}

// enter 被真实请求 goroutine 调用；返回的 leave 只归该次请求，必须登记为最先 defer（最后执行）。
func (a *activeOwner) enter() (func(), bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sealed {
		return nil, false
	}
	if a.count == 0 {
		a.idle = make(chan struct{})
	}
	a.count++
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.count--
		if a.count == 0 {
			close(a.idle)
		}
	}, true
}

// seal 由 Stop hook 调用；锁内修改保证不会出现“Wait 看见idle以后又新注册请求”。
func (a *activeOwner) seal()      { a.mu.Lock(); defer a.mu.Unlock(); a.sealed = true }
func (a *activeOwner) Count() int { a.mu.Lock(); defer a.mu.Unlock(); return a.count }

// wait 只能在 seal 后用于最终关闭；ctx 应为独立 Background 的收尾预算，不能沿用已取消请求。
func (a *activeOwner) wait(ctx context.Context) error {
	a.mu.Lock()
	idle := a.idle
	a.mu.Unlock()
	return waitDone(ctx, idle, "active handlers")
}

// observedTransport 没有重新实现框架，只装饰真实 transport.Server 的 Start/Stop 和 Endpointer。
// Kratos App 是 Start goroutine owner；此对象发布最后 Done，Root 在 App.Run 返回后仍独立核对它。
type observedTransport struct {
	server transport.Server
	done   chan struct{}
	result error
	name   string
}

func observe(name string, s transport.Server) *observedTransport {
	return &observedTransport{server: s, done: make(chan struct{}), name: name}
}
func (s *observedTransport) Endpoint() (*url.URL, error) {
	return s.server.(transport.Endpointer).Endpoint()
}

// Start 被 App.Run 的 errgroup 调用；具名 err 赋值后再 defer，result 存储在 close(done)之前同步发布。
func (s *observedTransport) Start(ctx context.Context) (err error) {
	defer close(s.done)
	defer func() { s.result = err }()
	return s.server.Start(ctx)
}
func (s *observedTransport) Stop(ctx context.Context) error { return s.server.Stop(ctx) }

// waitDone 由资源 owner 调用，它等完成通知而不是业务结果缓冲；超时保留哪个 owner 未结束的定位。
func waitDone(ctx context.Context, done <-chan struct{}, what string) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("join %s: %w", what, ctx.Err())
	}
}

// Task 是一个由实验/Root 创建的单次 goroutine owner。Result 只是预览，最终 Err 必须 Join Done 后读取。
// 即使结果先写入缓冲、之后还有 defer cancel/Close，也只有最后 defer close(Done) 代表收尾完成。
type Task struct {
	Result chan error
	Done   chan struct{}
	err    error
}

// StartTask 在创建 goroutine 同时交付停止/完成归属；work 不可无限阻塞，必须使用调用方给它的ctx/屏障。
// tail 是显式课程收尾屏障，nil表示没有尾部；caller 在提前失败路径也必须释放它然后 Join。
func StartTask(work func() error, tail <-chan struct{}) *Task {
	return StartOwnedTask(work, nil, tail)
}

// StartOwnedTask把真正的defer cleanup也纳入owner：work的error先发布Result，cleanup在Gate释放后执行，
// 它的error再Join入final err。Caller只能在Done后取完整错误链；cleanup应使用独立有界预算。
func StartOwnedTask(work func() error, cleanup func() error, tail <-chan struct{}) *Task {
	task := &Task{Result: make(chan error, 1), Done: make(chan struct{})}
	go func() {
		defer close(task.Done) // 第一个登记，最后执行；只有这里能 close Done。
		published := false
		defer func() {
			if value := recover(); value != nil {
				task.err = errors.Join(task.err, fmt.Errorf("owner panic: %v", value))
				if !published {
					task.Result <- task.err
				}
			}
		}()
		if cleanup != nil {
			defer func() { task.err = errors.Join(task.err, cleanup()) }()
		}
		if tail != nil {
			defer func() { <-tail }()
		} // 实验人为放大“结果已发布但defer尚未完成”的窗口。
		task.err = work()
		task.Result <- task.err // 容量1让发送不依赖收集者；它不是goroutine完成信号。
		published = true
	}()
	return task
}

// Join 先等最后 Done，channel close 建立 happens-before，然后才读取 final err，race下无需另加Mutex。
func (t *Task) Join(ctx context.Context) error {
	if err := waitDone(ctx, t.Done, "task final defer"); err != nil {
		return err
	}
	return t.err
}

// Finished只做当前状态观察；default说明Done此刻尚不可读。它不阻塞，也不读取未同步发布的final err。
func (t *Task) Finished() bool {
	select {
	case <-t.Done:
		return true
	default:
		return false
	}
}

// Tail 是 HTTP filter 的退出实验计划，不传业务数据；两个 close 权限分别属于 filter 与实验 owner。
type Tail struct {
	Entered, Returning, Done, Gate chan struct{}
	once                           sync.Once
}

func NewTail() *Tail {
	return &Tail{Entered: make(chan struct{}), Returning: make(chan struct{}), Done: make(chan struct{}), Gate: make(chan struct{})}
}
func (t *Tail) Release() { t.once.Do(func() { close(t.Gate) }) }
