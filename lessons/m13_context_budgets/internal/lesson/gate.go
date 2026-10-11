package lesson

import "sync"

// newOnceGate 由实验 owner 调用，返回可以安全重复调用的“放行”函数。
// ch 仍由这个 owner 独占关闭；sync.Once 使正常放行与 defer 兜底不发生重复 close panic。
// 返回的闭包捕获共享 once 和 channel；每次调用操作的是同一个状态。
func newOnceGate(ch chan struct{}) func() {
	var once sync.Once
	return func() {
		once.Do(func() { close(ch) })
	}
}
