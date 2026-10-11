package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	httppprof "net/http/pprof"
	"runtime/debug"
	"runtime/pprof"
	runtimetrace "runtime/trace"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Reply 是合成订单的可检查结果。日志/span 是观察证据，Committed(id) 所查的
// downstream.store 才是本次运行下游提交的权威状态；它只是有界内存状态，不声称落库。
type Reply struct {
	Outcome Outcome `json:"outcome"`
	Value   uint64  `json:"value,omitempty"`
}

type runningServer struct {
	server  *http.Server
	done    chan error
	address string
}

// Service 由一个实验拥有，创建三个动态独占 loopback listener 和一个 HTTP client。
// orderMu 是错误反例的全局锁；storeMu 仅保护权威 map；二者故意分开，避免查状态也卡住。
// events/gates 属于实验屏障，keys 是有界合成 ID；业务服务器存活只覆盖本次实验。
type Service struct {
	Variant   string
	Metrics   *Metrics
	Telemetry *Telemetry
	URL       string
	DebugURL  string
	downURL   string
	client    *http.Client
	transport *http.Transport
	ctx       context.Context
	cancel    context.CancelFunc
	servers   []runningServer
	orderMu   sync.Mutex
	storeMu   sync.Mutex
	store     map[string]bool
	eventMu   sync.Mutex
	events    map[string]chan struct{}
	gates     map[string]chan struct{}
	audits    sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// startServer 显式绑定 127.0.0.1:0，OS 为本实例分配未占用端口，避免并行课互撞。
// 不使用 DefaultServeMux：导入 net/http/pprof 的默认注册不会暴露到业务 listener。
// Serve 的 goroutine 归 Service 所有，Close 等待 done，Shutdown 返回才完成停止连接。
func (s *Service) startServer(handler http.Handler) (runningServer, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return runningServer{}, err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second, WriteTimeout: 5 * time.Second, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return s.ctx }}
	r := runningServer{server: server, done: make(chan error, 1), address: "http://" + listener.Addr().String()}
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		r.done <- err
	}()
	s.servers = append(s.servers, r)
	return r, nil
}

// NewService 被 bad/fixed 两轮分别调用，没有依赖外部系统或全局 OTel provider。
// client.Transport 禁用环境代理，真实 HTTP 只访问本课 loopback；每次 Body 都由调用层关闭。
func NewService(parent context.Context, variant string, telemetry *Telemetry) (*Service, error) {
	if variant != "bad" && variant != "fixed" {
		return nil, errors.New("variant must be bad or fixed")
	}
	ctx, cancel := context.WithCancel(parent)
	transport := &http.Transport{Proxy: nil, MaxConnsPerHost: 8, IdleConnTimeout: time.Second}
	s := &Service{Variant: variant, Metrics: NewMetrics(variant), Telemetry: telemetry, transport: transport, client: &http.Client{Transport: transport, Timeout: 4 * time.Second}, ctx: ctx, cancel: cancel, store: make(map[string]bool), events: make(map[string]chan struct{}), gates: make(map[string]chan struct{})}
	// 构造中途也拥有已启动的 server；原构造错误与收尾错误都交给上层检查。
	fail := func(err error) (*Service, error) { return nil, errors.Join(err, s.Close()) }
	downMux := http.NewServeMux()
	downMux.HandleFunc("POST /reserve", s.downstream)
	down, err := s.startServer(downMux)
	if err != nil {
		return fail(err)
	}
	s.downURL = down.address
	frontMux := http.NewServeMux()
	frontMux.HandleFunc("POST /checkout", s.checkout)
	front, err := s.startServer(frontMux)
	if err != nil {
		return fail(err)
	}
	s.URL = front.address
	debugMux := http.NewServeMux()
	debugMux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_ = s.Metrics.WritePrometheus(w)
	})
	// 只挂明确需要的 profile，不挂 cmdline。CPU/trace handler 进一步把采样上限写死，
	// 防止复制默认 seconds=30 的接口让学习服务持续采样。可访问端点仍然只限本机。
	for _, name := range []string{"goroutine", "goroutineleak", "heap", "allocs", "block", "mutex"} {
		debugMux.Handle("GET /debug/pprof/"+name, httppprof.Handler(name))
	}
	debugMux.HandleFunc("GET /debug/pprof/profile", func(w http.ResponseWriter, r *http.Request) { r.URL.RawQuery = "seconds=1"; httppprof.Profile(w, r) })
	debugMux.HandleFunc("GET /debug/pprof/trace", func(w http.ResponseWriter, r *http.Request) { r.URL.RawQuery = "seconds=0.25"; httppprof.Trace(w, r) })
	diagnostics, err := s.startServer(debugMux)
	if err != nil {
		return fail(err)
	}
	s.DebugURL = diagnostics.address
	return s, nil
}

