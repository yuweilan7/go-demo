package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testRig由每个独立测试创建真实两级HTTP环境。先注册总清理，后注册的Job清理会先执行；
// t.Fatal也运行t.Cleanup，所以早失败不会跳过cancel/Open/独立join。
func testRig(t *testing.T, trusted []string) (context.Context, *Rig, *Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	r, err := NewRig(trusted)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	c := NewClient()
	t.Cleanup(func() {
		cancel()
		c.Close()
		if err := r.Close(); err != nil {
			t.Error(err)
		}
		if c.Bodies.Load() != 0 || c.Calls.Load() != 0 || r.Jobs.Load() != 0 {
			t.Errorf("资源未清零 bodies=%d calls=%d jobs=%d", c.Bodies.Load(), c.Calls.Load(), r.Jobs.Load())
		}
		if r.App.Live() != 0 || r.Dependency.Live() != 0 || r.App.active.Load() != 0 || r.Dependency.active.Load() != 0 {
			t.Error("服务器连接/handler仍活跃")
		}
	})
	return ctx, r, c
}

// ownJob紧接startJob/StartRequest调用，清理使用Job.StopAndJoin内部的新预算，
// 不使用可能已取消的测试工作ctx。unblock必须包括任务可能依赖的每一道gate。
func ownJob(t *testing.T, job *Job, unblock func()) {
	t.Helper()
	t.Cleanup(func() {
		if err := job.StopAndJoin(unblock); err != nil {
			t.Errorf("Job未收尾: %v", err)
		}
	})
}

// decode给测试自己的协议断言一份Envelope；没有调用demo的assertEnvelope，
// json.Unmarshal会拒绝拼接两个JSON，因而测试能独立发现重复响应。
func decode(t *testing.T, reply Reply, status int, code string, committed bool) Envelope {
	t.Helper()
	var value Envelope
	if err := json.Unmarshal(reply.Body, &value); err != nil {
		t.Fatalf("响应不是单个JSON: %q %v", reply.Body, err)
	}
	if reply.Status != status || value.Code != code || value.Committed != committed {
		t.Fatalf("协议错误: status=%d value=%+v", reply.Status, value)
	}
	return value
}

