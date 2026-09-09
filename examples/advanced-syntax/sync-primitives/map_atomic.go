package main

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

func demoSyncMap() {
	// sync.Map 的零值就能使用，不需要 make。它的键和值类型都是 any，因此类型检查
	// 比 map[K]V 弱。大多数业务代码应优先选择普通 map 配 Mutex/RWMutex。
	var values sync.Map
	const entries = 6

	var wg sync.WaitGroup
	for key := 0; key < entries; key++ {
		key := key
		wg.Go(func() {
			// 各 goroutine 写不同的 key，正是 sync.Map 擅长的访问模式之一。
			values.Store(key, key*10)
		})
	}
	wg.Wait()

	// Range 不保证遍历顺序，也不保证是某一时刻的一致快照。这里已经停止并发写入，
	// 并把 key 排序，只是为了让教学输出稳定。
	keys := make([]int, 0, entries)
	values.Range(func(key, _ any) bool {
		keys = append(keys, key.(int))
		return true
	})
	sort.Ints(keys)

	orderedValues := make([]int, 0, len(keys))
	for _, key := range keys {
		value, _ := values.Load(key)
		orderedValues = append(orderedValues, value.(int))
	}

	actualExisting, loadedExisting := values.LoadOrStore(2, 200)
	actualNew, loadedNew := values.LoadOrStore(6, 60)
	values.Delete(5)
	_, keyFiveExists := values.Load(5)

	fmt.Printf("排序后的值=%v\n", orderedValues)
	fmt.Printf("LoadOrStore 已有 key：actual=%v, loaded=%v；新 key：actual=%v, loaded=%v\n",
		actualExisting, loadedExisting, actualNew, loadedNew)
	fmt.Printf("删除 key=5 后仍存在=%v\n", keyFiveExists)

	// sync.Map 尤其适合“每个 key 写一次、读很多次”，或不同 goroutine 操作互不重叠
	// 的 key 集合。它没有 Len；若业务需要跨多个键维护不变量，普通 map 加锁通常更清楚。
}

type runtimeSettings struct {
	Mode  string
	Limit int
}

func demoAtomic() {
	// Go 1.19 起提供 atomic.Int64 等强类型包装，通常比旧式 AddInt64(&x, 1) 易读。
	var counter atomic.Int64
	const increments = 1000
	var wg sync.WaitGroup
	for i := 0; i < increments; i++ {
		wg.Go(func() {
			counter.Add(1)
		})
	}
	wg.Wait()

	oldCount := counter.Load()
	swapped := counter.CompareAndSwap(oldCount, oldCount+10)
	fmt.Printf("原子累加=%d；CAS 成功=%v；CAS 后=%d\n", oldCount, swapped, counter.Load())

	// atomic.Value 适合“一次发布整份不可变配置快照”：读者只 Load 指针，不会看到
	// 更新了一半的结构。这里把对象视为不可变；发布后不要再原地修改其字段。
	var settings atomic.Value
	development := &runtimeSettings{Mode: "development", Limit: 10}
	production := &runtimeSettings{Mode: "production", Limit: 100}
	maintenance := &runtimeSettings{Mode: "maintenance", Limit: 1}

	settings.Store(development)
	previous := settings.Swap(production).(*runtimeSettings)
	staleCAS := settings.CompareAndSwap(development, maintenance)
	currentCAS := settings.CompareAndSwap(production, maintenance)
	current := settings.Load().(*runtimeSettings)

	fmt.Printf("Value：Swap 前=%+v；用旧指针 CAS=%v；用当前指针 CAS=%v；当前=%+v\n",
		*previous, staleCAS, currentCAS, *current)

	// 一个 atomic 操作不可分割，不等于一连串业务步骤整体不可分割。需要共同维护多个
	// 可变字段的不变量时，Mutex 往往更合适。atomic.Value 第一次 Store 后，后续值必须
	// 具有相同的具体类型，也不能 Store(nil)；Value 和 typed atomic 第一次使用后都不能复制。
}
