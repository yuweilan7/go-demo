package course

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	// ErrSource 表示生产端读取/解析失败；已经交付的订单仍要排空，不能默默丢掉原因。
	ErrSource = errors.New("合成订单来源解析失败")
	// ErrInvalidOrder 属于单个订单的业务拒绝，不等于整个 pipeline 的来源故障。
	ErrInvalidOrder = errors.New("订单金额必须大于 0")
	// ErrEarlyStop 表示消费者按 StopAfter 主动结束，只承诺返回已经消费的子集。
	ErrEarlyStop = errors.New("消费者主动提前停止")
)

// Order 是入口创建的合成订单。channel 发送复制整个 struct；本类型只含 string/int，
// 没有共享可变 slice/map/指针。外部调用者在 Start 复制输入期间也不能并发修改 input。
type Order struct {
	ID          string
	AmountCents int
}

// Result 是一次校验的值交付。Order 仍是值副本，Worker 仅供观察，Err 表示该订单的拒绝原因。
// 校验结果进入 channel 并不等于消费者已把它加入最终报告，更不等于数据库已经提交。
type Result struct {
	Order  Order
	Worker int
	Err    error
}

// Options 由入口指定。容量是有限待处理量，Workers 是并发校验数，FailAt=-1 为无故障。
// Blocked 是可选的观察通道，由观察者创建，不属于业务协议，也不由 pipeline 关闭。
// Produced 是可选来源进度通知；发送成功后报告累计条数，让饱和夹具有真实的交付屏障。
// 两种观察通知都非阻塞；观察者停止读取不能阻挡生产消费，遗漏通知不影响业务正确性。
type Options struct {
	Workers       int
	JobsBuffer    int
	ResultsBuffer int
	FailAt        int
	StopAfter     int
	Blocked       chan<- string
	Produced      chan<- int
}

// Completion 由唯一结果关闭者发送一次；它报告生产端结局和所有 worker 的退出事实。
// Produced 是 jobs 接受的数量，Validated 是校验次数，不是消费者业务完成次数。
type Completion struct {
	Produced      int
	Validated     int
	WorkersExited int
	SourceErr     error
	Canceled      bool
	CancelErr     error
}

// Pipeline 的 owner 是 Start 的调用者：必须持续消费，或 Cancel 后 Wait。
// Results/Done 只暴露接收能力，调用者不能编译出 close(p.Results)；内部 jobs 不向外暴露。
// Cancel 可重复调用；它发通知，不等待退出。只有 Wait 返回才能证明收尾完成。
type Pipeline struct {
	Results <-chan Result
	Done    <-chan Completion
	cancel  context.CancelFunc
	jobs    <-chan Order
}

// Cancel 由消费者提前结束或实验 owner 兜底清理时调用。
// context.CancelFunc 关闭 Done 通知，不能把 goroutine 强行从 CPU/I/O/任意等待中杀掉。
func (p *Pipeline) Cancel() { p.cancel() }

// Wait 由唯一 owner 在消费结束或 Cancel 后调用一次。completion 通道容量为 1，
// 即使消费者正在处理最后一条结果，协调者也能留下终态，不依赖消费者继续接收 Results。
// 如果等待 ctx 超时，返回错误表示尚未证明退出；不能把超时当成清理成功。
func (p *Pipeline) Wait(ctx context.Context) (Completion, error) {
	select {
	case completion, ok := <-p.Done:
		if !ok {
			return Completion{}, errors.New("终态已被读取；每个 Pipeline 只能由 owner Wait 一次")
		}
		// 再等终态通道关闭，证明协调者完成了最后一次发布及 close(done)。
		select {
		case _, open := <-p.Done:
			if open {
				return completion, errors.New("终态通道违反单次发布协议")
			}
			return completion, nil
		case <-ctx.Done():
			return completion, fmt.Errorf("等待终态通道关闭: %w", ctx.Err())
		}
	case <-ctx.Done():
		return Completion{}, fmt.Errorf("等待 pipeline 退出: %w", ctx.Err())
	}
}

