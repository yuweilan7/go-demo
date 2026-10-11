package lesson_test

import (
	"sync"
	"testing"
	"time"

	"go-demo/lessons/m02_values_methods/internal/lesson"
)

// TestCounterConcurrent 由 go test 调用，真实启动 4 个 worker，各累计 250 笔合成订单。
// 测试 goroutine 是 owner：拥有 start 屏障和 WaitGroup，等 worker 全部完成后才读取最终数值。
// 数量和时间都有上限；worker 之间的具体执行顺序不保证，也不纳入断言。
func TestCounterConcurrent(t *testing.T) {
	var counter lesson.Counter
	var workers sync.WaitGroup
	// struct{} 是没有字段的类型，这个 channel 不携带业务数据，只作为关闭通知。
	start := make(chan struct{}) // 未关闭时，worker 的 <-start 等待 owner 发起统一开始通知。
	// Go 1.22 起整数 range 可以表达固定次数迭代；for range 4 没有索引变量，执行 4 次。
	for range 4 {
		// Add 在启动前由 owner 执行，防止 Wait 看到零计数而提前返回。
		workers.Add(1)
		go func() {
			defer workers.Done() // 当前 worker 返回时通知 owner；不复制 WaitGroup，闭包用同一变量。
			<-start
			for range 250 {
				counter.Add(1) // 闭包引用同一 counter，指针方法用同一锁保护同一 int。
			}
		}()
	}
	finished := make(chan struct{})
	go func() {
		workers.Wait() // 这个等待 goroutine 只等待上述 4 个 worker，没有独立业务工作。
		close(finished)
	}()
	close(start) // 所有 worker 可继续；这只控制开始，不承诺某个 worker 最先取得锁。
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop() // 测试 owner 负责停止计时器；正常完成不留活动计时等待。
	// select 等待能够进行的接收分支；finished 关闭与 timer 到期是两种独立事实。
	select {
	case <-finished:
		// finished 关闭前 Wait 已返回，4 个 worker 的 Done 均完成；此时没有残留 worker 修改 counter。
	case <-timer.C:
		// 外层 go test -timeout 还会限制整个进程，防止实现回归造成锁死后长期残留测试任务。
		t.Fatal("3 秒内 worker 未全部退出：检查 Counter 锁释放与 WaitGroup 计数")
	}
	// if 的简短声明把 got 限定在本 if 内；它是完成后读取的 int 快照，不是共享变量别名。
	if got := counter.Value(); got != 1000 {
		t.Fatalf("订单计数不守恒: got=%d want=1000", got)
	}
}
