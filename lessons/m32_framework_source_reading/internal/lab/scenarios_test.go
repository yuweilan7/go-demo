package lab

import (
	"context"
	"errors"
	"fmt"
	pb "go-demo/lessons/m32_framework_source_reading/gen/inventoryv1"
	"go-demo/lessons/m32_framework_source_reading/internal/app"
	"go-demo/lessons/m32_framework_source_reading/internal/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"testing"
	"time"
)

// TestCompleteRealFramework检查结构化业务事实，而不是搜索打印PASS。Run含真实HTTP/gRPC错误/取消/停服。
func TestCompleteRealFramework(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	summary, err := Run(ctx, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := Summary{FinalStock: 10, LostWrites: 1, CancelWrites: 0, RetryWrites: 2, StreamMessages: 2, HandlersJoined: true, ConnectionsClosed: true}
	if summary != want {
		t.Fatalf("summary=%+v want=%+v", summary, want)
	}
}

// TestConcurrentStockCannotOverdraw独立证明共享仓储临界区：30个真实RPC争20库存，只20成功，余额非负。
// 每个Task启动即有完成owner，最后cleanup先join全部任务再Close连接/root，不用WaitGroup掩盖defer尚未完成。
func TestConcurrentStockCannotOverdraw(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	j := trace.New(nil)
	r, err := app.New(app.DefaultConfig(), j)
	if err != nil {
		t.Fatal(err)
	}
	r.Start()
	c, err := newClients(ctx, r, j, "m32")
	if err != nil {
		_ = r.Close()
		t.Fatal(err)
	}
	tasks := make([]*app.Task, 0, 30)
	replies := make([]*pb.ReserveReply, 30)
	callErrors := make([]error, 30)
	defer func() {
		// 新预算保证即便测试ctx已取消也能完成任务尾部；读取reply必须在Done同步之后。
		cancel()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cleanupCancel()
		for _, task := range tasks {
			if err := task.Join(cleanupCtx); err != nil {
				t.Error(err)
			}
		}
		if err := errors.Join(c.close(), r.Close()); err != nil {
			t.Error(err)
		}
	}()
	for index := range 30 {
		// Go1.27每次range迭代有独立index变量，闭包写不同slice元素；切片本身不append，不发生共享元素竞争。
		tasks = append(tasks, app.StartTask(func() error {
			replies[index], callErrors[index] = c.grpc.Reserve(ctx, request(fmt.Sprintf("concurrent-%02d", index), 1, pb.ProcessingMode_NORMAL))
			return nil
		}, nil))
	}
	for _, task := range tasks {
		if err := task.Join(ctx); err != nil {
			t.Fatal(err)
		}
	}
	successes, shortages := 0, 0
	remainingSet := make(map[int32]bool)
	for index, callErr := range callErrors {
		switch status.Code(callErr) {
		case codes.OK:
			successes++
			remaining := replies[index].GetRemaining()
			if remaining < 0 || remainingSet[remaining] {
				t.Fatalf("非法/重复remaining=%d", remaining)
			}
			remainingSet[remaining] = true
		case codes.FailedPrecondition:
			shortages++
		default:
			t.Fatalf("unexpected RPC error=%v", callErr)
		}
	}
	available, _, _ := r.Repository.Snapshot("")
	if successes != 20 || shortages != 10 || available != 0 {
		t.Fatalf("success=%d shortage=%d available=%d", successes, shortages, available)
	}
}

// TestCanceledOwnerStillClosesResources把总owner预先取消，真实Run必须失败；资源仍走独立Background清理预算。
// 它检查早退原因，而成功运行测试检查最终资源状态；既不重跑前置，也不把Canceled当Close成功证明。
func TestCanceledOwnerStillClosesResources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("原Canceled原因未保留: %v", err)
	}
}

// TestUnstartedRootOwnsListeners验证构造已绑端口但未Start的路径；框架Shutdown未见过的listener由root精确Close。
func TestUnstartedRootOwnsListeners(t *testing.T) {
	r, err := app.New(app.DefaultConfig(), trace.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = assertTCPStopped(ctx, r); err != nil {
		t.Fatal(err)
	}
	_, _, closed := r.Repository.Snapshot("")
	if !closed || r.Active() != 0 {
		t.Fatal("未Start资源仍未关闭")
	}
}

// TestTaskResultIsNotDone独立检查结果缓冲与final defer；主失败和尾部收尾失败通过errors.Join同时保留。
// gate阻塞是显式机制，先读Result再非阻塞看Done；没有用耗时推断task调度。
func TestTaskResultIsNotDone(t *testing.T) {
	mainErr := errors.New("main failure")
	cleanupErr := errors.New("cleanup failure")
	gate := make(chan struct{})
	task := app.StartOwnedTask(func() error { return mainErr }, func() error { return cleanupErr }, gate)
	released := false
	defer func() {
		if !released {
			close(gate)
		}
		if err := joinTask(task); !errors.Is(err, mainErr) || !errors.Is(err, cleanupErr) {
			t.Errorf("final错误链丢失: %v", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case preview := <-task.Result:
		if !errors.Is(preview, mainErr) {
			t.Fatal("Result丢主因")
		}
		if errors.Is(preview, cleanupErr) {
			t.Fatal("cleanup尚未发生却已进入Result")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if task.Finished() {
		t.Fatal("Result可读却把尾部defer当Done")
	}
	close(gate)
	released = true
	err := task.Join(ctx)
	if !errors.Is(err, mainErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Join=%v", err)
	}
}
