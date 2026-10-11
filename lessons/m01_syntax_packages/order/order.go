// Package order 放订单事实与校验规则。它不依赖命令入口或 service，导入方向不会成环。
package order

import "fmt"

// Status 是底层为 uint8 的命名类型。大写名称允许其他包写 order.Status。
// 它没有 Java enum 的封闭取值集合；Status(99) 合法，Validate 必须拒绝未知业务值。
type Status uint8

const (
	// iota 是当前 const 块中“常量声明项”的索引，从 0 起；不是物理行号。
	// 后续省略表达式的项复用 Status = iota 模板，分别得到 1、2、3、4。
	Unknown Status = iota
	Created
	Paid
	Shipped
	Cancelled
)

// 常量还未落入变量时可以保持无类型；本课按业务边界固定单位分与数量范围。
// MaxQuantity 可以被 int64 比较，也可以转换为 uint8，因为 255 可由目标类型表示。
const MaxQuantity = 255
const MaxUnitCents int64 = 1_000_000_000

// Order 是入口解析得到的一份订单事实。字段也要大写，其他包才能构造和读取。
// 本课字段全是值，不含指针、slice 或 map；传入 Validate 时是这组字段的一份副本。
type Order struct {
	ID        string
	Status    Status
	Quantity  uint8
	UnitCents int64
}

// Policy 表示“接收订单副本，返回校验错误”的函数类型。
// cmd/demo 把一个函数值交给 service，service 再交给 Validate；调用是同步的，没有 goroutine。
type Policy func(Order) error

// String 由 fmt 打印 Status 时调用，接收者 s 是状态值的副本，方法不会修改订单。
// M01 只需读懂 (s Status) 的签名；指针接收者与接口方法集合交给 M02/M04。
func (s Status) String() string {
	switch s {
	case Unknown:
		return "unknown"
	case Created:
		return "created"
	case Paid:
		return "paid"
	case Shipped:
		return "shipped"
	case Cancelled:
		return "cancelled"
	default:
		return fmt.Sprintf("Status(%d)", uint8(s))
	}
}
