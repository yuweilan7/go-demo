package lab

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// driver 把“业务结果已可读”与“goroutine 的 defer 已完成”分开。
// 两个 channel 都只关闭、从不发送值，所以任意观察者可重复等待，不能消费掉证明。
// result 只由 worker 写一次；读者先观察 resultReady 的关闭，建立同步后才读取。
type driver struct {
	name        string
	cancel      context.CancelFunc
	resultReady chan struct{}
	done        chan struct{}
	result      Result
}

func (d *driver) ResultReady() <-chan struct{} { return d.resultReady }
func (d *driver) Done() <-chan struct{}        { return d.done }
func (d *driver) Cancel()                      { d.cancel() }

// take 只等业务结果。工作预算到期时它可以返回错误，但没有 join goroutine。
// owner 随后的 Close 必须用新的清理预算等 Done，即使结果已经读取过也如此。
func take(ctx context.Context, d *driver) (Result, error) {
	select {
	case <-d.ResultReady():
		return d.result, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// driverOwner 由一个编排 goroutine 使用，不支持边 launch 边 Close。
// 每次启动立刻登记自己的 child cancel、Done 与 gate release，任一提前 return
// 都可由同一个 defer/Cleanup 收尾；Service.Close 另管 HTTP/audit，不能替代这里。
type driverOwner struct {
	drivers []*driver
	release []func()
}

func (o *driverOwner) start(ctx context.Context, name string, work func(context.Context) Result, cleanup, release func()) *driver {
	childCtx, cancel := context.WithCancel(ctx)
	d := &driver{name: name, cancel: cancel, resultReady: make(chan struct{}), done: make(chan struct{})}
	o.drivers = append(o.drivers, d)
	if release != nil {
		o.release = append(o.release, release)
	}
	go func() {
		// 反向执行 defer：worker cleanup → child cancel → close(Done)。
		// Done 只能在全部 worker 收尾完成后关闭，不能在刚拿到 HTTP 结果时关闭。
		defer close(d.done)
		defer cancel()
		if cleanup != nil {
			defer cleanup()
		}
		d.result = work(childCtx)
		close(d.resultReady)
	}()
	return d
}

// launch 的 release 与 child cancel 归本 owner，调用者还可 Cancel 触发特定实验。
// request 自身的 Body.Close/span.End 在 request 返回前已执行；外层 Done 继续证明
// 包装 goroutine 的收尾也完成。release gate 与 cancel 都是幂等通知，不是 join。
func (o *driverOwner) launch(ctx context.Context, s *Service, id, mode string) *driver {
	return o.start(ctx, id, func(childCtx context.Context) Result {
		return request(childCtx, s, id, mode)
	}, nil, func() { s.Release(id) })
}

// Close 的新预算来自 Background，不能继承已取消的工作 ctx。重复 Close 会重新
// 检查不可消费的 Done；首次超时不缓存为“完成”，测试放行后仍可真实加入。
func (o *driverOwner) Close() error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return o.closeWithContext(cleanupCtx)
}

func (o *driverOwner) closeWithContext(cleanupCtx context.Context) error {
	// 先通知全部任务，再逐个加入；否则第一个阻塞任务可能耽误对后续任务的停止。
	for _, d := range o.drivers {
		d.Cancel()
	}
	for _, release := range o.release {
		release()
	}
	var err error
	for _, d := range o.drivers {
		// 已完成时优先承认实际 Done，避免与已经到期的预算随机 select。
		select {
		case <-d.Done():
			continue
		default:
		}
		select {
		case <-d.Done():
		case <-cleanupCtx.Done():
			err = errors.Join(err, fmt.Errorf("driver %s cleanup incomplete: %w", d.name, cleanupCtx.Err()))
		}
	}
	return err
}
