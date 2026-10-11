// Package course 把同一笔合成订单的错误，沿 repository → service → 边界传播。
// 它只使用标准库；DatabaseError 是课程定义的合成驱动错误，不连接真实数据库。
package course

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// 哨兵在包初始化时各创建一次。文案给人读，变量身份给 errors.Is 匹配。
// error 是内置接口：type error interface { Error() string }。
// 实现 Error 方法就满足接口；Go 没有 Java 的 implements 声明或 checked exception。
var (
	ErrNotFound       = errors.New("order resource not found")
	ErrConflict       = errors.New("order conflict")
	ErrDependency     = errors.New("order dependency failed")
	ErrOutcomeUnknown = errors.New("order outcome unknown")
)

// OperationError 由 service 创建，保存当前业务动作、订单键和下层原因。
// Cause 是一个接口值的副本；若动态值是 *DatabaseError，副本仍指向同一个对象。
// 本课创建后不修改错误对象，避免后来改字段让日志与处理结果不一致。
type OperationError struct {
	Operation string
	OrderID   string
	Cause     error
}

// *OperationError 的方法集满足 error；OperationError 值本身没有 Error 方法。
var _ error = (*OperationError)(nil)

// Error 被 fmt、日志和调用者调用，仅把结构化字段显示为可读文本。
// nil 接收者保护只让打印安全，不会把装有该指针的 error 接口变成 nil。
func (e *OperationError) Error() string {
	if e == nil {
		return "<nil OperationError>"
	}
	return fmt.Sprintf("%s order=%s: %v", e.Operation, e.OrderID, e.Cause)
}

