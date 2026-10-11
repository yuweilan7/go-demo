package lab

import "testing"

// 诊断 sink 只用于保留跨迭代结果，明确让这条 benchmark 路径有堆生命周期。
// benchmark 默认串行运行；本文件不调用 RunParallel，因此这些变量没有并发写。
var pointerSink *Totals
var boxSink any
var closureSink func() int64
var integerSink int64

// BenchmarkKeyFmt 每次构造不同订单号的键，准备动作在第一次 B.Loop 之前。
// 循环条件必须保持字面形式 b.Loop()；当前工具链会保活循环体内的调用结果。
// 不同时套 b.N 循环，也不逐条打印日志，否则测量对象已变成日志/框架开销。
func BenchmarkKeyFmt(b *testing.B) {
	order := Order{42, 9001}
	var sequence uint64
	b.ReportAllocs()
	for b.Loop() {
		// 仅在 9001..9064 中轮转。不同机器迭代数不同，也不会变成不同位数的负载。
		order.OrderID = 9001 + int64(sequence&63)
		sequence++
		KeyFmt(order)
	}
}

// BenchmarkKeyAppend 与基线使用相同的输入更新与返回类型，只替换构造函数。
// B/op、allocs/op 有助于解释热点；ns/op 需要同平台多样本及环境控制才可比较。
func BenchmarkKeyAppend(b *testing.B) {
	order := Order{42, 9001}
	var sequence uint64
	b.ReportAllocs()
	for b.Loop() {
		order.OrderID = 9001 + int64(sequence&63)
		sequence++
		KeyAppend(order)
	}
}

// BenchmarkBadSetup 故意测错边界：每次都重建 4096 条“订单批次”。
// 它有真实可见结果，不是空循环；但数字回答的是“分配批次+构造键”的成本。
// 不允许把它与只构造一个键的 benchmark 排名后说 KeyFmt 性能提升数百倍。
func BenchmarkBadSetup(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		orders := make([]Order, 4096)
		for i := range orders {
			orders[i] = Order{42, int64(i) + 9001}
		}
		KeyFmt(orders[2048])
	}
}

// BenchmarkCorrectedSetup 把同一个 4096 条输入批次放在计时循环外。
// 修正改变的是测量边界而非订单键算法，不能当作生产代码优化的收益。
func BenchmarkCorrectedSetup(b *testing.B) {
	orders := make([]Order, 4096)
	for i := range orders {
		orders[i] = Order{42, int64(i) + 9001}
	}
	b.ReportAllocs()
	for b.Loop() {
		KeyFmt(orders[2048])
	}
}

// BenchmarkDiscardedLegacy 故意使用旧式 b.N 循环且丢弃可内联的纯函数结果。
// 编译器可能删除 PointerLocal 的实际工作，只剩循环；这组数字不代表请求成本。
// 它与 B.Loop 版本同时保留，便于从真实编译和结果理解失真，绝不作为优化证据。
func BenchmarkDiscardedLegacy(b *testing.B) {
	for i := 0; i < b.N; i++ {
		PointerLocal(int64(i))
	}
}

// BenchmarkEscapes 分开测“只本地使用”和“明确交给外部保留”，
// 不把不同对象生命周期当作同一业务负载来比较指针/接口的速度。
func BenchmarkEscapes(b *testing.B) {
	b.Run("pointer_local", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			PointerLocal(1024)
		}
	})
	b.Run("interface_local", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			BoxLocal(1024)
		}
	})
	b.Run("closure_local", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			ClosureLocal(1024)
		}
	})
	b.Run("pointer_retained", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			pointerSink = PointerRetained(1024)
		}
	})
	// 这个常量路径在本机实际为 0 B/0 alloc；超过小整数缓存范围仍可能静态装箱。
	b.Run("interface_constant_retained", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			boxSink = BoxRetained(1024)
		}
	})
	b.Run("interface_retained", func(b *testing.B) {
		n := int64(1024)
		b.ReportAllocs()
		for b.Loop() {
			// 每次不同的动态值被保留，不能把所有迭代都用同一个静态常量表示。
			n++
			boxSink = BoxRetained(n)
		}
	})
	b.Run("closure_retained", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			closureSink = ClosureRetained(1024)
			integerSink = closureSink()
		}
	})
}
