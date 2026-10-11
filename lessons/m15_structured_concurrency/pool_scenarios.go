package lesson

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type PoolReport struct {
	Rows      []Row
	Rejected  error
	Aggregate error
	Counts    Counts
	Blocked   bool
}

// stopPool由每个实验的defer调用。先通知/放门/关闭，再等待所有worker及提交者真正返回。
// owners只登记少数实验producer，不是“每个SKU一个goroutine”；加一位finisher发布join通知。
func stopPool(primary *error, p *Pool, gates []*Gate, owners *sync.WaitGroup) {
	p.Abort(context.Canceled)
	for _, g := range gates {
		g.Release()
	}
	var finisher sync.WaitGroup
	joined := make(chan struct{})
	var poolErr error
	finisher.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
		defer cancel()
		poolErr = p.waitJoined(ctx)
		owners.Wait()
		close(joined)
	})
	Cleanup(primary, func() {}, joined, finisher.Wait)
	if closed(joined) && poolErr != nil {
		*primary = errors.Join(*primary, poolErr)
	}
}

// heldFetch由实验owner生成，业务函数由worker调用。gate冻结正常执行，ctx解除取消等待。
// started只允许这一项任务使用；关闭它表示Fetch已开始，不表示任务完成或pool已join。
func heldFetch(gate *Gate, started chan struct{}) Fetch {
	return func(ctx context.Context, id string) (int64, error) {
		close(started)
		select {
		case <-gate.C:
			return Catalog()[id], nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

// readTickets在pool owner已Wait后按接纳顺序读取终态；Ticket指针仍别名同一个发布槽。
// 这里没有启动goroutine，也不承担关闭权；单项观察失败交给外层defer继续收尾。
func readTickets(ctx context.Context, tickets []*Ticket) ([]Row, error) {
	rows := make([]Row, 0, len(tickets))
	for _, ticket := range tickets {
		row, err := ticket.Result(ctx)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// PoolCloseScenario固定1worker+1队列：A运行、B排队、D提交者已登记等待版本。
// Close不需要等待该提交者持有的锁，D收到ErrClosed且无Ticket；A/B已转移给pool，必须drain。
func PoolCloseScenario(ctx context.Context) (report PoolReport, resultErr error) {
	p, err := NewPool(ctx, 1, 1, false)
	if err != nil {
		return report, err
	}
	gate := NewGate()
	started := make(chan struct{})
	var producers sync.WaitGroup
	defer func() { stopPool(&resultErr, p, []*Gate{gate}, &producers) }()
	a, err := p.Submit(ctx, "SKU-A", heldFetch(gate, started), nil)
	if err != nil {
		return report, err
	}
	if err := Await(ctx, started, "A实际运行"); err != nil {
		return report, err
	}
	b, err := p.Submit(ctx, "SKU-B", func(context.Context, string) (int64, error) { return 1800, nil }, nil)
	if err != nil {
		return report, err
	}
	waiting, submitted := make(chan struct{}, 1), make(chan struct{})
	var third *Ticket
	var submitErr error
	producers.Go(func() {
		third, submitErr = p.Submit(ctx, "SKU-D", func(context.Context, string) (int64, error) { return 4200, nil }, waiting)
		close(submitted)
	})
	if err := Await(ctx, waiting, "满队列提交等待版本"); err != nil {
		return report, err
	}
	report.Blocked = !closed(submitted) && p.QueueLen() == 1
	p.Close() // 与原反例不同：锁内没有阻塞send，关闭立即改变closed/closing并唤醒producer。
	if err := Await(ctx, submitted, "关闭期间提交被拒绝"); err != nil {
		return report, err
	}
	producers.Wait()
	if err := check(third == nil && errors.Is(submitErr, ErrClosed) && !closed(p.Joined()), "拒绝D，但A/B仍未join"); err != nil {
		return report, err
	}
	gate.Release()
	if err := p.Wait(ctx); err != nil {
		return report, err
	}
	report.Rows, err = readTickets(ctx, []*Ticket{a, b})
	report.Rejected = submitErr
	report.Counts = p.Counts()
	return report, errors.Join(err, check(report.Blocked && report.Counts.Active == 0 && report.Counts.Finished == 2, "背压和graceful drain结局"))
}

// PoolCancelSubmitScenario只取消D的接纳ctx：未成功入队的D仍属于caller，worker不得执行它。
// A/B已经接纳，与D的ctx无父子关系，所以仍按pool预算完成，之后owner关闭并Wait。
func PoolCancelSubmitScenario(ctx context.Context) (report PoolReport, resultErr error) {
	p, err := NewPool(ctx, 1, 1, false)
	if err != nil {
		return report, err
	}
	gate := NewGate()
	started := make(chan struct{})
	var producers sync.WaitGroup
	defer func() { stopPool(&resultErr, p, []*Gate{gate}, &producers) }()
	a, err := p.Submit(ctx, "SKU-A", heldFetch(gate, started), nil)
	if err != nil {
		return report, err
	}
	if err := Await(ctx, started, "A gate"); err != nil {
		return report, err
	}
	b, err := p.Submit(ctx, "SKU-B", func(context.Context, string) (int64, error) { return 1800, nil }, nil)
	if err != nil {
		return report, err
	}
	admission, cancel := context.WithCancel(ctx)
	defer cancel()
	waiting, submitted := make(chan struct{}, 1), make(chan struct{})
	var third *Ticket
	var submitErr error
	producers.Go(func() {
		third, submitErr = p.Submit(admission, "SKU-D", func(context.Context, string) (int64, error) { return 4200, nil }, waiting)
		close(submitted)
	})
	if err := Await(ctx, waiting, "D等待接纳"); err != nil {
		return report, err
	}
	cancel()
	if err := Await(ctx, submitted, "D取消返回"); err != nil {
		return report, err
	}
	producers.Wait()
	if err := check(third == nil && errors.Is(submitErr, context.Canceled) && p.QueueLen() == 1, "D取消未转移任务所有权"); err != nil {
		return report, err
	}
	gate.Release()
	p.Close()
	if err := p.Wait(ctx); err != nil {
		return report, err
	}
	report.Rows, err = readTickets(ctx, []*Ticket{a, b})
	report.Rejected = submitErr
	report.Counts = p.Counts()
	return report, err
}

// PoolPartialScenario选择“隔离单项失败”：同一worker执行panic、业务缺价、成功读取。
// invoke在这个worker内部recover；若在main recover，抓不到别的goroutine的panic。
// Ticket发布失败不阻塞下一项，最后聚合errors.Join保留所有失败，而非丢掉只剩首错。
func PoolPartialScenario(ctx context.Context) (report PoolReport, resultErr error) {
	p, err := NewPool(ctx, 1, 3, false)
	if err != nil {
		return report, err
	}
	var producers sync.WaitGroup
	defer func() { stopPool(&resultErr, p, nil, &producers) }()
	fetch := func(_ context.Context, id string) (int64, error) {
		switch id {
		case "SKU-A":
			panic("价格解码错误")
		case "SKU-C":
			return 0, ErrUnavailable
		default:
			return Catalog()[id], nil
		}
	}
	var tickets []*Ticket
	for _, id := range []string{"SKU-A", "SKU-C", "SKU-D"} {
		ticket, err := p.Submit(ctx, id, fetch, nil)
		if err != nil {
			return report, err
		}
		tickets = append(tickets, ticket)
	}
	p.Close()
	if err := p.Wait(ctx); err != nil {
		return report, err
	}
	report.Rows, err = readTickets(ctx, tickets)
	if err != nil {
		return report, err
	}
	for _, row := range report.Rows {
		if row.Err != nil {
			report.Aggregate = errors.Join(report.Aggregate, fmt.Errorf("%s: %w", row.ID, row.Err))
		}
	}
	report.Counts = p.Counts()
	var panicErr *PanicError
	return report, check(errors.As(report.Aggregate, &panicErr) && errors.Is(report.Aggregate, ErrUnavailable) && report.Rows[2].Price == 4200 && report.Counts.Finished == 3, "panic/业务失败/成功均有终态")
}

// PoolFirstErrorScenario把首错传播和最后join分开：B观察取消仍等cleanup门，C排队未开始。
// A故障触发cancel，worker仍将C标为同一取消原因，所有接纳项都发布结果，最后等B清理。
func PoolFirstErrorScenario(ctx context.Context) (report PoolReport, resultErr error) {
	p, err := NewPool(ctx, 2, 1, true)
	if err != nil {
		return report, err
	}
	fail, cleanup := NewGate(), NewGate()
	started := make(chan struct{}, 2)
	observed := make(chan struct{})
	var producers sync.WaitGroup
	defer func() { stopPool(&resultErr, p, []*Gate{fail, cleanup}, &producers) }()
	fetch := func(work context.Context, id string) (int64, error) {
		started <- struct{}{}
		if id == "SKU-A" {
			select {
			case <-fail.C:
				return 0, ErrBackend
			case <-work.Done():
				return 0, work.Err()
			}
		}
		<-work.Done()
		close(observed)
		<-cleanup.C
		return 0, work.Err()
	}
	a, err := p.Submit(ctx, "SKU-A", fetch, nil)
	if err != nil {
		return report, err
	}
	b, err := p.Submit(ctx, "SKU-B", fetch, nil)
	if err != nil {
		return report, err
	}
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			return report, ctx.Err()
		}
	}
	c, err := p.Submit(ctx, "SKU-D", func(context.Context, string) (int64, error) { return 4200, nil }, nil)
	if err != nil {
		return report, err
	}
	p.Close()
	fail.Release()
	if err := Await(ctx, observed, "B已观察首错取消"); err != nil {
		return report, err
	}
	report.Blocked = !closed(p.Joined())
	cleanup.Release()
	poolErr := p.Wait(ctx)
	if !errors.Is(poolErr, ErrBackend) {
		return report, fmt.Errorf("pool首错丢失: %w", poolErr)
	}
	report.Rows, err = readTickets(ctx, []*Ticket{a, b, c})
	report.Aggregate = poolErr
	report.Counts = p.Counts()
	return report, errors.Join(err, check(report.Blocked && report.Counts.Started == 2 && report.Counts.Finished == 2 && report.Counts.Active == 0, "排队D不执行，运行A/B全部join"))
}

// PoolConsumerExitScenario的owner不读任何Ticket就决定放弃。结果用独立完成槽发布，
// 不向无人接收的结果channel发送，所以取消后worker能退出；join后再读取两个终态。
func PoolConsumerExitScenario(ctx context.Context) (report PoolReport, resultErr error) {
	p, err := NewPool(ctx, 1, 2, false)
	if err != nil {
		return report, err
	}
	gate := NewGate()
	started := make(chan struct{})
	var producers sync.WaitGroup
	defer func() { stopPool(&resultErr, p, []*Gate{gate}, &producers) }()
	a, err := p.Submit(ctx, "SKU-A", heldFetch(gate, started), nil)
	if err != nil {
		return report, err
	}
	if err := Await(ctx, started, "consumer停止前A已开始"); err != nil {
		return report, err
	}
	b, err := p.Submit(ctx, "SKU-B", func(context.Context, string) (int64, error) { return 1800, nil }, nil)
	if err != nil {
		return report, err
	}
	p.Abort(context.Canceled)
	if err := p.Wait(ctx); err != nil {
		return report, err
	}
	report.Rows, err = readTickets(ctx, []*Ticket{a, b})
	report.Counts = p.Counts()
	return report, errors.Join(err, check(report.Counts.Started == 1 && report.Counts.Active == 0 && errors.Is(report.Rows[0].Err, context.Canceled) && errors.Is(report.Rows[1].Err, context.Canceled), "消费者不接收也能真实join，接纳项不遗失"))
}

// PoolRecursiveFixScenario让worker遇到满队列时TrySubmit立即拒绝，先返回自己的结果。
// 外部owner收到结果后再Submit子SKU，接纳位置来自worker继续消费，不再等worker接收自己。
func PoolRecursiveFixScenario(ctx context.Context) (report PoolReport, resultErr error) {
	p, err := NewPool(ctx, 1, 1, false)
	if err != nil {
		return report, err
	}
	gate := NewGate()
	started := make(chan struct{})
	rejected := make(chan error, 1)
	var producers sync.WaitGroup
	defer func() { stopPool(&resultErr, p, []*Gate{gate}, &producers) }()
	childFetch := func(_ context.Context, id string) (int64, error) { return Catalog()[id], nil }
	a, err := p.Submit(ctx, "SKU-A", func(work context.Context, id string) (int64, error) {
		close(started)
		select {
		case <-gate.C:
		case <-work.Done():
			return 0, work.Err()
		}
		_, err := p.TrySubmit("SKU-D", childFetch)
		rejected <- err
		return 12500, nil // child不属于本次成功接纳；外部owner继续安排，worker不能自己Wait池。
	}, nil)
	if err != nil {
		return report, err
	}
	if err := Await(ctx, started, "outer已占worker"); err != nil {
		return report, err
	}
	b, err := p.Submit(ctx, "SKU-B", childFetch, nil)
	if err != nil {
		return report, err
	}
	gate.Release()
	select {
	case report.Rejected = <-rejected:
	case <-ctx.Done():
		return report, ctx.Err()
	}
	if !errors.Is(report.Rejected, ErrQueueFull) {
		return report, fmt.Errorf("worker递归TrySubmit应拒绝: %v", report.Rejected)
	}
	if _, err := a.Result(ctx); err != nil {
		return report, err
	}
	d, err := p.Submit(ctx, "SKU-D", childFetch, nil)
	if err != nil {
		return report, err
	}
	p.Close()
	if err := p.Wait(ctx); err != nil {
		return report, err
	}
	report.Rows, err = readTickets(ctx, []*Ticket{a, b, d})
	report.Counts = p.Counts()
	return report, err
}
