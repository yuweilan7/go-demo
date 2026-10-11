package lesson_test

import (
	"io"
	"testing"

	"go-demo/lessons/m02_values_methods/internal/lesson"
)

// TestValuePointerAndRebind 由 go test 调用，断言 caller 的业务事实，不解析演示日志当正确性证据。
// 本测试创建自己的初始订单，同组的三个函数按预期状态串行对照；不共享其他测试的数据。
func TestValuePointerAndRebind(t *testing.T) {
	original := lesson.NewOrder()
	copy := lesson.ModifyValue(original)
	if original.Status != "created" || copy.Status != "paid" {
		t.Fatalf("顶层状态复制失败: caller=%s copy=%s", original.Status, copy.Status)
	}
	if original.Lines[0].Qty != 2 || original.Labels["review"] != "ok" || original.Buyer.Name != "Bob" {
		t.Fatalf("内部共享的副作用缺失: %+v", original)
	}
	if &original.Lines[0] != &copy.Lines[0] || original.Buyer != copy.Buyer {
		t.Fatal("预期同一底层数组元素和 Buyer 地址")
	}
	if !lesson.ModifyPointer(&original) || original.Status != "shipped" || original.Lines[0].Qty != 3 || original.Labels["review"] != "sent" || original.Buyer.Name != "Carol" {
		t.Fatal("指针参数没有修改共同目标")
	}
	if lesson.ModifyPointer(nil) {
		t.Fatal("nil 分支必须拒绝不存在的目标")
	}
	pointer := &original
	replacement := lesson.RebindPointer(pointer)
	if pointer != &original || pointer.ID != "ORDER-1001" || replacement == pointer || replacement.ID != "ORDER-REPLACEMENT" {
		t.Fatal("callee 重新绑定地址不应替 caller 改指针变量")
	}
	pointer = replacement
	if pointer.ID != "ORDER-REPLACEMENT" || original.Status != "shipped" {
		t.Fatal("caller 应能显式换目标，旧订单应保留其状态")
	}
}

// TestCloneIsolation 从克隆侧和原对象侧分别修改，验证隔离是双向的，且 map 新键也不会泄漏。
// 这里手动修改字段而不再次调用 CloneDTO 内部逻辑，断言独立的业务不变量。
func TestCloneIsolation(t *testing.T) {
	original := lesson.NewOrder()
	clone := lesson.CloneDTO(original)
	clone.Status = "paid"
	clone.Lines[0].Qty = 9
	clone.Labels["review"] = "clone"
	clone.Labels["only-clone"] = "yes"
	clone.Buyer.Name = "CloneBuyer"
	if original.Status != "created" || original.Lines[0].Qty != 1 || original.Labels["review"] != "pending" || original.Buyer.Name != "Alice" {
		t.Fatal("克隆对象的修改泄漏到原订单")
	}
	if _, exists := original.Labels["only-clone"]; exists {
		t.Fatal("克隆 map 的新增键泄漏到原 map")
	}
	original.Lines[0].Qty = 10
	original.Labels["only-original"] = "yes"
	original.Buyer.Name = "OriginalBuyer"
	if clone.Lines[0].Qty != 9 || clone.Buyer.Name != "CloneBuyer" {
		t.Fatal("原订单的后续修改泄漏到克隆对象")
	}
	if _, exists := clone.Labels["only-original"]; exists {
		t.Fatal("原 map 新增键泄漏到克隆 map")
	}
	// ShallowClone 的边界也要直接证明，否则读者可能误以为两个名字都是深克隆。
	shallow := lesson.ShallowClone(original)
	shallow.Lines[0].Qty = 11
	shallow.Labels["review"] = "shared"
	shallow.Buyer.Name = "SharedBuyer"
	if original.Lines[0].Qty != 11 || original.Labels["review"] != "shared" || original.Buyer.Name != "SharedBuyer" {
		t.Fatal("浅复制实验没有共享内部对象")
	}
}

// TestClonePreservesEmptyAndNil 区分零值与已初始化但为空的容器，避免修复顺便改变 DTO 语义。
func TestClonePreservesEmptyAndNil(t *testing.T) {
	zero := lesson.CloneDTO(lesson.Order{})
	if zero.Lines != nil || zero.Labels != nil || zero.Buyer != nil {
		t.Fatal("零值中的 nil 字段应保留")
	}
	empty := lesson.CloneDTO(lesson.Order{Lines: []lesson.Line{}, Labels: map[string]string{}})
	if empty.Lines == nil || empty.Labels == nil || len(empty.Lines) != 0 || len(empty.Labels) != 0 {
		t.Fatal("已初始化的空容器不应变成 nil")
	}
}