// Queues 只供屏障实验观察瞬时长度。len(channel) 是安全快照，却不能用来决定下一条发送
// 是否阻塞：若其他参与者仍可运行，读完 len 后状态就可能改变。
func (p *Pipeline) Queues() (jobs, results int) { return len(p.jobs), len(p.Results) }

// sourceCompletion 是 feeder 专属的终态，不混在 Results 中。恰好一个 feeder 写一次，
// 容量为 1 基于这个已知上限；这和随意扩大订单缓冲来掩盖无限积压完全不同。
type sourceCompletion struct {
	produced int
	err      error
}

// Start 由 demo/Run/测试创建一次批次。它校验有界参数、复制输入、启动 feeder、worker 和协调者。
// 返回后这些 goroutine 继续运行；Go 不会因 Start 返回而自动 join，它们的 owner 必须履行协议。
func Start(parent context.Context, input []Order, opts Options, trace *Trace) (*Pipeline, error) {
	if opts.Workers < 1 || opts.Workers > 8 || opts.JobsBuffer < 0 || opts.JobsBuffer > 16 ||
		opts.ResultsBuffer < 0 || opts.ResultsBuffer > 16 || len(input) > 128 || opts.FailAt < -1 ||
		opts.FailAt > len(input) || opts.StopAfter < 0 {
		return nil, errors.New("实验限额: worker=1..8，buffer=0..16，订单<=128，FailAt=-1..len，StopAfter>=0")
	}
	// := 在 Start 的作用域内新建 ctx/cancel。所有参与者共享取消范围，channel 值也是共享句柄。
	ctx, cancel := context.WithCancel(parent)
	orders := append([]Order(nil), input...) // 复制 slice 元素，避免调用者返回后复用底层数组。
	jobs := make(chan Order, opts.JobsBuffer)
	results := make(chan Result, opts.ResultsBuffer)
	sourceDone := make(chan sourceCompletion, 1)
	done := make(chan Completion, 1)

	var workers sync.WaitGroup
	counts := make([]int, opts.Workers) // 每个 worker 只写自己的元素，协调者 Wait 后统一读取。
	workers.Add(opts.Workers)           // 在启动与 Wait 之前登记完整人数，避免“还未登记已等完”。
	for id := 0; id < opts.Workers; id++ {
		// 实参 id 在 go 调用时求值并复制给 worker；不会让所有 worker 读取一个循环变量。
		go worker(ctx, id, jobs, results, opts.Blocked, &workers, counts, trace)
	}
	go feeder(ctx, orders, opts.FailAt, jobs, sourceDone, opts.Blocked, opts.Produced, trace)

	// 这是 results 的唯一关闭者。worker 没有 close(results) 权限约定：一个 worker 的
	// 退出不能代表其他 worker 已发完。WaitGroup 证明所有发送者已经越过最后一次发送并退出。
	go func() {
		workers.Wait()
		source := <-sourceDone // feeder 在 defer 的最后报告；读取后也证明它已完成 jobs 的关闭。
		validated := 0
		for _, count := range counts {
			validated += count
		}
		close(results)
		trace.Log("closer", "results_closed", "全部 %d 个 worker 已退出，唯一一次 close(results)", opts.Workers)
		contextErr := ctx.Err() // 只取一次取消状态，Canceled 与 CancelErr 必须来自同一事实。
		completion := Completion{Produced: source.produced, Validated: validated, WorkersExited: opts.Workers, SourceErr: source.err, Canceled: contextErr != nil, CancelErr: contextErr}
		// 先记录清理事实，再公布终态。done <- 不是订单发送，不受消费者提前停止 Results 的影响。
		cancel() // 正常结束也释放派生 context；Canceled 已在本次清理前取样，不因此变成 true。
		trace.Log("closer", "all_joined", "produced=%d validated=%d sourceErr=%v canceled=%t", source.produced, validated, source.err, completion.Canceled)
		done <- completion
		close(done)
	}()
	return &Pipeline{Results: results, Done: done, cancel: cancel, jobs: jobs}, nil
}

