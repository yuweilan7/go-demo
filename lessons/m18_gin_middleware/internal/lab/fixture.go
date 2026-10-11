package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// Endpoint 拥有一个动态 loopback listener、Serve goroutine 和连接计数。
// 连接计数来自真实 ConnState，active 来自真正的 ServeHTTP 调用，不是模拟 HTTP。
type Endpoint struct {
	URL      string
	server   *http.Server
	listener net.Listener
	finished chan struct{}
	serveErr error
	active   atomic.Int64
	mu       sync.Mutex
	live     int
	changed  chan struct{}
}

// newEndpoint 由 NewRig 创建应用/依赖各一份。root 是 Rig 独占取消范围，
// BaseContext 让 Rig 收尾时所有入站 handler 都能收到停止通知。
func newEndpoint(root context.Context, handler http.Handler) (*Endpoint, error) {
	// :=声明此函数局部的ln/err；err!=nil时尚未取得listener，不能对nil资源登记Close。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("创建本课 listener: %w", err)
	}
	e := &Endpoint{URL: "http://" + ln.Addr().String(), listener: ln, finished: make(chan struct{}), changed: make(chan struct{}, 1)}
	e.server = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			e.active.Add(1)
			defer e.active.Add(-1)
			handler.ServeHTTP(w, req)
		}),
		BaseContext:       func(net.Listener) context.Context { return root },
		ReadHeaderTimeout: time.Second, ReadTimeout: 3 * time.Second,
		WriteTimeout: 3 * time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 8 << 10,
		ErrorLog: log.New(io.Discard, "", 0),
		ConnState: func(_ net.Conn, state http.ConnState) {
			e.mu.Lock()
			if state == http.StateNew {
				e.live++
			} else if state == http.StateClosed {
				e.live--
			}
			e.mu.Unlock()
			// 通知可以合并，事实始终从 Live 受锁读取；不依赖通知次数计数。
			select {
			case e.changed <- struct{}{}:
			default:
			}
		},
	}
	go func() {
		defer close(e.finished)
		e.serveErr = e.server.Serve(ln)
	}()
	return e, nil
}

func (e *Endpoint) Live() int { e.mu.Lock(); defer e.mu.Unlock(); return e.live }

// Close 由 Rig 在所有 gate 释放后调用；Shutdown 等 active handler，Serve finished
// 是另一个需要 join 的事实。超时会 Close 自己的 server，并返回精确失败，绝不宣称完成。
func (e *Endpoint) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := e.server.Shutdown(ctx)
	if err != nil {
		err = errors.Join(err, e.server.Close())
	}
	if joinErr := wait(ctx, e.finished, "Server.Serve 返回"); joinErr != nil {
		return errors.Join(err, joinErr)
	}
	if !errors.Is(e.serveErr, http.ErrServerClosed) {
		err = errors.Join(err, e.serveErr)
	}
	for e.Live() != 0 {
		if waitErr := wait(ctx, e.changed, "ConnState live=0"); waitErr != nil {
			return errors.Join(err, waitErr)
		}
	}
	if e.active.Load() != 0 {
		err = errors.Join(err, fmt.Errorf("仍有 handler=%d", e.active.Load()))
	}
	return err
}

// DependencyGate 是一次 slow-order 专属的两级屏障。Entered 证明真实依赖 handler 已进入；
// Canceled 证明该 handler 观察到网络取消；Cleanup 允许故意延迟的清理继续；Done 才表示退出。
// inbound 由 Gin handler 写入后通过 AppEntered 同步发布，owner 可等待它的 Done。
type DependencyGate struct {
	Entered, Canceled, Release, Cleanup, Done, AppEntered, AppDone *Signal
	inbound                                                        context.Context
}

func newDependencyGate() *DependencyGate {
	return &DependencyGate{Entered: newSignal(), Canceled: newSignal(), Release: newSignal(), Cleanup: newSignal(), Done: newSignal(), AppEntered: newSignal(), AppDone: newSignal()}
}

// Open 由实验 owner 或 Rig 清理释放所有可能的等待；它不等待 Done。
func (g *DependencyGate) Open() { g.Release.Open(); g.Cleanup.Open() }

// Audit 是 allowlist 日志值副本；不含 Authorization、Cookie、URL query、请求/响应 body。
// FullPath 是路由模板，避免把订单编号或用户输入路径直接写日志。
type Audit struct {
	Request string
	Method  string
	Route   string
	User    string
	Peer    string
	Status  int
	Code    string
}

// Snapshot 是后台任务真正需要的不可变值，不包含 Gin Context、Request、Writer 或指针字段。
type Snapshot struct{ User, Request, Route string }

type Life struct {
	Started, RequestCanceled, Release, Cleanup *Signal
	Job                                        *Job
	Snapshot                                   Snapshot
	// 下列事实在 handler 同步调用 Copy 时提取，后台任务不保存或访问原 c/copy。
	SameRequest, WriterUnavailable, GinDoneNil, ShallowValue bool
	mu                                                       sync.Mutex
	effects                                                  int
}

func (l *Life) Effects() int { l.mu.Lock(); defer l.mu.Unlock(); return l.effects }

// Rig 是每个实验的 owner：应用/依赖服务、真实依赖 client、账本、后台任务和屏障。
// 所有共享 map 都由 mu 保护；源码中的 *Rig 接收者指向同一个对象，而不是复制整套锁/map。
type Rig struct {
	App, Dependency *Endpoint
	Ledger          Ledger
	root            context.Context
	cancel          context.CancelFunc
	depClient       *http.Client
	depTransport    *http.Transport
	Jobs            atomic.Int64
	requests        atomic.Int64
	mu              sync.Mutex
	gates           map[string]*DependencyGate
	lives           map[string]*Life
	traces          map[string][]string
	audits          map[string]Audit
	changed         chan struct{}
	previous        *gin.Context
}

