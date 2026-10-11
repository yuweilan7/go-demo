package order

import "fmt"

// Validate 由 service 的正确入口和错误入口调用，按顺序验证出库前的订单副本。
// checks 来自调用方；...Policy 在函数内部是 []Policy，可传零个、一个或多个检查。
// 本函数没有连接或 goroutine，唯一结果是 error；nil 表示本函数的所有检查均通过。
func Validate(o Order, checks ...Policy) error {
	// if 前的初始化语句只执行一次。length 只在这个 if/else 的隐含作用域内可读。
	// 条件是 bool，Go 不把空字符串直接当 false。len(string) 数的是字节，本例 ID 为 ASCII。
	if length := len(o.ID); length == 0 {
		return fmt.Errorf("订单 ID 为空，字节数=%d", length)
	}
	if o.Quantity == 0 {
		return fmt.Errorf("订单 %s 的数量必须在 [1,%d]", o.ID, MaxQuantity)
	}
	// || 从左向右短路；负金额或过大的单价都在计算总价之前拒绝。
	// 输入宽度和业务范围是两层边界：即使 int64 放得下，业务也不允许无限大的金额。
	if o.UnitCents < 0 || o.UnitCents > MaxUnitCents {
		return fmt.Errorf("订单 %s 的单价必须在 [0,%d] 分", o.ID, MaxUnitCents)
	}

	// switch 匹配后默认结束该 case，不需 Java 传统 switch 中的 break。
	// 没写 fallthrough，Paid 不会继续执行下一个 case；default 同样必须处理 Status(99)。
	switch o.Status {
	case Paid:
		// 只有已支付订单可出库，继续执行下面的额外策略。
	case Created, Unknown:
		return fmt.Errorf("订单 %s 尚未支付：status=%s", o.ID, o.Status)
	case Shipped, Cancelled:
		return fmt.Errorf("订单 %s 不能重复或撤销后出库：status=%s", o.ID, o.Status)
	default:
		return fmt.Errorf("订单 %s 的状态码不受支持：status=%s", o.ID, o.Status)
	}

	// range 这里产生索引与 Policy 函数值的副本；不修改 checks 的元素。
	// Go 1.27 模块使用 Go 1.22+ 的逐轮声明语义；这段代码也没有异步闭包捕获。
	for index, check := range checks {
		// nil 函数值不能调用。先检查它，把编程配置错误变为明确 error，而不是 panic。
		if check == nil {
			return fmt.Errorf("订单 %s 的策略[%d] 为 nil", o.ID, index)
		}
		// err 是这个 if 初始化语句声明的新变量，不属于外层 service 的 err。
		// check(o) 当场同步执行；一旦失败，return 会把错误交给 caller，后续策略不会执行。
		if err := check(o); err != nil {
			return fmt.Errorf("订单 %s 的策略[%d]：%w", o.ID, index, err)
		}
	}
	return nil
}

// TotalCents 由 service 在 Validate 成功后调用，把“单价分 × 件数”交回业务层。
// uint8 与 int64 不会自动混合运算，int64(o.Quantity) 明确转换后才能相乘。
// 前置边界保证结果最多为 255_000_000_000，远小于 int64 上限；本函数不单独宣称校验成功。
func TotalCents(o Order) int64 {
	return o.UnitCents * int64(o.Quantity)
}
