package lesson

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// Sequential 由 demo 对同一份 SKU 做顺序基线；当前 caller 同步调用 Fetch，没有额外任务。
// partial 仅放行 ErrUnavailable，超时/取消/服务故障仍立即终止，未派发行保留 ErrNotStarted。
func Sequential(ctx context.Context, ids []string, partial bool, fetch Fetch) ([]Row, error) {
	copyIDs, err := validate(ids, 1)
	if err != nil {
		return nil, err
	}
	rows := initialRows(copyIDs)
	for i, id := range copyIDs {
		if err := ctx.Err(); err != nil {
			return rows, err
		}
		price, err := invoke(ctx, id, fetch)
		rows[i] = Row{id, price, err}
		if err != nil && !(partial && errors.Is(err, ErrUnavailable)) {
			return rows, fmt.Errorf("SKU %s: %w", id, err)
		}
	}
	return rows, nil
}

// initialRows由批次owner在启动工作前分配稳定位置，未派发不伪装成“价格0的成功SKU”。
func initialRows(ids []string) []Row {
	rows := make([]Row, len(ids))
	for i, id := range ids {
		rows[i] = Row{ID: id, Err: ErrNotStarted}
	}
	return rows
}

// FanOut 由批次 owner 调用，把短期独立读取交给真实 errgroup。
// 每个闭包独占 rows[i]，Wait 后 caller 才读整个 slice；底层数组虽共享，元素写入互不冲突。
// SetLimit 限制已启动的 group 工作。Go 本身会等 permit，不能被 ctx 直接打断，
// 因此 Fetch 必须观察取消、不能无限等外部门；可取消接纳使用下面的 WeightedBatch。
func FanOut(parent context.Context, ids []string, limit int, partial bool, fetch Fetch) (rows []Row, resultErr error) {
	copyIDs, err := validate(ids, limit)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	g, workCtx := errgroup.WithContext(ctx)
	g.SetLimit(limit) // 在第一项 Go 前设置；运行中修改上限会违反 API 契约。
	ownedRows := initialRows(copyIDs)
	// defer 是提前返回的保险，但必须等派发结束后才能启动 Wait：不能让空组先 Wait 完。
	for i, id := range copyIDs {
		if workCtx.Err() != nil {
			break
		}
		g.Go(func() error {
			// Go1.27 的循环声明有每轮变量；这里 i/id 也是这一轮闭包的值。
			// 若限额等待期间出现首错，Go 仍可能接纳本项，故函数体再次检查取消。
			if err := workCtx.Err(); err != nil {
				ownedRows[i] = Row{ID: id, Err: err}
				return err
			}
			price, err := invoke(workCtx, id, fetch)
			ownedRows[i] = Row{id, price, err}
			if err == nil || (partial && errors.Is(err, ErrUnavailable)) {
				return nil
			}
			return fmt.Errorf("SKU %s: %w", id, err)
		})
	}
	joined := make(chan struct{})
	var joiner sync.WaitGroup
	var groupErr error
	joiner.Go(func() { groupErr = g.Wait(); close(joined) })
	// groupErr 由 joiner 写；owner 收 joined 后才读。resultErr仅由owner写，没有错误结果竞争。
	defer func() { Cleanup(&resultErr, cancel, joined, joiner.Wait) }()
	if err := Await(parent, joined, "errgroup.Wait"); err != nil {
		// 不在 joiner 仍写结果时写 resultErr；先取消并等待它，再合并 observer 错误。
		cancel()
		cleanCtx, stop := context.WithTimeout(context.Background(), cleanupBudget)
		defer stop()
		if cleanupErr := Await(cleanCtx, joined, "fan-out提前失败收尾"); cleanupErr != nil {
			return nil, errors.Join(err, cleanupErr)
		}
		joiner.Wait()
		resultErr = errors.Join(groupErr, err)
	} else {
		joiner.Wait()
		resultErr = groupErr
	}
	return ownedRows, resultErr
}

// WeightedBatch 用资源单位限制 fan-out；每项 Weight 至少 1，所以 goroutine 数也有上限。
// owner 在创建工作 goroutine 之前 Acquire；它阻塞的是单个派发者，而非每项一个等待者。
// partial 仍只容忍业务缺价，Semaphore 负责容量，不负责错误传播或每秒请求速率。
type WeightedSKU struct {
	ID     string
	Weight int64
}

// WeightedBatch由demo/业务owner调用；参数slice先复制，caller只在返回后读rows。
// 这里g没有SetLimit：permit总量<=8、每项至少1已约束已创建工作，额外只有一位joiner。
func WeightedBatch(parent context.Context, items []WeightedSKU, capacity int64, partial bool, fetch Fetch) (rows []Row, resultErr error) {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
		if item.Weight < 1 || item.Weight > capacity {
			return nil, errors.New("weight必须在1..capacity，不能等永远不可能取得的permit")
		}
	}
	copyIDs, err := validate(ids, int(capacity))
	if err != nil {
		return nil, err
	}
	owned := append([]WeightedSKU(nil), items...)
	ownedRows := initialRows(copyIDs)
	ctx, cancel := context.WithCancel(parent)
	g, workCtx := errgroup.WithContext(ctx)
	sem := semaphore.NewWeighted(capacity)
	var admissionErr error
	for i, item := range owned {
		if err := sem.Acquire(workCtx, item.Weight); err != nil {
			admissionErr = fmt.Errorf("SKU %s 接纳: %w", item.ID, err)
			break
		}
		// Acquire 已成功，当前 owner 把 permit 的释放责任交给这个已登记的工作函数。
		g.Go(func() error {
			defer sem.Release(item.Weight) // 包括取消、业务错误与 invoke 恢复 panic，均归还精确单位。
			if err := workCtx.Err(); err != nil {
				ownedRows[i] = Row{ID: item.ID, Err: err}
				return err
			}
			price, err := invoke(workCtx, item.ID, fetch)
			ownedRows[i] = Row{item.ID, price, err}
			if err == nil || (partial && errors.Is(err, ErrUnavailable)) {
				return nil
			}
			return fmt.Errorf("SKU %s: %w", item.ID, err)
		})
	}
	joined := make(chan struct{})
	var joiner sync.WaitGroup
	var groupErr error
	joiner.Go(func() { groupErr = g.Wait(); close(joined) })
	// 收尾始终独立于已取消的业务预算；groupErr只在Wait完成后读取。
	defer func() { Cleanup(&resultErr, cancel, joined, joiner.Wait) }()
	observerErr := Await(parent, joined, "weighted group")
	if observerErr != nil {
		cancel()
		ctx, stop := context.WithTimeout(context.Background(), cleanupBudget)
		defer stop()
		if err := Await(ctx, joined, "weighted清理"); err != nil {
			return nil, errors.Join(observerErr, err)
		}
	}
	joiner.Wait()
	resultErr = errors.Join(groupErr, admissionErr, observerErr)
	return ownedRows, resultErr
}
