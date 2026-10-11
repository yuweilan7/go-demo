package lesson

// RunRuntimePanic 由 demo/test 调用，用合法编译但运行时越界的 slice 访问对照 runtime fatal。
// runtime 产生的普通 bounds panic 在同 goroutine 的直接 defer 中可恢复；不是所有 runtime 故障都可恢复。
func RunRuntimePanic() (failure *PanicFailure) {
	defer func() {
		if value := recover(); value != nil {
			failure = captureFailure("order-7001", "runtime.slice-index", value)
		}
	}()
	quantities := []int{1}
	index := 2 // 变量索引让这个表达式通过编译，运行时才由边界检查产生 panic。
	_ = quantities[index]
	return nil
}

// RecoverRules 由 demo/test 调用，全部反例由外层的有效恢复边界兜住，主入口可以完整结束。
// 返回事件说明 recover 的调用位置；真正的未恢复进程另放在 RunChildCase。
func RecoverRules() []string {
	t := &trace{}
	if recover() == nil { // 普通函数体当前并没有正在展开的 panic，这里只得到 nil。
		t.add("ordinary=nil")
	}
	indirectBoundary(t)
	t.add("caller.after-indirect")
	directBoundary(t)
	t.add("caller.after-direct")
	bareRecoverBoundary(t)
	t.add("caller.after-bare")
	return t.events
}

// indirectBoundary 由 RecoverRules 调用，外层 closure 是正确恢复点。
// indirectInner 的错误 helper 不会终止 panic；展开继续到本函数，才在这里恢复。
func indirectBoundary(t *trace) {
	defer func() {
		if recover() != nil {
			t.add("outer.recovered")
		}
	}()
	indirectInner(t)
	// 此函数的 defer 恢复，因此这里不会执行。
}

// indirectInner 故意把 recover 放到再转调的普通 helper，演示“写在 defer 附近”不够。
func indirectInner(t *trace) {
	defer func() {
		if helperRecover() == nil {
			t.add("indirect.helper=nil")
		}
	}()
	panic("helper 不能替 deferred function recover")
}

// helperRecover 由 deferred closure 普通调用，自己的调用不是那个 defer 所延迟的函数。
// 不用 //go:noinline 控制这个实验：即使编译器内联优化，语言可观察语义仍必须相同。
func helperRecover() any { return recover() }

// directBoundary 把有名字的 recovery 函数本身延迟调用。
// “直接”描述调用关系，不要求必须匿名闭包；defer directRecovery(t) 是有效写法。
func directBoundary(t *trace) {
	defer directRecovery(t)
	panic("直接 deferred named function")
}

// directRecovery 就是 defer 真正延迟的函数，它直接调用 recover，所以能停止 panic。
func directRecovery(t *trace) {
	if recover() != nil {
		t.add("direct.named=recovered")
	}
}

// bareRecoverBoundary 增加一个容易凭字面误读的反例：defer recover() 本身不会恢复。
// 先注册有效 closure 是为了收住反例，否则此函数会连带正常 demo 崩溃。
func bareRecoverBoundary(t *trace) {
	defer func() {
		if recover() != nil {
			t.add("bare.outer=recovered")
		}
	}()
	defer recover() // recover 不是被另一个 deferred function 的函数体直接调用，返回值也被丢弃。
	panic("defer recover() 仍向外展开")
}
