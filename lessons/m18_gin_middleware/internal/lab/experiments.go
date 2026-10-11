package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

// Run 由cmd/demo同步调用，逐个实验创建独立Rig。每次runOne都会报告最终资源事实，
// 某项失败时先执行完整defer收尾再把error交回main；不会因PASS文字跳过业务断言。
func Run(ctx context.Context, out io.Writer) error {
	experiments := []struct {
		name string
		fn   func(context.Context, *Rig, *Client, io.Writer) error
	}{
		{"E01 M17订单协议/Next洋葱/幂等冲突", protocolExperiment},
		{"E02 Abort漏return/修复", abortExperiment},
		{"E03 Bind自动响应/ShouldBind/业务校验", bindingExperiment},
		{"E04 body重复读取/缓存修复", bodyExperiment},
		{"E05 Context.Copy/后台owner/结果非完成", lifecycleExperiment},
		{"E06 Recovery当前栈/提交不回滚/worker修复", recoveryExperiment},
		{"E07 认证归属/IP/敏感日志", securityExperiment},
		{"E08 真实依赖取消链/危险版/修复/超时", cancellationExperiment},
	}
	for _, experiment := range experiments {
		fmt.Fprintln(out, "\n==", experiment.name, "==")
		if err := runOne(ctx, out, experiment.fn); err != nil {
			return fmt.Errorf("%s: %w", experiment.name, err)
		}
		fmt.Fprintln(out, "PASS", experiment.name)
	}
	fmt.Fprintln(out, "\n== E09 可信代理显式配置 ==")
	if err := proxyExperiment(ctx, out); err != nil {
		return err
	}
	fmt.Fprintln(out, "PASS E09 可信代理显式配置")
	fmt.Fprintln(out, "\n== E10 有界故障子进程 ==")
	results, err := Faults(ctx)
	if err != nil {
		return err
	}
	for _, result := range results {
		fmt.Fprintf(out, "child mode=%s exit=%d panic_marker=%t process=joined\n", result.Mode, result.Exit, result.Marker)
	}
	fmt.Fprintln(out, "PASS E10 有界故障子进程；整轮10项完成")
	return nil
}

// runOne拥有创建成功的Client/Rig，具名err允许defer把清理错误加入主结果。
// 先收回客户端空闲连接，再Rig.Close取消/join后台任务及两份服务器；最后核对独立计数。
func runOne(ctx context.Context, out io.Writer, fn func(context.Context, *Rig, *Client, io.Writer) error) (err error) {
	r, err := NewRig(nil)
	if err != nil {
		return err
	}
	c := NewClient()
	defer func() {
		c.Close()
		err = errors.Join(err, r.Close())
		if c.Bodies.Load() != 0 || c.Calls.Load() != 0 {
			err = errors.Join(err, fmt.Errorf("客户端仍有bodies=%d calls=%d", c.Bodies.Load(), c.Calls.Load()))
		}
		fmt.Fprintf(out, "cleanup bodies=%d calls=%d jobs=%d app_live=%d dependency_live=%d app_handlers=%d dependency_handlers=%d Serve=joined\n", c.Bodies.Load(), c.Calls.Load(), r.Jobs.Load(), r.App.Live(), r.Dependency.Live(), r.App.active.Load(), r.Dependency.active.Load())
	}()
	return fn(ctx, r, c, out)
}

// request 是demo的短调用辅助，仍走真实Client.Request，返回body给实验直接解释。
func request(ctx context.Context, r *Rig, c *Client, path, body, token, key, id string) (Reply, error) {
	method := http.MethodGet
	if body != "" {
		method = http.MethodPost
	}
	return c.Request(ctx, method, r.App.URL+path, body, token, key, id, nil)
}

