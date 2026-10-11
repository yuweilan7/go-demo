package lab

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const orderJSON = `{"sku":"sku-m17","qty":1}`

// Observation 只保存已运行得到的事实；Run 按课程顺序打印。它不把精确耗时和偶然
// goroutine 排序当成保证，检查只针对屏障与协议所保证的状态。
type Observation struct {
	Name  string
	Facts []string
}
type experiment func(context.Context) (Observation, error)

// Run 是 cmd/demo 调用的同步总入口，每个实验获得 5 秒独立上限。withFixture 的清理
// 在实验函数返回前完成，因此输出 PASS 也意味着 body/连接/Serve 的 owner 收尾通过。
func Run(ctx context.Context, out io.Writer) error {
	experiments := []experiment{reuseExperiment, leakExperiment, errorsExperiment, duplicateExperiment,
		cancellationExperiment, headerExperiment, bodyExperiment, contextBodyExperiment,
		tlsExperiment, serverReadExperiment, serverWriteExperiment, streamExperiment, idleExperiment, cleanupBarrierExperiment}
	for _, run := range experiments {
		stepCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		result, err := run(stepCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", result.Name, err)
		}
		fmt.Fprintf(out, "\n[%s] PASS\n", result.Name)
		for _, fact := range result.Facts {
			fmt.Fprintln(out, "  "+fact)
		}
	}
	fmt.Fprintln(out, "\nM17 ALL PASS: 14 个真实网络实验；所有 body/request owner 清零、loopback listener 关闭、Serve 已等待退出。")
	return nil
}

// withFixture 统一资源作用域：创建真实 server 与可复用 client -> 运行实验 -> CloseIdle
// -> Shutdown -> Serve join -> live=0。具名返回 err 让 defer 把清理失败交给 Run，
// 不会出现业务实验返回 nil、清理失败只写日志却仍宣称 PASS 的情况。
func withFixture(name string, write, header, total time.Duration, maxConns int, work func(*Fixture, *Client) ([]string, error)) (result Observation, err error) {
	result.Name = name
	f, err := NewFixture(write)
	if err != nil {
		return result, err
	}
	c := NewClient(maxConns, header, total)
	defer func() {
		c.CloseIdle()
		open := c.OpenBodies.Load()
		owned := c.OwnedRequests.Load()
		if open != 0 {
			err = errors.Join(err, fmt.Errorf("仍拥有 %d 个未 Close 响应体", open))
		}
		if owned != 0 {
			err = errors.Join(err, fmt.Errorf("仍有 %d 个owned request未finished", owned))
		}
		err = errors.Join(err, f.Close())
		accepted, live, handled := f.Counts()
		result.Facts = append(result.Facts, fmt.Sprintf("cleanup: bodies=%d owned_requests=%d accepted=%d live=%d handled=%d Serve=joined listener=closed", open, owned, accepted, live, handled))
	}()
	result.Facts, err = work(f, c)
	return result, err
}

// require 是实验里的业务/协议不变量断言。条件不成立会让正常 demo 退出失败；它不
// 以“打印出预期字符串”替代验证。独立测试还从 HTTP 状态和账本直接读取事实。
func require(ok bool, message string) error {
	if !ok {
		return errors.New(message)
	}
	return nil
}

// waitSignal 当前 goroutine 同步等待指定事实，不启动新任务、不猜调度。channel 由
// 事实产生者关闭，ctx 由实验 owner 负责取消；ctx 上限到期时把缺失事实原样报告。
func waitSignal(ctx context.Context, signal <-chan struct{}, name string) error {
	select {
	case <-signal:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待 %s: %w", name, ctx.Err())
	}
}

