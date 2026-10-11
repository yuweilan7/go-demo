package lab

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Signal 是明确屏障：关闭 channel 广播一个事实。sync.Once 允许正常释放与失败清理
// 都调用 Open。Signal 自己不代表任务完成；Finished 才是每个 Job 的退出广播。
type Signal struct {
	C    chan struct{}
	once sync.Once
}

func newSignal() *Signal { return &Signal{C: make(chan struct{})} }
func (s *Signal) Open()  { s.once.Do(func() { close(s.C) }) }

// wait 由 owner 等一个事实，ctx 仅限制这次观察，不会取消被观察的 goroutine。
// 它返回错误时，caller 的 defer 仍必须 cancel、解除 gate 并用新预算 join。
func wait(ctx context.Context, signal <-chan struct{}, label string) error {
	// <-chan限制本函数只能接收；关闭signal或Done都使对应分支就绪，没有就绪分支时此行阻塞。
	select {
	case <-signal:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待 %s: %w", label, ctx.Err())
	}
}

// Job 的 result 容量为 1：worker 发结果不依赖 owner 此刻接收。
// cleanup 可在结果后等待另一屏障，所以结果可读绝不等于 Finished 已关闭。
// finished 关闭以前写 cleanupErr；owner 读取关闭信号以后才能无锁读取该 error。
type Job struct {
	cancel      context.CancelFunc
	result      chan error
	finished    chan struct{}
	joinWaiting *Signal
	cleanupErr  error
}

var ErrWorkerPanic = errors.New("后台任务panic已在自身goroutine入口隔离")

// guardedWork 在startJob创建的那个goroutine内同步调用work，并在同一调用栈defer recover。
// err是具名结果槽位；panic展开后defer把它改成可分类error，再交给Job.result。
// 这不会恢复panic语句后的业务代码，也不能撤销work在panic前已经完成的提交。
func guardedWork(ctx context.Context, work func(context.Context) error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrWorkerPanic
		}
	}()
	return work(ctx)
}

// startJob 由 Rig 或实验调用。work 是当前任务，cleanup 是退出前动作（通常 nil）；
// active 是创建者的共享计数。参数函数闭包应只捕获明确拥有的对象/不可变值，禁止捕获 c。
func startJob(parent context.Context, budget time.Duration, active *atomic.Int64, work func(context.Context) error, cleanup func() error) *Job {
	// :=在当前函数作用域同时声明ctx/cancel；ctx是接口值，内部引用同一取消对象，
	// 不是复制一份独立的取消状态。&Job创建可由owner/worker共同引用的任务句柄。
	ctx, cancel := context.WithTimeout(parent, budget)
	j := &Job{cancel: cancel, result: make(chan error, 1), finished: make(chan struct{}), joinWaiting: newSignal()}
	active.Add(1)
	go func() {
		// 后注册先执行：减计数 -> cancel 释放 timer -> 最后 finished 通知清理已完成。
		defer close(j.finished)
		defer cancel()
		defer active.Add(-1)
		j.result <- guardedWork(ctx, work)
		if cleanup != nil {
			j.cleanupErr = cleanup()
		}
	}()
	return j
}

// Result 由 owner 至多消费一次。第一个 error 是任务实际结果，第二个 error 表示观察失败；
// 观察失败没有消费 result、没有证明 worker 退出，调用方必须继续 StopAndJoin。
func (j *Job) Result(ctx context.Context) (error, error) {
	select {
	case err := <-j.result:
		return err, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("只结束 result 等待，任务尚未 join: %w", ctx.Err())
	}
}

func (j *Job) Cancel()                   { j.cancel() }
func (j *Job) Finished() <-chan struct{} { return j.finished }

// StopAndJoin 由每个 owner 在创建后立即 defer。顺序固定：cancel -> 解除必要 gate ->
// context.Background 派生独立 3 秒收尾预算 -> 等 finished。已经取消的工作 ctx 不参与 join。
// 这只等待完成广播，不二次消费 result；失败必须被 caller 报告，不能伪装成任务已完成。
func (j *Job) StopAndJoin(unblock func()) error {
	j.Cancel()
	if unblock != nil {
		unblock()
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	select {
	case <-j.finished:
		return j.cleanupErr
	default:
		j.joinWaiting.Open()
	}
	if err := wait(cleanupCtx, j.finished, "Job.Finished"); err != nil {
		return err
	}
	return j.cleanupErr
}

// joinInto 用于具名 error 返回的 defer；*err 指向 caller 的最终结果槽位。
// defer 在 caller return 之后执行，errors.Join 保留主失败与清理失败两个原因。
func joinInto(err *error, j *Job, unblock func()) {
	*err = errors.Join(*err, j.StopAndJoin(unblock))
}
