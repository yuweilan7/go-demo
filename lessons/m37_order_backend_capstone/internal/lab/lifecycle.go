package lab

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// Gate由实验创建和释放，业务hook只借用Wait。Entered表示真实进入该位置，
// Released只是放行通知。Canceled仅在Wait实际读到ctx取消时关闭，是观察提交路径
// 的屏障；它仍不是事务/handler最终Done，owner还须独立join caller/App。
type Gate struct {
	Entered                                chan struct{}
	Canceled                               chan struct{}
	released                               chan struct{}
	enteredOnce, releaseOnce, canceledOnce sync.Once
}

func NewGate() *Gate {
	return &Gate{Entered: make(chan struct{}), Canceled: make(chan struct{}), released: make(chan struct{})}
}
func (g *Gate) Release() { g.releaseOnce.Do(func() { close(g.released) }) }

// Wait由真实业务hook同步借用；select在这一行等待，不另开goroutine。两分支可能
// 同时就绪而select不保证优先级，所以醒来统一读ctx.Err，再报告实际取消观察。
func (g *Gate) Wait(ctx context.Context) error {
	g.enteredOnce.Do(func() { close(g.Entered) })
	select {
	case <-ctx.Done():
	case <-g.released:
	}
	err := ctx.Err()
	if err != nil {
		g.canceledOnce.Do(func() { close(g.Canceled) })
	}
	return err
}

// wait由观察者调用，使用整个实验预算。观察失败不代表被观察任务结束。
func wait(ctx context.Context, d <-chan struct{}, name string) error {
	select {
	case <-d:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待%s: %w", name, ctx.Err())
	}
}

type result struct {
	Status int
	Body   b.Envelope
	Err    error
}

// Caller从创建起就有取消函数和最终Done，便于owner提前失败也收尾。Preview是
// 缓冲结果通知，worker发送后还要执行cancel/tail清理，不能把它当最终join。
type Caller struct {
	Preview <-chan result
	Done    <-chan struct{}
	cancel  context.CancelFunc
	final   result
}

// StartCaller由Run/Repeats/故障观察者调用，返回的是同一Caller指针。work在新goroutine
// 收到自己的子ctx，Preview容量1避免owner提前失败时卡在发结果；final只由worker写。
// defer按后进先出执行：recover/cancel先完成，close(done)最后执行，owner再读final。
func StartCaller(ctx context.Context, work func(context.Context) result, tail *Gate) *Caller {
	life, cancel := context.WithCancel(ctx)
	preview := make(chan result, 1)
	done := make(chan struct{})
	c := &Caller{Preview: preview, Done: done, cancel: cancel}
	go func() {
		defer close(done)
		defer func() {
			cancel()
			if p := recover(); p != nil {
				cause, ok := p.(error)
				if !ok {
					cause = fmt.Errorf("caller panic: %v", p)
				}
				c.final.Err = errors.Join(c.final.Err, cause)
			}
		}()
		c.final = work(life)
		preview <- c.final
		if tail != nil {
			c.final.Err = errors.Join(c.final.Err, tail.Wait(life))
		}
	}()
	return c
}

// Finish是caller唯一owner的退出路径。先通知取消/放自己的门，再以新预算实际join，
// Done以后读final。等待预览结果失败也会走这同一路径，不遗留Go工作goroutine。
func (c *Caller) Finish(tail *Gate) (result, error) {
	c.cancel()
	if tail != nil {
		tail.Release()
	}
	if e := b.JoinDone(c.Done, "HTTP caller", 10*time.Second); e != nil {
		return result{}, e
	}
	return c.final, nil
}

// Completed用于正常路径，不先cancel正在执行的请求；等真实Done后拿最终结果。
func (c *Caller) Completed(ctx context.Context) (result, error) {
	if e := wait(ctx, c.Done, "caller.Done"); e != nil {
		return result{}, e
	}
	return c.final, nil
}
