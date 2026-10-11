// Package usecase 定义订单读取业务所需要的最小依赖；不导入 adapter。
package usecase

import (
	"context"
	"fmt"

	"go-demo/lessons/m04_interfaces_nil/internal/domain"
)

// OrderRepository 由使用者定义，只承诺按 ID 读取订单。
// Find 成功必须返回 nil error；未找到必须返回 ORDER_NOT_FOUND；取消必须保留 context 原因。
// 名字/方法签名匹配由编译器检查，以上业务含义还需要测试，接口声明本身证明不了。
type OrderRepository interface {
	Find(context.Context, string) (domain.Order, error)
}

// LogSink 只暴露本业务需要的写日志能力，不要求 Logger 的 Entries/Name 管理方法。
type LogSink interface {
	Log(string)
}

// OrderService 使用具名字段表达“拥有对依赖的引用”，不靠 embedding 暴露所有仓储方法。
// 把 *Repository 赋给 repo 时，只复制接口值中的指针，仍指向入口创建的仓储对象。
type OrderService struct {
	repo   OrderRepository
	logger LogSink
}

// NewOrderService 由组合入口及可替换实现实验调用，显式接收必需依赖并返回具体服务。
// 接口参数的前置契约是“可用的实现，禁止 typed nil”；两个 == nil 只能拒绝 nil 接口。
// app.Compose 在具体指针尚未转接口时验证；泛化到任意实现时，创建者同样必须保证契约。
func NewOrderService(repo OrderRepository, logger LogSink) (*OrderService, error) {
	if repo == nil {
		return nil, fmt.Errorf("order repository is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("log sink is required")
	}
	return &OrderService{repo: repo, logger: logger}, nil
}

// Read 由 cmd/demo 和测试调用，执行同步的“校验 → 仓储读取 → 记录结果”路径。
// id/ctx 是本次请求的值，不启动 goroutine，也不拥有仓储文件的生命周期。
func (s *OrderService) Read(ctx context.Context, id string) (domain.Order, error) {
	if err := domain.ValidateFixed(id); err != nil {
		// 请求数据错误在仓储调用之前返回；日志没有 begin，文件也没有开始读取。
		return domain.Order{}, err
	}
	s.logger.Log("order.read.begin id=" + id)
	order, err := s.repo.Find(ctx, id) // 接口分派到动态类型的 Find，当前 goroutine 等它返回。
	if err != nil {
		s.logger.Log("order.read.failed id=" + id)
		return domain.Order{}, fmt.Errorf("read order %q: %w", id, err)
	}
	s.logger.Log("order.read.success id=" + order.ID)
	return order, nil
}
