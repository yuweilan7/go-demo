package lab

import (
	"fmt"
	"io"
	"slices"

	"go-demo/lessons/m04_interfaces_nil/internal/adapter"
	"go-demo/lessons/m04_interfaces_nil/internal/domain"
	"go-demo/lessons/m04_interfaces_nil/internal/usecase"
)

// AssertOrder 由 any 实验和测试调用，试着从已失去静态订单信息的接口中取回订单值。
// comma-ok 失败返回 Order 的零值与 false；调用者检查 ok 后才使用 AmountCents。
func AssertOrder(value any) (domain.Order, bool) {
	order, ok := value.(domain.Order)
	return order, ok
}

// MustOrder 是显式反例，只有故障实验把它交给 ObservePanic。
// 单返回值断言只有在 caller 已有可靠类型保证时才合理；陌生输入不应在这里猜类型。
func MustOrder(value any) domain.Order {
	return value.(domain.Order) // value 动态类型为 *Order 时，不会自动解引用成 Order，会 panic。
}

// Describe 由 demo/测试调用，通过 type switch 为一组可能的动态类型选择读取方式。
// 每个单类型 case 内 current 的静态类型就是该类型；没有任何 case 能把 typed nil 变成 nil 接口。
func Describe(value any) string {
	switch current := value.(type) {
	case nil:
		return "nil interface"
	case domain.Order:
		return "Order value: " + current.ID
	case *domain.Order:
		// 该 case 包含 typed nil *Order；匹配成功后仍必须按当前具体指针检查 nil。
		if current == nil {
			return "typed nil *Order"
		}
		return "*Order pointer: " + current.ID
	case usecase.OrderRepository:
		// 检查动态类型是否满足接口，不要求它“动态类型正好是 OrderRepository”。
		// 只描述类型，不调用 Find：一个接口 case 匹配成功仍不能证明实现的内部状态可用。
		return fmt.Sprintf("OrderRepository implementation: %T", current)
	default:
		return fmt.Sprintf("unsupported: %T", current)
	}
}

// EqualAny 是显式危险比较，参数静态类型可比较不代表动态值总能比较。
// 两个动态类型相同且不可比较的接口值（本课为 []string）执行 == 会 panic。
func EqualAny(left, right any) bool {
	return left == right
}

// EqualOrderIDs 是同一业务意图的修复：参数保留订单 ID 列表类型，按元素判断业务相等。
// slices.Equal 用 string 的 == 比较元素，nil 与空列表在这个契约里都代表“零个订单”。
// 这里选择列表语义，不通过反射试图给任意对象定义同一种相等关系。
func EqualOrderIDs(left, right []string) bool {
	return slices.Equal(left, right)
}

// RunAny 由 demo 的 any/all 场景调用，复用同一订单数据，比较 any 的信息损失与修复。
// out 由入口拥有；这里只输出观察，不把 any 带入订单业务核心的依赖字段。
func RunAny(out io.Writer, repo *adapter.Repository) {
	fmt.Fprintln(out, "\n[E04] any、comma-ok、type switch 与不可比较动态值")
	order := domain.Order{ID: "ORDER-1001", Status: "PAID", AmountCents: 12900}
	boxed := any(order) // any 是 interface{} 的别名；any(order) 是类型转换，不是函数。
	actual, ok := AssertOrder(boxed)
	fmt.Fprintf(out, "Order assertion: ok=%t, amount_cents=%d\n", ok, actual.AmountCents)
	actual, ok = AssertOrder(any(&order))
	fmt.Fprintf(out, "*Order -> Order assertion: ok=%t, zero-result=%t\n", ok, actual == (domain.Order{}))
	fault := ObservePanic(func() { _ = MustOrder(&order) })
	fmt.Fprintf(out, "isolated single-result assertion: panicked=%t\n", fault != "")
	var absent *domain.Order
	for _, input := range []any{nil, order, &order, absent, repo, 1001} {
		fmt.Fprintln(out, "type switch:", Describe(input))
	}
	left, right := any([]string{order.ID}), any([]string{order.ID})
	// 与 nil 接口比较是安全的：nil 接口没有动态类型，不进入两个相同 slice 类型的值比较。
	fmt.Fprintf(out, "slice boxed in any: !=nil=%t\n", left != nil)
	fault = ObservePanic(func() { _ = EqualAny(left, right) })
	fmt.Fprintf(out, "isolated interface ==: panicked=%t; typed-list comparison=%t\n",
		fault != "", EqualOrderIDs([]string{order.ID}, []string{order.ID}))
	// 若需要单个订单去重，比较 order.ID 等可比较业务键，比比较整个 any 对象明确。
}
