package lab

import (
	"context"
	"errors"
	"sync"
	"time"
)

// CloseCaller 只拥有E06的一次Close调用，不是任务池或通用并行框架。
// result 是Close的预览结果；done 是caller自己的cancel和尾部清理全部结束后的广播。
// finalErr由caller写、Join在done关闭后读，channel关闭的同步保证读到最终原因。
type CloseCaller struct {
	result   chan error
	done     chan struct{}
	joining  chan struct{}
	joinOnce sync.Once
	finalErr error
}

// StartCloseCaller 由ResultBeforeDone或确定性所有权测试调用，创建一条由caller对象拥有的goroutine。
// call是实际a.Close，接收独立3秒关闭预算；afterCancel只用于演示/验证caller尾部清理。
// caller创建后，owner必须立即登记“解除自有屏障→新预算Join”的退出路径，包括观察失败。
func StartCloseCaller(call func(context.Context) error, afterCancel func() error) *CloseCaller {
	c := &CloseCaller{result: make(chan error, 1), done: make(chan struct{}), joining: make(chan struct{})}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var primary error
		// 这是caller唯一的最终defer：先cancel释放自己的timer，再做尾部清理、合并原因，
		// 最后关闭done。done之后不再有另一个用户defer执行cancel，owner才可认定最终完成。
		defer func() {
			cancel()
			var cleanupErr error
			if afterCancel != nil {
				cleanupErr = afterCancel()
			}
			c.finalErr = errors.Join(primary, cleanupErr)
			close(c.done)
		}()
		primary = call(ctx)
		c.result <- primary // 缓冲1只是交付a.Close结果；下面的defer尚未完成，不能把接收当join。
		close(c.result)
	}()
	return c
}

// Join 由owner在放开自己控制的门之后调用，ctx是全新、独立的等待预算。
// joining只广播“owner已进入这个等待分支”，供测试核对，无需猜goroutine是否被调度。
// 只有done关闭后才读finalErr；观察超时保留cause+ctx原因，不伪造caller已结束。
func (c *CloseCaller) Join(ctx context.Context, cause error) error {
	c.joinOnce.Do(func() { close(c.joining) })
	select {
	case <-c.done:
		return errors.Join(cause, c.finalErr)
	case <-ctx.Done():
		return errors.Join(cause, ctx.Err())
	}
}

func (c *CloseCaller) Result() <-chan error     { return c.result }
func (c *CloseCaller) Done() <-chan struct{}    { return c.done }
func (c *CloseCaller) Joining() <-chan struct{} { return c.joining }
