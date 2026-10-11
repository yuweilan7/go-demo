package course

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDecimalCents用独立业务期望检查金额边界，避免测试只是重复实现算式。
// 这是纯函数辅助证据，绝不算MySQL DECIMAL映射已经通过。
func TestDecimalCents(t *testing.T) {
	for input, expected := range map[string]int64{"0.10": 10, "12.34": 1234, "99999999.99": 9999999999} {
		actual, err := decimalCents(input)
		if err != nil || actual != expected {
			t.Fatalf("%q：actual=%d expected=%d err=%v", input, actual, expected, err)
		}
	}
	for _, input := range []string{"-1.00", "1.2", "1.234", "100000000.00", "1e2.00", "1;DROP.00", ""} {
		if _, err := decimalCents(input); err == nil {
			t.Fatalf("必须拒绝金额边界外输入 %q", input)
		}
	}
}

// TestIdentifierBoundary证明未经验证的标识符在SQL构造前就被拒绝。
// SQL值参数仍由真实MySQL实验检验，不能用本断言代替数据库参数绑定。
func TestIdentifierBoundary(t *testing.T) {
	if _, err := quotedTable("m20_0123456789abcdef01234567"); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"m20_x", "other", "m20_0123456789abcdef01234567` ; DROP TABLE other"} {
		if _, err := quotedTable(input); err == nil {
			t.Fatalf("必须拒绝标识符 %q", input)
		}
	}
}

// TestEarlyReturnCancelsAndJoins不用数据库替身，它独立检验owner在提前失败时
// 是否取消当前等待并收到Finished关闭，同时保留主错误和worker/清理错误。
func TestEarlyReturnCancelsAndJoins(t *testing.T) {
	started := make(chan struct{})
	cleanupFailure := errors.New("合成清理失败，用于错误链断言")
	worker := startTask(context.Background(), time.Second, func(ctx context.Context) outcome {
		close(started)
		<-ctx.Done() // worker实际等待ctx通知，owner负责发取消并join。
		return outcome{Err: ctx.Err(), CleanupErr: cleanupFailure}
	})
	<-started // channel屏障证明worker已进入等待路径，不用sleep猜调度。
	mainFailure := errors.New("合成主流程失败")
	err := mainFailure
	worker.stopAndJoin(&err)
	if worker.joined == nil || !errors.Is(err, mainFailure) || !errors.Is(err, context.Canceled) || !errors.Is(err, cleanupFailure) {
		t.Fatalf("owner必须join并保留三种原因：%v", err)
	}
}

// finishedProbeContext只为测试观察join实际进入等待Finished的select。
// Done被求值时close entered；这不是取消工作ctx，也不改变底层独立预算。
// once使这个完成广播重复观察安全，避免用sleep猜owner是否已经开始join。
type finishedProbeContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (probe *finishedProbeContext) Done() <-chan struct{} {
	probe.once.Do(func() { close(probe.entered) })
	return probe.Context.Done()
}

