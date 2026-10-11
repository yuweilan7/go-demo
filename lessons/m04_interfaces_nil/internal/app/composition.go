// Package app 是本课唯一的实际接线位置：把具体适配器转换为业务需要的小接口。
package app

import (
	"fmt"

	"go-demo/lessons/m04_interfaces_nil/internal/adapter"
	"go-demo/lessons/m04_interfaces_nil/internal/usecase"
)

// 接口契约断言放在同时知道使用者和适配器的组合层。
// adapter 无需导入 usecase，也无需写 implements；以后改坏签名会在这里编译失败。
var (
	_ usecase.OrderRepository = (*adapter.Repository)(nil)
	_ usecase.OrderRepository = adapter.Snapshot{}
	_ usecase.OrderRepository = (*adapter.Snapshot)(nil)
	_ usecase.LogSink         = (*adapter.Logger)(nil)
)

// Compose 由实际文件实验调用，参数是入口构造的具体指针，结果交回入口发起读取。
// 本函数只接线，不寻找全局 Bean、不启动服务、不创建资源；依赖归属从参数可直接追踪。
func Compose(repo *adapter.Repository, logger *adapter.Logger) (*usecase.OrderService, error) {
	// 还没转成接口时，repo == nil 就能正确检查 nil 仓储指针。
	// 这是本课的修复边界；不是给所有接口加反射 isNil，也不是把接线错误当订单未找到。
	if repo == nil {
		return nil, fmt.Errorf("compose: repository pointer is nil")
	}
	if logger == nil {
		return nil, fmt.Errorf("compose: logger pointer is nil")
	}
	return usecase.NewOrderService(repo, logger)
}
