package lesson

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrServiceStopped = errors.New("服务正在停止")
	ErrQueueFull      = errors.New("服务学习队列已满")
)

// BackgroundJob 是一份服务工作，只有服务 worker 写 receipt、关闭 started/done。
// release 是实验驱动器拥有的放行屏障，模拟外部依赖尚未返回，未声称进行实际网络 I/O。
// orderID 为请求中复制出来的不可变 string；不保留请求对象指针或请求 context。
type BackgroundJob struct {
	orderID string
	started chan struct{}
	release <-chan struct{}
	done    chan struct{}
	receipt Receipt
}

// NewBackgroundJob 由 handler/实验创建，返回的指针移交给 Service 后不可再写内部状态。
func NewBackgroundJob(orderID string, release <-chan struct{}) *BackgroundJob {
	return &BackgroundJob{orderID: orderID, started: make(chan struct{}), release: release, done: make(chan struct{})}
}

// HandleRequest 是 E04 真正会返回的请求函数，由 RunServiceScope/测试模拟调用。
// 参数是当前请求 context 与复制出的订单标识；release 仅由实验 driver 提供。
// 它把 job 移交给服务后立即返回句柄，不启动请求私有 goroutine，也不等待业务完成。
// 收集者持有返回句柄观察完成，Service 保有执行/停止责任；两种持有不是两个执行 owner。
func HandleRequest(requestCtx context.Context, service *Service, orderID string, release <-chan struct{}) (*BackgroundJob, error) {
	job := NewBackgroundJob(orderID, release)
	if err := service.Submit(requestCtx, job); err != nil {
		// %w 保留具体的 Canceled/QueueFull/ServiceStopped 原因供上层分类。
		return nil, fmt.Errorf("接纳后台订单 %s: %w", orderID, err)
	}
	return job, nil
}

func (j *BackgroundJob) Started() <-chan struct{} { return j.started }

// Result 由请求之外的收集者调用。done 的关闭发布 receipt，观察超时只结束本次等待。
func (j *BackgroundJob) Result(ctx context.Context) (Receipt, error) {
	if err := await(ctx, j.done, "background job done"); err != nil {
		return Receipt{}, err
	}
	return j.receipt, nil
}

// Service 是唯一后台 worker 的 owner，持有停止方法和 group；绝不从请求 ctx 派生。
// mu 保护 admission 与 ledger。jobs 容量为 1，最多一个运行任务和一个排队任务。
// 背景工作在进程内存中，进程崩溃会丢失；需要跨崩溃可靠性时应使用持久队列/事务方案。
type Service struct {
	ctx     context.Context
	cancel  context.CancelFunc
	trace   *Trace
	tracker *Tracker
	jobs    chan *BackgroundJob
	started chan struct{}
	group   sync.WaitGroup
	mu      sync.Mutex
	stopped bool
	ledger  []string
}

// NewService 由服务启动入口调用，ctx 的 owner 是应用生命周期而非任何一次请求。
// 此教学函数自行创建 service context；真实服务器通常从应用根 context 派生。
func NewService(trace *Trace, tracker *Tracker) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{ctx: ctx, cancel: cancel, trace: trace, tracker: tracker, jobs: make(chan *BackgroundJob, 1), started: make(chan struct{})}
	tracker.acquire("service-worker")
	s.group.Go(s.loop)
	return s
}

func (s *Service) Started() <-chan struct{} { return s.started }

// Submit 由请求 handler 同步调用，只确认接纳，不能确认任务已完成。
// 非阻塞入队避免满队列时拿着 mu 等待，进而妨碍 Shutdown 取得同一个锁。
// 同一 job 只能提交一次；本课的 handler 为每次提交创建独立 job。
func (s *Service) Submit(requestCtx context.Context, job *BackgroundJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := requestCtx.Err(); err != nil {
		return err
	}
	if s.stopped {
		return ErrServiceStopped
	}
	select {
	case s.jobs <- job:
		s.trace.Event("E04 request accepted " + job.orderID)
		return nil
	default:
		return ErrQueueFull
	}
}

