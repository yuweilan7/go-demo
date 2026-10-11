package lesson

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// workerReturn 是独立观察者取得的事实，既保留 API 结果，也检查真正的退出 channel。
// doneAtReturn 不读 result.Joined，避免只用实现自己的标志来“证明实现正确”。
type workerReturn struct {
	result          TaskResult
	err             error
	doneAtReturn    bool
	cleanupAtReturn bool
}

// observeWorkerReturn 在单独 owner goroutine 同步调用真实 runWorker。
// API 返回那一刻检查 done/cleaned 是否已关闭；随后通过缓冲 channel 给测试交付不可变快照。
func observeWorkerReturn(ctx context.Context, budget time.Duration, hooks *workerHooks, cleaned <-chan struct{}) <-chan workerReturn {
	returned := make(chan workerReturn, 1)
	go func() {
		result, err := runWorker(ctx, true, budget, hooks)
		observation := workerReturn{result: result, err: err}
		select {
		case <-hooks.done:
			observation.doneAtReturn = true
		default:
		}
		select {
		case <-cleaned:
			observation.cleanupAtReturn = true
		default:
		}
		returned <- observation
	}()
	return returned
}

// newWorkerGate 把一个等待屏障的释放权给测试，sync.Once 允许正常路径与失败清理都释放它。
// t.Cleanup 保证断言失败时也不会永久卡住测试创造的 worker；不是用 sleep 猜工作已经结束。
func newWorkerGate(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return gate, release
}

// awaitWorkerSignal 仅给测试自身一个三秒上限，channel 事件才是阶段到达的证明。
// guard timer 到期意味着缺失同步事实，绝不把“等得够久”当成工作完成。
func awaitWorkerSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	guard := time.NewTimer(3 * time.Second)
	defer guard.Stop()
	select {
	case <-signal:
	case <-guard.C:
		t.Fatalf("三秒内没有观察到 %s", label)
	}
}

// awaitWorkerReturn 等独立 owner 观察者交付结果，不读活跃 worker 的共享库存字段。
func awaitWorkerReturn(t *testing.T, returned <-chan workerReturn) workerReturn {
	t.Helper()
	guard := time.NewTimer(3 * time.Second)
	defer guard.Stop()
	select {
	case result := <-returned:
		return result
	case <-guard.C:
		t.Fatal("owner 没有在有界验收时间内返回")
		return workerReturn{}
	}
}

// TestWorkerCancelJoinsStartedTask 分别命中“等结果”和“等退出”的 ctx.Done 分支。
// 屏障确保取消在任务已经启动之后、相应清理/退出尚未完成之前发生。
// 取消后 owner 的独立预算仍有效；worker 清理放行之前，owner 不能交付正常完成结果。
func TestWorkerCancelJoinsStartedTask(t *testing.T) {
	for _, phase := range []string{"before-result", "after-result"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancelCause(context.Background())
				cause := errors.New("订单请求方取消；保留这个特定原因")
				defer cancel(cause)
				cleanupEntered := make(chan struct{})
				cleanupFinished := make(chan struct{})
				resultReceived := make(chan struct{})
				exitEntered := make(chan struct{})
				done := make(chan struct{})
				cleanupGate, releaseCleanup := newWorkerGate(t)
				exitGate, releaseExit := newWorkerGate(t)
				t.Cleanup(func() {
					// 本测试是屏障 owner，最后无条件放行，并实际等 worker 的 done，避免断言失败泄漏。
					releaseCleanup()
					releaseExit()
					awaitWorkerSignal(t, done, "测试创建的 worker 退出")
				})
				type cancelObservation struct {
					stage      string
					cleanupErr error
					cause      error
				}
				cancelObserved := make(chan cancelObservation, 1)
				hooks := &workerHooks{
					done: done,
					beforeCleanup: func() {
						close(cleanupEntered) // 已经预占并 panic，当前正进入任务清理，启动事实不可再含糊。
						if phase == "before-result" {
							<-cleanupGate
						}
					},
					onTaskComplete: func(TaskResult) { close(cleanupFinished) },
					afterResult:    func() { close(resultReceived) },
					beforeExit: func() {
						close(exitEntered)
						if phase == "after-result" {
							<-exitGate // 结果可先被接收，但最外层退出清理仍未完成，done 保持打开。
						}
					},
					onCancel: func(stage string, cleanupCtx context.Context) {
						cancelObserved <- cancelObservation{stage, cleanupCtx.Err(), context.Cause(cleanupCtx)}
					},
				}
				returned := observeWorkerReturn(ctx, workerCleanupBudget, hooks, cleanupFinished)
				awaitWorkerSignal(t, cleanupEntered, "worker 已启动并进入任务清理")
				wantStage := "等待结果时取消"
				if phase == "after-result" {
					awaitWorkerSignal(t, resultReceived, "owner 已收到任务结果")
					awaitWorkerSignal(t, exitEntered, "worker 停在外层退出清理")
					wantStage = "等待退出时取消"
				}
				cancel(cause)
				guard := time.NewTimer(3 * time.Second)
				defer guard.Stop()
				select {
				case observation := <-cancelObserved:
					if observation.stage != wantStage || observation.cleanupErr != nil || observation.cause != nil {
						t.Fatalf("必须进入预期分支且收尾 context 尚未取消: %+v", observation)
					}
				case early := <-returned:
					t.Fatalf("尚未进入独立收尾就返回，启动后的 worker 丢失 owner: %+v", early)
				case <-guard.C:
					t.Fatal("owner 没有观察已发出的取消")
				}
				// 让同一测试 bubble 的其他 goroutine 到达确定的持久阻塞点：owner 应等 done，worker 应等 gate。
				// 若 owner 错误地提前返回，观察者也已走完并把值放进 returned，所以后面的检查必然看见它。
				// Wait 不是 sleep，不猜它们“跑得够久”；Test 本身还会等待 bubble 内所有 goroutine 退出。
				synctest.Wait()
				select {
				case early := <-returned:
					t.Fatalf("worker 清理/退出尚被屏障挡住，owner 提前返回: %+v", early)
				default:
				}
				releaseCleanup()
				releaseExit()
				actual := awaitWorkerReturn(t, returned)
				if !actual.doneAtReturn || !actual.cleanupAtReturn || !actual.result.Joined || actual.result.Reserved != 0 || actual.result.State != "FAILED" || actual.result.Failure == nil {
					t.Fatalf("取消返回前必须清理预占、保留任务失败并完成 join: %+v", actual)
				}
				if !errors.Is(actual.err, cause) || !errors.Is(actual.err, context.Canceled) || errors.Is(actual.err, ErrWorkerCleanupTimeout) {
					t.Fatalf("成功收尾仍应保留原 context 状态和特定 cause: %v", actual.err)
				}
				t.Logf("%s：cause 保留，独立收尾未取消，预占=0，owner 返回时 done 已关闭", phase)
			})
		})
	}
}

