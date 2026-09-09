package main

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

var (
	ErrInvalidCapacity  = errors.New("pool capacity must be greater than zero")
	ErrInvalidQueueSize = errors.New("pool queue size cannot be negative")
	ErrNilTask          = errors.New("task and task function cannot be nil")
	ErrTaskSubmitted    = errors.New("task has already been submitted")
	ErrPoolClosed       = errors.New("pool is closed")
)

// Task 是一次只能提交一次的工作单元。
//
// done 会在任务函数返回（或 panic 被转换成 error）之后关闭。因此 Wait 既是在等待结果，
// 也是一个同步点：Wait 返回时，一定能看到 worker 在关闭 done 之前写入的 err。
type Task struct {
	name string
	f    func() error
	done chan struct{}

	// submitted 防止同一个 Task 被执行两次，否则第二次执行会重复 close(done) 并 panic。
	submitted atomic.Bool
	err       error
}

func NewTask(name string, f func() error) *Task {
	return &Task{
		name: name,
		f:    f,
		done: make(chan struct{}),
	}
}

func (t *Task) Name() string {
	if t == nil {
		return ""
	}
	return t.name
}

// Wait 等待已经成功提交的任务完成，并返回任务函数的错误。
// 对一个从未成功提交的任务调用 Wait 会一直等待，这是调用方需要遵守的生命周期约定。
func (t *Task) Wait() error {
	if t == nil {
		return ErrNilTask
	}

	<-t.done
	return t.err
}

func (t *Task) execute() {
	defer close(t.done)

	// 单个任务不应该因为 panic 杀死一个长期存活的 worker。
	// 这里选择把 panic 转成 error；真实服务也可以在此记录堆栈或让进程按策略退出。
	defer func() {
		if recovered := recover(); recovered != nil {
			t.err = fmt.Errorf("task %q panicked: %v", t.name, recovered)
		}
	}()

	t.err = t.f()
}

// Pool 由固定数量的长期 worker 和一个有界任务队列组成。
//
// capacity 限制的是“同时执行任务”的上限；queueSize 限制的是“等待执行任务”的上限。
// 两者不是一回事。queueSize 为 0 时使用无缓冲 channel，每次提交都必须直接交给某个 worker。
type Pool struct {
	capacity int
	jobs     chan *Task
	workers  sync.WaitGroup

	// stateMu 协调 Submit 和 CloseAndWait：
	// Submit 在可能阻塞的发送期间持有读锁，关闭方会等已进入 Submit 的任务成功入队后再 close。
	// 多个提交者可以同时持有读锁，所以它们不会被普通互斥锁完全串行化。
	stateMu sync.RWMutex
	closed  bool
}

// NewPool 创建并立即启动固定数量的 worker。
// worker 在 jobs 被关闭后才退出，所以无需靠 RunningWorkers 判断是否临时创建新 goroutine。
func NewPool(capacity, queueSize int) (*Pool, error) {
	if capacity <= 0 {
		return nil, ErrInvalidCapacity
	}
	if queueSize < 0 {
		return nil, ErrInvalidQueueSize
	}

	p := &Pool{
		capacity: capacity,
		jobs:     make(chan *Task, queueSize),
	}

	p.workers.Add(capacity)
	for i := 0; i < capacity; i++ {
		go p.worker()
	}

	return p, nil
}

func (p *Pool) Capacity() int {
	return p.capacity
}

// Submit 把任务放进队列。
//
// 只有在“没有 worker 能接手并且缓冲队列也已满”时，channel 发送才会阻塞，
// 这就是背压：生产任务的速度会被消费能力反向限制，而不是无限占用内存。
func (p *Pool) Submit(task *Task) error {
	if task == nil || task.f == nil {
		return ErrNilTask
	}

	p.stateMu.RLock()
	defer p.stateMu.RUnlock()

	if p.closed {
		return ErrPoolClosed
	}
	if !task.submitted.CompareAndSwap(false, true) {
		return ErrTaskSubmitted
	}

	p.jobs <- task
	return nil
}

// CloseAndWait 停止接收新任务，排空队列，并等待全部 worker 退出。
// 它可以被重复调用，但不要从池内任务中调用，否则任务会等待包含自己的 worker 退出。
func (p *Pool) CloseAndWait() {
	p.stateMu.Lock()
	if !p.closed {
		p.closed = true
		close(p.jobs)
	}
	p.stateMu.Unlock()

	p.workers.Wait()
}

func (p *Pool) worker() {
	defer p.workers.Done()

	for task := range p.jobs {
		// 哪个 worker 取到哪个任务由 Go runtime 和 channel 的就绪情况共同决定；
		// 程序只能依赖“最多 capacity 个任务并发执行”，不能依赖具体调度顺序。
		task.execute()
	}
}
