package course

import (
	"context"
	"errors"
	"time"
)

// task 是创建者持有的句柄。result 只交付业务结果；finished 才证明 wrapper 最后的清理完成。
// 字段 joined/err 只被 owner 操作，wrapperErr 只在 finished 关闭后被 owner 读取。
type task struct {
	cancel     context.CancelFunc
	result     chan error
	finished   chan struct{}
	wrapperErr error
	joined     bool
	err        error
}

// startTask 由分页/DDL函数调用，创建唯一 worker。work 必须在返回前释放自身 Rows/Tx/Conn。
// after 是 wrapper 的最后收尾（本课生产路径 nil，测试用 gate）；owner 必须 join，不能只读 result。
func startTask(parent context.Context, work func(context.Context) error, after func() error) *task {
	ctx, cancel := context.WithCancel(parent)
	t := &task{cancel: cancel, result: make(chan error, 1), finished: make(chan struct{})}
	go func() {
		// 最先注册的 defer 最后执行；finished 广播在 after/cancel 都执行完之后发生。
		defer close(t.finished)
		defer cancel()
		defer func() {
			if after != nil {
				t.wrapperErr = after()
			}
		}()
		t.result <- work(ctx) // 缓冲只让结果交付不阻塞，不等于 goroutine 已 Finished。
	}()
	return t
}

// join 由创建者调用；实际等待 finished，然后才消费结果并合并 wrapper 清理错误。
// select 同时就绪可选任一分支；超时分支只返回，owner 的 defer 仍须新预算 stopAndJoin。
func (t *task) join(ctx context.Context) error {
	select {
	case <-t.finished:
		if !t.joined {
			t.err = errors.Join(<-t.result, t.wrapperErr)
			t.joined = true
		}
		return t.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stopAndJoin 在 owner 提前失败时取消工作，再用与主 ctx 无关的预算等待真实退出。
// 已 join 的任务没有额外清理错误；否则将工作错误/清理错误保留给 owner 的 errors.Join。
func (t *task) stopAndJoin() error {
	if t.joined {
		return nil
	}
	t.cancel()
	cleanup, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	return t.join(cleanup)
}
