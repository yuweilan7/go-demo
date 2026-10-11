package lab

// Totals 是刻意很小的统计值，便于比较“地址语法”和实际逃逸去向。
type Totals struct{ Cents int64 }

// PointerLocal 由 demo/benchmark 调用；new 返回指针，但这个指针只在调用内使用。
// 当前编译器可能把对象放栈上甚至消除对象，只保留整数计算；new 不指定分配区。
func PointerLocal(n int64) int64 {
	p := new(Totals)
	p.Cents = n + 1
	return p.Cents
}

// BoxLocal 仅在当前调用内把 int64 装进 any，再立即取回。
// any 是 interface{} 的别名；接口有动态类型 int64 和动态值 n，不等于必有堆分配。
func BoxLocal(n int64) int64 {
	var value any = n
	return value.(int64) + 1
}

// ClosureLocal 的闭包读取本次参数 n，调用结束前执行完，未把函数值交给外部。
// 捕获变量是闭包的语义，不足以单独推出“闭包一定在堆上”。
func ClosureLocal(n int64) int64 {
	f := func() int64 { return n + 1 }
	return f()
}

// PointerRetained 把对象地址交给外部；Go 保证它仍可安全访问。
// 这个函数独立编译的 -m 输出显示对象逃逸，但内联后的调用上下文还能继续优化。
// 本课的 benchmark 把返回值写入包级诊断 sink，明确制造跨调用保留，不能被消掉。
func PointerRetained(n int64) *Totals { return &Totals{Cents: n + 1} }

// BoxRetained 把接口结果交给外部。benchmark 区分“可静态保存的常量”和动态变化的 n；
// 数值超过小整数缓存范围仍不足以证明分配：内联常量可能直接引用静态数据。
// 动态装箱的分配也属于当前编译器/运行时决策，并非接口语法的语言承诺。
func BoxRetained(n int64) any { return n }

// ClosureRetained 把捕获 n 的函数值交给外部，调用方以后执行时还需要 n。
// 返回函数值不是“异步启动”：本函数没有创建 goroutine，执行发生在调用方 f() 时。
func ClosureRetained(n int64) func() int64 { return func() int64 { return n + 1 } }
