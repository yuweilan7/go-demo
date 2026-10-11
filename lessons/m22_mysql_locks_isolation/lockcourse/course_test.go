package lockcourse

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// TestMySQL只在明确开关下执行Run全部真实实验；默认SKIP不能冒充实库证据。
// Run内部从另一个读连接查订单/库存，从PROCESSLIST查自有SQL，并验证DROP/Close。
func TestMySQL(t *testing.T) {
	if os.Getenv("GOLEARN_INTEGRATION") != "1" {
		t.Skip("真实MySQL实验需要GOLEARN_INTEGRATION=1；默认只跑资源/分类辅助断言")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	var output bytes.Buffer // 只有Run owner打印，没有worker并发写buffer。
	err := Run(ctx, &output, "all")
	t.Log(output.String())
	if err != nil {
		t.Fatal(err)
	}
}

// observedContext只给测试提供“join正在求值Done等待点”的同步通知，不伪造取消。
// 嵌入的Context继续提供真实deadline/Err；Once使反复求值Done不会双close。
type observedContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

// TestResultDoesNotMeanFinished让fn已发送result，wrapper却停在真实cleanup门上。
// owner实际进入join select后仍不能返回；开门后才能读到cleanup错误与Finished。
// 测试自己的owner goroutine也有最终done，defer负责放门并真正join，失败也不泄漏。
func TestResultDoesNotMeanFinished(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	workErr, cleanupErr := errors.New("work"), errors.New("wrapper cleanup")
	g := newGate()
	cleanupEntered := make(chan struct{})
	w := start(ctx, func(context.Context) taskResult { return taskResult{Err: workErr} }, func() error {
		close(cleanupEntered)
		if err := g.wait(context.Background()); err != nil {
			return err
		}
		return cleanupErr
	})
	ownerDone := make(chan struct{})
	ownerStarted := false
	ownerResult := make(chan taskResult, 1)
	joinEntered := make(chan struct{})
	defer func() {
		g.open()
		w.cancel()
		cleanup, c := cleanupContext()
		defer c()
		if ownerStarted {
			select {
			case <-ownerDone:
			case <-cleanup.Done():
				t.Error("测试owner未退出", cleanup.Err())
			}
		} else if _, e := w.join(cleanup); e != nil {
			t.Error("测试worker未退出", e)
		}
	}()
	select {
	case <-cleanupEntered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if len(w.result) != 1 {
		t.Fatal("cleanup已进入时工作result应已入buffer")
	}
	ownerStarted = true
	go func() {
		defer close(ownerDone)
		r, e := w.join(&observedContext{Context: ctx, entered: joinEntered})
		r.Err = errors.Join(r.Err, e)
		ownerResult <- r
	}()
	select {
	case <-joinEntered:
	case <-ctx.Done():
		t.Fatal("没有进入最终Finished等待", ctx.Err())
	}
	select {
	case <-ownerDone:
		t.Fatal("门没开，owner却提前宣布结束")
	default:
	}
	g.open()
	select {
	case <-ownerDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	r := <-ownerResult
	if !errors.Is(r.Err, workErr) || !errors.Is(r.Cleanup, cleanupErr) || !w.joined {
		t.Fatal("完成后工作/清理错误未分别保留", r)
	}
}

// TestEarlyFailureReleasesGateAndJoins直接执行生产scope.close保护路径。业务早退
// 时worker还在门上，必须放门+取消+join，且不能丢原失败或工作/资源清理失败。
func TestEarlyFailureReleasesGateAndJoins(t *testing.T) {
	prior, workErr, cleanupErr := errors.New("owner failure"), errors.New("worker failure"), errors.New("fn cleanup")
	var worker *task
	cleaned := make(chan struct{})
	run := func() (err error) {
		var own scope
		defer own.close(&err)
		g := own.gate()
		worker = own.start(context.Background(), func(ctx context.Context) (r taskResult) {
			defer captureTaskPanic(&r)
			defer func() { r.Cleanup = cleanupErr; close(cleaned) }()
			r.Err = errors.Join(workErr, g.wait(ctx))
			return
		})
		return prior
	}
	err := run()
	for _, cause := range []error{prior, workErr, cleanupErr} {
		if !errors.Is(err, cause) {
			t.Fatalf("丢失原因%v: %v", cause, err)
		}
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("资源defer未完成")
	}
	select {
	case <-worker.finished:
	default:
		t.Fatal("worker最终Finished未完成")
	}
	if !worker.joined || errors.Is(err, ErrJoin) {
		t.Fatal("没有真实join", err)
	}
}

// TestJoinTimeoutThenRealJoin验证清理预算耗尽是失败而非joined；随后解除自有门、
// 用新Background预算等待真实退出。20ms只是故意deadline，不能当调度成功证据。
func TestJoinTimeoutThenRealJoin(t *testing.T) {
	g := newGate()
	w := start(context.Background(), func(ctx context.Context) taskResult { return taskResult{Err: g.wait(ctx)} }, nil)
	defer func() {
		g.open()
		w.cancel()
		ctx, c := cleanupContext()
		defer c()
		if _, e := w.join(ctx); e != nil {
			t.Error(e)
		}
	}()
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err := w.join(short)
	cancel()
	prior := errors.New("original")
	err = errors.Join(prior, err)
	if !errors.Is(err, prior) || !errors.Is(err, ErrJoin) || !errors.Is(err, context.DeadlineExceeded) || w.joined {
		t.Fatal("超时不能冒充完成/丢原失败", err)
	}
	g.open()
	ctx, cancel := cleanupContext()
	defer cancel()
	r, err := w.join(ctx)
	if err != nil || r.Err != nil || !w.joined {
		t.Fatal("放门后仍未真实join", r, err)
	}
}

// TestPanicPreservesCleanup让资源fn故意panic。清理先写具名结果，最早登记的
// captureTaskPanic最后recover；结果保留原panic和cleanup，owner再等Finished。
func TestPanicPreservesCleanup(t *testing.T) {
	panicCause, cleanupErr := errors.New("panic cause"), errors.New("cleanup cause")
	w := start(context.Background(), func(context.Context) (r taskResult) {
		defer captureTaskPanic(&r)
		defer func() { r.Cleanup = cleanupErr }()
		panic(panicCause)
	}, nil)
	ctx, cancel := cleanupContext()
	defer cancel()
	r, err := w.join(ctx)
	if err != nil || !errors.Is(r.Err, panicCause) || !errors.Is(r.Cleanup, cleanupErr) {
		t.Fatal("panic/清理原因丢失", r, err)
	}
}

// TestRetryRequiresKnownRollback只验证错误树分类，不冒充真实死锁。实库1213与
// 1205的效果由TestMySQL覆盖；这里独立断言UNKNOWN/cleanup优先于可重试原因。
func TestRetryRequiresKnownRollback(t *testing.T) {
	deadlock := &mysql.MySQLError{Number: 1213, Message: "synthetic classification only"}
	for _, tc := range []struct {
		err  error
		kind uint16
		want bool
	}{
		{deadlock, 1213, true}, {errors.Join(deadlock, ErrUnknown), 1213, false},
		{errors.Join(deadlock, &txCleanupFailure{errors.New("rollback failed")}), 1213, false},
		{&mysql.MySQLError{Number: 1205}, 1213, false}, {ErrCAS, 0, true},
		{errors.Join(ErrCAS, &txCleanupFailure{errors.New("rollback failed")}), 0, false},
	} {
		if got := retryable(tc.err, tc.kind); got != tc.want {
			t.Fatalf("分类got=%t want=%t error=%v", got, tc.want, tc.err)
		}
	}
}