var modeOnce sync.Once

// NewRig 由 demo/测试创建；失败时收回已创建服务，成功后 caller 必须立即安排 Close。
// Gin mode 是全局配置，只设置一次；可信代理配置则属于每个 engine，默认明确不信任转发头。
func NewRig(trusted []string) (*Rig, error) {
	modeOnce.Do(func() { gin.SetMode(gin.ReleaseMode) })
	root, cancel := context.WithCancel(context.Background())
	r := &Rig{root: root, cancel: cancel, gates: make(map[string]*DependencyGate), lives: make(map[string]*Life), traces: make(map[string][]string), audits: make(map[string]Audit), changed: make(chan struct{}, 1)}
	transport := &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 4, ResponseHeaderTimeout: 2 * time.Second}
	r.depTransport, r.depClient = transport, &http.Client{Transport: transport, Timeout: 2500 * time.Millisecond}
	var err error
	r.Dependency, err = newEndpoint(root, http.HandlerFunc(r.dependencyHandler))
	if err != nil {
		cancel()
		return nil, err
	}
	engine, err := r.router(trusted)
	if err == nil {
		r.App, err = newEndpoint(root, engine)
	}
	if err != nil {
		cancel()
		return nil, errors.Join(err, r.Dependency.Close())
	}
	return r, nil
}

// Gate 必须在发 slow 请求以前注册，保证双方使用同一个屏障。名字属于当前 Rig。
func (r *Rig) Gate(name string) *DependencyGate {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := newDependencyGate()
	r.gates[name] = g
	return g
}

func (r *Rig) gate(name string) *DependencyGate {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gates[name]
}
func (r *Rig) Life(name string) *Life { r.mu.Lock(); defer r.mu.Unlock(); return r.lives[name] }

// trace 在请求 goroutine 内记录顺序，但共享存储使用锁。id 只用于本实验协调；真实日志
// 应使用服务器生成/严格校验的 request ID，不把任意头值直接信任为审计身份。
func (r *Rig) trace(id, event string) {
	r.mu.Lock()
	r.traces[id] = append(r.traces[id], event)
	r.mu.Unlock()
}
func (r *Rig) Trace(id string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.traces[id]...)
}

// WaitAudit 由 owner 在客户端已读取 body 后调用。body 到达不保证外层 middleware
// 已执行 after；这里等待实际审计记录，让顺序断言有真实同步依据。
func (r *Rig) WaitAudit(ctx context.Context, id string) (Audit, error) {
	for {
		r.mu.Lock()
		audit, exists := r.audits[id]
		r.mu.Unlock()
		if exists {
			return audit, nil
		}
		if err := wait(ctx, r.changed, "audit 写入"); err != nil {
			return Audit{}, err
		}
	}
}

// Close 覆盖正常和提前失败：先取消Rig root并解除gate，shutdown应用使后台注册者退出，
// 再逐个独立预算join Job、回收依赖池、shutdown依赖并join Serve。errors.Join保留清理诊断。
func (r *Rig) Close() error {
	r.cancel()
	r.mu.Lock()
	gates := make([]*DependencyGate, 0, len(r.gates))
	for _, g := range r.gates {
		gates = append(gates, g)
	}
	lives := make([]*Life, 0, len(r.lives))
	for _, life := range r.lives {
		lives = append(lives, life)
	}
	r.mu.Unlock()
	for _, g := range gates {
		g.Open()
	}
	// 先停止HTTP入口并等handler返回，再取后台任务快照；否则正执行lifecycle的handler
	// 可能在快照之后注册Job，导致早失败路径漏join。root已取消且gate已开，handler能退出。
	err := r.App.Close()
	r.mu.Lock()
	lives = lives[:0]
	for _, life := range r.lives {
		lives = append(lives, life)
	}
	r.mu.Unlock()
	for _, life := range lives {
		err = errors.Join(err, life.Job.StopAndJoin(func() { life.Release.Open(); life.Cleanup.Open() }))
	}
	r.depTransport.CloseIdleConnections()
	err = errors.Join(err, r.Dependency.Close())
	if r.Jobs.Load() != 0 {
		err = errors.Join(err, fmt.Errorf("后台 jobs=%d", r.Jobs.Load()))
	}
	return err
}

// dependencyHandler 是实际独立 HTTP 服务，由应用的 http.Client 经过 TCP 调用。
// select 真正等待 Release/请求取消/root。取消观察后仍在 Cleanup 等待，让实验能分辨
// “依赖已观察取消”与“依赖退出”；Rig root 和 3秒安全 timer 保证 owner 失败也能收回任务。
func (r *Rig) dependencyHandler(w http.ResponseWriter, req *http.Request) {
	g := r.gate(req.URL.Query().Get("gate"))
	if g == nil {
		http.Error(w, "GATE_REQUIRED", 400)
		return
	}
	defer g.Done.Open()
	g.Entered.Open()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-g.Release.C:
		if req.Context().Err() == nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"allowed":true}`)
		}
	case <-req.Context().Done():
		g.Canceled.Open()
	case <-r.root.Done():
	case <-timer.C:
	}
	select {
	case <-g.Cleanup.C:
	case <-r.root.Done():
	case <-timer.C:
	}
}