// watch/signal 以关闭 channel 广播“某阶段已经发生”。signal 不表示 handler 已退出，
// 例如 dispatched 只证明准备调用下游；finished 才证明请求 defer 的指标/日志完成。
// eventMu 只维护屏障 map，不与订单锁混用；close 由 signal 唯一执行且幂等。
func (s *Service) watch(key string) <-chan struct{} {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	ch, ok := s.events[key]
	if !ok {
		ch = make(chan struct{})
		s.events[key] = ch
	}
	return ch
}
func (s *Service) signal(key string) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	ch, ok := s.events[key]
	if !ok {
		ch = make(chan struct{})
		s.events[key] = ch
	}
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// Await 由实验 owner 调用：等待真实阶段或整个实验截止时间，不用 Sleep 猜调度。
func (s *Service) Await(ctx context.Context, key string) error {
	select {
	case <-s.watch(key):
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait %s: %w", key, ctx.Err())
	}
}

// gate 为 hold/unknown 请求创建独立下游门；owner Release 或 context 取消使它结束。
// Release 关闭门只是通知，调用者还要等 finished/down_finished 才证明任务真正退场。
func (s *Service) gate(id string) <-chan struct{} {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	ch, ok := s.gates[id]
	if !ok {
		ch = make(chan struct{})
		s.gates[id] = ch
	}
	return ch
}
func (s *Service) Release(id string) {
	_ = s.gate(id)
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	select {
	case <-s.gates[id]:
	default:
		close(s.gates[id])
	}
}

// Committed 通过受短锁保护的下游权威 map 查实际提交。调用日志不能替代这个查询。
func (s *Service) Committed(id string) bool {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	return s.store[id]
}
func (s *Service) commit(id string) {
	s.storeMu.Lock()
	s.store[id] = true
	s.storeMu.Unlock()
	s.signal(id + ":committed")
}

