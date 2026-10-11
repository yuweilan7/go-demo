package lesson

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
)

// SplitMapSale 重用 E01 的两人购买最后一件库存场景，改成 sync.Map.Load→Store。
// 每条方法都并发安全，但整个“检查+接受订单+扣减”并没有变成事务。
func SplitMapSale(ctx context.Context) (StockResult, error) {
	var values sync.Map
	values.Store("sku-1", int64(1))
	checked := make(chan struct{}, 2)
	release := make(chan struct{})
	var sold atomic.Int64
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			actual, _ := values.Load("sku-1")
			seen := actual.(int64) // any 中保存的是 int64；断言结果是该次读取的值副本。
			checked <- struct{}{}
			select {
			case <-release:
				if seen > 0 {
					values.Store("sku-1", seen-1)
					sold.Add(1)
				}
			case <-ctx.Done():
			}
		})
	}
	for range 2 {
		select {
		case <-checked:
		case <-ctx.Done():
			close(release)
			wg.Wait()
			return StockResult{}, ctx.Err()
		}
	}
	close(release)
	wg.Wait()
	actual, _ := values.Load("sku-1")
	return StockResult{1, actual.(int64), sold.Load()}, nil
}

// MapResult 把单 key 的原子操作与 Range 的访问轨迹分别保存，供日志/独立断言核对。
type MapResult struct {
	Candidates    int64
	CreateWinners int64
	PayWinners    int64
	FinalState    string
	RangeObserved map[string]int
	RangeVisits   int
	RangeFinal    map[string]int
	RangeMixed    bool
}

// ObserveMap 由 E05 调用。订单状态是不可变 string，避免把容器安全误当对象内部安全。
// owner 等待候选者与付款者退出，然后在没有并发访问的普通 map 中记录 Range 结果。
func ObserveMap() MapResult {
	var orders sync.Map
	var candidates, creators, payers atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			// 函数实参先求值，再进入 LoadOrStore；8 次候选构造不会被它变成“只执行一次”。
			candidate := func() string { candidates.Add(1); return "created" }()
			_, loaded := orders.LoadOrStore("order-1", candidate)
			if !loaded {
				// loaded=false 表示本次真正存入，只有这个获胜者增加“创建成功”的业务计数。
				creators.Add(1)
			}
		})
	}
	wg.Wait()
	for range 8 {
		wg.Go(func() {
			if orders.CompareAndSwap("order-1", "created", "paid") {
				payers.Add(1) // 单个 key 的 created→paid 只有一个获胜者。
			}
		})
	}
	wg.Wait()
	state, _ := orders.Load("order-1")
	result := MapResult{Candidates: candidates.Load(), CreateWinners: creators.Load(), PayWinners: payers.Load(), FinalState: state.(string), RangeObserved: make(map[string]int)}

	var versions sync.Map
	versions.Store("left", 1)
	versions.Store("right", 1)
	first := ""
	versions.Range(func(key, value any) bool {
		result.RangeVisits++
		name := key.(string)
		result.RangeObserved[name] = value.(int)
		if first == "" {
			first = name
			other := "left"
			if name == "left" {
				other = "right"
			}
			// f 可以调用同一 Map 的方法。先把已访问的 key 升到 2，再改另一个 key。
			// 实际整体状态只有 (1,1)→(2,1)→(2,2)，不会有“已访问=1，另一个=2”。
			versions.Store(name, 2)
			versions.Store(other, 2)
		}
		return true
	})
	result.RangeFinal = make(map[string]int, 2)
	for _, key := range []string{"left", "right"} {
		actual, _ := versions.Load(key)
		result.RangeFinal[key] = actual.(int)
	}
	// 当前 HashTrieMap 实验通常记录一个 1、一个 2；API 不承诺具体次序或一定混合。
	// 测试只核对不重复访问与最终写入；README 将混合记录标成当前版本观察。
	result.RangeMixed = result.RangeObserved["left"] != result.RangeObserved["right"]
	return result
}

// OnceResult 区分 Do 的 panic、错误初始化以及显式重试策略。
type OnceResult struct {
	NormalCalls    int
	AllReady       bool
	PanicRecovered bool
	PanicInitCalls int
	ErrorInitCalls int
	CachedError    bool
	RetryCalls     int
	RetryValue     string
}

// RetryingInitializer 由 E06 创建，保留已成功初始化的结果，但不把失败永久缓存。
// mu 串行化尝试，保证一个时刻至多一个 loader 在执行；指针只在成功后发布。
type RetryingInitializer struct {
	mu    sync.Mutex
	value *string
}

