package course

import (
	"context"
	"errors"
)

// Decision 是边界拥有的处理策略，并不是 error 接口自带的重试指令。
// HTTP/RPC/Message 只演示协议结果映射；本课没有启动真实 RPC 或消息 broker。
// Retry 说明下一步许可；即使是 may-retry，也必须由 M33 的预算、取消和幂等策略控制。
type Decision struct {
	Kind    string
	HTTP    int
	RPC     string
	Message string
	Retry   string
}

// Classify 由 HTTP/RPC/消费者边界调用，按当前业务上下文决定响应。
// 判定的是错误树中的语义，不是 Error() 文案。没有日志，也没有重复调用。
// 多原因优先级是本课程策略：未知结果优先于取消/超时，再到业务拒绝和依赖失败。
// Join 并不自动定义“主错误”；加入 Close 错误后必须仍以主操作的事实决定重试。
func Classify(err error) Decision {
	if err == nil {
		return Decision{"ok", 201, "OK", "ack", "not-needed"}
	}
	if errors.Is(err, ErrOutcomeUnknown) {
		// 响应丢失时 503 只说明调用不能确认完成，绝不授权无条件重放。
		// 消费者先停在待核实状态；实际 ack/重投需要业务幂等协议。
		return Decision{"outcome-unknown", 503, "UNAVAILABLE", "reconcile-before-ack", "reconcile"}
	}
	if errors.Is(err, context.Canceled) {
		// 499 是本课采用的常见网关约定，不是 HTTP 标准状态码。
		// 客户端已断开时可能根本无法写响应，取消也不保证副作用回滚。
		return Decision{"canceled", 499, "CANCELLED", "leave-unacked-on-shutdown", "do-not-retry"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Decision{"deadline", 504, "DEADLINE_EXCEEDED", "leave-unacked-until-policy", "do-not-retry"}
	}
	if errors.Is(err, ErrNotFound) {
		return Decision{"not-found", 404, "NOT_FOUND", "ack-with-business-rejection", "do-not-retry"}
	}
	if errors.Is(err, ErrConflict) {
		return Decision{"conflict", 409, "ALREADY_EXISTS", "ack-with-business-rejection", "do-not-retry"}
	}
	if errors.Is(err, ErrDependency) {
		// AsType 的 [*DatabaseError] 是类型参数，返回匹配到的指针和 bool。
		// ok 为 true 才能读取字段；dbErr!=nil 防止外部错误树里藏有 typed nil。
		if dbErr, ok := errors.AsType[*DatabaseError](err); ok && dbErr != nil &&
			dbErr.Code == "40001" && dbErr.Phase == "before-write" {
			return Decision{"dependency", 503, "UNAVAILABLE", "bounded-requeue-if-idempotent", "may-retry"}
		}
		// SCHEMA 等稳定故障会持续失败，不因属于依赖类就自动重复执行。
		return Decision{"dependency", 503, "UNAVAILABLE", "park-for-operator", "do-not-retry"}
	}
	return Decision{"internal", 500, "INTERNAL", "park-for-operator", "do-not-retry"}
}
