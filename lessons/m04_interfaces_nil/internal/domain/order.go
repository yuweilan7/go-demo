// Package domain 保存订单的数据和错误，不依赖文件、日志或业务入口。
package domain

import "fmt"

// Order 是一次读取返回的数据值。字段都是值类型，没有指向可变订单对象的指针。
// JSON 标签只指定文件中的字段名；它不会让 Order 自动实现仓储接口。
type Order struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	AmountCents int    `json:"amount_cents"`
}

// DomainError 保存调用者需要分类的业务事实。error 接口只要求 Error() string，
// Code/OrderID 是具体类型额外提供的字段，使用 error 变量时不能直接 err.Code。
type DomainError struct {
	Code    string
	OrderID string
}

// Error 由 fmt 格式化错误或上层日志调用，返回给人看的文本，不拥有外部资源。
// 接收者 e 的类型是 *DomainError：它是指针副本，仍指向原来的错误对象。
func (e *DomainError) Error() string {
	// 特意让 nil 接收者打印为 <nil>，以复现“打印像成功，分支却认为失败”。
	// 方法可以接收 nil 指针；是否能安全执行由方法体决定。这一分支不能改变外层接口值。
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: order=%q", e.Code, e.OrderID)
}

// 编译期契约断言：*DomainError 的方法集包含 Error() string。
// _ 丢弃结果；(*DomainError)(nil) 只提供类型检查用的零指针，绝不调用 Error。
var _ error = (*DomainError)(nil)

// ValidateConcrete 由两个返回边界实验调用，参数来自订单读取请求。
// 它返回具体指针：合法 ID 得到真正的 nil *DomainError；空 ID 得到一个错误对象。
// 这个函数适合展示具体结果，实际业务边界使用下面的 ValidateFixed 返回 error。
func ValidateConcrete(id string) *DomainError {
	if id == "" {
		return &DomainError{Code: "ORDER_ID_REQUIRED", OrderID: id}
	}
	return nil
}

// ValidateBad 是故意写错的 error 返回边界，由 nil 实验调用，业务主路径不使用它。
// 即使 ValidateConcrete 成功，return 也会把 nil *DomainError 转为 error，保留动态类型。
func ValidateBad(id string) error {
	return ValidateConcrete(id) // 错误点：成功也得到 (*DomainError, nil)，err != nil。
}

// ValidateFixed 由 OrderService.Read 和修复实验调用，把具体错误转换为公共 error 契约。
// caller 只需检查 error，不必知道实现采用指针还是结构体。
func ValidateFixed(id string) error {
	// := 在 if 的初始化部分声明 problem；它只在这个 if 及其分支中可见。
	// 先在 *DomainError 类型上比较 nil，只有确实存在错误对象时才转换为 error。
	if problem := ValidateConcrete(id); problem != nil {
		return problem
	}
	// 无错误时直接返回未定类型的 nil，目标 error 得到没有动态类型的 nil 接口。
	return nil
}
