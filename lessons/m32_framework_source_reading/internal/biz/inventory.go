// Package biz 定义库存语义和使用者需要的仓储接口；不依赖 Kratos、protobuf 或 data 包。
package biz

import (
	"context"
	"errors"
	"fmt"
	"go-demo/lessons/m32_framework_source_reading/internal/trace"
	"sync"
	"time"
)

const SKU = "001-SKU"
const (
	Normal int32 = iota
	DropContext
	PropagateContext
	CommitBeforeResponse
	PanicBeforeCommit
	PanicAfterCommit
)

var ErrNotFound = errors.New("SKU 不存在")
var ErrInvalid = errors.New("request_id/sku 必填且 quantity 必须为正")
var ErrClosed = errors.New("repository 已关闭")

// Insufficient 是业务事实，Requested/Available 由锁内库存检查填写；service 再映射到协议错误。
type Insufficient struct {
	SKU                  string
	Requested, Available int32
}

func (e *Insufficient) Error() string { return "库存不足" }

// Request 只有值字段，传值复制后没有 map/slice 别名；ID 只追踪请求，不提供幂等。
type Request struct {
	ID, SKU        string
	Quantity, Mode int32
}

// Repository 由使用者定义小接口。Root 拥有 Close，业务层只有 Commit/Inspect 能力，不能关全局对象。
type Repository interface {
	Commit(context.Context, Request, bool) (int32, error)
	Inspect(context.Context, string) (int32, error)
}

// Plan 是一个请求的屏障：实验 owner 关闭 Gate，业务 goroutine 关闭 Started/Committed/Done。
// Done 是 Reserve 最后执行的 defer，只证明业务函数清理结束；外层 middleware/transport 仍须另行 join。
type Plan struct {
	Gate, Started, Committed, Done chan struct{}
	release                        sync.Once
}

// NewPlan由Install调用，创建四个不同状态信号；容量0不是结果缓冲，close以后任意观察者都能收到通知。
func NewPlan() *Plan {
	return &Plan{Gate: make(chan struct{}), Started: make(chan struct{}), Committed: make(chan struct{}), Done: make(chan struct{})}
}

// Release 正常和提前失败路径均可调用；Once 防止两个收尾分支重复 close 同一 channel。
func (p *Plan) Release() { p.release.Do(func() { close(p.Gate) }) }

type tenantKey struct{}

// WithTenant 由认证中间件调用；context 保存本次请求的可信字符串值，不持有仓储或框架池化对象。
func WithTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantKey{}, tenant)
}
func Tenant(ctx context.Context) string { value, _ := ctx.Value(tenantKey{}).(string); return value }

type Usecase struct {
	repo    Repository
	journal *trace.Journal
	mu      sync.Mutex
	plans   map[string]*Plan
}

// New 保存接口的动态指针，接口复制不会复制仓储；具体指针 nil/ready 检查必须在 root 转接口前完成。
func New(repo Repository, j *trace.Journal) *Usecase {
	return &Usecase{repo: repo, journal: j, plans: make(map[string]*Plan)}
}

// Install 在发请求前建立计划，返回给实验 owner；方法本身不运行后台任务。
func (u *Usecase) Install(id string) *Plan {
	u.mu.Lock()
	defer u.mu.Unlock()
	p := NewPlan()
	u.plans[id] = p
	return p
}

// plan由请求goroutine同步查共享map；取出的是计划指针的副本，解锁后用channel同步，字段不再更换。
func (u *Usecase) plan(id string) *Plan { u.mu.Lock(); defer u.mu.Unlock(); return u.plans[id] }

// Remove 在 plan.Done 之后由 owner 调用，允许用相同 ID 发第二次普通请求；保留仓储写入事实。
func (u *Usecase) Remove(id string) { u.mu.Lock(); defer u.mu.Unlock(); delete(u.plans, id) }

// ReleaseAll 是 root 提前失败的保险：复制共享计划指针后释放锁，关闭各 Gate，让坏例也能有限结束。
func (u *Usecase) ReleaseAll() {
	u.mu.Lock()
	plans := make([]*Plan, 0, len(u.plans))
	for _, p := range u.plans {
		plans = append(plans, p)
	}
	u.mu.Unlock()
	for _, p := range plans {
		p.Release()
	}
}

// wait 是 Reserve 同 goroutine 内的真实业务等待点，不是另一个 worker。
// 选择 ctx.Done 只发回取消原因；已经提交的库存不会因这个返回值而回滚。
func wait(ctx context.Context, p *Plan) error {
	select {
	case <-p.Gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reserve 由 service.Reserve 同步调用。请求参数来自解码后的 protobuf 的值副本，ctx 来自 transport。
// Usecase 借用共享 repo；所有库存副作用只发生在 repo.Commit，调用方必须观察返回原因和仓储事实。
func (u *Usecase) Reserve(ctx context.Context, req Request) (remaining int32, err error) {
	p := u.plan(req.ID)
	if p != nil {
		// 最先登记的 defer 最后执行。下面各分支的 cancel/日志清理完成后才发布唯一 Done。
		defer close(p.Done)
		defer u.journal.Add(req.ID, "usecase.cleanup")
		close(p.Started)
	}
	u.journal.Add(req.ID, "usecase.enter tenant="+Tenant(ctx))
	if req.ID == "" || req.SKU == "" || req.Quantity <= 0 || req.Mode < Normal || req.Mode > PanicAfterCommit {
		return 0, ErrInvalid
	}
	if req.Mode == PanicBeforeCommit {
		panic("m32: panic before commit")
	}
	if req.Mode != Normal && req.Mode != PanicBeforeCommit && req.Mode != PanicAfterCommit && p == nil {
		return 0, ErrInvalid
	}
	commitCtx := ctx
	honorContext := true
	if req.Mode == DropContext {
		// 可复现坏例：新 Background 根丢失客户端 deadline、cancel 和 tenant。2 秒保险仅限制坏例寿命。
		var cancel context.CancelFunc
		commitCtx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		honorContext = false
		u.journal.Add(req.ID, "wait.lost-context tenant="+Tenant(commitCtx))
		if err = wait(commitCtx, p); err != nil {
			return 0, fmt.Errorf("lost wait: %w", err)
		}
	} else if req.Mode == PropagateContext {
		u.journal.Add(req.ID, "wait.request-context")
		if err = wait(ctx, p); err != nil {
			u.journal.Add(req.ID, "cancel.before-commit")
			return 0, fmt.Errorf("request wait: %w", err)
		}
	}
	remaining, err = u.repo.Commit(commitCtx, req, honorContext)
	if err != nil {
		return 0, fmt.Errorf("reserve repository: %w", err)
	}
	if p != nil {
		close(p.Committed)
	}
	if req.Mode == PanicAfterCommit {
		panic("m32: panic after commit")
	}
	if req.Mode == CommitBeforeResponse {
		u.journal.Add(req.ID, "wait.after-commit")
		if err = wait(ctx, p); err != nil {
			u.journal.Add(req.ID, "cancel.after-commit")
			return 0, fmt.Errorf("response wait: %w", err)
		}
	}
	return remaining, nil
}

// Inspect 借用同一仓储，caller 用新请求 ctx 查询超时之后的库存，不能用旧错误反推副作用。
func (u *Usecase) Inspect(ctx context.Context, sku string) (int32, error) {
	return u.repo.Inspect(ctx, sku)
}
