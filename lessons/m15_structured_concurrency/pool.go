package lesson

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Ticket 只在Submit成功后交给caller：从该线性化点起，pool必须发布且仅发布一次终态。
// done发布这项结果；它不代表整个worker退出。想释放pool资源必须再Pool.Wait。
type Ticket struct {
	done chan struct{}
	row  Row
}

// Result由接纳成功的caller调用，只等单项发布；ctx超时不会撤销已交给pool的任务。
// 接收done与worker关闭done建立同步关系，之后读row才不会与worker写入竞争。
func (t *Ticket) Result(ctx context.Context) (Row, error) {
	if err := Await(ctx, t.done, "任务结果"); err != nil {
		return Row{}, err
	}
	return t.row, nil
}
func (t *Ticket) Done() <-chan struct{} { return t.done }

type job struct {
	id     string
	fetch  Fetch
	ticket *Ticket
}

// Pool 修复已有示例的“持RLock阻塞发送”：锁内只尝试立即可完成的发送，满了就解锁等待。
// jobs、closing、changed全部由p拥有。只有Close关闭jobs，worker不关闭队列或别人的Ticket。
// 对象创建后必须传*Pool：复制Mutex/WaitGroup会破坏同一份生命周期状态。
type Pool struct {
	mu        sync.Mutex
	jobs      chan job
	closing   chan struct{}
	changed   chan struct{}
	closed    bool
	ctx       context.Context
	cancel    context.CancelCauseFunc
	failFast  bool
	firstOnce sync.Once
	first     error
	workers   sync.WaitGroup
	joiner    sync.WaitGroup
	joined    chan struct{}
	tracker   Tracker
}

// NewPool 由批次/服务owner创建，最多8个长期worker、队列为1..16项。
// parent应是该owner的预算；提交者ctx只管理排队接纳，成功之后任务使用pool的ctx。
// failFast=false汇总每个Ticket失败；true让首个任务错误取消兄弟，但仍排空已接纳项发布终态。
func NewPool(parent context.Context, workers, queue int, failFast bool) (*Pool, error) {
	if workers < 1 || workers > 8 || queue < 1 || queue > 16 {
		return nil, errors.New("pool限额：workers=1..8 queue=1..16")
	}
	ctx, cancel := context.WithCancelCause(parent)
	p := &Pool{jobs: make(chan job, queue), closing: make(chan struct{}), changed: make(chan struct{}), ctx: ctx, cancel: cancel, failFast: failFast, joined: make(chan struct{})}
	// 所有计数先Add，再启动任何worker和joiner；避免Wait先看到零而提前返回。
	p.workers.Add(workers)
	for range workers {
		go p.worker()
	}
	p.joiner.Go(func() { p.workers.Wait(); close(p.joined) })
	return p, nil
}

// Submit 由外部producer同步调用；waiting是可选实验通知，只报告“满队列且已取得等待版本”。
// 它不是runtime阻塞栈证明。成功返回的Ticket才代表所有权转移；失败返回nil，pool不欠结果。
// ctx取消与接纳同时发生可能成功，caller必须按实际返回值判断，而不能仅查ctx.Err猜归属。
func (p *Pool) Submit(ctx context.Context, id string, fetch Fetch, waiting chan<- struct{}) (*Ticket, error) {
	if id == "" || fetch == nil {
		return nil, errors.New("id/fetch不能为空")
	}
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrClosed
		}
		if err := ctx.Err(); err != nil {
			p.mu.Unlock()
			return nil, err
		}
		if err := p.ctx.Err(); err != nil {
			p.mu.Unlock()
			return nil, context.Cause(p.ctx)
		}
		ticket := &Ticket{done: make(chan struct{})}
		select {
		case p.jobs <- job{id, fetch, ticket}:
			// send与Close受同一把短锁协调；Close不可能同时close这个仍在发送的channel。
			p.mu.Unlock()
			return ticket, nil
		default:
			version := p.changed // channel句柄副本仍指向当前共享广播；锁保证不会漏掉下一次变化。
			p.mu.Unlock()
			if waiting != nil {
				select {
				case waiting <- struct{}{}:
				default:
				}
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-p.ctx.Done():
				return nil, context.Cause(p.ctx)
			case <-p.closing:
				return nil, ErrClosed
			case <-version: // worker取走任务后广播空间变化；醒来必须回锁内重新检查。
			}
		}
	}
}

