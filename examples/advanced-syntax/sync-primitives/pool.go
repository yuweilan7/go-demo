package main

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"
)

func demoPool() {
	var newCalls atomic.Int64
	pool := sync.Pool{
		// New 是“Get 没取到对象时调用的可选回调字段”，不是 Pool 的构造函数。
		New: func() any {
			newCalls.Add(1)
			return new(bytes.Buffer)
		},
	}

	first := pool.Get().(*bytes.Buffer)
	first.WriteString("temporary data")
	lengthBeforeReset := first.Len()

	// Pool 不会替调用者清理对象。归还前 Reset，防止下一个使用者看见旧状态，
	// 也避免 Buffer 继续引用本次任务中不再需要的数据。
	first.Reset()
	pool.Put(first)

	second := pool.Get().(*bytes.Buffer)
	reusedThisTime := first == second
	fmt.Printf("归还前长度=%d；再次 Get 的长度=%d；本次复用同一对象=%v；New 调用=%d 次\n",
		lengthBeforeReset, second.Len(), reusedThisTime, newCalls.Load())

	second.Reset()
	pool.Put(second)

	// 即使本次看到 reusedThisTime=true，也只是一次运行观察，不是 API 保证。Pool 中的
	// 任意对象都可能在没有通知的情况下被移除，所以它不能保存连接、缓存条目或任何
	// 必须存在的状态，也不能用来精确限制池大小。它适合跨并发请求摊薄临时对象分配。
}
