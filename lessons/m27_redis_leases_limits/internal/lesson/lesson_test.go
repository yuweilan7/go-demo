package lesson

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestSETClassification仅验证本课的分类契约，没有替身服务，不能充当真实Redis验收。
// 尤其把网络deadline留成UNKNOWN，防止调用者误以为失败返回false就可以重试写。
func TestSETClassification(t *testing.T) {
	cases := []struct {
		value string
		err   error
		want  acquireState
	}{
		{"OK", nil, granted}, {"", redis.Nil, contended},
		{"", redis.ErrNoScript, rejected}, // 真实库导出的服务端错误值；此处只验分类，不发SET。
		{"", redis.ErrClosed, rejected}, {"", replyTimeout{}, unknown},
	}
	for _, test := range cases {
		got := classifySET(test.value, test.err)
		if got.State != test.want {
			t.Fatalf("state=%s want=%s error=%v", got.State, test.want, test.err)
		}
		if test.want == contended && got.Err != nil {
			t.Fatal("竞争不应成为基础设施error")
		}
		if test.err != nil && test.want != contended && !errors.Is(got.Err, test.err) {
			t.Fatal("分类丢失原错误")
		}
	}
}

// TestNamesProtectScope证明标识符严格隔离和身份编码不会把不同组合拼成同一键。
// 不访问任何服务；拒绝主库/其他课/注入名字防止清理范围悄悄扩大。
func TestNamesProtectScope(t *testing.T) {
	for _, name := range []string{"m20_0123456789abcdef01234567", "orders", "m27_x;DROP TABLE business"} {
		if _, err := quotedTable(name); err == nil {
			t.Fatalf("unexpected accepted table %q", name)
		}
	}
	l := &lab{report: &Report{Prefix: "golearn:m27:test:"}}
	a := l.limitKey("a", "bc")
	b := l.limitKey("ab", "c")
	if a == b || a != l.limitKey("a", "bc") || len(l.report.Keys) != 2 {
		t.Fatal("用户/接口隔离或去重登记失败")
	}
}

// probeContext在join实际求值ctx.Done时通知测试owner，代替sleep猜join已开始。
// 嵌入Context保留Deadline/Err/Value；仅观察Done调用，不改变真实取消语义。
type probeContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (p *probeContext) Done() <-chan struct{} {
	p.once.Do(func() { close(p.entered) })
	return p.Context.Done()
}

// TestResultRequiresFinishedAndPreservesCleanup用独立完成门检验wrapper的最后defer。
// Result已可读时join必须仍在等待Finished；释放后原错误和cleanup错误都保留。
// 辅助owner goroutine也有ownerDone，测试任何失败出口都先取消/放门/真正join。
func TestResultRequiresFinishedAndPreservesCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	report := &Report{}
	g := newGroup(ctx, report)
	gate, cleanupEntered := make(chan struct{}), make(chan struct{})
	open := onceRelease(gate)
	g.releases = []func(){open}
	workFailure, cleanupFailure := errors.New("work failed"), errors.New("wrapper cleanup failed")
	worker := g.start("test work", func(context.Context) outcome { return outcome{Value: 42, Err: workFailure} }, func() error {
		close(cleanupEntered)
		<-gate
		return cleanupFailure
	})
	var mainErr error
	ownerDone := make(chan struct{})
	ownerStarted := false
	defer func() {
		g.cancel()
		open()
		if ownerStarted {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cleanupCancel()
			if err := await(cleanupCtx, ownerDone, "测试join owner完成"); err != nil {
				t.Error(err)
				return
			}
		}
		g.finish(&mainErr)
	}()
	if err := await(ctx, cleanupEntered, "wrapper清理门"); err != nil {
		t.Fatal(err)
	}
	if len(worker.result) != 1 {
		t.Fatal("清理已开始时Result应已发送")
	}
	probe := &probeContext{Context: ctx, entered: make(chan struct{})}
	var result outcome
	var joinErr error
	ownerStarted = true
	go func() { defer close(ownerDone); result, joinErr = g.join(probe, worker) }()
	if err := await(ctx, probe.entered, "join实际等待Finished"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ownerDone:
		t.Fatal("Result可读却跳过了wrapper清理")
	default:
	}
	open()
	if err := await(ctx, ownerDone, "join返回"); err != nil {
		t.Fatal(err)
	}
	if result.Value != 42 || !errors.Is(result.Err, workFailure) || !errors.Is(joinErr, cleanupFailure) {
		t.Fatalf("result=%+v cleanup=%v", result, joinErr)
	}
	if report.TasksStarted != 1 || report.TasksJoined != 1 {
		t.Fatal("真实join计数错误")
	}
}

// TestEarlyFailureCancelsUnblocksAndJoins检验owner在没有消费任何Result时就失败。
// worker等待取消，wrapper再等待门；finish必须同时取消、放门、新预算join，
// 最终errors.Is能找到owner、工作Canceled和wrapper清理三种原因。
func TestEarlyFailureCancelsUnblocksAndJoins(t *testing.T) {
	report := &Report{}
	g := newGroup(context.Background(), report)
	gate := make(chan struct{})
	g.releases = []func(){onceRelease(gate)}
	ownerFailure, cleanupFailure := errors.New("owner failed early"), errors.New("cleanup failed")
	g.start("early work", func(ctx context.Context) outcome { <-ctx.Done(); return outcome{Err: ctx.Err()} }, func() error { <-gate; return cleanupFailure })
	err := ownerFailure
	g.finish(&err)
	if !errors.Is(err, ownerFailure) || !errors.Is(err, context.Canceled) || !errors.Is(err, cleanupFailure) {
		t.Fatalf("原始/工作/清理错误链不完整: %v", err)
	}
	if report.TasksJoined != 1 {
		t.Fatal("取消并不等于join，计数应证明Finished")
	}
}

// TestIntegration实际运行整课，最后用另一个真实client独立查询清理清单。
// 默认明确SKIP，只有GOLEARN_M27_INTEGRATION=1才读取私有配置访问真实Redis/MySQL。
func TestIntegration(t *testing.T) {
	if os.Getenv("GOLEARN_M27_INTEGRATION") != "1" {
		t.Skip("未请求真实Redis+MySQL；设GOLEARN_M27_INTEGRATION=1")
	}
	cfg, err := LoadConfig(DefaultIndex())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	report, err := Run(ctx, cfg, "all", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Cleaned || report.Assertions < 28 || report.TasksJoined != report.TasksStarted || report.TasksStarted != 17 || len(report.Tables) != 2 {
		t.Fatalf("课程断言/收尾未完成: %+v", report)
	}
	checker := &lab{cfg: cfg, stats: &socketStats{}}
	client := redis.NewClient(checker.redisOptions())
	defer func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	}()
	remaining, err := client.Exists(ctx, report.Keys...).Result()
	if err != nil || remaining != 0 {
		t.Fatalf("另client清理后EXISTS=%d error=%v", remaining, err)
	}
	// SQLDROP事实已在Run的独立information_schema查询中验证。另client重复查Redis
	// 是为了排除只把本地registry清空就宣称服务端没键的错误实现。
	t.Logf("REAL PASS run=%s assertions=%d tasks=%d/%d exact_keys=%v exact_tables=%v sockets=%d/%d", report.RunID, report.Assertions,
		report.TasksJoined, report.TasksStarted, report.Keys, report.Tables, report.SocketsClosed, report.SocketsOpened)
}