// reuseExperiment 对比两次真实请求的服务端连接 ID 与客户端 GotConn.Reused。
// 第一条完整消费到网络 EOF 并关闭 body；第二条复用的是同一 Transport 的空闲连接。
func reuseExperiment(ctx context.Context) (Observation, error) {
	return withFixture("E01 复用 Client/Transport 与同步调用", 0, time.Second, 3*time.Second, 2, func(f *Fixture, c *Client) ([]string, error) {
		firstTrace, secondTrace := &Trace{}, &Trace{}
		first, err := RequestJSON(AttachTrace(ctx, firstTrace), c, "GET", f.URL+"/ok", nil, "", 2048)
		if err != nil {
			return nil, err
		}
		second, err := RequestJSON(AttachTrace(ctx, secondTrace), c, "GET", f.URL+"/ok", nil, "", 2048)
		if err != nil {
			return nil, err
		}
		connected, _, firstByte := firstTrace.Snapshot()
		_, reused, _ := secondTrace.Snapshot()
		if err := require(connected && firstByte && reused && first.Connection == second.Connection, "未观察到两端一致的 HTTP/1.1 连接复用"); err != nil {
			return nil, err
		}
		return []string{fmt.Sprintf("connect_done=%t first_response_byte=%t status=%d protocol=HTTP/1.1", connected, firstByte, first.Status), fmt.Sprintf("conn_ids=%s,%s second.GotConn.Reused=%t bodies=%d", first.Connection, second.Connection, reused, c.OpenBodies.Load())}, nil
	})
}

// leakExperiment 故意不读也不关第一条 body，使 MaxConnsPerHost=1 的第二条请求等待池。
// CloseIdleConnections 无法关闭这个占用；第二条 deadline 不会自动取消第一条请求。
// 修复只对已知上限 1 MiB 的本地响应做有界 EOF 读取，然后 Close，让第三条复用。
func leakExperiment(ctx context.Context) (Observation, error) {
	return withFixture("E02 body 泄漏 -> 池等待 -> 有界修复", 0, time.Second, 4*time.Second, 1, func(f *Fixture, c *Client) ([]string, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", f.URL+"/leak", nil)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close() // 危险窗口之后仍有兜底，整个 demo 不遗留泄漏。
		c.CloseIdle()
		blockedCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		_, blockedErr := RequestJSON(blockedCtx, c, "GET", f.URL+"/ok", nil, "", 2048)
		cancel()
		_, _, beforeHandled := f.Counts()
		if err := require(errors.Is(blockedErr, context.DeadlineExceeded) && c.OpenBodies.Load() == 1 && beforeHandled == 1, "泄漏应使第二请求在连接池等待，不能到达 handler"); err != nil {
			return nil, err
		}
		bytes, err := io.Copy(io.Discard, io.LimitReader(resp.Body, (1<<20)+1))
		if err != nil {
			return nil, err
		}
		if err := resp.Body.Close(); err != nil {
			return nil, err
		}
		trace := &Trace{}
		third, err := RequestJSON(AttachTrace(ctx, trace), c, "GET", f.URL+"/ok", nil, "", 2048)
		if err != nil {
			return nil, err
		}
		_, reused, _ := trace.Snapshot()
		if err := require(bytes == 1<<20 && reused && third.Connection == resp.Header.Get("X-Conn-ID") && c.OpenBodies.Load() == 0, "有界 EOF + Close 后第三请求应复用并清零 body owner"); err != nil {
			return nil, err
		}
		return []string{"danger: open_bodies=1; CloseIdleConnections 后仍被占用；second=deadline; second 未到达 handler", fmt.Sprintf("repair: EOF_bytes=%d third.conn=%s reused=%t open_bodies=0", bytes, third.Connection, reused)}, nil
	})
}

