package app_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"go-demo/lessons/m19_composition_architecture/internal/app"
	"go-demo/lessons/m19_composition_architecture/internal/config"
	"go-demo/lessons/m19_composition_architecture/internal/memory"
	"go-demo/lessons/m19_composition_architecture/internal/order"
	"net/http"
)

func valid() config.Config {
	return config.Config{Listen: "127.0.0.1:0", Version: "test-v1", Token: "test-public-token", RequestMS: 250}
}

// closeOwner 由每个 t.Cleanup 调用，用全新预算结束本测试创建的 App。
func closeOwner(t *testing.T, a *app.App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.Close(ctx); err != nil {
		t.Errorf("cleanup: %v", err)
	}
}

// TestConcreteBoundary 与 M04 契约一致：具体指针 nil 和 ready 分别验证；
// 注入转换不能为业务方法补上自动生命周期。
func TestConcreteBoundary(t *testing.T) {
	var missing *memory.Repository
	if _, err := app.WireService(missing); err == nil {
		t.Fatal("typed nil pointer accepted")
	}
	r := memory.New(nil)
	t.Cleanup(func() { _ = r.Close() })
	if _, err := app.WireService(r); !errors.Is(err, order.ErrNotReady) {
		t.Fatalf("new state: %v", err)
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.WireService(r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	if _, err := app.WireService(r); !errors.Is(err, order.ErrClosed) {
		t.Fatalf("closed state: %v", err)
	}
}

// TestPartialConstructionRealListener 检查实际 TCP 对象和仓储最终状态，而非只匹配错误文案。
// 资源+error 的返回必须清理，errors.Join 应保留构造和额外清理两种原因。
func TestPartialConstructionRealListener(t *testing.T) {
	r := memory.New(nil)
	t.Cleanup(func() { _ = r.Close() })
	primary := errors.New("primary")
	cleanup := errors.New("cleanup")
	var ln net.Listener
	a, err := app.Build(valid(), app.Factories{
		OpenRepository: func() (*memory.Repository, error) { return r, nil },
		Listen: func(addr string) (net.Listener, error) {
			var err error
			ln, err = net.Listen("tcp", addr)
			if err != nil {
				return nil, err
			}
			return ln, primary
		},
		AfterRepositoryClose: func() error { return cleanup },
	})
	if ln != nil {
		t.Cleanup(func() { _ = ln.Close() })
	}
	if a != nil || !errors.Is(err, primary) || !errors.Is(err, cleanup) {
		t.Fatalf("construction reasons: a=%v err=%v", a, err)
	}
	if r.State() != "closed" {
		t.Fatal("repository left ready")
	}
	if ln == nil {
		t.Fatal("listener not acquired")
	}
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Now().Add(500 * time.Millisecond))
	}
	_, err = ln.Accept()
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("actual listener state: %v", err)
	}
}

// TestPartialRepositoryAndValidation 控制两种更早失败。验证失败必须零资源创建；
// factory 已创建 repository 再返回 error 时，root 仍拥有这份半成品的关闭职责。
func TestPartialRepositoryAndValidation(t *testing.T) {
	cfg := valid()
	cfg.Token = ""
	calls := 0
	_, err := app.Build(cfg, app.Factories{OpenRepository: func() (*memory.Repository, error) { calls++; return memory.New(nil), nil }})
	if err == nil || calls != 0 {
		t.Fatalf("validation did not run first: calls=%d err=%v", calls, err)
	}
	r := memory.New(nil)
	primary := errors.New("open failed after allocation")
	_, err = app.Build(valid(), app.Factories{OpenRepository: func() (*memory.Repository, error) { return r, primary }})
	if !errors.Is(err, primary) || r.State() != "closed" {
		t.Fatalf("partial repository: state=%s err=%v", r.State(), err)
	}
}