// TestMethodsBinding 断言方法值创建时保存的接收者与表达式调用时提供的接收者。
// pointer 变量的重新赋值不会改已经保存的地址，但同一对象的后续字段写入仍对指针方法可见。
func TestMethodsBinding(t *testing.T) {
	order := lesson.NewOrder()
	if changed := order.SetStatusOnCopy("discarded"); changed != "discarded" || order.Status != "created" {
		t.Fatal("值接收者的状态写入应只留在副本和返回值中")
	}
	if changed := (&order).SetStatusOnCopy("also-discarded"); changed != "also-discarded" || order.Status != "created" {
		t.Fatal("通过指针调用值接收者方法也应复制目标值")
	}
	statusValue := order.StatusText
	buyerValue := order.BuyerName
	order.Status = "new-status"
	order.Buyer.Name = "new-buyer"
	if statusValue() != "created" || buyerValue() != "new-buyer" || lesson.Order.StatusText(order) != "new-status" {
		t.Fatal("方法值应保存浅层接收者副本，表达式应读取本次传入的值")
	}
	first, second := lesson.NewOrder(), lesson.NewOrder()
	pointer := &first
	bound := pointer.SetStatus
	pointer = &second
	bound("bound")
	if first.Status != "bound" || second.Status != "created" {
		t.Fatal("绑定方法值不应追踪 pointer 变量后续重新赋值")
	}
	(*lesson.Order).SetStatus(pointer, "expression")
	if second.Status != "expression" || (*lesson.Order).StatusText(&first) != "bound" {
		t.Fatal("方法表达式应使用本次明确传入的接收者")
	}
}

// TestMapWritebackBoundary 验证 map 的顶层值必须写回，slice/map/指针字段的共享规则仍独立存在。
// 不可编译的语法写在 demo.go 相应行的注释里；这个测试验证可运行修复的真实状态。
func TestMapWritebackBoundary(t *testing.T) {
	orders := map[string]lesson.Order{"one": lesson.NewOrder()}
	copy := orders["one"]
	copy.SetStatus("paid")
	copy.Lines[0].Qty = 4
	if orders["one"].Status != "created" || orders["one"].Lines[0].Qty != 4 {
		t.Fatal("map 元素副本应隔离顶层字段，但内部 slice 仍共享")
	}
	orders["one"] = copy
	if orders["one"].Status != "paid" {
		t.Fatal("整项写回后 map 中应保存新顶层状态")
	}
	byPointer := map[string]*lesson.Order{"one": lesson.LocalOrder()}
	byPointer["one"].SetStatus("direct")
	if byPointer["one"].Status != "direct" {
		t.Fatal("指针 map 应修改其指向的订单")
	}
}

// TestLocalAddressAndNewExpression 验证函数返回后仍能读写局部对象，并核对当前版本 new(expr) 的值。
// 不断言对象必须在堆上；语义正确性与编译器的逃逸优化是不同问题。
func TestLocalAddressAndNewExpression(t *testing.T) {
	first, second := lesson.LocalOrder(), lesson.LocalOrder()
	first.SetStatus("after-return")
	if first.Status != "after-return" || second.Status != "created" || first == second {
		t.Fatal("两次创建应独立，返回后对象仍有效")
	}
	zero := new(lesson.Order)
	answer := new(42)
	initialized := new(lesson.Order{Status: "new-expr"})
	if zero.Status != "" || zero.Buyer != nil || *answer != 42 || initialized.Status != "new-expr" {
		t.Fatal("new(T) 零值与 new(expr) 初始值不符合当前语言规则")
	}
	base := lesson.NewOrder()
	shallow := new(base)
	shallow.Buyer.Name = "shared-new"
	if base.Buyer.Name != "shared-new" || shallow == &base {
		t.Fatal("new(existing-struct) 应创建顶层变量，但内部字段仍按值复制")
	}
}

// TestNilReceiver 在测试自己的恢复边界验证风险，避免依赖教学代码的 observePanic 判定。
func TestNilReceiver(t *testing.T) {
	var missing *lesson.Order
	if missing.SafeStatus() != "<missing-order>" {
		t.Fatal("带 nil 守卫的方法应返回明确缺失状态")
	}
	assertPanics(t, func() { missing.SetStatus("paid") })
	assertPanics(t, func() { _ = missing.StatusText() })
}

// assertPanics 由 TestNilReceiver 调用；defer 直接 recover 当前 goroutine 的 panic。
// 不比较平台相关错误文案，只确认 API 在危险输入上的实际行为。
func assertPanics(t *testing.T, action func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("预期 nil 解引用 panic，实际没有发生")
		}
	}()
	action()
}

// TestRunSelection 验证入口路由能完成与错误分支；业务正确性由上面独立字段断言负责。
func TestRunSelection(t *testing.T) {
	for _, name := range []string{"all", "core", "methods", "allocation", "clone", "counter", "nil"} {
		if err := lesson.Run(io.Discard, name); err != nil {
			t.Fatalf("case %s failed: %v", name, err)
		}
	}
	if err := lesson.Run(io.Discard, "unknown"); err == nil {
		t.Fatal("未知实验名应返回 error 给入口")
	}
}
