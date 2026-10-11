//go:build faults

package faults

import (
	"sync"
	"testing"
)

// TestRaceSharedCounter 是 E04 唯一故意不受同步保护的共享变量，必须用显式 -race 命令运行。
// start 是共同启动条件，不是两次 counter++ 之间的同步；Wait 只在结束后让 owner 安全读取最终值。
func TestRaceSharedCounter(t *testing.T) {
	var counter int
	var group sync.WaitGroup
	start := make(chan struct{})
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for range 10_000 {
				counter++ // 故意错误：读、加一、写不是一个原子操作，也没有锁。
			}
		}()
	}
	close(start)
	group.Wait()
	t.Logf("observed count=%d; even 20000 would not prove absence of a data race", counter)
}