// TestResultNotDoneAndCloseRetry 用两条真实可观察状态证明 Result 与最终 Done 的先后边界。
// 已取消预算造成一次 Close 未完成；不会提前关闭仓储，放开屏障后新预算能继续收尾。
func TestResultNotDoneAndCloseRetry(t *testing.T) {
	tail := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(tail) }) }
	a, err := app.Build(valid(), app.Factories{FinalizeServe: func() { <-tail }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { release(); closeOwner(t, a) })
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Close 超时可以是 Shutdown 的超时，也可以是 Done 等待超时；二者都必须保留原因。
	short, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	err = a.Close(short)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected incomplete close: %v", err)
	}
	select {
	case err := <-a.Result():
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-a.Done():
		t.Fatal("finalizer still gated but Done closed")
	default:
	}
	if a.Repository().State() != "ready" {
		t.Fatal("repository closed before owner joined")
	}
	release()
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Repository().State() != "closed" {
		t.Fatal("retry did not finish")
	}
	if err := a.Start(); err == nil {
		t.Fatal("reused closed App")
	}
}

// TestNeverStartedClose 验证完整构造后、还没启动 Serve 就退出时也能收回 listener 和仓储。
func TestNeverStartedClose(t *testing.T) {
	a, err := app.New(valid())
	if err != nil {
		t.Fatal(err)
	}
	closeOwner(t, a)
	if a.Repository().State() != "closed" {
		t.Fatal("unstarted resource left open")
	}
	if err := a.Start(); err == nil {
		t.Fatal("closed App started")
	}
}

// TestConstructionPanicUnwindsResources 验证异常展开同样先执行 root 的回滚 defer。
// recover 位于这个测试所在 goroutine 的入口闭包，不把 panic 假装成普通业务 error。
func TestConstructionPanicUnwindsResources(t *testing.T) {
	r := memory.New(nil)
	var observed any
	func() {
		defer func() { observed = recover() }()
		_, _ = app.Build(valid(), app.Factories{
			OpenRepository: func() (*memory.Repository, error) { return r, nil },
			Router:         func(*order.Service, config.Config) (http.Handler, error) { panic("construction-panic") },
		})
	}()
	if observed != "construction-panic" || r.State() != "closed" {
		t.Fatalf("panic=%v state=%s", observed, r.State())
	}
}

// TestForcedCloseStillJoinsRealHandler 让一个真实HTTP handler停在受控屏障且暂不观察ctx。
// 第一Close超时断TCP，但handler仍active；第二Close不能仅因连接表空就提前关闭仓储。
// 最终由测试owner解除屏障，等handler函数返回，再新预算Close完成收尾。
func TestForcedCloseStillJoinsRealHandler(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	a, err := app.Build(valid(), app.Factories{Router: func(*order.Service, config.Config) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(finished)
			close(entered)
			<-release // 明确的故障屏障，测试owner负责解除；不声称第三方driver可被cancel中断。
			w.WriteHeader(http.StatusNoContent)
		}), nil
	}})
	if err != nil {
		open()
		t.Fatal(err)
	}
	t.Cleanup(func() { open(); closeOwner(t, a) })
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, err := client.Get(a.URL() + "/blocked")
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	t.Cleanup(func() {
		client.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		select {
		case <-clientDone:
		case <-ctx.Done():
			t.Error(ctx.Err())
		}
	})
	observe, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-observe.Done():
		t.Fatal(observe.Err())
	}
	for range 2 {
		short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
		err = a.Close(short)
		stop()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("active handler falsely considered joined: %v", err)
		}
		if a.ActiveHandlers() != 1 || a.Repository().State() != "ready" {
			t.Fatalf("premature cleanup: active=%d state=%s", a.ActiveHandlers(), a.Repository().State())
		}
	}
	open()
	select {
	case <-finished:
	case <-observe.Done():
		t.Fatal(observe.Err())
	}
	closeOwner(t, a)
	if a.ActiveHandlers() != 0 || a.Repository().State() != "closed" {
		t.Fatal("final ownership incomplete")
	}
}