// protocolExperiment 用同一个sku-m17/JSON/教学身份/key复用M17已验收的协议断言。
// Gin只替换路由/绑定/响应层；账本仍独立校验真实写入，冲突是应用新加的保护。
func protocolExperiment(ctx context.Context, r *Rig, c *Client, out io.Writer) error {
	first, err := request(ctx, r, c, "/api/orders", OrderJSON, AliceToken, "same", "onion-1")
	if err != nil {
		return err
	}
	if err := assertEnvelope(first, 201, "OK", true); err != nil {
		return err
	}
	if _, err := r.WaitAudit(ctx, "onion-1"); err != nil {
		return err
	}
	want := []string{"A.before", "B.before", "H", "B.after", "A.after"}
	trace := r.Trace("onion-1")
	if !slices.Equal(trace, want) {
		return fmt.Errorf("洋葱顺序不符: %v", trace)
	}
	second, err := request(ctx, r, c, "/api/orders", OrderJSON, AliceToken, "same", "onion-2")
	if err != nil {
		return err
	}
	if err := assertEnvelope(second, 201, "OK", true); err != nil {
		return err
	}
	var a, b Envelope
	_ = json.Unmarshal(first.Body, &a)
	_ = json.Unmarshal(second.Body, &b)
	if a.OrderID != 1 || b.OrderID != 1 || r.Ledger.Count() != 1 {
		return errors.New("同key重复下单产生额外写入")
	}
	conflict, err := request(ctx, r, c, "/api/orders", `{"sku":"sku-m17","qty":2}`, AliceToken, "same", "conflict")
	if err != nil {
		return err
	}
	if err := assertEnvelope(conflict, 409, "CONFLICT", false); err != nil {
		return err
	}
	if r.Ledger.Count() != 1 {
		return errors.New("冲突仍写订单")
	}
	fmt.Fprintf(out, "Next=%v\nfirst=%s repeat=%s conflict=%d/%s rows=%d\n", trace, first.Body, second.Body, conflict.Status, conflict.Body, r.Ledger.Count())
	return nil
}

// abortExperiment 直接读取坏响应的两个JSON对象与账本，再核对修复拒绝没有新增写入。
// H.unexpected不存在证明Abort确实阻止后续handler，bad.current-handler-write证明当前函数继续。
func abortExperiment(ctx context.Context, r *Rig, c *Client, out io.Writer) error {
	bad, err := request(ctx, r, c, "/fault/abort-bad", OrderJSON, "", "bad", "abort-bad")
	if err != nil {
		return err
	}
	objects, err := joinedJSONCount(bad.Body)
	if err != nil {
		return err
	}
	if _, err := r.WaitAudit(ctx, "abort-bad"); err != nil {
		return err
	}
	trace := r.Trace("abort-bad")
	if bad.Status != 401 || objects != 2 || r.Ledger.Count() != 1 || slices.Contains(trace, "H.unexpected") || !slices.Contains(trace, "bad.current-handler-write") {
		return fmt.Errorf("反例未复现: status=%d objects=%d trace=%v rows=%d", bad.Status, objects, trace, r.Ledger.Count())
	}
	fixed, err := request(ctx, r, c, "/fault/abort-fixed", OrderJSON, "", "bad", "abort-fixed")
	if err != nil {
		return err
	}
	if err := assertEnvelope(fixed, 401, "AUTH", false); err != nil {
		return err
	}
	if r.Ledger.Count() != 1 {
		return errors.New("修复拒绝仍写订单")
	}
	fmt.Fprintf(out, "bad=%d/%s objects=%d rows=1 trace=%v\nfixed=%d/%s rows=1\n", bad.Status, bad.Body, objects, trace, fixed.Status, fixed.Body)
	return nil
}

// bindingExperiment 按绑定错误和业务错误分别对照。错误返回必须停止当前函数，
// ShouldBind只交回error；qty=-1要靠应用判断，不能用“JSON解析成功”当订单正确。
func bindingExperiment(ctx context.Context, r *Rig, c *Client, out io.Writer) error {
	bad, err := request(ctx, r, c, "/fault/bind-bad", `{"sku":`, "", "", "bind-bad")
	if err != nil {
		return err
	}
	objects, err := joinedJSONCount(bad.Body)
	if err != nil {
		return err
	}
	if bad.Status != 400 || objects != 2 || r.Ledger.Count() != 1 {
		return errors.New("Bind反例未复现")
	}
	fixed, err := request(ctx, r, c, "/api/orders", `{"sku":`, AliceToken, "x", "bind-fixed")
	if err != nil {
		return err
	}
	if err := assertEnvelope(fixed, 400, "INPUT", false); err != nil {
		return err
	}
	invalid := `{"sku":"sku-m17","qty":-1}`
	accepted, err := request(ctx, r, c, "/fault/validation-bad", invalid, "", "", "validation-bad")
	if err != nil {
		return err
	}
	if err := assertEnvelope(accepted, 201, "OK", true); err != nil {
		return err
	}
	rejected, err := request(ctx, r, c, "/api/orders", invalid, AliceToken, "invalid", "validation-fixed")
	if err != nil {
		return err
	}
	if err := assertEnvelope(rejected, 422, "ORDER_INVALID", false); err != nil {
		return err
	}
	if r.Ledger.Count() != 2 {
		return errors.New("绑定/校验失败新增了订单")
	}
	fmt.Fprintf(out, "Bind bad=%d content_type=%q body=%s objects=%d\nShouldBind fixed=%d/%s; qty=-1 bad=%d fixed=%d/%s rows=2\n", bad.Status, bad.Header.Get("Content-Type"), bad.Body, objects, fixed.Status, fixed.Body, accepted.Status, rejected.Status, rejected.Body)
	return nil
}