// TestResultReadyStillWaitsForWrapperCleanup证明收到一次结果不能替代Finished。
// worker被取消后先交付结果，附加cleanup仍停在release gate；owner已经进入join
// 也不能返回。只在放门、wrapper cleanup返回、cancel执行后Finished才关闭。
func TestResultReadyStillWaitsForWrapperCleanup(t *testing.T) {
	testCtx, testCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer testCancel()
	cleanupEntered := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCleanup) }) }
	ownerFinished := make(chan struct{})
	ownerStarted := false
	innerCleanupFailure := errors.New("工作函数清理失败")
	wrapperCleanupFailure := errors.New("wrapper附加资源清理失败")
	worker := startOwnedTask(context.Background(), 5*time.Second, func(ctx context.Context) outcome {
		<-ctx.Done()
		return outcome{Err: ctx.Err(), CleanupErr: innerCleanupFailure}
	}, func() error {
		close(cleanupEntered)
		<-releaseCleanup // test是gate owner；生产任务没有这个人工故障门。
		return wrapperCleanupFailure
	})
	defer func() {
		// t.Fatal会让本测试goroutine Goexit，仍会执行defer。每个提前失败也放门、
		// cancel并用新预算join；owner已启动时只等它的完成广播，避免两个owner
		// 并发读写worker.pending/joined缓存。
		release()
		worker.cancel()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if ownerStarted {
			select {
			case <-ownerFinished:
			case <-cleanupCtx.Done():
				t.Errorf("测试owner清理未完成：%v", cleanupCtx.Err())
			}
		} else if _, joinErr := worker.join(cleanupCtx); joinErr != nil {
			t.Errorf("测试worker独立清理join失败：%v", joinErr)
		}
	}()
	worker.cancel()
	// 结果已可读，先消费到pending。这个缓存只表示工作结果，不能标成joined。
	select {
	case result := <-worker.result:
		worker.pending = &result
	case <-testCtx.Done():
		t.Fatal("取消后的工作结果没有交付")
	}
	select {
	case <-cleanupEntered:
	case <-testCtx.Done():
		t.Fatal("wrapper没有进入附加清理")
	}
	select {
	case <-worker.finished:
		t.Fatal("清理gate尚未释放，Finished不能关闭")
	default:
	}
	probe := &finishedProbeContext{Context: testCtx, entered: make(chan struct{})}
	joined := make(chan error, 1)
	mainFailure := errors.New("owner原始业务失败")
	ownerStarted = true
	go func() {
		defer close(ownerFinished) // owner所有join/收尾之后最后关闭，测试defer重复观察。
		result, joinErr := worker.join(probe)
		err := errors.Join(mainFailure, result.Err, result.CleanupErr, joinErr)
		worker.stopAndJoin(&err) // 已join缓存仍核对Finished，独立预算做最终收尾。
		joined <- err
	}()
	select {
	case <-probe.entered:
		// Done求值屏障证明owner已经进入join的Finished等待选择。
	case err := <-joined:
		t.Fatalf("清理门未开时owner提前返回：%v", err)
	case <-testCtx.Done():
		t.Fatal("owner没有进入Finished等待")
	}
	select {
	case err := <-joined:
		t.Fatalf("结果已消费但wrapper未清理，join不能成功：%v", err)
	default:
	}
	release()
	select {
	case err := <-joined:
		if !errors.Is(err, mainFailure) || !errors.Is(err, context.Canceled) ||
			!errors.Is(err, innerCleanupFailure) || !errors.Is(err, wrapperCleanupFailure) {
			t.Fatalf("必须保留业务/工作取消/两层清理错误：%v", err)
		}
	case <-testCtx.Done():
		t.Fatal("清理门释放后owner未退出")
	}
	select {
	case <-worker.finished:
		// 广播可重复观察，收到joined结果没有消费Finished事实。
	default:
		t.Fatal("owner返回时Finished必须已关闭")
	}
}

// TestMySQL需要显式GOLEARN_INTEGRATION=1，运行Run的全部真实实验和内部SELECT断言。
// 默认跳过实库是清楚的范围标签，PASS的纯单测不能冒充这个测试已经运行。
func TestMySQL(t *testing.T) {
	if os.Getenv("GOLEARN_INTEGRATION") != "1" {
		t.Skip("实库未请求：设置 GOLEARN_INTEGRATION=1 才执行真实MySQL全范围")
	}
	config, err := LoadConfig(ServiceIndexPath())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err = Run(ctx, config, &output); err != nil {
		t.Fatalf("真实MySQL失败：%v\n%s", err, output.String())
	}
	// 内容断言确认跑到了关键结束点；业务不变量由Run里的真实SELECT证明。
	for _, marker := range []string{"[PASS]", "[Rows修复/副作用SELECT]", "[Tx修复/副作用SELECT]", "[SQL取消/副作用SELECT]", "remaining=0", "Close 后 open=0 inuse=0 idle=0"} {
		if !strings.Contains(output.String(), marker) {
			t.Fatalf("缺少真实完成证据 %q\n%s", marker, output.String())
		}
	}
	t.Log(output.String())
}
