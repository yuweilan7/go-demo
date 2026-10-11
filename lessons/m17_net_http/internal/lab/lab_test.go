package lab

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// testRig 由独立测试调用，测试自己的 HTTP 行为而非仅搜索 demo 的 PASS 文本。
// cleanup 顺序先 client 空闲池再服务器；Close 的错误和 ownership 未清零都会失败。
func testRig(t *testing.T, maxConns int) (context.Context, *Fixture, *Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	f, err := NewFixture(0)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	c := NewClient(maxConns, time.Second, 3*time.Second)
	t.Cleanup(func() {
		cancel()
		c.CloseIdle()
		if open := c.OpenBodies.Load(); open != 0 {
			t.Errorf("响应体 owner 未清零: %d", open)
		}
		if owned := c.OwnedRequests.Load(); owned != 0 {
			t.Errorf("owned request未finished: %d", owned)
		}
		if err := f.Close(); err != nil {
			t.Error(err)
		}
		_, live, _ := f.Counts()
		if live != 0 {
			t.Errorf("连接未清零: %d", live)
		}
	})
	return ctx, f, c
}

// ownRequestForTest必须在doAsync之后立即调用。t.Cleanup按后注册先执行，故本清理
// 先于testRig的fixture清理：t.Fatal也先cancel、Open gate、独立join，不遗留owned请求。
func ownRequestForTest(t *testing.T, request *OwnedRequest, unblock func()) {
	t.Helper()
	t.Cleanup(func() {
		if err := request.StopAndJoin(unblock); err != nil {
			t.Errorf("请求owner收尾失败: %v", err)
		}
	})
}

// TestConcurrentIdempotency 同时发送 8 个同 key 请求，独立读取账本与返回 ID，
// 验证查重和写入是一项原子决策。它不假定 goroutine 调度顺序或哪个请求最先到达。
func TestConcurrentIdempotency(t *testing.T) {
	ctx, f, c := testRig(t, 4)
	start := make(chan struct{})
	results := make(chan Reply, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			select {
			case <-start:
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
			reply, err := RequestJSON(ctx, c, "POST", f.URL+"/orders", strings.NewReader(orderJSON), "concurrent-key", 2048)
			results <- reply
			errs <- err
		})
	}
	close(start)
	wg.Wait() // 每个网络调用具有 ctx/Client.Timeout 上限，等待的是 owned goroutine 退出。
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		reply := <-results
		if reply.Status != 201 || reply.Envelope.OrderID != 1 || !reply.Envelope.Committed {
			t.Fatalf("协议不一致: %+v", reply)
		}
	}
	if f.Ledger.Count() != 1 {
		t.Fatalf("重复副作用: rows=%d", f.Ledger.Count())
	}
}

// TestUnsafeDuplicateHasTwoEffects 独立检查危险入口的真实账本，两次成功响应没有抹平
// 重复业务事实；这个故障被限定在测试自己的 fixture 里。
func TestUnsafeDuplicateHasTwoEffects(t *testing.T) {
	ctx, f, c := testRig(t, 2)
	for i := 0; i < 2; i++ {
		if _, err := RequestJSON(ctx, c, "POST", f.URL+"/orders?unsafe=1", strings.NewReader(orderJSON), "same", 2048); err != nil {
			t.Fatal(err)
		}
	}
	if f.Ledger.Count() != 2 {
		t.Fatalf("应观察两次副作用: %d", f.Ledger.Count())
	}
}

// TestLostOrderIsCommittedWithoutAutomaticReplay 使用已复用连接制造提交后丢响应，
// 不用日志猜是否重发。GetBody=nil 的 POST 只进入一次 handler，但账本已新增一行。
func TestLostOrderIsCommittedWithoutAutomaticReplay(t *testing.T) {
	ctx, f, c := testRig(t, 1)
	if _, err := RequestJSON(ctx, c, "GET", f.URL+"/ok", nil, "", 1024); err != nil {
		t.Fatal(err)
	}
	_, _, before := f.Counts()
	_, err := RequestJSON(ctx, c, "POST", f.URL+"/lost-order", strings.NewReader(orderJSON), "lost-reply", 1024)
	_, _, after := f.Counts()
	if err == nil || after-before != 1 || f.Ledger.Count() != 1 {
		t.Fatalf("丢响应/重发边界错误: err=%v handlers=%d rows=%d", err, after-before, f.Ledger.Count())
	}
}