// bodyExperiment由Run调用，用实际请求证明中间件已经耗尽body以及缓存修复真实新增一次订单。
func bodyExperiment(ctx context.Context, r *Rig, c *Client, out io.Writer) error {
	bad, err := request(ctx, r, c, "/fault/body-bad", OrderJSON, "", "read", "body-bad")
	if err != nil {
		return err
	}
	if err := assertEnvelope(bad, 400, "BODY_ALREADY_READ", false); err != nil {
		return err
	}
	if r.Ledger.Count() != 0 {
		return errors.New("body EOF后仍写订单")
	}
	fixed, err := request(ctx, r, c, "/fault/body-fixed", OrderJSON, AliceToken, "read", "body-fixed")
	if err != nil {
		return err
	}
	if err := assertEnvelope(fixed, 201, "OK", true); err != nil {
		return err
	}
	if r.Ledger.Count() != 1 {
		return errors.New("body缓存修复无真实订单")
	}
	fmt.Fprintf(out, "network-reread=%d/%s rows=0; bounded-cache=%d/%s rows=1\n", bad.Status, bad.Body, fixed.Status, fixed.Body)
	return nil
}

// lifecycleExperiment 一次真实响应结束后分别观察request派生任务和owner派生任务。
// 每个Life已由Rig负责最终收尾；实验额外马上登记defer，让中途观察失败也释放两道gate/join。
func lifecycleExperiment(ctx context.Context, r *Rig, c *Client, out io.Writer) (err error) {
	for _, mode := range []string{"request", "owned"} {
		name := "life-" + mode
		reply, requestErr := request(ctx, r, c, "/api/lifecycle/"+mode, "", AliceToken, "", name)
		if requestErr != nil {
			return requestErr
		}
		if assertErr := assertEnvelope(reply, 202, "ACCEPTED", false); assertErr != nil {
			return assertErr
		}
		life := r.Life(name)
		if life == nil {
			return errors.New("后台任务未注册")
		}
		defer joinInto(&err, life.Job, func() { life.Release.Open(); life.Cleanup.Open() })
		if !life.SameRequest || !life.WriterUnavailable || !life.GinDoneNil || !life.ShallowValue {
			return fmt.Errorf("Copy源码边界不符: %+v", life)
		}
		if waitErr := wait(ctx, life.Started.C, "后台Started"); waitErr != nil {
			return waitErr
		}
		if mode == "request" {
			workErr, waitErr := life.Job.Result(ctx)
			if waitErr != nil {
				return waitErr
			}
			if !errors.Is(workErr, context.Canceled) || life.Effects() != 0 {
				return fmt.Errorf("request派生任务应取消: %v effects=%d", workErr, life.Effects())
			}
			life.Cleanup.Open()
			if joinErr := life.Job.StopAndJoin(life.Release.Open); joinErr != nil {
				return joinErr
			}
			fmt.Fprintln(out, "request-derived: handler返回 -> request ctx canceled -> worker result=Canceled effects=0 -> cleanup/join")
			continue
		}
		if waitErr := wait(ctx, life.RequestCanceled.C, "入站request已取消"); waitErr != nil {
			return waitErr
		}
		life.Release.Open()
		workErr, waitErr := life.Job.Result(ctx)
		if waitErr != nil || workErr != nil {
			return errors.Join(workErr, waitErr)
		}
		select {
		case <-life.Job.Finished():
			return errors.New("cleanup gate未开但任务已完成")
		default:
		}
		// owner本身也使用Job句柄，立即登记完整收尾；等joinWaiting证明它确实在join。
		joiner := startJob(ctx, 3*time.Second, &r.Jobs, func(context.Context) error { return life.Job.StopAndJoin(nil) }, nil)
		defer joinInto(&err, joiner, life.Cleanup.Open)
		if waitErr := wait(ctx, life.Job.joinWaiting.C, "owner已经等待finished"); waitErr != nil {
			return waitErr
		}
		select {
		case <-joiner.Finished():
			return errors.New("owner把result当join")
		default:
		}
		fmt.Fprintf(out, "owned: request canceled -> snapshot=%+v effects=%d result=nil finished=false owner_returned=false\n", life.Snapshot, life.Effects())
		life.Cleanup.Open()
		if workErr, waitErr := joiner.Result(ctx); workErr != nil || waitErr != nil {
			return errors.Join(workErr, waitErr)
		}
		if joinErr := joiner.StopAndJoin(life.Cleanup.Open); joinErr != nil {
			return joinErr
		}
		fmt.Fprintln(out, "owned: cleanup gate opened -> worker finished -> owner joined; Copy same_request=true writer=nil shallow_pointer=true Gin.Done=nil")
	}
	reused := 0
	for i := 0; i < 16; i++ {
		token := AliceToken
		if i%2 == 1 {
			token = BobToken
		}
		reply, requestErr := request(ctx, r, c, "/api/pool", "", token, "", fmt.Sprintf("pool-%d", i))
		if requestErr != nil {
			return requestErr
		}
		var probe struct {
			Reused bool
			User   string
		}
		if decodeErr := json.Unmarshal(reply.Body, &probe); decodeErr != nil {
			return decodeErr
		}
		if probe.Reused {
			reused++
		}
		if probe.User != identity(token) {
			return errors.New("身份污染到下一请求")
		}
	}
	fmt.Fprintf(out, "sync.Pool observation: sequential_requests=16 address_reused=%d; 当前身份每次重设，复用次数不是契约\n", reused)
	return nil
}

