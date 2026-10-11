// Package lesson 给订单后台任务补上“停止接纳、结果交付、资源退出”三个不同的边界。
// M15 的 Pool/Ticket 是实际依赖；本课只增加服务 owner，不复制前课的 worker 实现。
package lesson

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	m15 "go-demo/lessons/m15_structured_concurrency"
)

const CleanupBudget = 2 * time.Second

// await 是实验/停机 owner 的有界观察：超时只结束这次等待，不会强杀被观察的 goroutine。
// 先检查 ctx.Err，是为了把已经用完的预算明确报告为失败；通知和取消同时到来仍没有全局优先级。
func await(ctx context.Context, done <-chan struct{}, label string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s: %w", label, ctx.Err())
	}
}

// gate 由实验 owner 创建并关闭；其他参与者仅接收 C。Once 让正常与提前失败的释放可以重复调用。
// close 是广播“可以继续”，不是 join。拿到 gate 指针的人共享同一个状态，不能复制 Once。
type gate struct {
	once sync.Once
	C    chan struct{}
}

func newGate() *gate     { return &gate{C: make(chan struct{})} }
func (g *gate) release() { g.once.Do(func() { close(g.C) }) }

// joinCleanup 由实验的 defer 调用。先取消/解除所有门，再用全新的预算等待真正的 owner.Wait。
// resultErr 仅由当前实验 goroutine 写；发布者写结果后 close(done)，两者不同时修改同一个 error。
func joinCleanup(resultErr *error, stop func(), done <-chan struct{}, wait func()) bool {
	stop()
	ctx, cancel := context.WithTimeout(context.Background(), CleanupBudget)
	defer cancel()
	if err := await(ctx, done, "实验独立清理 join"); err != nil {
		*resultErr = errors.Join(*resultErr, err)
		return false
	}
	wait()
	return true
}

// ShutdownReport 是停机 coordinator 已完成后的值快照。UnknownAtDeadline 只描述预算到期那一刻，
// FinalRows 描述之后成功 join 得到的终态；不能用最终成功覆盖当时“还不知道”的事实。
type ShutdownReport struct {
	Events            []string
	UnknownAtDeadline []string
	FinalRows         []m15.Row
	ActiveAtDeadline  int
	QueueAtDeadline   int
	ProducerJoined    bool
	WorkersJoined     bool
	ClientClosed      bool
	Err               error
}

// Service 拥有一个 M15 Pool、一个周期生产者和共享客户端；外部请求只借用 Submit。
// mu 把“入口状态检查、TrySubmit、记录 Ticket”变成短小的接纳边界，锁内不会等待队列位置。
// producerDone/producerWG 管生产者；Pool.Wait 管 worker；done 仅在全部依赖释放后关闭。
// finished 则只表示停机 coordinator 返回，即使清理失败也会关闭，不能拿它冒充 done。
type Service struct {
	mu            sync.Mutex
	accepting     bool
	tickets       []*m15.Ticket
	ids           []string
	pool          *m15.Pool
	fetch         m15.Fetch
	workCancel    context.CancelCauseFunc
	producerStop  context.CancelFunc
	producerReady chan struct{}
	producerDone  chan struct{}
	producerWG    sync.WaitGroup
	shutdownWG    sync.WaitGroup
	shutdownOnce  sync.Once
	finished      chan struct{}
	entryStopped  chan struct{}
	done          chan struct{}
	report        ShutdownReport
	closeClient   func() error
	releaseWaits  func()
}

