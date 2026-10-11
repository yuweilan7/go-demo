// Package lesson 把订单中的值复制、内部共享、接收者与锁身份放在同一组实验中。
// 包内没有数据库、网络连接或后台任务；订单数据只存在于本次进程内。
package lesson

// Line 表示一个商品行。SKU 和 Qty 都可以作为独立值复制；目前没有可变引用字段。
// 如果以后增加 []byte、map 或指针，CloneDTO 对 Line 的复制策略也要重新检查。
type Line struct {
	SKU string
	Qty int
}

// Buyer 的实例由 NewOrder 创建；Order.Buyer 保存它的地址。
// 复制 Order.Buyer 会产生另一个指针值，两者仍然指向同一个 Buyer。
type Buyer struct {
	Name string
}

// Order 是本课共同的合成业务数据。它本身是一个 struct 值，不是隐含的 Java 对象引用。
// Status 的赋值只影响所在 struct；Lines、Labels、Buyer 的复制不复制它们指向的数据。
// Lines 的值描述一段底层数组，Labels 的值关联同一份 map 数据，Buyer 的值是地址。
// 字段名以大写字母开头，其他包能访问；Counter 中小写字段则只允许 lesson 包访问。
type Order struct {
	ID     string
	Status string
	Lines  []Line
	Labels map[string]string
	Buyer  *Buyer
}

// NewOrder 由各实验和测试调用，给每个对照分支创建独立的初始订单。
// 返回 Order 值，但 slice 底层数组、map 数据和 Buyer 都属于这次创建；不复用全局样本。
func NewOrder() Order {
	// Order{字段名: 值} 是结构体字面量，按字段初始化一个值，不是调用隐含构造函数。
	// []Line{{...}} 是有一个 Line 元素的 slice 字面量，内层元素省略了重复的 Line 类型名。
	// map 字面量在这里创建可写的标签集合；&Buyer{...} 创建 Buyer 值并取得其地址。
	return Order{
		ID:     "ORDER-1001",
		Status: "created",
		Lines:  []Line{{SKU: "SKU-A", Qty: 1}},
		Labels: map[string]string{"review": "pending"},
		Buyer:  &Buyer{Name: "Alice"},
	}
}

// ModifyValue 是 E01 的第一个修改函数，由 runCore 和状态测试传入 NewOrder 的完整值。
// 参数 order 是调用方 struct 的副本；返回值让 caller 看到副本中的 Status 已经改成 paid。
// 输入契约：Lines 至少一行，Labels 已初始化，Buyer 非 nil；本课样本均满足该契约。
// 返回值不是深克隆，它与 caller 仍共享 Lines 的底层数组、map 数据和 Buyer。
func ModifyValue(order Order) Order {
	order.Status = "paid"         // 改的是参数副本内部的 string 字段，caller 的 Status 仍是 created。
	order.Lines[0].Qty = 2        // 通过复制来的 slice 值选中同一底层数组元素，caller 会看到 Qty=2。
	order.Labels["review"] = "ok" // 修改同一份 map 数据；没有给 caller 的 Labels 字段重新赋值。
	order.Buyer.Name = "Bob"      // order.Buyer 与 caller.Buyer 地址相同，点号等价于 (*order.Buyer).Name。
	return order
}

// ModifyPointer 是 E01 的第二个修改函数，caller 把自己订单的地址传入这里。
// 参数仍然传值：这里只复制一个 *Order 地址值，没有复制那份 Order，也没有传入 caller 变量本身。
// nil 表示没有目标订单，返回 false 让 caller 决定是否报业务错误，这个分支不会解引用 nil。
// 非 nil 订单仍须满足 ModifyValue 的输入契约；缺商品行/未初始化 map/缺 Buyer 都可能 panic。
func ModifyPointer(order *Order) bool {
	if order == nil {
		return false
	}
	order.Status = "shipped" // 自动解引用指针并修改目标字段，与 (*order).Status = "shipped" 等效。
	order.Lines[0].Qty = 3
	order.Labels["review"] = "sent"
	order.Buyer.Name = "Carol"
	return true
}

// RebindPointer 是 E01 的第三个修改函数：展示“指针参数也是自己的变量”。
// caller 的 pointer 和这里的 order 是不同变量，最初保存同一地址。
// 给 order 重新赋地址只改本函数的参数变量；返回新地址后，只有 caller 接收并赋值才会换目标。
func RebindPointer(order *Order) *Order {
	// 最后的 new(replacement) 是 Go 1.26 起允许的 new(表达式)；得到一个新的 *Order。
	// 新对象是完整 NewOrder 的副本，内部对象属于这次 NewOrder 调用，不共享旧订单的数据。
	replacement := NewOrder()
	replacement.ID = "ORDER-REPLACEMENT"
	replacement.Status = "replacement"
	order = new(replacement)
	return order
}

// ShallowClone 是 E04 的错误隔离策略，由 caller 传入待发给下游的 DTO。
// 它只返回 Order 的字段副本，方法名不能改变 Go 的赋值语义：可变内部数据依然共享。
func ShallowClone(order Order) Order {
	return order
}

// CloneDTO 是 E04 的修复版本：复制本课 DTO 中全部可变内部对象，返回隔离后的快照。
// 它由 runClone/测试调用，原始订单仍归 caller 所有；这里不加锁，不承诺并发读取原对象安全。
// 输入必须在复制期间保持稳定，例如在持有原对象锁时调用，或由单个 goroutine 独占。
// 这不是通用深克隆：只覆盖当前 Line、Buyer 和 string map 的字段，不处理任意图/循环引用。
func CloneDTO(order Order) Order {
	clone := order // := 在本函数块声明 clone；先复制顶层字段，再替换所有需要隔离的字段。
	if order.Lines != nil {
		// make 返回 []Line 值，并为它创建新的底层数组；copy 逐个复制 Line 值。
		// 保留 nil 与非 nil 空 slice 的区别；Line 当前的字段复制后都可独立修改。
		clone.Lines = make([]Line, len(order.Lines))
		copy(clone.Lines, order.Lines)
	}
	if order.Labels != nil {
		clone.Labels = make(map[string]string, len(order.Labels))
		// range 的 key/value 是当前键值；string 内容不可原地修改，逐项赋值足以隔离 map 写入。
		for key, value := range order.Labels {
			clone.Labels[key] = value
		}
	}
	if order.Buyer != nil {
		buyerCopy := *order.Buyer // 解引用得到 Buyer 值并复制，随后取得副本的地址。
		clone.Buyer = &buyerCopy  // 返回局部变量地址安全，存储必须活到 caller 不再需要它时。
	}
	return clone
}
