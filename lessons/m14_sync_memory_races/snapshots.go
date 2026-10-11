package lesson

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
)

// Catalog 演示 RWMutex 保护 map 及 map 中 slice 的同一套访问协议。
// 锁必须与真正的共享存储对应；给同一个 map 的不同包装对象各配一把锁仍会 race。
type Catalog struct {
	mu    sync.RWMutex
	items map[string][]string
}

// NewCatalog 创建由本轮实验独占的商品分类；零值 map 不能写，所以用 make 分配。
func NewCatalog() *Catalog { return &Catalog{items: make(map[string][]string)} }

// Set 由写者调用，把调用者的 slice 内容复制后存入 Catalog。
// 仅复制 slice 变量只复制“指针、长度、容量”，会把底层数组的别名留下。
func (c *Catalog) Set(key string, values []string) {
	owned := append([]string(nil), values...)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = owned
}

// Get 由读者调用。RLock 期间只读取共享 map/数组，再返回数组副本给调用者。
// 不能直接 return c.items[key]：RUnlock 后调用者还能修改同一底层数组，绕开 c.mu。
func (c *Catalog) Get(key string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.items[key]...)
}

// RWResult 记录确定的协调事实，而不是假定某个 reader/writer 的调度先后。
type RWResult struct {
	ConcurrentReaders   int
	WriterBeforeRelease bool
	FinalValue          int
}

// ObserveRW 由 E03 调用。owner 等两个 reader 确实取得 RLock，才创建 writer。
// 每个 goroutine 都有有限工作；ctx 到期时 owner 也释放读者并等待所有任务完成。
func ObserveRW(ctx context.Context) (RWResult, error) {
	var rw sync.RWMutex
	var value int
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	writerStarted := make(chan struct{})
	writerDone := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			rw.RLock()
			defer rw.RUnlock()
			ready <- struct{}{}
			<-release // owner 独占关闭权；两个 reader 都在持有读锁时等待这个广播。
		})
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			close(release)
			wg.Wait()
			return RWResult{}, ctx.Err()
		}
	}
	wg.Go(func() {
		close(writerStarted) // 只说明 writer 走到尝试写锁前，不声称已取得锁。
		rw.Lock()
		value = 1
		rw.Unlock()
		close(writerDone)
	})
	<-writerStarted
	result := RWResult{ConcurrentReaders: 2}
	select {
	case <-writerDone:
		result.WriterBeforeRelease = true
	default:
		// 持有的两份 RLock 让 writer 不可能完成；这个结论来自锁契约而非 sleep。
	}
	close(release)
	wg.Wait()
	result.FinalValue = value // Wait 建立完成边界后，owner 才读取 writer 的普通变量。
	return result, nil
}

// Config 是一整份配置快照。Version、Limits、Labels 必须属于同一版本。
// 所有字段在发布前构建好，发布后视为不可变；atomic.Value 不替内部 map/slice 加锁。
type Config struct {
	Version int
	Limits  map[string]int
	Labels  []string
}

// cloneConfig 为发布者或读者创建自己拥有的存储；本课字段只有一层 map 和 []string。
// 若值里还有 *Mutable 或 [][]byte，必须继续复制到相应层，不能把这里当万能深复制。
func cloneConfig(in Config) Config {
	out := Config{Version: in.Version, Limits: make(map[string]int, len(in.Limits)), Labels: append([]string(nil), in.Labels...)}
	for key, value := range in.Limits {
		out.Limits[key] = value
	}
	return out
}

// Snapshots 将每次读固定到一个完整的 *Config；writerMu 串行化“读旧版、生成新版、发布”。
// Value 保证单次 Store/Load 原子；两个 writer 的 Load→Store 序列仍需额外协议。
type Snapshots struct {
	writerMu sync.Mutex
	value    atomic.Value
}

// NewSnapshots 在读者启动前存入第一个 *Config，消除 Load()==nil 的未初始化分支。
func NewSnapshots(initial Config) *Snapshots {
	s := &Snapshots{}
	owned := cloneConfig(initial)
	s.value.Store(&owned)
	return s
}

// Read 由业务读者调用，一次 Load 获得某一整版指针，再复制为调用者可修改的结果。
// .(*Config) 是类型断言：any 中的动态类型必须是 *Config；本类型封装保证这一点。
func (s *Snapshots) Read() Config {
	current := s.value.Load().(*Config)
	return cloneConfig(*current)
}

// Update 由配置写者调用，只修改尚未发布的 owned 副本。
// update 不能把 owned 的 map/slice 偷存到外部并在返回后继续改；这里约定它同步完成。
func (s *Snapshots) Update(update func(*Config)) {
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	owned := cloneConfig(*s.value.Load().(*Config))
	update(&owned)
	// 再复制一次防止 callback 保留的 owned 别名指向发布对象；callback 本身不得后台并发写。
	sealed := cloneConfig(owned)
	s.value.Store(&sealed)
}

// Visibility 由 E04 调用，刻意用普通 payload + atomic.Bool 演示发布边界。
// owner 创建一个写者；ready 只从 false→true 一次，写者此后不再修改 payload。
func Visibility(ctx context.Context) (string, error) {
	var payload string
	var ready atomic.Bool
	var wg sync.WaitGroup
	wg.Go(func() {
		payload = "sku-1:完整配置"
		ready.Store(true) // 普通写发生在发布之前；读者观察到这个 Store 才能读 payload。
	})
	for !ready.Load() {
		if err := ctx.Err(); err != nil {
			wg.Wait()
			return "", err
		}
		// 这是有上限的教学观察循环，不是生产推荐的等待方式；通常 channel/Once 更自然。
		runtime.Gosched()
	}
	result := payload // Load 观察到 true 的同步边界已成立；不是靠猜写者“应该跑完了”。
	wg.Wait()
	return result, nil
}