// recoveryExperiment由Run调用，核对同请求panic前后的账本，再用显式worker入口recover+join修复。
func recoveryExperiment(ctx context.Context, r *Rig, c *Client, out io.Writer) (err error) {
	before, err := request(ctx, r, c, "/api/panic", "", AliceToken, "", "panic-before")
	if err != nil {
		return err
	}
	if err := assertEnvelope(before, 500, "PANIC", false); err != nil {
		return err
	}
	if r.Ledger.Count() != 0 {
		return errors.New("panic-before仍写订单")
	}
	after, err := request(ctx, r, c, "/api/panic-after", OrderJSON, AliceToken, "", "panic-after")
	if err != nil {
		return err
	}
	if err := assertEnvelope(after, 500, "PANIC", false); err != nil {
		return err
	}
	if r.Ledger.Count() != 1 {
		return errors.New("Recovery错误地撤销已提交事实")
	}
	var active atomic.Int64
	job := startJob(ctx, 3*time.Second, &active, func(context.Context) error { panic("synthetic worker") }, nil)
	defer joinInto(&err, job, nil)
	workErr, waitErr := job.Result(ctx)
	if waitErr != nil || !errors.Is(workErr, ErrWorkerPanic) {
		return errors.Join(waitErr, fmt.Errorf("worker隔离失败: %v", workErr))
	}
	if joinErr := job.StopAndJoin(nil); joinErr != nil {
		return joinErr
	}
	fmt.Fprintf(out, "same-goroutine before=%d/%s rows=0; after-submit=%d/%s rows=1; guarded-worker=%v finished=true active=%d\n", before.Status, before.Body, after.Status, after.Body, workErr, active.Load())
	return nil
}

// securityExperiment由Run调用，使用真实认证Bob和伪造字段请求Alice订单，再审查实际日志值。
func securityExperiment(ctx context.Context, r *Rig, c *Client, out io.Writer) error {
	created, err := request(ctx, r, c, "/api/orders", OrderJSON, AliceToken, "owner", "owner-create")
	if err != nil {
		return err
	}
	if err := assertEnvelope(created, 201, "OK", true); err != nil {
		return err
	}
	denied, err := c.Request(ctx, http.MethodGet, r.App.URL+"/api/orders/1?secret=PRIVATE_QUERY", "", BobToken, "", "owner-denied", map[string]string{"X-User": "alice", "Cookie": "SESSION_SECRET", "X-Forwarded-For": "203.0.113.9"})
	if err != nil {
		return err
	}
	if err := assertEnvelope(denied, 403, "FORBIDDEN", false); err != nil {
		return err
	}
	audit, err := r.WaitAudit(ctx, "owner-denied")
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(audit)
	for _, secret := range []string{BobToken, "SESSION_SECRET", "PRIVATE_QUERY", "203.0.113.9"} {
		if bytesContains(encoded, secret) {
			return errors.New("allowlist日志含敏感输入或伪造IP")
		}
	}
	if audit.User != "bob" || audit.Peer != "127.0.0.1" || audit.Route != "/api/orders/:id" || r.Ledger.Count() != 1 {
		return fmt.Errorf("身份/代理边界不符: %+v rows=%d", audit, r.Ledger.Count())
	}
	fmt.Fprintf(out, "Bob+伪造X-User=alice -> %d/%s; safe_audit=%s rows=1\n", denied.Status, denied.Body, encoded)
	return nil
}

// bytesContains只读数据，给当前审计反例一个明确的字符串检查；不是生产脱敏算法。
func bytesContains(data []byte, value string) bool {
	return strings.Contains(string(data), value)
}

