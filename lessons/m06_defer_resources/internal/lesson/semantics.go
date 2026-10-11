package lesson

// SemanticsResult 把独立观察值交给测试。它不从日志文字反推语义。
type SemanticsResult struct {
	SavedArgument, ClosureValue, PointerValue    int
	FunctionWasReplaced                          bool
	Order                                        []int
	PointerReceiver, ValueReceiver, LateReceiver string
}

// RunSemantics 由 demo 调用，先运行参数与函数值实验，再运行接收者实验。
// r 是本次调用专属结果；传入 &r 的辅助函数共享同一对象，defer 填好后才返回。
func RunSemantics(t *Trace) SemanticsResult {
	t.Event("E01", "注册实参、函数值、闭包、指针与 LIFO")
	r := SemanticsResult{}
	registerValues(t, &r)
	receiverValues(t, &r)
	t.Event("E01.caller", "arg=%d closure=%d pointer=%d replaced_called=%t order=%v",
		r.SavedArgument, r.ClosureValue, r.PointerValue, r.FunctionWasReplaced, r.Order)
	return r
}

// registerValues 让 caller 看到“注册调用”与“执行调用”是两个时刻。
// value 是本函数局部变量；r 是 caller 的结果指针，所有观察都写入这个对象。
func registerValues(t *Trace, r *SemanticsResult) {
	value := 10
	fn := func(n int) {
		r.SavedArgument = n
		r.Order = append(r.Order, 1)
		t.Event("E01.defer1", "旧函数收到注册时保存的实参=%d", n)
	}
	// fn 的函数值立即保存。evaluateArgument 也立即调用，返回的 int 副本 10
	// 成为延迟调用的实参；延迟的是 fn(...)，不是 evaluateArgument(...)。
	defer fn(evaluateArgument(t, value))
	t.Event("E01.body", "defer1 已注册")

	// 这里没有把 value 当参数传入。闭包保存访问变量的能力，稍后执行函数体
	// 才读取同一局部变量 value；它不是把注册时的 10 自动拍成快照。
	defer func() {
		r.ClosureValue = value
		r.Order = append(r.Order, 2)
		t.Event("E01.defer2", "闭包执行时读取 value=%d", value)
	}()
	t.Event("E01.body", "defer2 已注册")

	// &value 是立即求出的指针值。指针自身被复制并保存，所指的 int 仍可变化。
	// “参数已保存”只固定指针值，不会把整个可达对象深拷贝。
	defer func(p *int) {
		r.PointerValue = *p
		r.Order = append(r.Order, 3)
		t.Event("E01.defer3", "通过已保存指针读取 *p=%d", *p)
	}(&value)
	t.Event("E01.body", "defer3 已注册")

	value = 20
	fn = func(int) { r.FunctionWasReplaced = true }
	// fn 在此处被替换，但已经登记的 defer1 仍持有旧函数值，不会再查变量 fn。
	_ = fn
	t.Event("E01.body", "value=20，fn 已替换；函数体结束")
	// 函数体结束也会返回。执行顺序严格为 defer3、defer2、defer1。
}

// evaluateArgument 由 defer 的实参表达式同步调用，它的日志早于“defer1 已注册”。
// 返回值是 value 的副本，之后修改 registerValues 的变量不会改变这个副本。
func evaluateArgument(t *Trace, value int) int {
	t.Event("E01.eval", "实参表达式现在执行 value=%d", value)
	return value
}

// receiver 是接收者语义的标签对象，未持有真实外部资源；文件关闭在 E03/E04 验证。
type receiver struct{ name string }

// pointerName 以指针接收者调用。r 指向注册时选中的对象，调用时读取它的 name。
func (r *receiver) pointerName(t *Trace, out *SemanticsResult) {
	out.PointerReceiver = r.name
	t.Event("E01.receiver.pointer", "旧对象的当前字段=%s", r.name)
}

// valueName 以值接收者调用。这里 r 是注册时接收者的结构体副本，不是原对象别名。
func (r receiver) valueName(t *Trace, out *SemanticsResult) {
	out.ValueReceiver = r.name
	t.Event("E01.receiver.value", "接收者副本的字段=%s", r.name)
}

// receiverValues 由 RunSemantics 调用，对比旧指针、值副本与闭包晚读接收者变量。
// current 是可重新赋值的指针变量；first 和 second 是两个不同对象。
func receiverValues(t *Trace, out *SemanticsResult) {
	first := &receiver{name: "A"}
	second := &receiver{name: "B"}
	current := first
	defer current.pointerName(t, out) // 保存 first 指针；稍后 current=second 不会改它。
	defer current.valueName(t, out)   // 保存 first 当前结构体值，name="A"。
	defer func() {
		out.LateReceiver = current.name
		t.Event("E01.receiver.closure", "闭包晚读 current=%s", current.name)
	}()
	first.name = "A*"
	current = second
	t.Event("E01.receiver.body", "旧对象 name=A*，current 改指 B")
}