// TestWorkerCancelCleanupTimeout 保持真实任务清理 gate，直到独立预算真正耗尽。
// 这验证的是“清理未确认必须报失败”，不是用短 sleep 假设 goroutine 还没有结束。
// timeout 后测试继续拥有这个故障 gate，放行后再等 done，确保最终没有遗留任务。
func TestWorkerCancelCleanupTimeout(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("取消订单请求，但 worker 清理还没结束")
	defer cancel(cause)
	entered := make(chan struct{})
	cleaned := make(chan struct{})
	done := make(chan struct{})
	gate, release := newWorkerGate(t)
	t.Cleanup(func() { release(); awaitWorkerSignal(t, done, "超时测试的 worker 最终退出") })
	hooks := &workerHooks{
		done: done,
		beforeCleanup: func() {
			close(entered)
			<-gate
		},
		onTaskComplete: func(TaskResult) { close(cleaned) },
	}
	returned := observeWorkerReturn(ctx, 25*time.Millisecond, hooks, cleaned)
	awaitWorkerSignal(t, entered, "worker 已启动且停在预占清理前")
	cancel(cause)
	actual := awaitWorkerReturn(t, returned)
	if !errors.Is(actual.err, cause) || !errors.Is(actual.err, context.Canceled) || !errors.Is(actual.err, ErrWorkerCleanupTimeout) || !errors.Is(actual.err, context.DeadlineExceeded) {
		t.Fatalf("必须同时保留调用取消及独立收尾超时: %v", actual.err)
	}
	if actual.doneAtReturn || actual.cleanupAtReturn || actual.result.Joined || actual.result.State != "CLEANUP_INCOMPLETE" {
		t.Fatalf("预算耗尽时不得伪造已清理/已退出: %+v", actual)
	}
	release()
	awaitWorkerSignal(t, cleaned, "超时后故障 gate 放行，预占清理完成")
	awaitWorkerSignal(t, done, "超时后 worker 真正退出")
	t.Log("25ms 收尾预算实际耗尽，cause 与超时均保留；放行后清理/退出已观察")
}

// TestWorkerRejectsCanceledContextBeforeStart 验证启动前拒绝只是优化，另两个测试仍验启动后责任。
func TestWorkerRejectsCanceledContextBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("调用前已经取消")
	cancel(cause)
	result, err := RunWorker(ctx, true)
	if !errors.Is(err, cause) || !errors.Is(err, context.Canceled) || result.Joined || result.TaskID != "" {
		t.Fatalf("启动前应保留取消原因且不伪造已启动任务: %+v err=%v", result, err)
	}
}
