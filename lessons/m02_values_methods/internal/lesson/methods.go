package lesson

// StatusText 由 E02 的直接调用、方法值和方法表达式调用；返回接收者副本中的状态。
// (order Order) 是值接收者，相当于隐藏的第一个值参数；读取副本并不自动读取 caller 的最新字段。
// 本课故意与指针方法混用来对照；真实可变聚合通常统一使用指针接收者，见 README。
func (order Order) StatusText() string {
	return order.Status
}

// BuyerName 由 E02 的第二个方法值调用，展示“接收者副本”不等于不可变深快照。
// order.Buyer 是复制的地址值；别处修改同一 Buyer 时，这次读取仍能看到它的新 Name。
func (order Order) BuyerName() string {
	if order.Buyer == nil {
		return "<missing-buyer>"
	}
	return order.Buyer.Name
}

// SetStatusOnCopy 由 E02 传入新状态；修改值接收者副本，再把副本状态返回 caller。
// caller 忽略返回值时原订单不变；如果方法改 Lines/Labels/Buyer，副本仍可能改到共享对象。
func (order Order) SetStatusOnCopy(status string) string {
	order.Status = status
	return order.Status
}

// SetStatus 是实际写方法，由 E02 的可寻址变量、*Order 或 StatusSetter 接口调用。
// 指针接收者的 order 仍是地址值副本；字段写入落在共同目标 Order 上。
// 此 API 的业务契约要求非 nil；E06 用它展示没有 nil 守卫的风险。
func (order *Order) SetStatus(status string) {
	order.Status = status // nil 接收者能进入方法，但在这一行访问字段时会 panic。
}

// SafeStatus 由 E06 调用；允许 nil 代表未找到订单，因此在读取字段前显式分支。
// nil *Order 调用指针接收者方法本身是合法的，是否安全由方法体和其 API 契约决定。
func (order *Order) SafeStatus() string {
	if order == nil {
		return "<missing-order>"
	}
	return order.Status
}

// StatusSetter 表示上层需要的“写状态”能力。接口机制在后续课展开，这里只核对方法集边界。
type StatusSetter interface {
	SetStatus(string)
}

// 编译时断言：*Order 的方法集含 SetStatus；这里只检查类型，不会调用 nil 接收者。
// Order 的方法集没有指针接收者方法，自动取址是调用语法便利，不会让 Order 实现这个接口。
var _ StatusSetter = (*Order)(nil)

// var _ StatusSetter = Order{} // 编译错误：Order does not implement StatusSetter (method SetStatus has pointer receiver)。