// NewService 由服务 owner 调用。workParent 是任务的归属范围，不能用已经取消的 request ctx。
// 生命周期 stop 通知单独交给 Shutdown；正常 drain 不会取消已接纳订单。周期任务每轮只产生触发，
// 队列满就跳过本轮；需要可靠交付的订单应来自持久业务队列，不能指望 ticker 保存每一次 tick。
// releaseWaits 是实验资源的救援函数，生产系统应换成关闭可解除的 I/O/取消依赖，而不是强杀线程。
func NewService(workParent context.Context, workers, queue int, period time.Duration, fetch m15.Fetch, closeClient func() error, releaseWaits func()) (*Service, error) {
	if period <= 0 || fetch == nil || closeClient == nil || releaseWaits == nil {
		return nil, errors.New("period 必须为正，fetch/closeClient/releaseWaits 必须提供")
	}
	workCtx, workCancel := context.WithCancelCause(workParent)
	pool, err := m15.NewPool(workCtx, workers, queue, false)
	if err != nil {
		workCancel(err)
		return nil, err
	}
	producerCtx, producerStop := context.WithCancel(context.Background())
	s := &Service{accepting: true, pool: pool, fetch: fetch, workCancel: workCancel,
		producerStop: producerStop, producerReady: make(chan struct{}), producerDone: make(chan struct{}),
		finished: make(chan struct{}), entryStopped: make(chan struct{}), done: make(chan struct{}), closeClient: closeClient, releaseWaits: releaseWaits}
	s.producerWG.Go(func() {
		// ticker 的创建、Stop 和 goroutine 的退出都属于生产者。defer 后注册先执行：
		// 这里先 Stop，再 close(producerDone)，最后 WaitGroup 的内部 Done 才执行。
		defer close(s.producerDone)
		ticker := time.NewTicker(period)
		defer ticker.Stop()
		close(s.producerReady)
		for {
			select {
			case <-producerCtx.Done():
				return
			case <-ticker.C:
				// 固定 ID 表示一次价格/订单扫描触发；本课核心实验设一小时周期防止背景触发干扰。
				// Submit 的返回值决定实际接纳；取消与 tick 同时就绪可能仍进这个分支，
				// 但入口 mu 已设 accepting=false 后就不会再转移任务所有权。
				// 这里不阻塞等待单项Result；Submit成功的Ticket已登记在s.tickets，
				// owner最终Shutdown收集每项错误。生产服务还应按业务需要及时上报失败/重试，
				// 不能把忽略本次接纳返回值解释成“任务成功”。满队列/Closed表示未接纳。
				_, _ = s.Submit("periodic")
			}
		}
	})
	return s, nil
}

// Submit 由请求/生产者调用。返回 Ticket 才说明服务欠这项订单一个终态；QueueFull/Closed
// 则没有转移所有权，调用方决定拒绝或稍后重试。外部请求超时不能撤回已经接纳的后台订单。
func (s *Service) Submit(id string) (*m15.Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accepting {
		return nil, m15.ErrClosed
	}
	ticket, err := s.pool.TrySubmit(id, s.fetch)
	if err != nil {
		return nil, err
	}
	s.ids = append(s.ids, id)
	s.tickets = append(s.tickets, ticket)
	return ticket, nil
}

// Shutdown 由外部服务 owner 调用。第一次调用在当前 goroutine 同步关闭入口并通知生产者，
// 然后唯一 coordinator 执行排空；后续调用只等待同一次结果，不会重复 close channel/客户端。
// drainCtx 限制正常 drain；caller 等待用完预算后，可用新 ctx 再次调用读取最终清理结果。
func (s *Service) Shutdown(drainCtx context.Context) (ShutdownReport, error) {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.accepting = false
		s.mu.Unlock()
		close(s.entryStopped)
		s.producerStop()
		s.shutdownWG.Go(func() { s.shutdown(drainCtx) })
	})
	if err := await(drainCtx, s.finished, "等待停机 coordinator"); err != nil {
		// coordinator 尚可能在独立清理；不读取它仍在写的 s.report，也不伪称 Done。
		return ShutdownReport{}, err
	}
	s.shutdownWG.Wait() // close(finished) 在函数末尾 defer；再等 coordinator 自身真的返回。
	return s.report, s.report.Err
}

