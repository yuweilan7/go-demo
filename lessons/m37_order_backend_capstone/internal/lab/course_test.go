package lab

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// TestLive显式开启才运行真实MySQL/Redis/Kafka/Gin/gRPC整链，普通SKIP不是实库证据。
// Run已拥有全部resources/任务并在返回前收尾；测试总预算120s外还有Go测试180s保险。
func TestLive(t *testing.T) {
	if os.Getenv("GOLEARN_M37_INTEGRATION") != "1" {
		t.Skip("必须显式 GOLEARN_M37_INTEGRATION=1 才验真实服务")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if e := Run(ctx, os.Stdout); e != nil {
		t.Fatal(e)
	}
}

// TestCallerFinalDone固定Result后还有真实tail门。观察者提前失败也从创建后立即登记
// 唯一guard：先取消和放门再fresh预算join，最终原失败/取消都能errors.Is识别。
func TestCallerFinalDone(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "early"}[early], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			tail := NewGate()
			original := errors.New("preview-original")
			c := StartCaller(ctx, func(context.Context) result { return result{Err: original} }, tail)
			defer func() {
				_, e := c.Finish(tail)
				if e != nil {
					t.Errorf("guard cleanup: %v", e)
				}
			}()
			if e := wait(ctx, tail.Entered, "tail actually entered"); e != nil {
				t.Fatal(e)
			}
			select {
			case preview := <-c.Preview:
				if !errors.Is(preview.Err, original) {
					t.Fatal("预览丢原失败")
				}
			default:
				t.Fatal("Entered以前应该已发送预览")
			}
			select {
			case <-c.Done:
				t.Fatal("tail尚未释放，不能最终Done")
			default:
			}
			var final result
			var e error
			if early {
				final, e = c.Finish(tail)
			} else {
				tail.Release()
				final, e = c.Completed(ctx)
			}
			if e != nil || !errors.Is(final.Err, original) {
				t.Fatalf("最终原因丢失: %v %v", e, final.Err)
			}
			if early && !errors.Is(final.Err, context.Canceled) {
				t.Fatal("提前退出必须保留实际cancel")
			}
		})
	}
}

// TestCallerPanic是实验caller入口真正panic/recover/Done链；辅助证明不访问broker。
// 可写err仅在Done关闭以后读取，因此race检查也能验证此同步关系。
func TestCallerPanic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	g := NewGate()
	cause := errors.New("task panic")
	c := StartCaller(ctx, func(ctx context.Context) result {
		if e := g.Wait(ctx); e != nil {
			return result{Err: e}
		}
		panic(cause)
	}, nil)
	defer func() {
		g.Release()
		_, e := c.Finish(nil)
		if e != nil {
			t.Error(e)
		}
	}()
	if e := wait(ctx, g.Entered, "panic task gate"); e != nil {
		t.Fatal(e)
	}
	g.Release()
	r, e := c.Completed(ctx)
	if e != nil || !errors.Is(r.Err, cause) {
		t.Fatalf("panic cause/done: %v %v", r.Err, e)
	}
}

// TestCanceledOwnerRealCleanup以已取消root运行真实构造，只接受error并检查Run的
// 成功取得资源亦会关闭。这是实库提前构造失败测试，必须同一integration开关。
func TestCanceledOwnerRealCleanup(t *testing.T) {
	if os.Getenv("GOLEARN_M37_INTEGRATION") != "1" {
		t.Skip("显式实库开关")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := Run(ctx, io.Discard); !errors.Is(e, context.Canceled) || errors.Is(e, b.ErrCleanup) {
		t.Fatalf("取消构造应保留取消并成功收尾: %v", e)
	}
}

// TestBorrowedResourcesRealRecovery以真实旧client最终关闭→fresh构造，验证公开恢复
// 能力和原owner清理责任。不是强杀矩阵，也不会增加同时存活的SQL池总容量。
func TestBorrowedResourcesRealRecovery(t *testing.T) {
	if os.Getenv("GOLEARN_M37_INTEGRATION") != "1" {
		t.Skip("显式实库开关")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if e := RunRecovery(ctx, os.Stdout); e != nil {
		t.Fatal(e)
	}
}

// TestJoinErrorIdentity只验证错误树，不将模拟清理cause写成网络故障证据。
func TestJoinErrorIdentity(t *testing.T) {
	original := errors.New("original")
	clean := errors.New("cleanup")
	e := errors.Join(original, b.CleanupCause(clean))
	if !errors.Is(e, original) || !errors.Is(e, clean) || !errors.Is(e, b.ErrCleanup) {
		t.Fatal("Join未保留全部身份")
	}
}