// errorsExperiment 同一批合成数据逐层区分 HTTP、JSON、业务、读取超限和提前断开。
// legacy 的 HTTP200/OK 与实际 ledger=0 同时存在，客户端必须按 Committed 协议拒绝。
func errorsExperiment(ctx context.Context) (Observation, error) {
	return withFixture("E03 状态/JSON/业务/大小/早断开", 0, time.Second, 3*time.Second, 2, func(f *Fixture, c *Client) ([]string, error) {
		facts := []string{}
		for _, path := range []string{"/missing", "/bad-json", "/business-error", "/legacy", "/large", "/early", "/late-status", "/fixed-status"} {
			reply, err := RequestJSON(ctx, c, "GET", f.URL+path, nil, "", 1024)
			var statusErr *HTTPError
			var businessErr *BusinessError
			var syntaxErr *json.SyntaxError
			ok := false
			switch path {
			case "/missing":
				ok = errors.As(err, &statusErr) && statusErr.Status == 404
			case "/bad-json":
				ok = errors.As(err, &syntaxErr) && reply.Status == 200
			case "/business-error":
				ok = errors.As(err, &businessErr) && businessErr.Code == "OUT_OF_STOCK"
			case "/legacy":
				ok = errors.As(err, &businessErr) && businessErr.Code == "NOT_COMMITTED" && f.Ledger.Count() == 0
			case "/large":
				ok = errors.Is(err, ErrTooLarge) && reply.Bytes == 1025
			case "/early":
				ok = errors.Is(err, io.ErrUnexpectedEOF) && reply.Status == 200
			case "/late-status":
				ok = errors.As(err, &businessErr) && reply.Status == 200
			case "/fixed-status":
				ok = errors.As(err, &statusErr) && reply.Status == 409
			}
			if check := require(ok && c.OpenBodies.Load() == 0, "错误层不符合预期或 body 未释放: "+path); check != nil {
				return facts, errors.Join(check, err)
			}
			facts = append(facts, fmt.Sprintf("%s status=%d bytes=%d error=%v body_owner=0", path, reply.Status, reply.Bytes, err))
		}
		facts = append(facts, fmt.Sprintf("legacy: HTTP200 != 业务提交；实际账本 rows=%d；late-status=200, fixed-status=409", f.Ledger.Count()))
		return facts, nil
	})
}

// duplicateExperiment 明确发送两次 POST，不实现任何自动重试。unsafe 分支写两行，
// 正常分支用同一个 key 返回同一编号且只写一行；账本事实独立于 HTTP 状态。
func duplicateExperiment(ctx context.Context) (Observation, error) {
	return withFixture("E04 重复下单与输入边界", 0, time.Second, 3*time.Second, 2, func(f *Fixture, c *Client) ([]string, error) {
		for i := 0; i < 2; i++ {
			if _, err := RequestJSON(ctx, c, "POST", f.URL+"/orders?unsafe=1", strings.NewReader(orderJSON), "same-key", 2048); err != nil {
				return nil, err
			}
		}
		unsafeRows := f.Ledger.Count()
		first, err := RequestJSON(ctx, c, "POST", f.URL+"/orders", strings.NewReader(orderJSON), "safe-key", 2048)
		if err != nil {
			return nil, err
		}
		second, err := RequestJSON(ctx, c, "POST", f.URL+"/orders", strings.NewReader(orderJSON), "safe-key", 2048)
		if err != nil {
			return nil, err
		}
		if err := require(unsafeRows == 2 && f.Ledger.Count() == 3 && first.Envelope.OrderID == second.Envelope.OrderID, "相同幂等键必须只新增一行"); err != nil {
			return nil, err
		}
		oversize := `{"sku":"` + strings.Repeat("s", 300) + `","qty":1}`
		tooBig, sizeErr := RequestJSON(ctx, c, "POST", f.URL+"/orders", strings.NewReader(oversize), "big-key", 2048)
		trailing, trailingErr := RequestJSON(ctx, c, "POST", f.URL+"/orders", strings.NewReader(orderJSON+orderJSON), "trailing-key", 2048)
		req, _ := http.NewRequestWithContext(ctx, "POST", f.URL+"/orders", strings.NewReader(orderJSON))
		req.Header.Set("Content-Type", "application/json")
		unauthorized, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		_, readErr := io.Copy(io.Discard, io.LimitReader(unauthorized.Body, 2049))
		closeErr := unauthorized.Body.Close()
		if err := require(sizeErr != nil && tooBig.Status == 413 && trailingErr != nil && trailing.Status == 400 && unauthorized.StatusCode == 401 && f.Ledger.Count() == 3, "拒绝输入不得新增账本行"); err != nil {
			return nil, err
		}
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		// 先用成功请求确保下一次 POST 走已复用连接。lost-order 在提交后直接断开，
		// 如果忘记移除 GetBody，Idempotency-Key 会让 Transport 有机会自动重发。
		if _, err := RequestJSON(ctx, c, "GET", f.URL+"/ok", nil, "", 2048); err != nil {
			return nil, err
		}
		_, _, beforeLost := f.Counts()
		_, lostErr := RequestJSON(ctx, c, "POST", f.URL+"/lost-order", strings.NewReader(orderJSON), "lost-key", 2048)
		_, _, afterLost := f.Counts()
		if err := require(lostErr != nil && afterLost-beforeLost == 1 && f.Ledger.Count() == 4, "丢失响应订单必须已提交且只有一次实际 handler 调用"); err != nil {
			return nil, errors.Join(err, lostErr)
		}
		return []string{fmt.Sprintf("danger duplicate_rows=%d; repair same_order_ids=%d,%d safe_stage_total_rows=3（仅修复分支新增1行）", unsafeRows, first.Envelope.OrderID, second.Envelope.OrderID), "input: oversize=413 trailing_json=400 missing_auth=401 rejected_side_effects=0", fmt.Sprintf("lost-order: commit 后断开，client=%v；handler_delta=%d ledger=3->4；GetBody=nil，POST自动重发=0", lostErr, afterLost-beforeLost)}, nil
	})
}

