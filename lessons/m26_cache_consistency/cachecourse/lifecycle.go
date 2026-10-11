package cachecourse

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// gate 是一次广播门，创建者拥有 close 权限。关闭只通知 waiter，绝不等于任务退出。
// 指针接收者让所有 caller 使用同一个 Once/channel，不能复制有状态的 gate。
type gate struct {
	once sync.Once
	c    chan struct{}
}

func newGate() *gate  { return &gate{c: make(chan struct{})} }
func (g *gate) open() { g.once.Do(func() { close(g.c) }) }

// await 在当前 goroutine 等指定事件，错误带等待对象名，方便判断是哪个屏障未到。
// <-chan 只授予接收权；ctx 取消是另一条可就绪分支，不会撤销已经发生的 SQL/SET。
func await(ctx context.Context, c <-chan struct{}, label string) error {
	select {
	case <-c:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待 %s：%w", label, ctx.Err())
	}
}

// task 保存一项已登记工作。err 只由 worker 写，owner 收到 done 后才读；close/receive
// 给出同步关系。result 另设缓冲槽，业务结果被发布后 wrapper 仍可能在 cleanup 门等待。
type task struct {
	done chan struct{}
	err  error
}
type owner struct {
	ctx    context.Context
	cancel context.CancelFunc
	tasks  []*task
	gates  []*gate
}

// newOwner 由实验创建一个有界生命周期范围；返回的 owner 不并发登记任务，只有实验
// 主 goroutine 调 start/registerGate，worker 仅操作自己的槽。finish 负责取消、放门、join。
func newOwner(ctx context.Context) *owner {
	c, cancel := context.WithCancel(ctx)
	return &owner{ctx: c, cancel: cancel}
}
func (o *owner) registerGate(g *gate) { o.gates = append(o.gates, g) }

// start 在 go 之前把 done/结果槽写入 owner 账本，避免提前 return 忘掉刚启动的 goroutine。
// fn 借用 owner ctx；本入口在同一 goroutine recover，防止辅助实验 panic 丢失终态。
func (o *owner) start(fn func(context.Context) error) *task {
	t := &task{done: make(chan struct{})}
	o.tasks = append(o.tasks, t)
	go func() {
		defer close(t.done) // 最后发布真实终态；所有更内层 defer 先执行。
		defer func() {
			if v := recover(); v != nil {
				t.err = fmt.Errorf("工作 panic：%v", v)
			}
		}()
		t.err = fn(o.ctx)
	}()
	return t
}

// join 不以 result 就绪代替 done。ctx 是 owner 提供的新预算；返回同时保留所有工作错误。
func (o *owner) join(ctx context.Context) error {
	var err error
	for _, t := range o.tasks {
		if e := await(ctx, t.done, "worker Done"); e != nil {
			return errors.Join(err, e)
		}
		err = errors.Join(err, t.err)
	}
	return err
}

// finish 注册在每个实验入口，正常/提前失败都执行。先通知停止并释放所有自己拥有的门，
// 再用 Background 的6秒预算真 join；清理失败加入原 error，不能被预期 Canceled 吞掉。
func (o *owner) finish(err *error) {
	o.cancel()
	for _, g := range o.gates {
		g.open()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	*err = errors.Join(*err, o.join(ctx))
}

// flightResult 是调用者的一项业务结果；Shared 描述本次库结果是否分给多 caller。
// DTO 中 map/slice 默认仍别名共享，是否 clone 由 startFlight 的边界参数决定。
type flightResult struct {
	Row    DTO
	Err    error
	Shared bool
}
type ticket struct {
	result chan flightResult
	done   <-chan struct{}
}

// wait 由请求 goroutine 调用：请求 ctx 可以先结束，owner 的 drainer 却仍会消费库结果。
// 两分支同时就绪时 Go select 可任选；不能承诺取消通知后一律不返回成功。
func (t ticket) wait(ctx context.Context) (flightResult, error) {
	select {
	case r := <-t.result:
		return r, r.Err
	case <-ctx.Done():
		return flightResult{}, ctx.Err()
	}
}

// startFlight 是 singleflight.DoChan 的生命周期适配层，实验主 goroutine 同步调用它。
// DoChan 返回前已在库互斥锁下登记同 key；因此 owner 逐个拿到 ticket 后才开源库门，
// 可以证明所有 follower 已加入，而无需 sleep 猜调度。库自己创建的 doCall goroutine
// 由库拥有；本课在调用前登记候选 body 的 Done，并拥有消费结果的应用 drainer。
// fn 只借用显式 workCtx（可选 leader 请求 ctx 或服务 ctx），不会隐式采用 follower ctx。
func startFlight(o *owner, g *singleflight.Group, key string, workCtx context.Context, clone bool, cleanup *gate, fn func(context.Context) (DTO, error)) ticket {
	body := &task{done: make(chan struct{})}
	o.tasks = append(o.tasks, body) // 在 DoChan 可能创建内部 goroutine 之前登记候选函数。
	started := make(chan struct{})
	ch := g.DoChan(key, func() (value any, err error) {
		close(started)
		defer close(body.done) // fn 内的资源 defer 完成后才到这里；不把请求取消当成 body完成。
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("singleflight body panic：%v", v)
			}
		}()
		row, e := fn(workCtx)
		// 本 body 的错误通过 Result 交业务层；body.err 专门表示生命周期清理错误，此处为nil。
		return row, e
	})
	result := make(chan flightResult, 1)
	t := o.start(func(context.Context) error {
		r := <-ch // 库通道容量1且不关闭，必须接一次；不能 range 等一个永远不会来的 close。
		select {
		case <-started: // 当选 leader，body.done 已由真实函数退出关闭。
		default:
			close(body.done)
		} // callback 未运行，候选没有实际资源，由 drainer 结束登记。
		row, ok := r.Val.(DTO) // any 动态值是 DTO；断言失败是程序契约错误，不冒充缓存 miss。
		if !ok && r.Err == nil {
			r.Err = errors.New("singleflight 结果不是 DTO")
		}
		if clone {
			row = row.Clone()
		} // 深复制发生在交给各caller之前，不在caller并发修改之后。
		result <- flightResult{Row: row, Err: r.Err, Shared: r.Shared}
		if cleanup != nil {
			return await(o.ctx, cleanup.c, "drainer 收尾门")
		}
		return nil
	})
	return ticket{result: result, done: t.done}
}