// mustRequest让每个测试的输入在调用点可见；网络error与HTTP业务拒绝分开处理。
// 成功取得的body已由Client.Request关闭，测试读取Reply.Body是普通byte slice副本。
func mustRequest(t *testing.T, ctx context.Context, r *Rig, c *Client, path, body, token, key, id string) Reply {
	t.Helper()
	reply, err := request(ctx, r, c, path, body, token, key, id)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

// TestM17ContractConcurrentIdempotency按M17验收过的8并发同key不变量，直接检查所有订单ID
// 与账本，而不是串行demo是否PASS。start屏障只保证请求同时可出发，不规定谁先到达。
func TestM17ContractConcurrentIdempotency(t *testing.T) {
	ctx, r, c := testRig(t, nil)
	start := newSignal()
	var active atomic.Int64
	jobs := make([]*Job, 8)
	replies := make([]Reply, 8)
	for index := range jobs {
		// index是这轮迭代的值，closure写自己那一格；各格没有并发读写。
		jobs[index] = startJob(ctx, 4*time.Second, &active, func(workCtx context.Context) error {
			if err := wait(workCtx, start.C, "并发起跑"); err != nil {
				return err
			}
			var err error
			replies[index], err = c.Request(workCtx, http.MethodPost, r.App.URL+"/api/orders", OrderJSON, AliceToken, "same", fmt.Sprintf("parallel-%d", index), nil)
			return err
		}, nil)
		ownJob(t, jobs[index], start.Open)
	}
	start.Open()
	for index, job := range jobs {
		workErr, waitErr := job.Result(ctx)
		if workErr != nil || waitErr != nil {
			t.Fatal(errors.Join(workErr, waitErr))
		}
		value := decode(t, replies[index], 201, "OK", true)
		if value.OrderID != 1 {
			t.Fatalf("重复key返回不同ID: %+v", value)
		}
		if err := job.StopAndJoin(start.Open); err != nil {
			t.Fatal(err)
		}
	}
	if r.Ledger.Count() != 1 || active.Load() != 0 {
		t.Fatalf("实际副作用/owner错误 rows=%d active=%d", r.Ledger.Count(), active.Load())
	}
	conflict := mustRequest(t, ctx, r, c, "/api/orders", `{"sku":"sku-m17","qty":2}`, AliceToken, "same", "conflict")
	decode(t, conflict, 409, "CONFLICT", false)
	if r.Ledger.Count() != 1 {
		t.Fatal("冲突新增了订单")
	}
}

// TestNextAndGroupOrdering等待外层audit写入再读trace，避免把收到body误当middleware都返回。
// 预期只包含机制保证的顺序，不检查goroutine编号、耗时或连接复用次数。
func TestNextAndGroupOrdering(t *testing.T) {
	ctx, r, c := testRig(t, nil)
	decode(t, mustRequest(t, ctx, r, c, "/api/orders", OrderJSON, AliceToken, "key", "next"), 201, "OK", true)
	if _, err := r.WaitAudit(ctx, "next"); err != nil {
		t.Fatal(err)
	}
	if got := r.Trace("next"); !slices.Equal(got, []string{"A.before", "B.before", "H", "B.after", "A.after"}) {
		t.Fatalf("Next顺序=%v", got)
	}
	decode(t, mustRequest(t, ctx, r, c, "/api/orders", OrderJSON, "", "key", "denied"), 401, "AUTH", false)
	if _, err := r.WaitAudit(ctx, "denied"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.Trace("denied"), "H") || r.Ledger.Count() != 1 {
		t.Fatal("认证拒绝仍调用订单handler/新增订单")
	}
}

// TestAbortReturnAndBindResponse以原始body和独立账本核对错误；危险路径应该真的错误，
// 修复路径必须只有一个JSON且没有额外append。这不是只检查401/400状态码。
func TestAbortReturnAndBindResponse(t *testing.T) {
	ctx, r, c := testRig(t, nil)
	bad := mustRequest(t, ctx, r, c, "/fault/abort-bad", OrderJSON, "", "", "abort")
	var value Envelope
	if bad.Status != 401 || json.Unmarshal(bad.Body, &value) == nil || !strings.Contains(string(bad.Body), `"committed":true`) || r.Ledger.Count() != 1 {
		t.Fatalf("Abort反例未出现 %d %s rows=%d", bad.Status, bad.Body, r.Ledger.Count())
	}
	if _, err := r.WaitAudit(ctx, "abort"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.Trace("abort"), "H.unexpected") {
		t.Fatal("Abort未阻止后续handler")
	}
	decode(t, mustRequest(t, ctx, r, c, "/fault/abort-fixed", OrderJSON, "", "", "abort-fixed"), 401, "AUTH", false)
	if r.Ledger.Count() != 1 {
		t.Fatal("Abort+return修复仍写订单")
	}
	bad = mustRequest(t, ctx, r, c, "/fault/bind-bad", `{"sku":`, "", "", "bind")
	if bad.Status != 400 || json.Unmarshal(bad.Body, &value) == nil || r.Ledger.Count() != 2 {
		t.Fatalf("Bind反例未出现 %d %s rows=%d", bad.Status, bad.Body, r.Ledger.Count())
	}
	decode(t, mustRequest(t, ctx, r, c, "/api/orders", `{"sku":`, AliceToken, "x", "bind-fixed"), 400, "INPUT", false)
	if r.Ledger.Count() != 2 {
		t.Fatal("ShouldBind错误后仍写订单")
	}
}

// TestRejectedInputsHaveNoEffects保留M17的实际协议限制：未知字段、尾随JSON、大小上限、
// auth、content-type、数量和key；每一条都重新读账本，证明拒绝没有发生真实副作用。
func TestRejectedInputsHaveNoEffects(t *testing.T) {
	ctx, r, c := testRig(t, nil)
	cases := []struct {
		name, body, token, key, content, code string
		status                                int
	}{
		{"auth", OrderJSON, "", "k", "application/json", "AUTH", 401},
		{"json", `{"sku":`, AliceToken, "k", "application/json", "INPUT", 400},
		{"unknown", `{"sku":"sku-m17","qty":1,"admin":true}`, AliceToken, "k", "application/json", "INPUT", 400},
		{"trailing", OrderJSON + OrderJSON, AliceToken, "k", "application/json", "INPUT", 400},
		{"large", strings.Repeat("x", 257), AliceToken, "k", "application/json", "INPUT", 413},
		{"missing", `{"sku":"sku-m17"}`, AliceToken, "k", "application/json", "INPUT", 400},
		{"negative", `{"sku":"sku-m17","qty":-1}`, AliceToken, "k", "application/json", "ORDER_INVALID", 422},
		{"sku", `{"sku":"OTHER","qty":1}`, AliceToken, "k", "application/json", "ORDER_INVALID", 422},
		{"key", OrderJSON, AliceToken, "", "application/json", "KEY_REQUIRED", 400},
		{"content", OrderJSON, AliceToken, "k", "text/plain", "CONTENT_TYPE", 415},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			reply, err := c.Request(ctx, http.MethodPost, r.App.URL+"/api/orders", item.body, item.token, item.key, item.name, map[string]string{"Content-Type": item.content})
			if err != nil {
				t.Fatal(err)
			}
			decode(t, reply, item.status, item.code, false)
			if r.Ledger.Count() != 0 {
				t.Fatalf("拒绝却新增rows=%d", r.Ledger.Count())
			}
		})
	}
}

