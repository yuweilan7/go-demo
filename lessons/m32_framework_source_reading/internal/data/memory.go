// Package data 是本课真实进程内库存仓储。网络协议是真实 TCP；它没有 SQL、事务或持久化承诺。
package data

import (
	"context"
	"go-demo/lessons/m32_framework_source_reading/internal/biz"
	"go-demo/lessons/m32_framework_source_reading/internal/trace"
	"sync"
)

type Memory struct {
	mu        sync.Mutex
	available int32
	writes    map[string]int
	closed    bool
	journal   *trace.Journal
}

// New 由 composition root 创建一份 ready 仓储，初始20与 M31 相同；所有 service 共享该指针。
func New(j *trace.Journal) *Memory {
	return &Memory{available: 20, writes: make(map[string]int), journal: j}
}

// Commit 是唯一副作用点，Usecase 同步调用。锁保护“查库存→检查→扣减→记录次数”的整体。
// honorContext=false 只供显式坏例；即使 true，锁内检查也不是“未来取消会撤销已经完成的扣减”。
func (r *Memory) Commit(ctx context.Context, req biz.Request, honorContext bool) (int32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, biz.ErrClosed
	}
	if honorContext && ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if req.SKU != biz.SKU {
		return 0, biz.ErrNotFound
	}
	if req.Quantity > r.available {
		return 0, &biz.Insufficient{SKU: req.SKU, Requested: req.Quantity, Available: r.available}
	}
	r.available -= req.Quantity
	r.writes[req.ID]++ // ID 没有唯一约束；再次发同 ID 就再次扣减，框架不会自动替业务幂等。
	r.journal.Add(req.ID, "repository.commit")
	return r.available, nil
}

// Inspect 被真实 RPC 的 Usecase 调用；取到锁后读出值副本，网络响应不会别名到内部库存。
func (r *Memory) Inspect(ctx context.Context, sku string) (int32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, biz.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if sku != biz.SKU {
		return 0, biz.ErrNotFound
	}
	return r.available, nil
}

// Snapshot 为实验提供独立副作用断言，返回可用库存/某 ID 写入次数/closed 的值副本。
func (r *Memory) Snapshot(id string) (int32, int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.available, r.writes[id], r.closed
}

// Close 只由 root 在实际 handler 都退出后调用；请求不能通过 biz.Repository 调这个生命周期方法。
func (r *Memory) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.journal.Add("root", "repository.closed")
	return nil
}