// TestBodyLimitAndEarlyEOF 直接检查字节数/error 原因与最终 ownership，而非依据 HTTP200。
func TestBodyLimitAndEarlyEOF(t *testing.T) {
	ctx, f, c := testRig(t, 2)
	reply, err := RequestJSON(ctx, c, "GET", f.URL+"/large", nil, "", 64)
	if !errors.Is(err, ErrTooLarge) || reply.Bytes != 65 {
		t.Fatalf("读取边界错误: reply=%+v err=%v", reply, err)
	}
	if c.OpenBodies.Load() != 0 {
		t.Fatal("超限未 Close body")
	}
	reply, err = RequestJSON(ctx, c, "GET", f.URL+"/early", nil, "", 128)
	if reply.Status != 200 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("早断开应在 body 阶段失败: reply=%+v err=%v", reply, err)
	}
}

// TestHTTP200IsNotCommit 独立检查 legacy 的“协议成功但未写账本”和固定状态码规则。
func TestHTTP200IsNotCommit(t *testing.T) {
	ctx, f, c := testRig(t, 2)
	reply, err := RequestJSON(ctx, c, "GET", f.URL+"/legacy", nil, "", 1024)
	var business *BusinessError
	if reply.Status != 200 || !errors.As(err, &business) || business.Code != "NOT_COMMITTED" || f.Ledger.Count() != 0 {
		t.Fatalf("把HTTP200误判为提交: %+v %v", reply, err)
	}
	reply, err = RequestJSON(ctx, c, "GET", f.URL+"/late-status", nil, "", 1024)
	if reply.Status != 200 || err == nil {
		t.Fatalf("已选择的200被错误改写: %+v %v", reply, err)
	}
	reply, err = RequestJSON(ctx, c, "GET", f.URL+"/fixed-status", nil, "", 1024)
	var status *HTTPError
	if reply.Status != 409 || !errors.As(err, &status) {
		t.Fatalf("修复应在写之前选409: %+v %v", reply, err)
	}
}

