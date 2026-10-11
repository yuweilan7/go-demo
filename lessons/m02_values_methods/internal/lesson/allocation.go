package lesson

// LocalOrder 由 E03 与测试调用，把局部 order 的地址返回给 caller。
// order 的词法名字在本函数外不可用，但其所指对象不能在返回时失效；caller 之后可读写它。
// 这个保证不要求我们知道对象最终在栈还是堆，也不要求程序员调用 free。
func LocalOrder() *Order {
	order := NewOrder()
	return &order
}

// observePanic 只由本包的 E03/E06 故障观察调用，把一次同步调用的 panic 变成日志事实。
// 返回 observed 用于验证危险操作确实发生；value 仅供显示，不依赖具体 panic 文本做判断。
// 不创建 goroutine；recover 必须直接处于当前调用栈的 deferred 函数里才能恢复本次 panic。
// any 是 interface{} 的预声明别名，这里仅用来容纳不同类型的 panic 值，不进行接口分派。
func observePanic(action func()) (observed bool, value any) {
	// 注册的是闭包，函数退出时才写具名返回变量 observed/value，不是注册时复制它们。
	defer func() {
		if recovered := recover(); recovered != nil {
			observed = true
			value = recovered
		}
	}()
	action() // 若 panic，下一行不执行；栈展开执行上面的 defer，恢复后本函数返回 caller。
	return false, nil
}