// TestBusinessValidationAndBodyReuse把“required成功”与业务有效分开，再证明body只能被网络
// 读取一次；缓存版实际返回201并新增一行，而不是只证明第二次绑定err=nil。
func TestBusinessValidationAndBodyReuse(t *testing.T) {
	ctx, r, c := testRig(t, nil)
	invalid := `{"sku":"sku-m17","qty":-1}`
	decode(t, mustRequest(t, ctx, r, c, "/fault/validation-bad", invalid, "", "", "bad-rule"), 201, "OK", true)
	decode(t, mustRequest(t, ctx, r, c, "/api/orders", invalid, AliceToken, "invalid", "fixed-rule"), 422, "ORDER_INVALID", false)
	if r.Ledger.Count() != 1 {
		t.Fatal("业务校验未阻止新增")
	}
	decode(t, mustRequest(t, ctx, r, c, "/fault/body-bad", OrderJSON, "", "", "reread"), 400, "BODY_ALREADY_READ", false)
	if r.Ledger.Count() != 1 {
		t.Fatal("EOF后新增订单")
	}
	decode(t, mustRequest(t, ctx, r, c, "/fault/body-fixed", OrderJSON, AliceToken, "cache", "cached"), 201, "OK", true)
	if r.Ledger.Count() != 2 {
		t.Fatal("缓存修复没有真实订单")
	}
	// 解决重复读body不能顺带放松输入契约；直接给修复路由未知字段/尾随JSON/大body/空key。
	for index, item := range []struct {
		body, key, code string
		status          int
	}{
		{`{"sku":"sku-m17","qty":1,"admin":true}`, "x", "INPUT", 400},
		{OrderJSON + OrderJSON, "x", "INPUT", 400},
		{strings.Repeat("x", 257), "x", "INPUT", 413},
		{OrderJSON, "", "KEY_REQUIRED", 400},
	} {
		decode(t, mustRequest(t, ctx, r, c, "/fault/body-fixed", item.body, AliceToken, item.key, fmt.Sprintf("cache-reject-%d", index)), item.status, item.code, false)
		if r.Ledger.Count() != 2 {
			t.Fatal("缓存修复拒绝仍新增订单")
		}
	}
}