// shutdown 只由第一次 Shutdown 创建的 coordinator 调用，它是 report/done 的唯一写入者。
// 顺序由依赖决定：停止入口 → 通知/加入生产者 → 关闭队列/drain → 加入 worker → 关闭客户端。
// 即使 drainCtx 在第一步已失效，也必须先解除等待再建立独立清理预算，主错和清理错都保留。
func (s *Service) shutdown(drainCtx context.Context) {
	defer close(s.finished)
	r := ShutdownReport{Events: []string{"入口 stopped", "生产者 cancel 通知"}}
	if err := await(drainCtx, s.producerDone, "正常预算等待生产者"); err != nil {
		r.Err = errors.Join(r.Err, err)
	} else {
		s.producerWG.Wait()
		r.ProducerJoined = true
		r.Events = append(r.Events, "生产者 joined")
	}
	s.pool.Close() // Close 不等 worker，也不取消已接纳项；M15 worker range 排空至 channel 结束。
	r.Events = append(r.Events, "队列 closed，选择 drain")
	if err := s.pool.Wait(drainCtx); err != nil {
		r.Err = errors.Join(r.Err, fmt.Errorf("正常 drain: %w", err))
		counts := s.pool.Counts()
		r.ActiveAtDeadline, r.QueueAtDeadline = counts.Active, s.pool.QueueLen()
		s.mu.Lock()
		for i, ticket := range s.tickets {
			select {
			case <-ticket.Done():
			default:
				r.UnknownAtDeadline = append(r.UnknownAtDeadline, s.ids[i])
			}
		}
		s.mu.Unlock()
		r.Events = append(r.Events, "drain 失败，未完成任务当时 unknown")
	} else {
		r.WorkersJoined = true
		r.Events = append(r.Events, "worker joined")
	}
	if r.Err != nil {
		// 先发协作取消并解除实验门。若先 Wait 后解除门，新的清理预算也会耗尽。
		s.pool.Abort(r.Err)
		s.workCancel(r.Err)
		s.releaseWaits()
		r.Events = append(r.Events, "Abort + 解除等待，再创建独立清理预算")
	}
	cleanCtx, cancel := context.WithTimeout(context.Background(), CleanupBudget)
	defer cancel() // timer 归 coordinator；业务 ctx 被取消不会让这个新预算立即失败。
	if !r.ProducerJoined {
		if err := await(cleanCtx, s.producerDone, "清理 join 生产者"); err != nil {
			r.Err = errors.Join(r.Err, err)
		} else {
			s.producerWG.Wait()
			r.ProducerJoined = true
		}
	}
	if !r.WorkersJoined {
		if err := s.pool.Wait(cleanCtx); err != nil {
			r.Err = errors.Join(r.Err, fmt.Errorf("清理 join worker: %w", err))
		} else {
			r.WorkersJoined = true
		}
	}
	if r.WorkersJoined {
		// 此时不会再有 worker 调用共享客户端；Ticket.Result 读取单项终态，不能代替这一步 Wait。
		s.mu.Lock()
		for _, ticket := range s.tickets {
			row, err := ticket.Result(cleanCtx)
			if err != nil {
				r.Err = errors.Join(r.Err, err)
			} else {
				r.FinalRows = append(r.FinalRows, row)
			}
		}
		s.mu.Unlock()
		if err := s.closeClient(); err != nil {
			r.Err = errors.Join(r.Err, fmt.Errorf("关闭客户端: %w", err))
		} else {
			r.ClientClosed = true
			r.Events = append(r.Events, "worker 之后 client closed")
		}
	}
	s.workCancel(context.Canceled)
	if r.ProducerJoined && r.WorkersJoined && r.ClientClosed {
		close(s.done) // 真正资源收尾完成，可能同时仍有“正常 drain 曾超时”的错误。
	}
	s.report = r // 写入先于 close(finished)，caller 收通知后才可读。
}

func (s *Service) Ready() <-chan struct{}           { return s.producerReady }
func (s *Service) Done() <-chan struct{}            { return s.done }
func (s *Service) EntranceStopped() <-chan struct{} { return s.entryStopped }