// feeder 由 Start 启动，唯一拥有 jobs 的发送与关闭。input 是 Start 自有的稳定值快照。
// 来源失败或取消都从当前函数返回；defer 无论走哪个 return，都先 close(jobs)，再留下终态。
func feeder(ctx context.Context, input []Order, failAt int, jobs chan<- Order, done chan<- sourceCompletion, blocked chan<- string, progress chan<- int, trace *Trace) {
	status := sourceCompletion{}
	defer func() {
		close(jobs)
		trace.Log("feeder", "jobs_closed", "唯一发送者结束，produced=%d err=%v", status.produced, status.err)
		done <- status
	}()
	for index, order := range input {
		if index == failAt {
			status.err = fmt.Errorf("source index=%d: %w", index, ErrSource)
			trace.Log("feeder", "source_error", "%v；关闭 jobs，让先前值继续排空", status.err)
			return
		}
		if err := send(ctx, jobs, order, "jobs", blocked, trace, "feeder"); err != nil {
			status.err = err
			return
		}
		status.produced++
		if progress != nil {
			select {
			case progress <- status.produced:
			default: // 观察者停止读取时只丢教学通知，不阻塞可靠业务传输。
			}
		}
		trace.Log("feeder", "job_accepted", "ID=%s 已交给 worker 或进入 jobs 缓冲", order.ID)
	}
	if failAt == len(input) {
		status.err = fmt.Errorf("source index=%d: %w", failAt, ErrSource)
		trace.Log("feeder", "source_error", "%v；订单已交付但批次来源仍报告失败", status.err)
	}
}

// worker 由 Start 启动，在 jobs 上领取订单，把 Result 发送给消费者。
// jobs/results 是同一批次共享 channel 的方向受限视图，并不是复制了一套队列。
// counts 的 id 槽位只由本 worker 写；WaitGroup 完成后的 coordinator 才读取它。
func worker(ctx context.Context, id int, jobs <-chan Order, results chan<- Result, blocked chan<- string, group *sync.WaitGroup, counts []int, trace *Trace) {
	actor := fmt.Sprintf("worker-%d", id)
	processed := 0
	defer func() {
		counts[id] = processed
		trace.Log(actor, "worker_exit", "校验次数=%d；已不再发送 results", processed)
		group.Done() // defer 先记录退出，再减计数；最后一个 Done 使唯一关闭者的 Wait 可以返回。
	}()
	for {
		// Err 检查减少取消后继续拿新任务的机会，但检查与下一条语句并非原子动作。
		if ctx.Err() != nil {
			trace.Log(actor, "cancel_observed", "循环入口看到 %v", ctx.Err())
			return
		}
		select {
		case <-ctx.Done():
			trace.Log(actor, "cancel_observed", "在接收 jobs 的 select 看见 %v", ctx.Err())
			return
		case order, ok := <-jobs:
			// ok=false 表示关闭且排空，不能通过 ID/Amount 的零值猜测结束。
			if !ok {
				trace.Log(actor, "jobs_drained", "没有更多订单")
				return
			}
			result := validate(order, id)
			processed++
			if err := send(ctx, results, result, "results", blocked, trace, actor); err != nil {
				trace.Log(actor, "result_abandoned", "ID=%s 未交付: %v", order.ID, err)
				return
			}
			trace.Log(actor, "result_accepted", "ID=%s err=%v；不代表 consumer 已完成处理", order.ID, result.Err)
		}
	}
}

// validate 由 worker 同步调用，没有额外 goroutine、共享可变状态或外部资源。
// 非法金额形成该订单的 Result.Err，上层可以继续处理其他订单，而非把整个任务 panic 掉。
func validate(order Order, workerID int) Result {
	result := Result{Order: order, Worker: workerID}
	if order.AmountCents <= 0 {
		result.Err = fmt.Errorf("订单 %s amount=%d: %w", order.ID, order.AmountCents, ErrInvalidOrder)
	}
	return result
}