// cancellationExperiment 的四个模式使用独立gate/幂等key，先等真实依赖Entered再操纵取消。
// 任一早失败均触发defer cancel/Open/独立join，最后Rig.Close再join服务端handler。
func cancellationExperiment(ctx context.Context, r *Rig, c *Client, out io.Writer) (err error) {
	for _, mode := range []string{"detached", "gin-context", "forward", "timeout"} {
		name := "cancel-" + mode
		g := r.Gate(name)
		call := c.StartRequest(ctx, r.App.URL+"/api/slow-orders?gate="+name+"&mode="+mode, name, nil)
		defer joinInto(&err, call.Job, g.Open)
		if waitErr := wait(ctx, g.AppEntered.C, "Gin slowOrder进入"); waitErr != nil {
			return waitErr
		}
		if waitErr := wait(ctx, g.Entered.C, "真实依赖进入"); waitErr != nil {
			return waitErr
		}
		before := r.Ledger.Count()
		if mode != "timeout" {
			call.Job.Cancel()
		}
		if mode == "detached" || mode == "gin-context" {
			if waitErr := wait(ctx, g.inbound.Done(), "入站request取消"); waitErr != nil {
				return waitErr
			}
			select {
			case <-g.Canceled.C:
				return errors.New("被截断的取消链却取消了依赖")
			default:
			}
			g.Open()
		} else {
			if waitErr := wait(ctx, g.Canceled.C, "依赖观察取消"); waitErr != nil {
				return waitErr
			}
			select {
			case <-g.Done.C:
				return errors.New("依赖cleanup未放行却完成")
			default:
			}
			g.Cleanup.Open()
		}
		workErr, waitErr := call.Job.Result(ctx)
		if waitErr != nil {
			return waitErr
		}
		if mode == "timeout" {
			if workErr != nil {
				return workErr
			}
			if assertErr := assertEnvelope(call.Reply, 504, "DEPENDENCY_TIMEOUT", false); assertErr != nil {
				return assertErr
			}
		} else if !errors.Is(workErr, context.Canceled) {
			return fmt.Errorf("客户端应Canceled: %v", workErr)
		}
		if waitErr := wait(ctx, g.Done.C, "依赖handler退出"); waitErr != nil {
			return waitErr
		}
		dependencyCanceled := false
		select {
		case <-g.Canceled.C:
			dependencyCanceled = true
		default:
		}
		if dependencyCanceled != (mode == "forward" || mode == "timeout") {
			return fmt.Errorf("依赖取消事实不符 mode=%s observed=%t", mode, dependencyCanceled)
		}
		if waitErr := wait(ctx, g.AppDone.C, "Gin slowOrder退出"); waitErr != nil {
			return waitErr
		}
		if joinErr := call.Job.StopAndJoin(g.Open); joinErr != nil {
			return joinErr
		}
		delta := r.Ledger.Count() - before
		want := 0
		if mode == "detached" {
			want = 1
		}
		if delta != want {
			return fmt.Errorf("取消副作用错误 mode=%s delta=%d", mode, delta)
		}
		fmt.Fprintf(out, "mode=%s client_err=%v HTTP=%d body=%s rows=%d->%d dependency_canceled=%t dependency=joined app=joined call=joined\n", mode, workErr, call.Reply.Status, call.Reply.Body, before, r.Ledger.Count(), dependencyCanceled)
	}
	return nil
}

// proxyExperiment显式把loopback列为受信任代理，仅用于合成“代理传入”的请求。
// 真部署还必须确保直接用户无法访问该受信任入口，否则用户仍能自行伪造转发头。
func proxyExperiment(ctx context.Context, out io.Writer) (err error) {
	r, err := NewRig([]string{"127.0.0.1"})
	if err != nil {
		return err
	}
	c := NewClient()
	defer func() { c.Close(); err = errors.Join(err, r.Close()) }()
	reply, err := c.Request(ctx, http.MethodGet, r.App.URL+"/api/identity", "", AliceToken, "", "proxy", map[string]string{"X-Forwarded-For": "203.0.113.9, 127.0.0.1"})
	if err != nil {
		return err
	}
	var value Envelope
	if err := json.Unmarshal(reply.Body, &value); err != nil {
		return err
	}
	if value.Peer != "203.0.113.9" || value.User != "alice" {
		return fmt.Errorf("显式代理解析不符: %s", reply.Body)
	}
	fmt.Fprintf(out, "trusted_proxies=[127.0.0.1] proxy_forwarded=%s identity=alice (IP不作为身份)\n", reply.Body)
	return nil
}