// Unwrap 由 errors.Is/As/AsType 调用，暴露一个下层原因，连接错误链。
// 单独实现 Error() 只能显示原因的文案，不能建立标准库可遍历的关系。
func (e *OperationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// DatabaseError 是合成驱动返回值：Code 表示结构化原因，Phase 表示已知执行阶段。
// 这里的 code 是本课 fixture 契约，不能把它直接当成所有数据库的通用重试规则。
// 项目里要读取实际驱动的错误类型、数据库文档、事务阶段与幂等约定。
type DatabaseError struct {
	Code  string
	Phase string
	Cause error
}

var _ error = (*DatabaseError)(nil)

// Error 给边界日志展示数据库原因，但分类函数不解析这个字符串。
func (e *DatabaseError) Error() string {
	if e == nil {
		return "<nil DatabaseError>"
	}
	return fmt.Sprintf("database code=%s phase=%s: %v", e.Code, e.Phase, e.Cause)
}

// Unwrap 保留取消、超时、依赖失败或结果未知的语义标记。
// Cause 也可以是 errors.Join 形成的多分支树，外层仍然只持有一个 error。
func (e *DatabaseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ValueCodeError 故意使用值接收者：ValueCodeError 和 *ValueCodeError 都满足 error。
// 用它观察“实现同一个接口”不等于“T 与 *T 是同一个动态类型”。
// 正常业务错误统一采用指针接收者，如上面的 DatabaseError。
type ValueCodeError struct{ Code string }

// Error 接收的是值副本；这里仅读字符串，不修改调用者的对象。
func (e ValueCodeError) Error() string { return "fixture code=" + e.Code }

// RuleError 表示输入校验失败。typed nil 实验故意把 nil 指针放进 error 接口。
type RuleError struct{ Field string }

// Error 支持 nil 接收者便于观察接口；调用者仍要修复错误的返回方式。
func (e *RuleError) Error() string {
	if e == nil {
		return "<nil RuleError>"
	}
	return "invalid field=" + e.Field
}

// BadValidate 仅供标为反例的实验调用；它没有资源和副作用。
// 通过返回类型转换，err 从“具体指针 nil”变成“动态类型 *RuleError、动态值 nil”。
// error 接口只有类型和值都为空时才等于 nil，因此合法输入仍被误报失败。
func BadValidate(orderID string) error {
	var err *RuleError // 此处 err 的静态类型是 *RuleError，零值是 nil 指针。
	if orderID == "" {
		err = &RuleError{Field: "order_id"}
	}
	return err // 错误点：自动装入 error 接口，并没有返回空接口。
}

// Validate 被 CreateOrder 调用，明确约定：合法输入返回真正的 nil error。
// orderID 是字符串值副本；不负责资源，不发起异步工作。
func Validate(orderID string) error {
	if orderID == "" {
		return &RuleError{Field: "order_id"}
	}
	return nil // 直接返回无类型的 nil，使 error 的动态类型与动态值都为空。
}

// Receipt 仅在 CreateOrder 的 err==nil 时有效，这是本业务 API 的明确约定。
// 与 io.Reader 不同：本函数失败时返回零值 Receipt，不承诺部分订单结果。
type Receipt struct{ OrderID string }

// Scenario 在入口选定一个有界故障。每次调用只执行一个同步分支，没有重试。
type Scenario string

const (
	Normal        Scenario = "normal"
	MissingSKU    Scenario = "not-found"
	Duplicate     Scenario = "conflict"
	BeforeWrite   Scenario = "before-write"
	BadSchema     Scenario = "bad-schema"
	LostCommitAck Scenario = "lost-commit-ack"
)

// CreateOrder 是 usecase/service 层，由演示入口或测试同步调用。
// 它校验参数、调用 repository，再增加业务上下文；这里不逐层打印日志。
// ctx 来自边界并由边界拥有取消函数；本函数只观察，不替调用者 cancel。
func CreateOrder(ctx context.Context, orderID string, scenario Scenario) (Receipt, error) {
	if err := Validate(orderID); err != nil {
		// := 的 err 只在这个 if 语句范围可见；return 把错误交回入口。
		return Receipt{}, &OperationError{Operation: "validate", OrderID: orderID, Cause: err}
	}
	receipt, err := persistOrder(ctx, orderID, scenario)
	if err != nil {
		// 包装保留原始错误对象；上层可定位数据库 Code 或取消类别。
		return Receipt{}, &OperationError{Operation: "create", OrderID: orderID, Cause: err}
	}
	return receipt, nil
}

// persistOrder 是合成 repository：生成可检查的数据库原因，不伪装实库验收。
// caller 是 CreateOrder；返回值立即被它处理。真实 HTTP 副作用另见 unknown.go。
func persistOrder(ctx context.Context, orderID string, scenario Scenario) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		// 这里在任何 fixture 写入前观察取消/截止时间，故本分支已知没有写入。
		// 其他系统若先写再超时，就不能由 ctx.Err() 推导“没有副作用”。
		return Receipt{}, fmt.Errorf("repository preflight: %w", err)
	}
	switch scenario {
	case Normal:
		return Receipt{OrderID: orderID}, nil
	case MissingSKU:
		return Receipt{}, fmt.Errorf("lookup sku for order %s: %w", orderID, ErrNotFound)
	case Duplicate:
		return Receipt{}, fmt.Errorf("unique order key %s: %w", orderID, ErrConflict)
	case BeforeWrite:
		// fixture 明确保证写入尚未开始。只是“可以考虑重试”，并不启动重试。
		return Receipt{}, &DatabaseError{Code: "40001", Phase: "before-write", Cause: ErrDependency}
	case BadSchema:
		// 同样属于依赖失败，却是修配置/SQL 才能解决的稳定故障。
		return Receipt{}, &DatabaseError{Code: "SCHEMA", Phase: "before-write", Cause: ErrDependency}
	case LostCommitAck:
		// 这里表示 Commit 已被发出但确认丢失：可能提交，也可能未提交。
		// Join 同时保留“未知结果”与“通信结束”两个事实，不能只看后者重放订单。
		return Receipt{}, &DatabaseError{
			Code: "ACK_LOST", Phase: "commit-ack",
			Cause: errors.Join(ErrOutcomeUnknown, io.ErrUnexpectedEOF),
		}
	default:
		return Receipt{}, fmt.Errorf("unsupported fixture scenario %q", scenario)
	}
}
