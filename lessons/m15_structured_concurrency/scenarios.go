package lesson

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"
)

// check只把课程不变量变为error，独立测试仍直接核对业务数据而非依赖这行PASS。
func check(ok bool, label string) error {
	if !ok {
		return fmt.Errorf("断言失败: %s", label)
	}
	return nil
}
func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// receiveID由HTTP owner等待已进入handler/已完成响应的稳定ID。等待可取消且不猜顺序。
func receiveID(ctx context.Context, in <-chan string) (string, error) {
	select {
	case id := <-in:
		return id, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type HTTPReport struct {
	Sequential, Parallel []Row
	Completed            []string
	Counts               Counts
}

// HTTPScenario由demo/test运行同一份Catalog：先顺序，再以2个活跃请求并行。
// 它拥有batch goroutine、client和fixture；任何提前失败都先取消、放门、真实join，再关闭HTTP。
func HTTPScenario(parent context.Context) (report HTTPReport, resultErr error) {
	baseline, err := NewHTTPFixture(false)
	if err != nil {
		return report, err
	}
	client := NewHTTPClient(baseline.URL)
	report.Sequential, err = Sequential(parent, DemoIDs, true, client.Fetch)
	client.Close()
	closeErr := baseline.Close()
	if err != nil || closeErr != nil {
		return report, errors.Join(err, closeErr)
	}
	f, err := NewHTTPFixture(true)
	if err != nil {
		return report, err
	}
	c := NewHTTPClient(f.URL)
	ctx, cancel := context.WithCancel(parent)
	var batch sync.WaitGroup
	joined := make(chan struct{})
	var rows []Row
	var batchErr error
	batch.Go(func() { rows, batchErr = FanOut(ctx, DemoIDs, 2, true, c.Fetch); close(joined) })
	defer func() {
		Cleanup(&resultErr, func() { cancel(); f.ReleaseAll() }, joined, batch.Wait)
		c.Close()
		resultErr = errors.Join(resultErr, f.Close())
	}()
	first, err := receiveID(parent, f.Entered)
	if err != nil {
		return report, err
	}
	second, err := receiveID(parent, f.Entered)
	if err != nil {
		return report, err
	}
	if err := check((first == "SKU-A" && second == "SKU-B") || (first == "SKU-B" && second == "SKU-A"), "首批仅A/B进入"); err != nil {
		return report, err
	}
	// A一直占一个位置；依次放B/C/D，使完成顺序被门固定为B,C,D,A，与输入顺序不同。
	for _, id := range []string{"SKU-B", "SKU-C", "SKU-D"} {
		if err := f.Release(id); err != nil {
			return report, err
		}
		finished, err := receiveID(parent, f.Finished)
		if err != nil {
			return report, err
		}
		if finished != id {
			return report, fmt.Errorf("响应完成=%s，预期=%s", finished, id)
		}
		if id != "SKU-D" {
			next, err := receiveID(parent, f.Entered)
			if err != nil {
				return report, err
			}
			want := "SKU-C"
			if id == "SKU-C" {
				want = "SKU-D"
			}
			if next != want {
				return report, fmt.Errorf("新接纳=%s，预期=%s", next, want)
			}
		}
	}
	if err := f.Release("SKU-A"); err != nil {
		return report, err
	}
	if err := Await(parent, joined, "真实HTTP批次"); err != nil {
		return report, err
	}
	batch.Wait()
	if batchErr != nil {
		return report, batchErr
	}
	if err := f.Close(); err != nil {
		return report, err
	}
	report.Parallel = rows
	report.Completed = f.Completed()
	report.Counts = f.Counts()
	return report, check(report.Counts.Max == 2 && report.Counts.Active == 0, "HTTP并发上限2且handler实际离开")
}

type GroupReport struct {
	AdmissionBlocked, WaitBlocked bool
	First                         error
	Counts                        Counts
}

// GroupScenario展示两个容易误判的等待：Go的限额接纳和Wait的完整清理。
// parent取消后首个任务故意停在cleanupGate，owner可以救援；这是可控故障，不遗留任务。
func GroupScenario(parent context.Context) (report GroupReport, resultErr error) {
	ctx, cancel := context.WithCancel(parent)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(1)
	gate := NewGate()
	started, observed := make(chan struct{}), make(chan struct{})
	g.Go(func() error { close(started); <-gctx.Done(); close(observed); <-gate.C; return gctx.Err() })
	var dispatcher, joiner sync.WaitGroup
	beforeGo, admitted, joined := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var groupErr error
	dispatcher.Go(func() { close(beforeGo); g.Go(func() error { return gctx.Err() }); close(admitted) })
	joiner.Go(func() { dispatcher.Wait(); groupErr = g.Wait(); close(joined) })
	defer func() { Cleanup(&resultErr, func() { cancel(); gate.Release() }, joined, joiner.Wait) }()
	if err := Await(parent, started, "SetLimit首项"); err != nil {
		return report, err
	}
	if err := Await(parent, beforeGo, "第二次Go接纳边界"); err != nil {
		return report, err
	}
	cancel()
	if err := Await(parent, observed, "首项已观察取消"); err != nil {
		return report, err
	}
	// permit仍归首项，第二次Go不可能完成；ctx取消并不参与errgroup内部的permit send。
	report.AdmissionBlocked = !closed(admitted)
	gate.Release()
	if err := Await(parent, joined, "限额实验实际Wait"); err != nil {
		return report, err
	}
	joiner.Wait()
	if err := check(report.AdmissionBlocked && errors.Is(groupErr, context.Canceled), "取消不直接唤醒Go限额等待"); err != nil {
		return report, err
	}
	first, counts, blocked, err := FirstErrorScenario(parent)
	report.First = first
	report.Counts = counts
	report.WaitBlocked = blocked
	return report, err
}

// FirstErrorScenario的A先返回ErrBackend，B观察group取消后仍在资源清理门处。
// 首错已确定不等于Wait完成；owner放行B后g.Wait才返回A的原错误链。
func FirstErrorScenario(parent context.Context) (first error, counts Counts, blocked bool, resultErr error) {
	ctx, cancel := context.WithCancel(parent)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(2)
	fail, cleanup := NewGate(), NewGate()
	started := make(chan struct{}, 2)
	observed := make(chan struct{})
	tracker := &Tracker{}
	g.Go(func() error {
		leave := tracker.Enter(1)
		defer leave()
		started <- struct{}{}
		select {
		case <-fail.C:
			return ErrBackend
		case <-gctx.Done():
			return gctx.Err()
		}
	})
	g.Go(func() error {
		leave := tracker.Enter(1)
		defer leave()
		started <- struct{}{}
		<-gctx.Done()
		close(observed)
		<-cleanup.C
		return gctx.Err()
	})
	var joiner sync.WaitGroup
	joined := make(chan struct{})
	var groupErr error
	joiner.Go(func() { groupErr = g.Wait(); close(joined) })
	defer func() {
		Cleanup(&resultErr, func() { cancel(); fail.Release(); cleanup.Release() }, joined, joiner.Wait)
	}()
	for range 2 {
		select {
		case <-started:
		case <-parent.Done():
			return nil, Counts{}, false, parent.Err()
		}
	}
	fail.Release()
	if err := Await(parent, observed, "B取消观察"); err != nil {
		return nil, Counts{}, false, err
	}
	blocked = !closed(joined)
	cleanup.Release()
	if err := Await(parent, joined, "首错兄弟清理后的Wait"); err != nil {
		return nil, Counts{}, blocked, err
	}
	joiner.Wait()
	counts = tracker.Counts()
	return groupErr, counts, blocked, check(blocked && errors.Is(groupErr, ErrBackend) && counts.Active == 0 && counts.Finished == 2, "首错保留且所有任务真实完成")
}

// SemaphoreScenario在capacity=3时先运行weight=2和1，第三项weight=2在派发者内等待。
// 两项Fetch只在ctx或owner门上等待，取消会同时释放工作与Acquire，随后真正group.Wait。
func SemaphoreScenario(parent context.Context) (counts Counts, rows []Row, batchErr error, resultErr error) {
	ctx, cancel := context.WithCancel(parent)
	gate := NewGate()
	started := make(chan struct{}, 3)
	tracker := &Tracker{}
	items := []WeightedSKU{{"SKU-A", 2}, {"SKU-B", 1}, {"SKU-D", 2}}
	fetch := func(ctx context.Context, id string) (int64, error) {
		units := 1
		if id != "SKU-B" {
			units = 2
		}
		leave := tracker.Enter(units)
		defer leave()
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-gate.C:
			return Catalog()[id], nil
		}
	}
	var owner sync.WaitGroup
	joined := make(chan struct{})
	var ownedRows []Row
	var ownedErr error
	owner.Go(func() { ownedRows, ownedErr = WeightedBatch(ctx, items, 3, true, fetch); close(joined) })
	defer func() { Cleanup(&resultErr, func() { cancel(); gate.Release() }, joined, owner.Wait) }()
	for range 2 {
		select {
		case <-started:
		case <-parent.Done():
			return Counts{}, nil, nil, parent.Err()
		}
	}
	before := tracker.Counts()
	if err := check(before.Units == 3 && before.Active == 2 && before.Started == 2, "第三项尚未进入Fetch，资源单位已饱和"); err != nil {
		return Counts{}, nil, nil, err
	}
	cancel()
	if err := Await(parent, joined, "取消Acquire和已有工作后join"); err != nil {
		return Counts{}, nil, nil, err
	}
	owner.Wait()
	counts = tracker.Counts()
	rows = ownedRows
	batchErr = ownedErr
	return counts, rows, batchErr, check(counts.MaxUnits == 3 && counts.Started == 2 && counts.Active == 0 && counts.Units == 0 && errors.Is(batchErr, context.Canceled), "加权上限、取消接纳和permit归还")
}