// send 是 feeder/worker 共用的取消感知发送。T 是本次 channel 元素类型，value 是值副本。
// 快速探测只为记录“当前不能发送”；第二个 select 才实际等待空位/接收者或取消。
// out 不允许被接收者关闭：select 并不能把向 closed channel 发送的 panic 变成 error。
func send[T any](ctx context.Context, out chan<- T, value T, stage string, blocked chan<- string, trace *Trace, actor string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case out <- value:
		return nil
	default:
		trace.Log(actor, "send_wait", "%s 暂时不能发送；接下来同时等待发送机会和 ctx.Done", stage)
		if blocked != nil {
			select {
			case blocked <- stage:
			default: // 观察通道满时丢通知；它不是可靠业务队列，不能反过来制造堵塞。
			}
		}
	}
	select {
	case out <- value:
		// 发送和取消同时 ready 时可能走这里。不能声称取消发生后绝对不会再交付一个值。
		return nil
	case <-ctx.Done():
		trace.Log(actor, "send_canceled", "%s 发送等待观察到 %v", stage, ctx.Err())
		return ctx.Err()
	}
}

// Report 由 consumer 拥有，只有 Collect 的 goroutine append Results；没有并发写 slice。
// Err 是批次结局，可能为来源失败、消费者主动停止、外部取消或等待上限。
type Report struct {
	Results    []Result
	Completion Completion
	Err        error
}

// Collect 是实际消费者，由 Run 或饱和实验调用。收到值后 append 才算本课“业务处理完成”。
// 遇到结束、主动停止或外部取消，全部路径都通知/等待 owner 清理，不让 goroutine 留在后台。
func Collect(ctx context.Context, pipeline *Pipeline, stopAfter int, trace *Trace) Report {
	defer pipeline.Cancel() // 注册的是方法调用；本函数退出时释放取消资源，Wait 仍在下面明确执行。
	report := Report{}
consume:
	for {
		select {
		case <-ctx.Done():
			report.Err = ctx.Err()
			pipeline.Cancel()
			break consume
		case result, ok := <-pipeline.Results:
			if !ok {
				trace.Log("consumer", "results_drained", "关闭且排空，结束接收")
				break consume
			}
			report.Results = append(report.Results, result)
			trace.Log("consumer", "business_done", "ID=%s 第 %d 条已纳入报告 err=%v", result.Order.ID, len(report.Results), result.Err)
			if stopAfter > 0 && len(report.Results) == stopAfter {
				report.Err = ErrEarlyStop
				trace.Log("consumer", "cancel_requested", "已消费 %d 条；停止读取 results 并发出取消", stopAfter)
				pipeline.Cancel()
				break consume
			}
		}
	}
	// 清理等待与已经取消的业务 ctx 分开：若继续拿 ctx 等待，Done 已 ready 会立即返回，
	// 根本不能证明 worker 退出。清理仍有独立上限，不使用无限 background 等待。
	cleanup, cancel := context.WithTimeout(context.Background(), CleanupLimit)
	defer cancel()
	completion, err := pipeline.Wait(cleanup)
	report.Completion = completion
	if err != nil {
		report.Err = errors.Join(report.Err, err)
	} else {
		if completion.SourceErr != nil && !errors.Is(completion.SourceErr, context.Canceled) {
			// 来源 error 有专门终态通道，即使消费者提前退出也不会丢掉；Join 保留已有主动停止原因。
			report.Err = errors.Join(report.Err, completion.SourceErr)
		}
		if completion.CancelErr != nil && !errors.Is(report.Err, ErrEarlyStop) && !errors.Is(report.Err, completion.CancelErr) {
			// 主动停止产生的内部 Canceled 已由 ErrEarlyStop 表达；外部取消/截止仍保留原始类型。
			report.Err = errors.Join(report.Err, completion.CancelErr)
		}
	}
	trace.Log("owner", "wait_returned", "consumed=%d workersExited=%d err=%v", len(report.Results), completion.WorkersExited, report.Err)
	return report
}

// Run 是核心入口调用链的 owner 包装：Start 失败不产生 goroutine，成功则交给 Collect 收尾。
func Run(ctx context.Context, input []Order, opts Options, trace *Trace) Report {
	pipeline, err := Start(ctx, input, opts, trace)
	if err != nil {
		return Report{Err: err}
	}
	return Collect(ctx, pipeline, opts.StopAfter, trace)
}