// TrySubmit 给不能等待本池的worker使用：满了立即拒绝，由外部owner稍后安排子任务。
// 本修复采用正容量队列：空队列就有接纳位置，不依赖worker恰好已进入receive。
func (p *Pool) TrySubmit(id string, fetch Fetch) (*Ticket, error) {
	if id == "" || fetch == nil {
		return nil, errors.New("id/fetch不能为空")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrClosed
	}
	if p.ctx.Err() != nil {
		return nil, context.Cause(p.ctx)
	}
	ticket := &Ticket{done: make(chan struct{})}
	select {
	case p.jobs <- job{id, fetch, ticket}:
		return ticket, nil
	default:
		return nil, ErrQueueFull
	}
}

// changedLocked 只在mu内由worker调用。关闭旧广播唤醒所有waiter，下一轮使用新版本。
// caller若先取到旧版本随后才开始select，旧channel已关闭仍会立即就绪，不会丢通知。
func (p *Pool) changedLocked() { close(p.changed); p.changed = make(chan struct{}) }

// worker 只拥有一个固定工作位置。即使ctx取消也继续range排空队列，
// 对未开始的任务发布Canceled，而不是直接退出后遗失已经转移所有权的任务。
func (p *Pool) worker() {
	defer p.workers.Done()
	for item := range p.jobs { // 空且未关闭时具体阻塞在接收；因此ctx取消后owner仍必须Close/Abort。
		p.mu.Lock()
		p.changedLocked()
		p.mu.Unlock()
		row := Row{ID: item.id}
		if p.ctx.Err() != nil {
			row.Err = context.Cause(p.ctx)
		} else {
			// 只统计真正调用业务的工作；panic恢复后返回invoke，离开函数前归还统计令牌。
			leave := p.tracker.Enter(1)
			row.Price, row.Err = invoke(p.ctx, item.id, item.fetch)
			leave()
		}
		if row.Err != nil && p.failFast {
			p.firstOnce.Do(func() { p.first = fmt.Errorf("SKU %s 首错: %w", item.id, row.Err); p.cancel(p.first) })
		}
		item.ticket.row = row
		close(item.ticket.done) // 每个已接纳job仅被一个worker领取，因此仅此处关闭一次。
	}
}

// Close 是graceful drain：先禁止新提交再关闭队列，已接纳项仍执行。
// 它不取消任务也不等待worker；外部owner要随后Wait。不要从本池任务里Wait自己。
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.closing)
		close(p.jobs)
	}
}

// Abort 是消费者退出/owner提前失败时的停止协议：通知工作取消并关闭接纳。
// 已经成功的读取结果仍保留，已经发生的外部副作用不能靠取消撤回。
func (p *Pool) Abort(cause error) {
	if cause == nil {
		cause = context.Canceled
	}
	p.cancel(cause)
	p.Close()
}

// Wait 由外部owner调用，先等真正workers.Wait的完成通知，再加入通知发布者。
// waitCtx超时只是owner的等待失败，不自动Abort；caller必须用独立清理预算收尾。
func (p *Pool) Wait(waitCtx context.Context) error {
	if err := p.waitJoined(waitCtx); err != nil {
		return err
	}
	return p.first
}

// waitJoined只返回收尾等待错误，使清理路径不会把预期业务首错误当清理失败或反过来吞掉。
func (p *Pool) waitJoined(ctx context.Context) error {
	if err := Await(ctx, p.joined, "pool worker join"); err != nil {
		return err
	}
	p.joiner.Wait()
	p.cancel(context.Canceled)
	return nil
}
func (p *Pool) Joined() <-chan struct{} { return p.joined }
func (p *Pool) Counts() Counts          { return p.tracker.Counts() }
func (p *Pool) QueueLen() int           { return len(p.jobs) }