// cancellationExperiment 以 Entered 和 Canceled 建立时间线：客户端发 cancel -> 客户端
// 返回 canceled -> handler 观察 ctx -> owner 开门 -> 危险 handler 仍提交。修复分支观察
// ctx 后直接返回，owner 等 Done；不能把 cancel() 当成服务端事务回滚或 goroutine join。
func cancellationExperiment(ctx context.Context) (Observation, error) {
	return withFixture("E05 请求取消与仍发生的业务副作用", 0, time.Second, 3*time.Second, 2, func(f *Fixture, c *Client) ([]string, error) {
		facts := []string{}
		for _, path := range []string{"/cancel-order", "/cooperative-order"} {
			fact, err := cancellationCase(ctx, f, c, path)
			if err != nil {
				return facts, err
			}
			facts = append(facts, fact)
		}
		return facts, nil
	})
}

// cancellationCase为每一次请求建立函数作用域：创建后立即注册独立cancel/unblock/join。
// 无论Entered/Result/Canceled/Done哪一项等待失败，都先完成owned request收尾再返回。
func cancellationCase(ctx context.Context, f *Fixture, c *Client, path string) (fact string, err error) {
	g := f.Gate(path)
	request := doAsync(ctx, c, "POST", f.URL+path+"?gate="+path, orderJSON)
	defer combineOwnedCleanup(&err, request, g.Open)
	if err = waitSignal(ctx, g.Entered, "handler entered"); err != nil {
		return "", err
	}
	before := f.Ledger.Count()
	request.Cancel()
	clientErr, waitErr := request.Result(ctx)
	if waitErr != nil {
		return "", waitErr
	}
	if err = waitSignal(ctx, g.Canceled, "handler observed cancellation"); err != nil {
		return "", err
	}
	// 危险handler观察取消后仍等Release，owner此时可确认尚未提交，再明确允许故障继续。
	if err = require(errors.Is(clientErr, context.Canceled) && f.Ledger.Count() == before, "客户端取消与提交前屏障不一致"); err != nil {
		return "", err
	}
	g.Open()
	if err = waitSignal(ctx, g.Done, "handler exit"); err != nil {
		return "", err
	}
	after, expected := f.Ledger.Count(), before
	if path == "/cancel-order" {
		expected++
	}
	if err = require(after == expected, "取消路径副作用不符合预期"); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s entered -> client_result=Canceled -> server_observed_cancel -> release -> handler_done -> independent_request_join; ledger=%d->%d", path, before, after), nil
}

