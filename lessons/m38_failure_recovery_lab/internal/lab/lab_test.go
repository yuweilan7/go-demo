package lab

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// TestErrorTree 验证独立语义：动态error panic身份与严重UNKNOWN/cleanup均保留。
// 它只是分类辅助测试，不能冒称实库rollback或通信截断已经发生。
func TestErrorTree(t *testing.T) {
	cause := errors.New("test原cause")
	got := panicCause("worker", cause)
	if !errors.Is(got, cause) {
		t.Fatal("recover丢失原error身份")
	}
	joined := errors.Join(got, b.ErrUnknown, b.ErrCleanup)
	if classify(joined) != "CLEANUP_FAILED" || !errors.Is(joined, b.ErrUnknown) || !errors.Is(joined, cause) {
		t.Fatal("多重严重原因不应被成功归一化")
	}
}

// TestJobPanic 同一个真实goroutine入口recover保留cause；只等待结果或取消通知
// 不能读其error。本测试等Done后读取，证明panic最终归owner而不是静默丢失。
func TestJobPanic(t *testing.T) {
	cause := errors.New("真实worker panic cause")
	j := launch(context.Background(), func(context.Context) error { panic(cause) })
	t.Cleanup(func() {
		if e := j.finish(); e != nil {
			t.Error(e)
		}
	})
	if e := b.JoinDone(j.done, "panic worker最终Done", time.Second); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(j.err, cause) {
		t.Fatalf("入口recover应保留原cause: %v", j.err)
	}
}

// TestGateAndFinalDone 用同步门证明“取消通知/阶段 ≠ worker完成”。worker在观察
// 取消后还有明确尾部等待，测试只能放尾部门后才join最终Done，不能sleep猜调度。
func TestGateAndFinalDone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	g := newGate()
	tail := make(chan struct{})
	enteredTail := make(chan struct{})
	j := launch(ctx, func(ctx context.Context) error { e := g.wait(ctx); close(enteredTail); <-tail; return e })
	// 取得job立即登记Cleanup，哪条断言失败都取消/放门/放尾部并最终加入任务。
	t.Cleanup(func() {
		j.cancel()
		g.open()
		select {
		case <-tail:
		default:
			close(tail)
		}
		if e := j.finish(); e != nil {
			t.Error(e)
		}
	})
	if e := await(ctx, g.entered, "hook entered"); e != nil {
		t.Fatal(e)
	}
	j.cancel()
	if e := await(ctx, g.observed, "hook观察取消"); e != nil {
		t.Fatal(e)
	}
	if e := await(ctx, enteredTail, "尾部清理"); e != nil {
		t.Fatal(e)
	}
	select {
	case <-j.done:
		t.Fatal("取消/阶段不可替代最终Done")
	default:
	}
	close(tail)
	if e := await(ctx, j.done, "worker最终Done"); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(j.err, context.Canceled) {
		t.Fatal("取消身份应保留")
	}
}

// TestProtocolFrames 用实际序列化协议字节验证包边界，防止proxy按TCP读长度猜。
// 这里只证明解析器，不证明远端COMMIT或Redis已接受；后者由LiveProxies独立读回。
func TestProtocolFrames(t *testing.T) {
	raw := []byte{7, 0, 0, 1, 0, 1, 2, 3, 4, 5, 6}
	got, e := packet(bytes.NewReader(raw))
	if e != nil || !bytes.Equal(got, raw) {
		t.Fatalf("packet frame %v", e)
	}
	if _, e = packet(bytes.NewReader(raw[:8])); e == nil {
		t.Fatal("截断packet不能成功")
	}
	for _, value := range []string{"+OK\r\n", "$3\r\nabc\r\n", "%1\r\n+key\r\n:1\r\n", "*2\r\n$3\r\nGET\r\n$1\r\na\r\n"} {
		got, e := respFrame(bufio.NewReader(strings.NewReader(value)), 0)
		if e != nil || string(got) != value {
			t.Fatalf("RESP complete frame %q %v", value, e)
		}
	}
}

// live 独立断言入口采用明确环境开关。没有服务授权时SKIP只说明未运行，STATUS
// 不把它计实库通过；开启时完整业务矩阵也必须精确清理并真实join所有进程/代理。
func live(t *testing.T, scenario string) {
	t.Helper()
	if os.Getenv("GOLEARN_M38_INTEGRATION") != "1" {
		t.Skip("未启用真实服务；设置 GOLEARN_M38_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	var log strings.Builder
	// Run内SafeWriter保护多goroutine写，只有Run所有owner收尾后才读log结果。
	if e := Run(ctx, scenario, io.MultiWriter(os.Stdout, &log)); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(log.String(), "cleanup_ok=true") || !strings.Contains(log.String(), "M38 PASS") {
		t.Fatal("必须由真实最终清理与断言生成通过日志")
	}
}

func TestLiveCore(t *testing.T)    { live(t, "core") }
func TestLiveProxies(t *testing.T) { live(t, "proxies") }
func TestLiveKills(t *testing.T)   { live(t, "kills") }
