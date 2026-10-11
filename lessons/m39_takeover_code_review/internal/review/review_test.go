package review

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

// TestContiguousPrefix独立给collector乱序完成事实，断言协议不变量而非字符串日志。
// 1/3先完成不能越0/2缺口；0补齐只能进2；2补齐才进4。这只是算法辅助证据。
func TestContiguousPrefix(t *testing.T) {
	p := &prefix{}
	for _, step := range []struct{ offset, want int64 }{{1, 0}, {3, 0}, {0, 2}, {2, 4}} {
		if got := p.mark(step.offset); got != step.want {
			t.Fatalf("offset=%d next=%d want=%d", step.offset, got, step.want)
		}
	}
}

// TestLanguageContracts独立执行nil/defer事实，限制为语言层，实库证据在Live。
func TestLanguageContracts(t *testing.T) {
	if e := Helpers(io.Discard); e != nil {
		t.Fatal(e)
	}
}

// TestTaskPanicIdentity让真实goroutine panic一个error，owner最终Done之后读结果。
// 它证明动态error身份没被fmt文本化，不能用它代替HTTP recovery或SQL回滚证据。
func TestTaskPanicIdentity(t *testing.T) {
	cause := errors.New("动态error panic")
	job := startTask("panic identity", func() error { panic(cause) })
	if e := job.join(); !errors.Is(e, cause) {
		t.Fatalf("panic原cause丢失: %v", e)
	}
}

// requireLive让普通test明确SKIP；默认demo仍实际完整Live。只有显式开关1才运行。
func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("GOLEARN_M39_INTEGRATION") != "1" {
		t.Skip("真实服务测试须GOLEARN_M39_INTEGRATION=1；SKIP不是证明")
	}
}

// TestLiveIsolation针对最小PR影响范围独立运行真实SQL/Redis/gRPC/HTTP与授权回归。
func TestLiveIsolation(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var log bytes.Buffer
	if e := Isolation(ctx, &log, false); e != nil {
		t.Fatalf("%v\n%s", e, log.String())
	}
	t.Log(log.String())
}

// TestLiveReviewCases覆盖另外实际故障/owner路径，不重跑M01～M38历史课程。
func TestLiveReviewCases(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	for _, trial := range []struct {
		name string
		run  func(context.Context, io.Writer) error
	}{
		{"transactions", Transactions}, {"contexts", Contexts}, {"offsets", Offsets}, {"pools", Pools}, {"unknown", Unknown},
	} {
		t.Run(trial.name, func(t *testing.T) {
			var log bytes.Buffer
			if e := trial.run(ctx, &log); e != nil {
				t.Fatalf("%v\n%s", e, log.String())
			}
			t.Log(log.String())
		})
	}
}

// TestLiveEarlyOwners验证observer提前error/panic/cancel时，真实在途Tx/owner的最终
// Done与清理顺序；beforeCleanup先检查SQL0单才删对象，原cause仍可errors.Is识别。
func TestLiveEarlyOwners(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var log bytes.Buffer
	if e := EarlyOwners(ctx, &log); e != nil {
		t.Fatalf("%v\n%s", e, log.String())
	}
	t.Log(log.String())
}