// headerExperiment 未发送任何响应头时让 ResponseHeaderTimeout 触发。Entered 证明 TCP
// 和请求发送已完成，handler 正在 gate 等待；该 timeout 不包含读取 response body。
func headerExperiment(ctx context.Context) (Observation, error) {
	return withFixture("E06 ResponseHeaderTimeout 所处阶段", 0, 150*time.Millisecond, 3*time.Second, 2, func(f *Fixture, c *Client) (facts []string, err error) {
		g := f.Gate("headers")
		request := doAsync(ctx, c, "GET", f.URL+"/headers?gate=headers", "")
		defer combineOwnedCleanup(&err, request, g.Open)
		if err := waitSignal(ctx, g.Entered, "已进入 handler，但未写响应头"); err != nil {
			return nil, err
		}
		requestErr, waitErr := request.Result(ctx)
		if waitErr != nil {
			return nil, waitErr
		}
		if err := waitSignal(ctx, g.Canceled, "server observed header timeout cancellation"); err != nil {
			return nil, err
		}
		if err := waitSignal(ctx, g.Done, "header handler exited"); err != nil {
			return nil, err
		}
		var netErr net.Error
		if check := require(errors.As(requestErr, &netErr) && netErr.Timeout(), "应该在 response header 等待超时"); check != nil {
			return nil, errors.Join(check, requestErr)
		}
		return []string{fmt.Sprintf("handler_entered=true response_headers_sent=false budget=150ms error=%v server_ctx_canceled=true request_finished=joined", requestErr)}, nil
	})
}

// bodyExperiment 的 server 先 Flush 200 响应头，让 Do 正常返回，然后不发送 body。
// Client.Timeout 的计时仍在继续，io.ReadAll 才返回 DeadlineExceeded；Do 返回不等于完成。
func bodyExperiment(ctx context.Context) (Observation, error) {
	return withFixture("E07 Client.Timeout 包含 body 读取", 0, time.Second, 180*time.Millisecond, 2, func(f *Fixture, c *Client) ([]string, error) {
		g := f.Gate("body")
		req, _ := http.NewRequestWithContext(ctx, "GET", f.URL+"/body?gate=body", nil)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("Do 应先拿到200响应头: %w", err)
		}
		defer resp.Body.Close()
		if err := waitSignal(ctx, g.Entered, "已 Flush 的响应头"); err != nil {
			return nil, err
		}
		data, bodyErr := io.ReadAll(io.LimitReader(resp.Body, 2049))
		if err := resp.Body.Close(); err != nil {
			return nil, err
		}
		if err := waitSignal(ctx, g.Done, "body timeout 后 handler 退出"); err != nil {
			return nil, err
		}
		if err := require(resp.StatusCode == 200 && len(data) == 0 && errors.Is(bodyErr, context.DeadlineExceeded), "整体 Client.Timeout 应打断 body 读取"); err != nil {
			return nil, errors.Join(err, bodyErr)
		}
		return []string{fmt.Sprintf("Do status=200 err=nil -> ReadAll bytes=0 error=%v; Client.Timeout=180ms，header预算=1s", bodyErr)}, nil
	})
}