// Get 由业务请求调用，loader 来自调用者的初始化策略，返回结果交回请求。
// 本课只用立即返回的合成 loader；真实网络初始化必须传 ctx/超时，否则持锁等待会放大延迟。
func (r *RetryingInitializer) Get(loader func() (string, error)) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.value != nil {
		return *r.value, nil
	}
	value, err := loader()
	if err != nil {
		return "", err // 不改 r.value：下一次请求仍能重试；错误由本次调用者处理。
	}
	r.value = &value
	return value, nil
}

// ObserveOnce 由 E06 调用，panic 在同一个 goroutine 的闭包边界恢复，正常入口继续。
// recover 不能拦截 runtime fatal；这里用的是普通 panic，且恢复点包围 once.Do 的调用。
func ObserveOnce() OnceResult {
	var result OnceResult
	var normal sync.Once
	var config string
	results := make([]string, 8)
	var wg sync.WaitGroup
	for id := range 8 {
		wg.Go(func() {
			normal.Do(func() { result.NormalCalls++; config = "ready" })
			// Do 返回前已等初始化完成，config 的普通写对每个读者可见。
			results[id] = config
		})
	}
	wg.Wait()
	result.AllReady = true
	for _, value := range results {
		result.AllReady = result.AllReady && value == "ready"
	}
	var panicking sync.Once
	func() {
		defer func() { result.PanicRecovered = recover() == "初始化 panic" }()
		panicking.Do(func() { result.PanicInitCalls++; panic("初始化 panic") })
	}()
	panicking.Do(func() { result.PanicInitCalls++ }) // Do 把 panic 那次算作已执行，因此这里不再调用。

	failed := errors.New("配置服务暂时不可用")
	var failing sync.Once
	var initErr error
	for range 2 {
		failing.Do(func() { result.ErrorInitCalls++; initErr = failed })
	}
	result.CachedError = errors.Is(initErr, failed)
	// OnceValues 能缓存多返回值，但仍会缓存第一次返回的 error，不会因 error 非 nil 自动重试。
	valuesCalls := 0
	get := sync.OnceValues(func() (string, error) { valuesCalls++; return "", failed })
	_, firstErr := get()
	_, secondErr := get()
	result.CachedError = result.CachedError && valuesCalls == 1 && errors.Is(firstErr, failed) && errors.Is(secondErr, failed)

	var retry RetryingInitializer
	loader := func() (string, error) {
		result.RetryCalls++
		if result.RetryCalls == 1 {
			return "", failed
		}
		return "ready", nil
	}
	_, _ = retry.Get(loader) // 第一次错误是预期注入；下一次显式调用构成重试。
	result.RetryValue, _ = retry.Get(loader)
	_, _ = retry.Get(loader) // 成功缓存后第三次请求不调用 loader。
	return result
}

// PoolResult 保存观察而不承诺复用顺序/数量。Pool 没有“下一次必拿同一对象”的契约。
type PoolResult struct {
	DirtyReused bool
	DirtyLength int
	CleanLength int
	NewBeforeGC int64
	NewAfterGC  int64
}

// ObservePool 由 E07 调用。Get 后借用者独占 buffer；Put 后借用者不再访问它。
// bytes.Buffer.Reset 清空可见长度，容量可能保留；它不保证把旧秘密的字节逐一擦除。
func ObservePool() PoolResult {
	dirty := &sync.Pool{New: func() any { return new(bytes.Buffer) }}
	first := dirty.Get().(*bytes.Buffer)
	first.WriteString("buyer-1-private")
	dirty.Put(first) // 危险：没有 Reset。容器自身安全并不会重置对象。
	second := dirty.Get().(*bytes.Buffer)
	result := PoolResult{DirtyReused: first == second, DirtyLength: second.Len()}
	second.Reset()
	dirty.Put(second)

	var newCalls atomic.Int64
	clean := &sync.Pool{New: func() any { newCalls.Add(1); return new(bytes.Buffer) }}
	owned := clean.Get().(*bytes.Buffer)
	owned.WriteString("buyer-1-private")
	owned.Reset()
	clean.Put(owned)
	next := clean.Get().(*bytes.Buffer)
	result.CleanLength = next.Len() // 无论重用还是 New，所有能进入这个 clean Pool 的对象都为干净状态。
	next.Reset()
	clean.Put(next)
	result.NewBeforeGC = newCalls.Load()
	runtime.GC()
	runtime.GC()
	after := clean.Get().(*bytes.Buffer)
	result.NewAfterGC = newCalls.Load() // 只记录本次观察；任意对象可能被移除，不能用 Pool 保存持久状态。
	after.Reset()
	clean.Put(after)
	return result
}