// TestPoolWaitDoesNotReachServer 只打开第一个 body 后发第二次请求，使用 ledger 外的
// handler_calls 证明第二请求根本没被发送；CloseIdleConnections 无法代替释放 body。
func TestPoolWaitDoesNotReachServer(t *testing.T) {
	ctx, f, c := testRig(t, 1)
	req, _ := http.NewRequestWithContext(ctx, "GET", f.URL+"/leak", nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	c.CloseIdle()
	shortCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, err = RequestJSON(shortCtx, c, "GET", f.URL+"/ok", nil, "", 1024)
	cancel()
	_, _, handled := f.Counts()
	if !errors.Is(err, context.DeadlineExceeded) || handled != 1 || c.OpenBodies.Load() != 1 {
		t.Fatalf("未证明池等待: handled=%d bodies=%d err=%v", handled, c.OpenBodies.Load(), err)
	}
	_ = resp.Body.Close()
}

// TestCancellationSideEffectWindow 分别直接读账本；Cancel、server observed、handler Done
// 是三项不同事实。Release 只在危险分支观察到取消之后发，建立提交窗口的可证明顺序。
func TestCancellationSideEffectWindow(t *testing.T) {
	for _, path := range []string{"/cancel-order", "/cooperative-order"} {
		t.Run(path, func(t *testing.T) {
			ctx, f, c := testRig(t, 2)
			g := f.Gate("cancel")
			request := doAsync(ctx, c, "POST", f.URL+path+"?gate=cancel", orderJSON)
			ownRequestForTest(t, request, g.Open)
			if err := waitSignal(ctx, g.Entered, "request entered"); err != nil {
				t.Fatal(err)
			}
			request.Cancel()
			requestErr, waitErr := request.Result(ctx)
			if waitErr != nil {
				t.Fatal(waitErr)
			}
			if !errors.Is(requestErr, context.Canceled) {
				t.Fatalf("client没有取消: %v", requestErr)
			}
			if err := waitSignal(ctx, g.Canceled, "server observed"); err != nil {
				t.Fatal(err)
			}
			if f.Ledger.Count() != 0 {
				t.Fatal("Release前不应提交")
			}
			g.Open()
			if err := waitSignal(ctx, g.Done, "handler done"); err != nil {
				t.Fatal(err)
			}
			want := 0
			if path == "/cancel-order" {
				want = 1
			}
			if f.Ledger.Count() != want {
				t.Fatalf("取消后的业务事实错误: want=%d got=%d", want, f.Ledger.Count())
			}
		})
	}
}

// TestInputRejectionAndProxyBoundary 检查未知字段/尾随JSON/非法数量和身份边界，
// 最后把伪造 X-Forwarded-For 送入真实 handler 并确认它仍使用 TCP RemoteAddr。
func TestInputRejectionAndProxyBoundary(t *testing.T) {
	ctx, f, c := testRig(t, 2)
	for _, data := range []string{`{"sku":"sku-m17","qty":0}`, `{"sku":"sku-m17","qty":1,"admin":true}`, orderJSON + orderJSON} {
		_, err := RequestJSON(ctx, c, "POST", f.URL+"/orders", strings.NewReader(data), "rejected", 1024)
		var status *HTTPError
		if !errors.As(err, &status) || (status.Status != 400 && status.Status != 422) {
			t.Fatalf("应拒绝数据 %s: %v", data, err)
		}
	}
	if f.Ledger.Count() != 0 {
		t.Fatal("非法输入发生副作用")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", f.URL+"/identity", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !strings.Contains(string(data), "127.0.0.1:") || strings.Contains(string(data), "203.0.113.99") {
		t.Fatalf("信任了伪造代理头: %s", data)
	}
}

// TestPhaseExperiments 从真正的服务/故障 peer 复跑时间预算边界；辅助测试与 demo
// 共用 server 工具，但没有用假的网络响应替代协议。Timeout 方法必须来自实际错误链。
func TestPhaseExperiments(t *testing.T) {
	for _, run := range []experiment{headerExperiment, bodyExperiment, contextBodyExperiment, tlsExperiment, serverReadExperiment, serverWriteExperiment, streamExperiment, idleExperiment} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		observation, err := run(ctx)
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", observation.Name, err)
		}
	}
}

// TestHeaderTimeoutIsNetError 保留独立错误类型判断，Timeout 不靠错误文案。
func TestHeaderTimeoutIsNetError(t *testing.T) {
	ctx, f, c := testRig(t, 2)
	c.Transport.ResponseHeaderTimeout = 80 * time.Millisecond // 在首次使用前配置，不并发修改 Transport。
	g := f.Gate("header-test")
	request := doAsync(ctx, c, "GET", f.URL+"/headers?gate=header-test", "")
	ownRequestForTest(t, request, g.Open)
	if err := waitSignal(ctx, g.Entered, "header entered"); err != nil {
		t.Fatal(err)
	}
	err, waitErr := request.Result(ctx)
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("不是实际网络超时: %v", err)
	}
	if err := waitSignal(ctx, g.Done, "timeout handler done"); err != nil {
		t.Fatal(err)
	}
}

// TestCanceledRequestMustFinishCleanup检查真实取消后result已可读、工作ctx已取消，但
// 独立cleanup gate未释放时owner必须保持等待；事件顺序和计数来自实际通道而非日志。
func TestCanceledRequestMustFinishCleanup(t *testing.T) {
	ctx, f, c := testRig(t, 2)
	proof, err := cleanupBarrierProof(ctx, f, c)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(proof.RequestErr, context.Canceled) || !errors.Is(proof.WorkErr, context.Canceled) {
		t.Fatalf("没有实际观察取消: %+v", proof)
	}
	if proof.BeforeFinished || proof.BeforeOwnerReturned || proof.BeforeOwned != 1 || proof.AfterOwned != 0 {
		t.Fatalf("owner提前返回或未收尾: %+v", proof)
	}
	if len(proof.Events) != 2 || proof.Events[0] != "cleanup-finished" || proof.Events[1] != "owner-return" {
		t.Fatalf("取消清理顺序错误: %v", proof.Events)
	}
}