// contextBodyExperiment 不依赖计时器猜时序：Do 返回响应头后 owner 主动 cancel request
// context，再读取 body。即使 Client.Timeout 尚未到期，body 也会被请求 context 终止。
func contextBodyExperiment(ctx context.Context) (Observation, error) {
	return withFixture("E08 Request.Context 覆盖 body 阶段", 0, time.Second, 3*time.Second, 2, func(f *Fixture, c *Client) ([]string, error) {
		g := f.Gate("ctxbody")
		requestCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		req, _ := http.NewRequestWithContext(requestCtx, "GET", f.URL+"/body?gate=ctxbody", nil)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if err := waitSignal(ctx, g.Entered, "body handler flushed"); err != nil {
			return nil, err
		}
		cancel()
		_, bodyErr := io.ReadAll(io.LimitReader(resp.Body, 2049))
		_ = resp.Body.Close()
		if err := waitSignal(ctx, g.Done, "context body handler done"); err != nil {
			return nil, err
		}
		if err := require(errors.Is(bodyErr, context.Canceled), "请求 ctx 应打断 Do 返回后的 body"); err != nil {
			return nil, errors.Join(err, bodyErr)
		}
		return []string{fmt.Sprintf("Do=200 -> owner.cancel -> Body.Read=%v -> handler_done; total Client.Timeout=3s 尚非触发源", bodyErr)}, nil
	})
}

// serverReadExperiment 用原始 TCP 写入不完整 header/body，实际触发 Server 的读 deadline。
// 这是故障输入的有界等待，不是 sleep 猜 goroutine 调度；raw socket 由本函数关闭。
func serverReadExperiment(ctx context.Context) (Observation, error) {
	return withFixture("E10 服务端 header/body 读预算", 0, time.Second, 3*time.Second, 2, func(f *Fixture, c *Client) ([]string, error) {
		address := strings.TrimPrefix(f.URL, "http://")
		dialer := &net.Dialer{Timeout: time.Second}
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = io.WriteString(conn, "GET /ok HTTP/1.1\r\nHost: localhost\r\nX-Unfinished:")
		if err != nil {
			return nil, err
		}
		_, headerErr := bufio.NewReader(conn).ReadByte()
		_ = conn.Close()
		_, _, headerHandled := f.Counts()
		if err := require(errors.Is(headerErr, io.EOF) && headerHandled == 0, "不完整 header 应在进入 handler 前关闭"); err != nil {
			return nil, errors.Join(err, headerErr)
		}
		bodyConn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		defer bodyConn.Close()
		_ = bodyConn.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = fmt.Fprintf(bodyConn, "POST /orders HTTP/1.1\r\nHost: localhost\r\nAuthorization: %s\r\nContent-Type: application/json\r\nIdempotency-Key: raw-body\r\nContent-Length: 27\r\nConnection: close\r\n\r\n{", token)
		if err != nil {
			return nil, err
		}
		resp, err := http.ReadResponse(bufio.NewReader(bodyConn), &http.Request{Method: "POST"})
		if err != nil {
			return nil, err
		}
		_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 2049))
		closeErr := resp.Body.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		if err := require(resp.StatusCode == 400 && f.Ledger.Count() == 0, "慢请求体应被拒绝且不提交"); err != nil {
			return nil, err
		}
		return []string{fmt.Sprintf("raw partial header -> EOF；handler_calls=%d；ReadHeaderTimeout=250ms", headerHandled), "raw partial body -> status=400 ledger=0；ReadTimeout=700ms（从请求读取开始，不是每次 Read 重置）"}, nil
	})
}

