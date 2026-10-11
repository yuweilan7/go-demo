//go:build fatalfault

package faults

import (
	"testing"
	"time"
)

// TestFatalInWorker 是 E05 的显式反例，使用 -vet=off 才能运行，go vet 的 testinggoroutine 会拒绝它。
// 不能在真实业务测试里这样写：Fatal 只 Goexit 当前 worker，owner 仍继续，并不会自动加入其他后台任务。
func TestFatalInWorker(t *testing.T) {
	done := make(chan struct{}) // worker 的 defer 关闭，owner 用它确认 Fatal 之后 worker 栈已清理。
	go func() {
		defer close(done)
		t.Fatal("故意错误：在 worker 中 Fatal")
	}()
	select {
	case <-done:
		t.Log("owner continued after worker Fatal; worker defer closed done")
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit within diagnostic limit")
	}
	t.Log("owner reached its final line; the test remains failed")
}
