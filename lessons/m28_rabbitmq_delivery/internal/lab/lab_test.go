package lab

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

// TestLedgerConcurrentCommit 只测试本进程业务原子性；这不是MQ替身验收。
// 多个调用者共享同一Ledger，WaitGroup由test拥有，Wait后读取真实effects。
func TestLedgerConcurrentCommit(t *testing.T) {
	l := NewLedger()
	var group sync.WaitGroup
	// range整数只重复24次；group.Go增加计数并启动goroutine，返回不代表已完成。
	// 每个闭包共享l指针，Commit自己加锁；真正完成的屏障是下面的group.Wait。
	for range 24 {
		group.Go(func() {
			if _, err := l.Commit("same-order"); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if got := l.Count("same-order"); got != 1 {
		t.Fatalf("并发幂等effect=%d want=1", got)
	}
	if _, err := l.Commit(""); err == nil {
		t.Fatal("空业务键必须拒绝")
	}
}

// TestTaskJoinAfterCleanup 验证owner生命周期，不模拟RabbitMQ。
// work已观察取消但其defer仍在清理屏障；finished必须尚未关闭，放行后join
// 才能读取原取消错误。t.Cleanup在start前登记，因此t.Fatal也能解除等待并join。
func TestTaskJoinAfterCleanup(t *testing.T) {
	workCtx, cancelWork := context.WithCancel(context.Background())
	gate := newGate()
	task := newTask()
	t.Cleanup(func() {
		cancelWork()
		gate.open()
		if err := joinTask(task); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("清理join: %v", err)
		}
	})
	task.start(func() error {
		defer func() { close(gate.entered); <-gate.release }()
		<-workCtx.Done()
		return workCtx.Err()
	})
	cancelWork()
	observer, cancelObserver := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelObserver()
	select {
	case <-gate.entered:
	case <-observer.Done():
		t.Fatal("worker没有进入清理屏障")
	}
	select {
	case <-task.finished:
		t.Fatal("清理门仍关闭，不能已经join完成")
	default:
	}
	gate.open()
	if err := joinTask(task); !errors.Is(err, context.Canceled) {
		t.Fatalf("join必须在收尾后保留工作取消错误，got=%v", err)
	}
}

// TestIntegration 的开关要求显式指定；默认test只证明Ledger/收尾，缺失broker不标PASS。
// GOLEARN_M28_INTEGRATION=1 时与demo运行同一真实AMQP断言，超时后仍执行清理。
func TestIntegration(t *testing.T) {
	if os.Getenv("GOLEARN_M28_INTEGRATION") != "1" {
		t.Skip("真实RabbitMQ实验需GOLEARN_M28_INTEGRATION=1；默认单测不代表broker验收")
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err = Run(ctx, cfg, "all", io.Discard); err != nil {
		t.Fatal(err)
	}
}