// loop 是 Service.group.Go 的函数体，其阻塞点在 jobs 接收和 process 的 release 接收。
// 退出时释放服务令牌；group 在 loop 返回后减计数，Shutdown 是最终 waiter。
func (s *Service) loop() {
	defer func() {
		s.tracker.release("service-worker")
		s.trace.Event("E04 service worker resources-released")
	}()
	close(s.started)
	for {
		select {
		case <-s.ctx.Done():
			// Shutdown 已锁住 admission 并标 stopped；不再有新提交。
			// 排空已接纳的队列并发布取消结果，让每个持有 job 的收集者都有结束信号。
			for {
				select {
				case job := <-s.jobs:
					job.receipt = Receipt{OrderID: job.orderID, Err: s.ctx.Err()}
					close(job.done)
					s.trace.Event("E04 service queued-job canceled " + job.orderID)
				default:
					return
				}
			}
		case job := <-s.jobs:
			s.process(job)
		}
	}
}

// process 由唯一服务 worker 同步调用，没有为每份任务再无限创建 goroutine。
// 先写 receipt/ledger，后 close(done)，以发布业务事实与同步可见性。
func (s *Service) process(job *BackgroundJob) {
	close(job.started)
	s.trace.Event("E04 service worker waiting-release " + job.orderID)
	select {
	case <-job.release:
		job.receipt = Receipt{OrderID: job.orderID, Amount: 200}
		s.mu.Lock()
		s.ledger = append(s.ledger, job.orderID)
		s.mu.Unlock()
		s.trace.Event("E04 service ledger-written " + job.orderID)
	case <-s.ctx.Done():
		job.receipt = Receipt{OrderID: job.orderID, Err: s.ctx.Err()}
		s.trace.Event("E04 service running-job canceled " + job.orderID)
	}
	// release 与取消同时就绪时 select 可能选择任一分支；本课不承诺取消后的零副作用。
	close(job.done)
}

// Shutdown 由应用 owner 调用；先拒绝新请求，再通知停止，最后 join 服务 worker。
// jobs 不关闭，因为接纳/停止协议已经由 mu + stopped + ctx 表达，避免发送到已关闭 channel。
func (s *Service) Shutdown() {
	s.mu.Lock()
	alreadyStopped := s.stopped
	s.stopped = true
	s.cancel()
	s.mu.Unlock()
	s.group.Wait()
	if !alreadyStopped {
		s.trace.Event("E04 service owner Shutdown-joined")
	}
}

// Ledger 复制真实实验业务记录，调用者不取得内部 slice 的写权限。
func (s *Service) Ledger() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ledger...)
}

// RunServiceScope 由入口同步调用：请求结束后，已被服务接纳的 O-101 仍可完成。
// requestCancel 与 service.cancel 是不同作用域；两者各自有明确 owner。
func RunServiceScope(ctx context.Context, trace *Trace) ([]string, error) {
	tracker := NewTracker()
	service := NewService(trace, tracker)
	defer service.Shutdown() // 正常/错误返回均由应用 owner 收尾，worker 不等待自己。
	if err := await(ctx, service.Started(), "service started"); err != nil {
		return nil, err
	}
	requestCtx, requestCancel := context.WithCancel(ctx)
	defer requestCancel()
	release := make(chan struct{})
	job, err := HandleRequest(requestCtx, service, "O-101", release)
	if err != nil {
		return nil, err
	}
	trace.Event("E04 request handler-returned (仅确认接纳，服务尚有自己的责任)")
	if err := await(ctx, job.Started(), "job started"); err != nil {
		return nil, err
	}
	// HandleRequest 已真实返回；此处模拟服务器结束请求 context，不关闭服务 ctx。
	requestCancel()
	active, resources := tracker.Counts()
	trace.Event(fmt.Sprintf("E04 request returned+canceled; service-active=%d resources=%d", active, resources))
	if err := require(active == 1 && resources == 1 && requestCtx.Err() == context.Canceled, "请求结束不取消服务作用域"); err != nil {
		return nil, err
	}
	close(release)
	receipt, err := job.Result(ctx)
	if err != nil {
		return nil, err
	}
	ledger := service.Ledger()
	if err := require(receipt.Err == nil && receipt.Amount == 200 && len(ledger) == 1 && ledger[0] == "O-101", "请求返回后服务真实写入"); err != nil {
		return nil, err
	}
	trace.Event("E04 owner observed-job-done ledger=[O-101]")
	// 主动在返回前关停一次以记录最终计数；defer 中第二次 Shutdown 是安全的幂等调用。
	service.Shutdown()
	active, resources = tracker.Counts()
	trace.Event(fmt.Sprintf("E04 final active=%d resources=%d", active, resources))
	return ledger, require(active == 0 && resources == 0, "服务 owner 已 join 并释放令牌")
}
