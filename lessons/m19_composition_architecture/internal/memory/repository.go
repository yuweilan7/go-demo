// Package memory 是真实进程内仓储。它没有 SQL driver，不能作为持久数据库证据。
package memory

import (
	"context"
	"errors"
	"sync"

	"go-demo/lessons/m19_composition_architecture/internal/order"
)

// Gate 仅在取消实验启用。Entered 表示 Commit 已走到真实写入前的等待点；
// release 的关闭让等待继续。通知是广播且可重复 Release，不靠 sleep 猜请求到了哪里。
type Gate struct {
	Entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func NewGate() *Gate     { return &Gate{Entered: make(chan struct{}), release: make(chan struct{})} }
func (g *Gate) Release() { g.releaseOnce.Do(func() { close(g.release) }) }

type Repository struct {
	mu    sync.Mutex
	state string
	rows  []order.Record
	keys  map[string]order.Record
	gate  *Gate
}

// New 由 root 或有界反例调用；只分配普通内存，当前状态 new，尚未接受业务。
// gate 是可选实验屏障的共享指针，不创建后台 goroutine，也不拥有其关闭职责。
func New(gate *Gate) *Repository {
	return &Repository{state: "new", keys: make(map[string]order.Record), gate: gate}
}

// Start 是 root 在接线之前调用的显式生命周期动作。状态转移由同一锁保护。
// 它不像 Java Bean 的自动初始化回调；没有调用方主动调用，就一直是 new。
func (r *Repository) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == "closed" {
		return order.ErrClosed
	}
	r.state = "ready"
	return nil
}

// Ready 只给 root 做构造断言，不进入业务 Repository 接口。
func (r *Repository) Ready() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == "closed" {
		return order.ErrClosed
	}
	if r.state != "ready" {
		return order.ErrNotReady
	}
	return nil
}

// Commit 由 Service 的接口分派调用。输入值不别名内部 map/slice，结果也返回 Record 值。
// 真正的副作用是锁内 append+keys 写入；gate 只是让我们控制取消发生在这一步之前。
func (r *Repository) Commit(ctx context.Context, req order.Request) (order.Record, error) {
	if err := r.Ready(); err != nil {
		return order.Record{}, err
	}
	if r.gate != nil {
		r.gate.enterOnce.Do(func() { close(r.gate.Entered) })
		select {
		case <-r.gate.release: // 已释放时继续；与取消同时就绪时不保证 select 选哪条。
		case <-ctx.Done():
			return order.Record{}, ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// 再检查状态与 ctx：Ready 和写入之间可能被 Close/取消改变。
	// ctx 检查与外界 cancel 并非原子承诺；这里只证明已观察取消的屏障窗口不写入。
	if r.state == "closed" {
		return order.Record{}, order.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return order.Record{}, err
	}
	namespace := req.Owner + "|" + req.Key
	if previous, exists := r.keys[namespace]; exists {
		if previous.SKU != req.SKU || previous.Qty != req.Qty {
			return order.Record{}, order.ErrConflict
		}
		return previous, nil
	}
	record := order.Record{ID: len(r.rows) + 1, Owner: req.Owner, SKU: req.SKU, Qty: req.Qty}
	r.rows = append(r.rows, record)
	r.keys[namespace] = record
	return record, nil
}

// Close 由 root 在所有请求/Serve 完成后调用；幂等标记 closed，保留快照供事后验收。
// 没有磁盘连接需要关闭，但 closed 状态使错误的生命周期使用立即暴露。
func (r *Repository) Close() error  { r.mu.Lock(); defer r.mu.Unlock(); r.state = "closed"; return nil }
func (r *Repository) State() string { r.mu.Lock(); defer r.mu.Unlock(); return r.state }

// Snapshot 返回新的 slice 与 Record 值，测试/日志修改它不会改共享仓储。
func (r *Repository) Snapshot() []order.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]order.Record(nil), r.rows...)
}

// CloseWithError 是根清理错误测试用的独立包装原因；业务仓储本身不伪造 SQL 失败。
var ErrCleanup = errors.New("synthetic cleanup observation")