// downstream 是真实 net/http handler，由 HTTP server goroutine 调用。
// 服务端 Extract 读取从真实 HTTP header 传来的 traceparent；server span 的 Parent
// 应等于 callDependency 所建的 client span，且 Parent.IsRemote()==true。
func (s *Service) downstream(w http.ResponseWriter, r *http.Request) {
	id, mode := r.Header.Get("X-Request-ID"), r.URL.Query().Get("mode")
	ctx := s.Telemetry.Propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := s.Telemetry.Tracer.Start(ctx, "reserve.server", trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()
	defer s.signal(id + ":down_finished")
	s.signal(id + ":down_started")
	if mode == "business" {
		span.SetAttributes(attribute.String("outcome", string(BusinessError)))
		http.Error(w, "synthetic stock unavailable", http.StatusConflict)
		return
	}
	if mode == "dependency" {
		span.SetStatus(codes.Error, "synthetic dependency unavailable")
		http.Error(w, "synthetic unavailable", http.StatusServiceUnavailable)
		return
	}
	if mode == "unknown" {
		// 真实副作用已发生，随后故意阻止响应。客户端取消不能撤回这条 map 写入。
		s.commit(id)
	}
	if mode == "hold" || mode == "unknown" {
		region := runtimetrace.StartRegion(ctx, "downstream.response_gate")
		select {
		case <-s.gate(id):
		case <-ctx.Done():
			region.End()
			span.RecordError(ctx.Err())
			s.signal(id + ":down_canceled")
			return
		}
		region.End()
	}
	if mode != "unknown" {
		s.commit(id)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Reply{Outcome: Success})
}

// checkout 是业务入口，net/http 对每个连接/请求调用它。outcome 是本次 handler
// 的局部值，退出 defer 使用闭包读取最终值，不会在注册 defer 时冻结默认 Success。
// request_id 只接受有限安全字符且限长，日志不记录 header/body/URL 中的任意凭据。
func (s *Service) checkout(w http.ResponseWriter, r *http.Request) {
	id, mode := r.Header.Get("X-Request-ID"), r.URL.Query().Get("mode")
	if !safeID(id) || !validMode(mode) {
		http.Error(w, "invalid synthetic input", 400)
		return
	}
	ctx := s.Telemetry.Propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := s.Telemetry.Tracer.Start(ctx, "checkout.server", trace.WithSpanKind(trace.SpanKindServer))
	ctx, task := runtimetrace.NewTask(ctx, "m34.checkout")
	defer task.End()
	runtimetrace.Log(ctx, "request_id", id)
	started := time.Now()
	outcome := Success
	queue, dependency := time.Duration(0), time.Duration(0)
	queueObserved, dependencyObserved := false, false
	logger := s.Telemetry.Logger.With("request_id", id, "operation", "checkout", "variant", s.Variant, "trace_id", span.SpanContext().TraceID().String())
	s.Metrics.begin()
	defer func() {
		if recovered := recover(); recovered != nil {
			// recover 必须在发生 panic 的同一个 goroutine 的 defer 中直接调用。
			// debug.Stack 是 panic 路径的定位栈；普通 error 不会自动携带栈。
			outcome = InternalError
			span.SetStatus(codes.Error, "synthetic panic")
			logger.ErrorContext(ctx, "request.panic", "outcome", outcome, "stack", string(debug.Stack()))
			http.Error(w, "synthetic internal failure", 500)
		}
		duration := time.Since(started)
		s.Metrics.finish(outcome, duration)
		span.SetAttributes(attribute.String("outcome", string(outcome)))
		// 没有进入这个阶段时输出 null + observed=false，不把观测缺失伪装成零耗时。
		// any 是 interface{} 的别名；初始 nil 经 JSONHandler 编码为 null。
		var queueMS, dependencyMS any
		if queueObserved {
			queueMS = float64(queue) / float64(time.Millisecond)
		}
		if dependencyObserved {
			dependencyMS = float64(dependency) / float64(time.Millisecond)
		}
		logger.InfoContext(ctx, "request.finished", "outcome", outcome, "duration_ms", float64(duration)/float64(time.Millisecond), "queue_observed", queueObserved, "queue_ms", queueMS, "dependency_observed", dependencyObserved, "dependency_ms", dependencyMS)
		span.End()
		s.signal(id + ":finished")
	}()
	logger.InfoContext(ctx, "request.started", "outcome", "in_progress", "duration_ms", 0)
	if mode == "panic" {
		panic("synthetic checkout bug")
	}
	if mode == "cancel" {
		queueObserved = true
		// 取消样本是在派发下游前等入场门：没有副作用，因而可以确认 canceled。
		// select 对 Done 的观察与 owner 发 cancel 是两个时刻，不能混作同一事件。
		waitStarted := time.Now()
		s.Metrics.queueBegin()
		s.signal(id + ":queue_entered")
		region := runtimetrace.StartRegion(ctx, "queue.cancelable")
		select {
		case <-s.gate(id):
		case <-ctx.Done():
			outcome = Canceled
		}
		region.End()
		queue = time.Since(waitStarted)
		s.Metrics.queueEnd(queue)
		if outcome == Canceled {
			span.RecordError(ctx.Err())
			http.Error(w, "synthetic admission canceled", 499)
			return
		}
	}
	var reply Reply
	var err error
	if mode == "cpu_bad" || mode == "cpu_fixed" {
		// pprof.Do 给当前 goroutine 添加有限标签。它不会建立 distributed span，
		// CPU/goroutine profile 使用标签；heap/block/mutex 不因此带订单关联。
		pprof.Do(ctx, pprof.Labels("operation", "checkout", "phase", "cpu", "variant", mode), func(ctx context.Context) {
			runtimetrace.WithRegion(ctx, "cpu.digest", func() { reply.Value, _ = Digest(mode == "cpu_fixed", DigestIterations) })
		})
		reply.Outcome = Success
	} else if s.Variant == "bad" {
		reply, queue, dependency, err = s.badCheckout(ctx, id, mode)
		queueObserved, dependencyObserved = true, true
	} else {
		reply, queue, dependency, err = s.fixedCheckout(ctx, id, mode)
		queueObserved, dependencyObserved = true, true
	}
	outcome = reply.Outcome
	if err != nil {
		// %w 在底层保留 error 原因，当前日志只记已分类文案与显式代码，不打印任意 URL。
		span.RecordError(err)
		span.SetStatus(codes.Error, string(outcome))
		logger.WarnContext(ctx, "request.failed", "outcome", outcome, "error", err.Error(), "duration_ms", float64(time.Since(started))/float64(time.Millisecond))
	}
	status := http.StatusOK
	switch outcome {
	case BusinessError:
		status = 409
	case DependencyError:
		status = 503
	case Unknown:
		status = 504
	}
	if outcome == Success {
		s.startAudit(span.SpanContext(), id)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(reply)
}

// badCheckout 展示具体错误：订单 A 的下游 I/O 被包在全局 orderMu 里，订单 B
// 即使没有业务冲突也必须等 A 的网络返回。Mutex.Lock 没有 context 参数，不响应取消。
// defer Unlock 保证异常返回释放锁，却不能修复过大的锁范围。
func (s *Service) badCheckout(ctx context.Context, id, mode string) (Reply, time.Duration, time.Duration, error) {
	started := time.Now()
	s.Metrics.queueBegin()
	s.signal(id + ":queue_entered")
	queueRegion := runtimetrace.StartRegion(ctx, "queue.global_mutex")
	s.orderMu.Lock() // goroutine snapshot 与 block profile 应定位到这一行。
	queueRegion.End()
	queue := time.Since(started)
	s.Metrics.queueEnd(queue)
	defer s.orderMu.Unlock() // mutex profile 记录造成争用的持锁者退出临界区的栈。
	reply, dependency, err := s.callDependency(ctx, id, mode)
	return reply, queue, dependency, err
}

// fixedCheckout 修复“把网络当临界区”的误用。当前唯一共享业务状态在下游 storeMu
// 内按 map 写入短锁保护，两个不同请求没有跨 I/O 的共享不变量，因此不需要 orderMu。
// 若真实业务有库存事务/幂等要求，应由相应权威存储保障，不能照抄删锁来证明一致性。
func (s *Service) fixedCheckout(ctx context.Context, id, mode string) (Reply, time.Duration, time.Duration, error) {
	started := time.Now()
	s.Metrics.queueBegin()
	s.signal(id + ":queue_entered")
	queue := time.Since(started)
	s.Metrics.queueEnd(queue)
	reply, dependency, err := s.callDependency(ctx, id, mode)
	return reply, queue, dependency, err
}

// callDependency 由两种 checkout 调用，拥有 client span 和 Response.Body。
// Inject 修改这次请求专属 header；HTTP 从网络传值，context 指针并不跨进程共享。
// 已派发 unknown 写入返回通信错误时，error 不能证明下游没写，因此保持 Unknown。
// dependency duration 只量 signal/Do 到响应头或错误，Body 读取不在此计时范围。
// build/body 分支以 %w 保留各自原始错误；Do 错误只保留非 nil ctx.Err() 的取消原因。
// ctx.Err()==nil 时刻意返回固定分类 cause，未原样保留 *url.Error/底层网络错误链。
func (s *Service) callDependency(ctx context.Context, id, mode string) (Reply, time.Duration, error) {
	ctx, span := s.Telemetry.Tracer.Start(ctx, "reserve.client", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	request, err := http.NewRequestWithContext(ctx, "POST", s.downURL+"/reserve?mode="+mode, nil)
	if err != nil {
		return Reply{Outcome: DependencyError}, 0, fmt.Errorf("build synthetic reserve: %w", err)
	}
	request.Header.Set("X-Request-ID", id)
	s.Telemetry.Propagator.Inject(ctx, propagation.HeaderCarrier(request.Header))
	started := time.Now()
	region := runtimetrace.StartRegion(ctx, "dependency.http")
	s.signal(id + ":dispatched")
	response, err := s.client.Do(request)
	duration := time.Since(started)
	region.End()
	s.Metrics.dependency(duration)
	if err != nil {
		outcome := DependencyError
		if mode == "unknown" {
			outcome = Unknown
		}
		// 保留取消/截止原因以便 errors.Is；其余 Do 错误替换为固定 cause，避免把完整
		// URL 带入日志。这里牺牲 errors.As(*url.Error) 与底层网络错误细节，不声称全保留。
		cause := errors.New("transport did not return a usable response")
		if ctx.Err() != nil {
			cause = ctx.Err()
		}
		wrapped := fmt.Errorf("reserve response unavailable: %w", cause)
		span.RecordError(wrapped)
		return Reply{Outcome: outcome}, duration, wrapped
	}
	defer response.Body.Close()
	// 读取有界 body 并关闭，让 Transport 可复用/释放连接；不把 body 中任意内容记日志。
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if err != nil {
		return Reply{Outcome: Unknown}, duration, fmt.Errorf("reserve body incomplete: %w", err)
	}
	switch response.StatusCode {
	case 200:
		return Reply{Outcome: Success}, duration, nil
	case 409:
		return Reply{Outcome: BusinessError}, duration, nil
	default:
		return Reply{Outcome: DependencyError}, duration, errors.New("synthetic reserve rejected by infrastructure")
	}
}

// startAudit 创建请求结束后仍有意义的新任务，Service 是 goroutine 的 owner。
// 它从 Service.ctx 取得有界生命周期，使用 NewRoot + Link 关联原请求；直接沿用
// r.Context() 会在请求返回时取消它。新 span 由 audit goroutine End，Close 等它退出。
func (s *Service) startAudit(parent trace.SpanContext, id string) {
	s.audits.Add(1)
	go func() {
		defer s.audits.Done()
		ctx, cancel := context.WithTimeout(s.ctx, time.Second)
		defer cancel()
		ctx, span := s.Telemetry.Tracer.Start(ctx, "audit.async", trace.WithNewRoot(), trace.WithLinks(trace.Link{SpanContext: parent}))
		defer span.End()
		select {
		case <-ctx.Done():
			span.RecordError(ctx.Err())
		default:
			span.SetAttributes(attribute.Bool("committed", s.Committed(id)))
		}
		s.signal(id + ":audit_finished")
	}()
}

// Close 先广播停止，解除下游门/入场门的 context 等待，再关闭三个 server 并 join。
// Shutdown 的 deadline 只是清理上限；超时还会调用 Close 强制关连接并报告真实错误。
func (s *Service) Close() error {
	// 多个清理路径可以调用 Close；Once 的 Do 会等待正在执行的第一次清理完成。
	// 不重复读取已经消费的 Serve done channel，否则第二次 Close 会凭空等待超时。
	s.closeOnce.Do(func() { s.closeErr = s.close() })
	return s.closeErr
}

// close 是 Close 唯一执行的资源释放正文，返回错误由 Once 完成后交给所有 caller。
func (s *Service) close() error {
	s.cancel()
	s.transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var result error
	for _, r := range s.servers {
		if err := r.server.Shutdown(ctx); err != nil {
			result = errors.Join(result, err)
			_ = r.server.Close()
		}
		select {
		case err := <-r.done:
			result = errors.Join(result, err)
		case <-ctx.Done():
			result = errors.Join(result, ctx.Err())
		}
	}
	// Shutdown 等待 handler 完成之后，已经不会发生新的 audits.Add。
	done := make(chan struct{})
	go func() { s.audits.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		result = errors.Join(result, ctx.Err())
	}
	return result
}

// safeID/validMode 是课程入口的有限输入协议；指标标签不会由 query 的任意值扩张。
func safeID(id string) bool {
	if len(id) == 0 || len(id) > 48 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_", r) {
			return false
		}
	}
	return true
}
func validMode(mode string) bool {
	switch mode {
	case "normal", "hold", "business", "dependency", "cancel", "unknown", "panic", "cpu_bad", "cpu_fixed":
		return true
	}
	return false
}