// serverWriteExperiment 在 handler Entered 后等待一个明确的写预算过期 timer，再开门。
// WriteTimeout 限制网络写，不会主动停止 handler 的 CPU/外部副作用；Flush 的 Timeout
// 与客户端未得到完整响应共同证明写阶段失败。
func serverWriteExperiment(ctx context.Context) (Observation, error) {
	return withFixture("E11 服务端 WriteTimeout 与 Flush", 100*time.Millisecond, time.Second, 3*time.Second, 2, func(f *Fixture, c *Client) (facts []string, err error) {
		g := f.Gate("write")
		request := doAsync(ctx, c, "GET", f.URL+"/write-stall?gate=write", "")
		defer combineOwnedCleanup(&err, request, g.Open)
		if err := waitSignal(ctx, g.Entered, "writer handler entered"); err != nil {
			return nil, err
		}
		timer := time.NewTimer(220 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		g.Open()
		clientErr, waitErr := request.Result(ctx)
		if waitErr != nil {
			return nil, waitErr
		}
		if err := waitSignal(ctx, g.Done, "writer handler done"); err != nil {
			return nil, err
		}
		var netErr net.Error
		if err := require(clientErr != nil && errors.As(g.Error(), &netErr) && netErr.Timeout(), "写 deadline 过期后 Flush 应失败"); err != nil {
			return nil, errors.Join(err, clientErr, g.Error())
		}
		return []string{fmt.Sprintf("WriteTimeout=100ms -> owner 在明确过期后 Release -> Flush=%v；client=%v；handler 仍执行到 Done", g.Error(), clientErr)}, nil
	})
}

// streamExperiment 展示普通 JSON 与长流的区别。owner 先实际读到第一行才释放第二行，
// 不靠时间估计 Flush 成功。reader 只是 body 的缓冲视图，不拥有另一个 socket。
func streamExperiment(ctx context.Context) (Observation, error) {
	return withFixture("E12 长流按段 Flush/写预算", 0, time.Second, 3*time.Second, 2, func(f *Fixture, c *Client) ([]string, error) {
		g := f.Gate("stream")
		req, _ := http.NewRequestWithContext(ctx, "GET", f.URL+"/stream?gate=stream", nil)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		reader := bufio.NewReader(io.LimitReader(resp.Body, 1025))
		first, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if err := waitSignal(ctx, g.Entered, "stream first flush"); err != nil {
			return nil, err
		}
		g.Open()
		second, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		_, eofErr := reader.ReadByte()
		_ = resp.Body.Close()
		if err := waitSignal(ctx, g.Done, "stream handler exit"); err != nil {
			return nil, err
		}
		if err := require(first == "{\"part\":1}\n" && second == "{\"part\":2}\n" && errors.Is(eofErr, io.EOF) && g.Error() == nil, "长流两段内容/EOF 不一致"); err != nil {
			return nil, err
		}
		return []string{"client 实际读到 part=1 -> owner Release -> part=2 -> EOF -> Close；每段设置1s写 deadline；总大小限制1024"}, nil
	})
}

// idleExperiment 在真实 ConnState 通知上等待 live=0，证明 IdleTimeout 回收空闲连接。
// 此时 body 已完整释放；IdleTimeout 不停止业务 handler，也不限制并发任务数。
func idleExperiment(ctx context.Context) (Observation, error) {
	return withFixture("E13 IdleTimeout 与池限制边界", 0, time.Second, 3*time.Second, 1, func(f *Fixture, c *Client) ([]string, error) {
		// 在 Transport 首次使用前配置，让客户端空闲预算比 server 的 500ms 长；
		// 这样 ConnState 关闭来自服务端 IdleTimeout，避免两个同值 timer 混淆原因。
		c.Transport.IdleConnTimeout = 2 * time.Second
		if _, err := RequestJSON(ctx, c, "GET", f.URL+"/ok", nil, "", 2048); err != nil {
			return nil, err
		}
		for {
			_, live, _ := f.Counts()
			if live == 0 {
				break
			}
			select {
			case <-f.changed:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		trace := &Trace{}
		reply, err := RequestJSON(AttachTrace(ctx, trace), c, "GET", f.URL+"/ok", nil, "", 2048)
		if err != nil {
			return nil, err
		}
		_, reused, _ := trace.Snapshot()
		accepted, _, _ := f.Counts()
		if err := require(!reused && accepted == 2, "空闲连接回收后应重新拨号"); err != nil {
			return nil, err
		}
		return []string{fmt.Sprintf("server IdleTimeout=500ms < client IdleConnTimeout=2s；body EOF/Close -> ConnState live=0 -> next.conn=%s reused=%t accepted=%d；池容量 != 业务并发限流", reply.Connection, reused, accepted)}, nil
	})
}
