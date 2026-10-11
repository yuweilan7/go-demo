// Package adapter 提供实际的文件订单仓储和内存日志；本课没有数据库替身冒充实库。
package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"go-demo/lessons/m04_interfaces_nil/internal/domain"
)

// Repository 保存文件地址。它是具体适配器，不要求使用者依赖这一类型。
// path 是私有字段；接口 Find 只暴露读取契约，不暴露文件实现细节。
type Repository struct {
	path string
}

// NewRepository 由组合入口调用，接收命令行提供的文件地址，返回具体类型。
// 它不打开文件：配置错误在这里拒绝，文件不存在/损坏属于 Find 的实际读取错误。
// 返回 *Repository 让入口还能使用具体类型的 Name，再把它交给业务小接口。
func NewRepository(path string) (*Repository, error) {
	if path == "" {
		return nil, fmt.Errorf("repository path is required")
	}
	return &Repository{path: path}, nil
}

// Name 是值接收者方法，由 embedding 实验观察同名选择器冲突。
// r 是 Repository 的值副本；本方法只读 path。它属于 Repository 和 *Repository 的方法集。
func (r Repository) Name() string {
	return "file:" + r.path
}

// Find 由 OrderService.Read 通过 OrderRepository 接口同步调用。
// ctx/id 来自本次请求；返回 Order 值副本或 error，上层负责决定如何展示错误。
// 指针接收者使 *Repository 实现读取接口，Repository 值不实现；不是调用时自动改接口规则。
func (r *Repository) Find(ctx context.Context, id string) (domain.Order, error) {
	// 本方法契约要求 r 可用。故意不把 nil r 当成“没有订单”：那是接线错误。
	// nil 仓储实验会在下方 r.path 解引用处 panic，由显式故障边界捕获；业务入口先校验指针。
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}
	// ReadFile 真正打开文件、读取字节，并在返回前关闭文件；本函数不持有可复用连接。
	// 它不是支持 context 的 I/O API，因此 ctx 取消不能中断已经开始的 ReadFile。
	// 数据集只有两条合成订单，无循环等待、网络调用和后台 goroutine。
	data, err := os.ReadFile(r.path)
	if err != nil {
		// %w 让调用者仍能用 errors.Is/As 判断原始文件错误；增加“哪一步失败”的上下文。
		return domain.Order{}, fmt.Errorf("read orders file: %w", err)
	}
	// 在 I/O 返回后再次观察取消，避免已取消请求继续解码/返回业务结果。
	// 这仍是协作检查，不能保证检查后的任何瞬间都不会再收到取消通知。
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}
	var orders []domain.Order
	if err := json.Unmarshal(data, &orders); err != nil {
		return domain.Order{}, fmt.Errorf("decode orders file: %w", err)
	}
	for _, order := range orders {
		// range 每轮给 order 一份元素值副本。返回它不会把内部切片交给 caller 修改。
		if order.ID == id {
			return order, nil // 成功边界直接返回 nil error，遵守业务接口契约。
		}
	}
	return domain.Order{}, &domain.DomainError{Code: "ORDER_NOT_FOUND", OrderID: id}
}

// Snapshot 是另一种真正可运行的读取策略：它拥有一份传入的订单值，不读文件。
// 它用于方法集/可替换依赖实验，不是用来证明数据库或文件 I/O 的替身。
type Snapshot struct {
	Order domain.Order
}

// Find 的值接收者 s 是副本，所以 Snapshot 与 *Snapshot 都满足读取接口。
// Service 使用者只要求同一契约；是否读文件由入口注入的具体实现决定。
func (s Snapshot) Find(ctx context.Context, id string) (domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}
	if id != s.Order.ID {
		return domain.Order{}, &domain.DomainError{Code: "ORDER_NOT_FOUND", OrderID: id}
	}
	return s.Order, nil
}