// TestResultWaitCancellationIsNotJoin在真实handler等待期间取消“结果观察者ctx”，
// request仍在活动且无result，因此等待错误不能冒充join；随后正常通知并独立等待完成。
func TestResultWaitCancellationIsNotJoin(t *testing.T) {
	ctx, f, c := testRig(t, 2)
	g := f.Gate("observer-cancel")
	request := doAsync(ctx, c, "GET", f.URL+"/headers?gate=observer-cancel", "")
	ownRequestForTest(t, request, g.Open)
	if err := waitSignal(ctx, g.Entered, "实际请求仍等response header"); err != nil {
		t.Fatal(err)
	}
	observerCtx, cancelObserver := context.WithCancel(ctx)
	cancelObserver()
	requestErr, waitErr := request.Result(observerCtx)
	if requestErr != nil || !errors.Is(waitErr, context.Canceled) || closedNow(request.Finished()) || c.OwnedRequests.Load() != 1 {
		t.Fatalf("仅取消结果等待却被当成join: result=%v wait=%v owned=%d", requestErr, waitErr, c.OwnedRequests.Load())
	}
	// 同一个已取消观察ctx也不能拿来join：finished仍未关闭，必须返回join失败。
	// 后面的StopAndJoin创建独立预算，才有机会真正等待这个请求结束。
	if joinErr := request.waitFinished(observerCtx); !errors.Is(joinErr, context.Canceled) || closedNow(request.Finished()) {
		t.Fatalf("已取消ctx被误当join成功: %v", joinErr)
	}
	if err := request.StopAndJoin(g.Open); err != nil {
		t.Fatal(err)
	}
	if !closedNow(request.Finished()) || c.OwnedRequests.Load() != 0 {
		t.Fatal("独立join之后仍未完成")
	}
}

// TestEarlyCanceledCallerStillJoins直接触发原Entered早退出缺口：工作ctx事先取消，
// cancellationCase的defer仍必须等待其新建请求finished，再把Canceled主错误交回测试。
func TestEarlyCanceledCallerStillJoins(t *testing.T) {
	ctx, f, c := testRig(t, 2)
	workCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err := cancellationCase(workCtx, f, c, "/cooperative-order")
	if !errors.Is(err, context.Canceled) || c.OwnedRequests.Load() != 0 {
		t.Fatalf("早返回未join: err=%v owned=%d", err, c.OwnedRequests.Load())
	}
}

// TestCleanupAndMainErrorsAreBothPreserved用真实取消请求产生业务结果，再给附加清理
// 返回明确错误；combineOwnedCleanup必须同时保留主错误与清理错误，且finished已关闭。
func TestCleanupAndMainErrorsAreBothPreserved(t *testing.T) {
	ctx, f, c := testRig(t, 2)
	g := f.Gate("cleanup-error")
	cleanupErr := errors.New("本次实验清理失败")
	request := startRequest(ctx, c, "GET", f.URL+"/headers?gate=cleanup-error", "", func() error { return cleanupErr })
	// 这个测试故意期望cleanupErr，因此自定义Cleanup验证它，仍保持先join后fixture。
	t.Cleanup(func() {
		if err := request.StopAndJoin(g.Open); !errors.Is(err, cleanupErr) {
			t.Errorf("清理原因丢失: %v", err)
		}
	})
	if err := waitSignal(ctx, g.Entered, "cleanup-error真实handler"); err != nil {
		t.Fatal(err)
	}
	request.Cancel()
	requestErr, waitErr := request.Result(ctx)
	if waitErr != nil || !errors.Is(requestErr, context.Canceled) {
		t.Fatalf("没有真实取消结果: %v / %v", requestErr, waitErr)
	}
	mainErr := errors.New("本次实验主错误")
	combined := mainErr
	combineOwnedCleanup(&combined, request, g.Open)
	if !errors.Is(combined, mainErr) || !errors.Is(combined, cleanupErr) || !closedNow(request.Finished()) {
		t.Fatalf("主/清理错误或完成事实丢失: %v", combined)
	}
}
