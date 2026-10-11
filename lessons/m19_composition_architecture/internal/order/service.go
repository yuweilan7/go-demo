// Package order 定义业务输入、错误与使用者需要的一种仓储能力；不依赖 Gin 或 memory。
package order

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrInvalid = errors.New("order input invalid")
var ErrConflict = errors.New("idempotency content conflict")
var ErrNotReady = errors.New("repository is not ready")
var ErrClosed = errors.New("repository is closed")

// Request 是一次请求专属的值，身份来自认证层；不持有 *gin.Context 或 body。
// SKU/Qty 沿用 M18 的 sku-m17 和 1..3 契约；Owner+Key 定义本进程幂等命名空间。
type Request struct {
	Owner string
	Key   string
	SKU   string `json:"sku"`
	Qty   int    `json:"qty"`
}
type Record struct {
	ID    int    `json:"order_id"`
	Owner string `json:"owner"`
	SKU   string `json:"sku"`
	Qty   int    `json:"qty"`
}

// Repository 位于使用者一侧，只承诺业务 Commit；它不暴露 Start/Close 这类 root 职责。
// Commit 必须原子地查同身份/key、检查内容冲突、写入，并保留 ctx 错误原因。
// 接口只验证签名，语义由 HTTP 实验与仓储事实断言验证。
type Repository interface {
	Commit(context.Context, Request) (Record, error)
}

type Service struct{ repo Repository }

// NewService 由 app.WireService 调用，保存接口值并返回具体 *Service。
// 它只能拒绝 nil 接口。参数契约要求可用实现：(*memory.Repository)(nil) 转接口后 != nil，
// 所以 root 必须在具体指针转接口前检查；这是复用 M04 的修复边界，而非遍地反射 nil。
func NewService(repo Repository) (*Service, error) {
	if repo == nil {
		return nil, errors.New("repository interface is required")
	}
	return &Service{repo: repo}, nil
}

// Create 由 HTTP handler 同步调用；ctx 是当前请求预算，req 是局部值副本。
// Service 借用仓储，绝不 Close 它；同一个 Service 指针可被多个请求并发使用。
func (s *Service) Create(ctx context.Context, req Request) (Record, error) {
	if strings.TrimSpace(req.Owner) == "" || strings.TrimSpace(req.Key) == "" || len(req.Key) > 64 || req.SKU != "sku-m17" || req.Qty < 1 || req.Qty > 3 {
		return Record{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Record{}, fmt.Errorf("create before commit: %w", err)
	}
	record, err := s.repo.Commit(ctx, req) // 当前 goroutine 在此等接口动态对象的 Commit 返回。
	if err != nil {
		return Record{}, fmt.Errorf("create order: %w", err)
	}
	return record, nil
}
