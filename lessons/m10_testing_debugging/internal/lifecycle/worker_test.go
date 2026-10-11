package lifecycle_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"go-demo/lessons/m10_testing_debugging/internal/lifecycle"
)

// TestStopNotificationIsNotExit 用两道屏障稳定复现取消后 worker 仍在清理等待，没有 sleep 猜调度。
// 当前测试 goroutine 是 owner，Cleanup 负责即便中途 Fatal 也通知、放行、等退出。
func TestStopNotificationIsNotExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	w := lifecycle.Start(ctx)
	t.Cleanup(func() {
		w.Stop()
		w.ReleaseCleanup()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cleanupCancel() // 独立清理上限，不能复用可能已经过期的业务 ctx。
		if err := lifecycle.Await(cleanupCtx, w.Done, "cleanup done"); err != nil {
			t.Error(err) // 当前 callback 在测试 goroutine 执行；报告清理失败并返回。
		}
	})
	if err := lifecycle.Await(ctx, w.Started, "started"); err != nil {
		t.Fatal(err)
	}
	w.Stop()
	if err := lifecycle.Await(ctx, w.Observed, "observed cancellation"); err != nil {
		t.Fatal(err)
	}
	// observed 保证 worker 走到清理屏障，release 尚未关闭，因此 Done 不可能关闭。
	select {
	case <-w.Done:
		t.Fatal("worker exited before cleanup was released")
	default:
	}
	if w.Cleaned() {
		t.Fatal("cleanup marked complete while release gate was closed")
	}
	t.Log("Stop returned; worker observed cancellation; Done remains open")
	w.ReleaseCleanup()
	if err := lifecycle.Await(ctx, w.Done, "done"); err != nil {
		t.Fatal(err)
	}
	if !w.Cleaned() {
		t.Fatal("owner joined worker but cleanup fact is missing")
	}
	t.Log("cleanup gate released; owner joined Done; cleaned=true")
}

// TestVirtualDeadline 使用 Go 1.27 的 bubble 时钟，只覆盖纯 context/channel 的取消契约。
// bubble 内的 channel/ctx/worker 全部在回调里创建，外部 goroutine 不操作这些隔离对象。
func TestVirtualDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		w := lifecycle.Start(ctx)
		// bubble 中的 Cleanup 在 Test 返回前执行；放开 worker，避免失败路径留下永久阻塞。
		t.Cleanup(func() { w.Stop(); w.ReleaseCleanup() })
		synctest.Wait() // 等其他 bubble goroutine 都停在可确定的等待点，不能用于真实 socket I/O。
		synctest.Sleep(10*time.Second - time.Nanosecond)
		if err := ctx.Err(); err != nil {
			t.Fatalf("deadline fired early: %v", err)
		}
		synctest.Sleep(time.Nanosecond) // Go 1.27 新 API：推进虚拟时间后再 Wait，消除恰好截止时的调度歧义。
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("ctx.Err()=%v; want DeadlineExceeded", ctx.Err())
		}
		select {
		case <-w.Observed:
		default:
			t.Fatal("virtual deadline fired but worker has not observed it after Wait")
		}
		if w.Cleaned() {
			t.Fatal("virtual deadline incorrectly bypassed cleanup gate")
		}
		w.ReleaseCleanup()
		synctest.Wait()
		select {
		case <-w.Done:
		default:
			t.Fatal("released worker did not exit")
		}
		if elapsed := time.Since(start); elapsed != 10*time.Second {
			t.Fatalf("virtual elapsed=%s; want 10s", elapsed)
		}
		t.Log("virtual elapsed=10s; deadline observed; cleanup joined")
	})
}

// TestBackgroundErrorReturnedToOwner 让 worker 返回错误和清理证据，owner 在自己 goroutine 中做 Fatal。
// t.Fatal 内部 Goexit 只退出调用它的 goroutine，不能当成可跨 goroutine 传播的 Java 异常。
func TestBackgroundErrorReturnedToOwner(t *testing.T) {
	want := errors.New("合成的支付校验失败")
	cleaned := make(chan struct{}) // job 关闭，owner 接收；证明 job 的 defer 已经执行。
	result := lifecycle.RunJob(func() error {
		defer close(cleaned)
		return want
	})
	select {
	case err := <-result:
		if !errors.Is(err, want) {
			t.Fatalf("background err=%v; want original cause", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bounded background job did not report result")
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("result arrived before job cleanup completed")
	}
	// result 的关闭是 RunJob wrapper 退出前最后动作；接收直到 ok=false，加入 wrapper 的生命周期。
	if extra, ok := <-result; ok {
		t.Fatalf("unexpected second job result=%v", extra)
	}
}

// TestAtomicCounterFix 是 faults 中共享计数器 race 的修复版。
// start 只让两个 worker 都获得启动条件，不提供两个 worker 之间的内存互斥；atomic.Add 提供每次递增的原子性。
func TestAtomicCounterFix(t *testing.T) {
	var counter atomic.Int64
	var group sync.WaitGroup
	start := make(chan struct{}) // owner 关闭；worker 等它，然后开始各自的 10,000 次记录。
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for range 10_000 {
				counter.Add(1)
			}
		}()
	}
	close(start)
	group.Wait() // 等两个 worker 的全部记录完成，之后才核验业务数量。
	if got := counter.Load(); got != 20_000 {
		t.Fatalf("recorded payments=%d; want 20000", got)
	}
}