// TestAuthenticationOwnershipProxyAndLogs把token身份与订单归属分开；Bob已登录却不能读Alice订单。
// 伪造X-User/XFF/Cookie/query不会替换身份/IP或出现在审计字段；ID由服务器生成。
func TestAuthenticationOwnershipProxyAndLogs(t *testing.T) {
	ctx, r, c := testRig(t, nil)
	decode(t, mustRequest(t, ctx, r, c, "/api/orders", OrderJSON, AliceToken, "key", "create"), 201, "OK", true)
	reply, err := c.Request(ctx, http.MethodGet, r.App.URL+"/api/orders/1?secret=QUERY_SECRET", "", BobToken, "", "HEADER_SECRET", map[string]string{"X-User": "alice", "X-Forwarded-For": "203.0.113.9", "Cookie": "COOKIE_SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	decode(t, reply, 403, "FORBIDDEN", false)
	audit, err := r.WaitAudit(ctx, "HEADER_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(audit)
	for _, secret := range []string{BobToken, "QUERY_SECRET", "HEADER_SECRET", "COOKIE_SECRET", "203.0.113.9"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("日志包含敏感输入: %s", data)
		}
	}
	if audit.User != "bob" || audit.Peer != "127.0.0.1" || audit.Route != "/api/orders/:id" || r.Ledger.Count() != 1 {
		t.Fatalf("授权/代理边界错误 %+v rows=%d", audit, r.Ledger.Count())
	}
	decode(t, mustRequest(t, ctx, r, c, "/api/orders/1", "", AliceToken, "", "lookup"), 200, "OK", true)
	decode(t, mustRequest(t, ctx, r, c, "/api/orders/999", "", AliceToken, "", "missing"), 404, "NOT_FOUND", false)
}

// TestExplicitTrustedProxy只模拟配置过的代理链，不把IP当用户身份；默认不信任的对照在上个测试。
func TestExplicitTrustedProxy(t *testing.T) {
	ctx, r, c := testRig(t, []string{"127.0.0.1"})
	reply, err := c.Request(ctx, http.MethodGet, r.App.URL+"/api/identity", "", AliceToken, "", "proxy", map[string]string{"X-Forwarded-For": "203.0.113.9, 127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	value := decode(t, reply, 200, "OK", false)
	if value.Peer != "203.0.113.9" || value.User != "alice" {
		t.Fatalf("IP解析/身份不符 %+v", value)
	}
}

// TestRecoveryAndAlreadyCommitted检查Recovery前后真实写入事实；500/committed=false只是响应
// 声明，不能否认panic前rows已新增。guardedWork只保护自己goroutine，必须另行join。
func TestRecoveryAndAlreadyCommitted(t *testing.T) {
	ctx, r, c := testRig(t, nil)
	decode(t, mustRequest(t, ctx, r, c, "/api/panic", "", AliceToken, "", "panic"), 500, "PANIC", false)
	if r.Ledger.Count() != 0 {
		t.Fatal("panic前写入")
	}
	decode(t, mustRequest(t, ctx, r, c, "/api/panic-after", OrderJSON, AliceToken, "", "after"), 500, "PANIC", false)
	if r.Ledger.Count() != 1 {
		t.Fatal("已提交事实被Recovery误判")
	}
	var active atomic.Int64
	job := startJob(ctx, time.Second, &active, func(context.Context) error { panic("synthetic") }, nil)
	ownJob(t, job, nil)
	workErr, waitErr := job.Result(ctx)
	if waitErr != nil || !errors.Is(workErr, ErrWorkerPanic) {
		t.Fatalf("worker panic未隔离 %v %v", workErr, waitErr)
	}
	if err := job.StopAndJoin(nil); err != nil {
		t.Fatal(err)
	}
	if active.Load() != 0 {
		t.Fatal("worker未join")
	}
}

// TestContextCopyAndExplicitBackgroundOwner直接通过真实GET结束请求，确认Copy仍指向原Request、
// writer为空、默认Gin.Done=nil、Keys指针值浅复制。独立owner用Snapshot完成一次效果。
func TestContextCopyAndExplicitBackgroundOwner(t *testing.T) {
	ctx, r, c := testRig(t, nil)
	for _, mode := range []string{"request", "owned"} {
		name := "life-" + mode
		decode(t, mustRequest(t, ctx, r, c, "/api/lifecycle/"+mode, "", AliceToken, "", name), 202, "ACCEPTED", false)
		life := r.Life(name)
		if life == nil {
			t.Fatal("Life未发布")
		}
		ownJob(t, life.Job, func() { life.Release.Open(); life.Cleanup.Open() })
		if !life.SameRequest || !life.WriterUnavailable || !life.GinDoneNil || !life.ShallowValue {
			t.Fatal("Copy/ctx边界未观察")
		}
		if mode == "owned" {
			if err := wait(ctx, life.RequestCanceled.C, "request.Done"); err != nil {
				t.Fatal(err)
			}
			life.Release.Open()
		}
		workErr, waitErr := life.Job.Result(ctx)
		if waitErr != nil {
			t.Fatal(waitErr)
		}
		if mode == "request" && (!errors.Is(workErr, context.Canceled) || life.Effects() != 0) {
			t.Fatalf("request派生任务错误 err=%v effects=%d", workErr, life.Effects())
		}
		if mode == "owned" && (workErr != nil || life.Effects() != 1) {
			t.Fatalf("独立owner任务错误 err=%v effects=%d", workErr, life.Effects())
		}
		select {
		case <-life.Job.Finished():
			t.Fatal("cleanup gate未开却已finished")
		default:
		}
		life.Cleanup.Open()
		if err := life.Job.StopAndJoin(life.Release.Open); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRealDependencyCancellationAndTimeout以真实依赖Entered为取消起点，并直接核对rows。
// detached截断链后多一行；gin-context没有传播取消但提交检查阻止写入；forward及时传播；
// timeout返回504完整业务协议而非Do错误。测试不把客户端Canceled翻译成订单永远没写。
func TestRealDependencyCancellationAndTimeout(t *testing.T) {
	for _, mode := range []string{"detached", "gin-context", "forward", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, r, c := testRig(t, nil)
			g := r.Gate(mode)
			call := c.StartRequest(ctx, r.App.URL+"/api/slow-orders?gate="+mode+"&mode="+mode, mode, nil)
			ownJob(t, call.Job, g.Open)
			if err := wait(ctx, g.AppEntered.C, "应用进入"); err != nil {
				t.Fatal(err)
			}
			if err := wait(ctx, g.Entered.C, "真实依赖进入"); err != nil {
				t.Fatal(err)
			}
			if mode != "timeout" {
				call.Job.Cancel()
			}
			if mode == "detached" || mode == "gin-context" {
				if err := wait(ctx, g.inbound.Done(), "入站取消"); err != nil {
					t.Fatal(err)
				}
				select {
				case <-g.Canceled.C:
					t.Fatal("取消链反例未出现")
				default:
				}
				g.Open()
			} else {
				if err := wait(ctx, g.Canceled.C, "依赖观察取消"); err != nil {
					t.Fatal(err)
				}
				select {
				case <-g.Done.C:
					t.Fatal("观察取消被当完成")
				default:
				}
				g.Cleanup.Open()
			}
			workErr, waitErr := call.Job.Result(ctx)
			if waitErr != nil {
				t.Fatal(waitErr)
			}
			if mode == "timeout" {
				if workErr != nil {
					t.Fatal(workErr)
				}
				decode(t, call.Reply, 504, "DEPENDENCY_TIMEOUT", false)
			} else if !errors.Is(workErr, context.Canceled) {
				t.Fatalf("客户端未取消 %v", workErr)
			}
			if err := wait(ctx, g.Done.C, "依赖退出"); err != nil {
				t.Fatal(err)
			}
			dependencyCanceled := false
			select {
			case <-g.Canceled.C:
				dependencyCanceled = true
			default:
			}
			if dependencyCanceled != (mode == "forward" || mode == "timeout") {
				t.Fatalf("依赖实际取消事实 mode=%s observed=%t", mode, dependencyCanceled)
			}
			if err := wait(ctx, g.AppDone.C, "应用退出"); err != nil {
				t.Fatal(err)
			}
			if err := call.Job.StopAndJoin(g.Open); err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "detached" {
				want = 1
			}
			if r.Ledger.Count() != want {
				t.Fatalf("取消后的副作用错误 rows=%d want=%d", r.Ledger.Count(), want)
			}
		})
	}
}

// TestCanceledWorkAndResultAreNotJoin制造真实HTTP先返回Canceled、随后清理仍被gate卡住。
// owner自己的StopAndJoin已进等待，工作ctx已取消也不能让owner伪装完成；开放清理后才join。
func TestCanceledWorkAndResultAreNotJoin(t *testing.T) {
	ctx, r, c := testRig(t, nil)
	workCtx, cancelWork := context.WithCancel(ctx)
	t.Cleanup(cancelWork)
	g := r.Gate("cleanup")
	cleanup := newSignal()
	call := c.StartRequest(workCtx, r.App.URL+"/api/slow-orders?gate=cleanup&mode=forward", "cleanup", func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return wait(cleanupCtx, cleanup.C, "请求附加清理")
	})
	ownJob(t, call.Job, func() { g.Open(); cleanup.Open() })
	if err := wait(ctx, g.Entered.C, "依赖进入"); err != nil {
		t.Fatal(err)
	}
	cancelWork()
	workErr, waitErr := call.Job.Result(ctx)
	if waitErr != nil || !errors.Is(workErr, context.Canceled) {
		t.Fatalf("取消结果不符 %v %v", workErr, waitErr)
	}
	select {
	case <-call.Job.Finished():
		t.Fatal("结果可读被当finished")
	default:
	}
	observer, stopObserver := context.WithCancel(ctx)
	stopObserver()
	if err := wait(observer, call.Job.Finished(), "已取消observer"); !errors.Is(err, context.Canceled) {
		t.Fatalf("observer取消结果 %v", err)
	}
	var active atomic.Int64
	owner := startJob(ctx, 4*time.Second, &active, func(context.Context) error { return call.Job.StopAndJoin(g.Open) }, nil)
	ownJob(t, owner, func() { g.Open(); cleanup.Open() })
	if err := wait(ctx, call.Job.joinWaiting.C, "owner开始join"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-owner.Finished():
		t.Fatal("owner提前返回")
	default:
	}
	if c.Calls.Load() != 1 || c.Bodies.Load() != 0 {
		t.Fatalf("真实请求/清理状态 calls=%d bodies=%d", c.Calls.Load(), c.Bodies.Load())
	}
	cleanup.Open()
	if workErr, waitErr := owner.Result(ctx); workErr != nil || waitErr != nil {
		t.Fatal(errors.Join(workErr, waitErr))
	}
	if err := owner.StopAndJoin(cleanup.Open); err != nil {
		t.Fatal(err)
	}
	if c.Calls.Load() != 0 || active.Load() != 0 || r.Ledger.Count() != 0 {
		t.Fatal("清理后资源/副作用错误")
	}
}

// TestEarlyObservationFailureStillJoins把观察ctx提前取消，此时Entered可能尚未发生；
// 清理仍必须取消真正请求、开两道依赖gate并用独立预算join，不依赖结果是否读到。
func TestEarlyObservationFailureStillJoins(t *testing.T) {
	ctx, r, c := testRig(t, nil)
	g := r.Gate("early")
	call := c.StartRequest(ctx, r.App.URL+"/api/slow-orders?gate=early&mode=forward", "early", nil)
	ownJob(t, call.Job, g.Open)
	observer, cancel := context.WithCancel(ctx)
	cancel()
	if err := wait(observer, make(chan struct{}), "故意失败的观察"); !errors.Is(err, context.Canceled) {
		t.Fatal("观察未失败")
	}
	if err := call.Job.StopAndJoin(g.Open); err != nil {
		t.Fatal(err)
	}
	if c.Calls.Load() != 0 || c.Bodies.Load() != 0 {
		t.Fatal("早失败未join请求")
	}
}

// TestCleanupErrorPreservesPrimary验证失败收尾不是静默吞错：主error与cleanup error同时保留。
// 这个辅助测试不是HTTP证明，真实HTTP的收尾屏障已在前两个测试单独验证。
func TestCleanupErrorPreservesPrimary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	primary := errors.New("primary")
	cleanup := errors.New("cleanup")
	var active atomic.Int64
	job := startJob(ctx, time.Second, &active, func(context.Context) error { return primary }, func() error { return cleanup })
	// 此Job的cleanup错误是测试输入，收尾仍必须执行；专门核对预期error，而不当成未知失败。
	t.Cleanup(func() {
		if err := job.StopAndJoin(nil); !errors.Is(err, cleanup) {
			t.Errorf("清理结果错误: %v", err)
		}
	})
	workErr, waitErr := job.Result(ctx)
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	combined := errors.Join(workErr, job.StopAndJoin(nil))
	if !errors.Is(combined, primary) || !errors.Is(combined, cleanup) || active.Load() != 0 {
		t.Fatalf("错误/完成丢失: %v active=%d", combined, active.Load())
	}
}

// TestFaultProcesses必须看到两个实际子进程非零退出及panic来源；预算到期不算故障复现。
// Faults同步等待并删除自己创建的binary目录，测试结束没有长期子进程或监听器。
func TestFaultProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	results, err := Faults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("故障结果数量=%d", len(results))
	}
	for _, result := range results {
		if result.Exit != 2 || !result.Marker {
			t.Fatalf("故障退出不符 %+v", result)
		}
	}
}
