package course

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// TestDecimalCents 独立验证金额边界；错误类型不能靠被测函数自己宣称合法。
func TestDecimalCents(t *testing.T) {
	for text, want := range map[string]int64{"0.00": 0, "0.10": 10, "16777217.03": 1677721703, "9999999999.99": 999999999999} {
		got, err := decimalCents(text)
		if err != nil || got != want {
			t.Fatalf("%q got=%d err=%v want=%d", text, got, err, want)
		}
	}
	for _, text := range []string{"-1.00", "1.2", "1.000", "NaN", "10000000000.00", "1e2.00", ".00"} {
		if _, err := decimalCents(text); err == nil {
			t.Fatalf("必须拒绝%q", text)
		}
	}
}

// TestOwnedIdentifiers 证明清理出口不能接收别课/任意SQL表名；生成前缀是必要所有权边界。
func TestOwnedIdentifiers(t *testing.T) {
	if _, err := quote("m24_0123456789abcdef01234567_orders"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"orders", "m20_0123456789abcdef01234567", "m24_0123456789abcdef01234567_orders`", "m24_0123456789abcdef01234567_unknown"} {
		if _, err := quote(name); err == nil {
			t.Fatalf("必须拒绝%q", name)
		}
	}
}

// TestResultDoesNotMeanFinished 用两个gate证明wrapper的最后收尾尚在等待。
// 结果先可读，finished仍未关闭；释放gate后join才可以完成，并保留work/cleanup两种错误。
func TestResultDoesNotMeanFinished(t *testing.T) {
	workErr := errors.New("work failed")
	cleanupErr := errors.New("cleanup failed")
	cleanupEntered := make(chan struct{})
	release := make(chan struct{})
	task := startTask(context.Background(), func(context.Context) error { return workErr }, func() error { close(cleanupEntered); <-release; return cleanupErr })
	// defer保证任何Fatal路径也先释放gate，再等待worker，测试本身不能泄漏任务。
	released := false
	defer func() {
		if !released {
			close(release)
		}
		_ = task.stopAndJoin()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	select {
	case <-cleanupEntered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-task.finished:
		t.Fatal("清理gate未释放，不能Finished")
	default:
	}
	if len(task.result) != 1 {
		t.Fatal("业务结果应已经缓冲，但不能作为Finished")
	}
	close(release)
	released = true
	err := task.join(ctx)
	if !errors.Is(err, workErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("必须保留两条错误链: %v", err)
	}
}

// TestMySQL 是完整真实集成入口：默认明确Skip，显式环境开关才访问课程实例。
// Run内断言独立SELECT事实，测试再核对关键事件和最终清理；不把日志PASS当唯一证据。
func TestMySQL(t *testing.T) {
	if os.Getenv("GOLEARN_INTEGRATION") != "1" {
		t.Skip("未请求真实MySQL；设置GOLEARN_INTEGRATION=1")
	}
	config, err := LoadConfig(ServiceIndexPath())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var log bytes.Buffer
	if err = Run(ctx, config, &log); err != nil {
		t.Fatalf("真实课程失败: %v\n%s", err, log.String())
	}
	for _, event := range []string{"[bad types]", "[PLAN covering-feed]", "[mutable-sort]", "[snapshot repair]", "[migration failed accurately]", "[implicit commit] UPDATE=30", "[DDL actual wait]", "[DDL lock finished]", "[DB finished] open=0 inuse=0 idle=0"} {
		if !strings.Contains(log.String(), event) {
			t.Fatalf("缺少真实事件%s\n%s", event, log.String())
		}
	}
	if strings.Count(log.String(), "remaining=0 check_ok=true") != 7 {
		t.Fatalf("必须核对七张精确独占表不存在\n%s", log.String())
	}
	t.Log(log.String())
}

// cancelWriter 是测试专用的可观察屏障：owner输出准备Explain的事件时发取消通知。
// 输出仅owner写，ctx.CancelFunc可安全调用；它不修改SQL/驱动，也不充当数据库替身。
type cancelWriter struct {
	log    bytes.Buffer
	cancel context.CancelFunc
	fired  bool
}

func (w *cancelWriter) Write(p []byte) (int, error) {
	n, err := w.log.Write(p)
	if !w.fired && bytes.Contains(p, []byte("[SQL covering-feed]")) {
		w.fired = true
		w.cancel()
	}
	return n, err
}

// TestMySQLCanceledOwner 实测准备种子已commit、Explain尚未发出时取消主预算。
// 独占表仍须由新cleanup预算精确DROP并读回0，pool须关闭；不能用取消后的ctx清理。
func TestMySQLCanceledOwner(t *testing.T) {
	if os.Getenv("GOLEARN_INTEGRATION") != "1" {
		t.Skip("未请求真实MySQL取消清理")
	}
	config, err := LoadConfig(ServiceIndexPath())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out := &cancelWriter{cancel: cancel}
	err = Run(ctx, config, out)
	if !out.fired || !errors.Is(err, context.Canceled) {
		t.Fatalf("应在Explain之前观察取消: fired=%t err=%v\n%s", out.fired, err, out.log.String())
	}
	if strings.Count(out.log.String(), "remaining=0 check_ok=true") != 3 || !strings.Contains(out.log.String(), "[DB finished] open=0 inuse=0 idle=0") {
		t.Fatalf("取消后必须真实核对已创建的3张表和池清理\n%s", out.log.String())
	}
	t.Log(out.log.String())
}
