// Package lab 用真实 loopback TCP/HTTP 服务演示响应体所有权、连接复用和副作用边界。
package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const token = "Bearer m17-local-learning"

// Envelope 是服务端与客户端共同使用的合成订单协议。HTTP 状态、Code 和 Committed
// 分别回答“HTTP 处理结果”“业务结果”“本实验账本是否已写入”，不能用 200 替代后两项。
type Envelope struct {
	Code      string `json:"code"`
	Committed bool   `json:"committed"`
	OrderID   int    `json:"order_id,omitempty"`
	Peer      string `json:"peer,omitempty"`
}

type Order struct {
	SKU string `json:"sku"`
	Qty int    `json:"qty"`
}

// Ledger 是本轮实验的真实内存业务状态，不是数据库替身；它只证明当前进程内的副作用。
// mu 保护 rows 与 keys 的同一原子决策；不把“先查 key，解锁，再写”误当成并发幂等保证。
type Ledger struct {
	mu   sync.Mutex
	rows []Order
	keys map[string]int
}

// Commit 由订单 handler 调用，返回订单编号和是否新写入。unsafe=true 是显式危险入口，
// 故意忽略幂等键以观察重复下单；正常分支在同一临界区内完成查重与写入。
func (l *Ledger) Commit(order Order, key string, unsafe bool) (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !unsafe {
		if id, exists := l.keys[key]; exists {
			return id, false
		}
	}
	l.rows = append(l.rows, order)
	id := len(l.rows)
	if !unsafe {
		l.keys[key] = id
	}
	return id, true
}

// Count 给实验 owner 一份账本行数值副本；不向外返回共享 slice，避免观察者造成数据竞争。
func (l *Ledger) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.rows)
}

// Gate 是一次请求专属的协议屏障。handler 关闭 Entered/Canceled/Done 表示事实发生；
// 实验 owner 关闭 Release 允许继续。close 是广播通知，不能当成“已退出”的事实。
// sync.Once 让失败清理与正常释放可以重复调用 Open，不会 double close panic。
type Gate struct {
	Entered  chan struct{}
	Release  chan struct{}
	Canceled chan struct{}
	Done     chan struct{}
	open     sync.Once
	canceled sync.Once
	mu       sync.Mutex
	err      error
}

// newGate 由 Fixture.Gate 调用，为一个请求创建四个独立的未关闭 channel。
func newGate() *Gate {
	return &Gate{Entered: make(chan struct{}), Release: make(chan struct{}), Canceled: make(chan struct{}), Done: make(chan struct{})}
}

// Open 由 owner 或 Fixture.Close 发允许继续的通知；它不等待 handler.Done。
func (g *Gate) Open() { g.open.Do(func() { close(g.Release) }) }

// markCanceled 只记录 handler 已观察到 Request.Context 的取消；它不提交、不退出、不关闭连接。
func (g *Gate) markCanceled() { g.canceled.Do(func() { close(g.Canceled) }) }

// setError/Error 在 handler 与实验 owner 之间传递实际 Flush 错误；返回 error 接口值副本。
func (g *Gate) setError(err error) { g.mu.Lock(); g.err = err; g.mu.Unlock() }
func (g *Gate) Error() error       { g.mu.Lock(); defer g.mu.Unlock(); return g.err }

type connKey struct{}

// Fixture 拥有监听器、Serve goroutine、全部 gate 和账本。每个实验新建一个实例，
// 127.0.0.1:0 让操作系统分配独占端口；只占用本模块创建的连接，不依赖外部服务。
type Fixture struct {
	URL       string
	Server    *http.Server
	Ledger    Ledger
	listener  net.Listener
	serveDone chan error
	stop      chan struct{}
	mu        sync.Mutex
	gates     map[string]*Gate
	conns     map[net.Conn]int
	nextID    int
	live      int
	handled   int
	changed   chan struct{}
}

// NewFixture 由实验入口或测试调用。WriteTimeout 可以缩短以单独观察写阶段，零值参数
// 使用普通短 JSON 服务的 2 秒预算。这些值适合本地合成实验，生产值须来自请求大小/SLO。
func NewFixture(writeBudget time.Duration) (*Fixture, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("创建本课 listener: %w", err)
	}
	f := &Fixture{URL: "http://" + ln.Addr().String(), listener: ln, serveDone: make(chan error, 1), stop: make(chan struct{}), gates: make(map[string]*Gate), conns: make(map[net.Conn]int), changed: make(chan struct{}, 1)}
	f.Ledger.keys = make(map[string]int)
	if writeBudget == 0 {
		writeBudget = 2 * time.Second
	}
	f.Server = &http.Server{
		Handler:           http.HandlerFunc(f.serveHTTP),
		ReadHeaderTimeout: 250 * time.Millisecond,
		ReadTimeout:       700 * time.Millisecond,
		IdleTimeout:       500 * time.Millisecond,
		WriteTimeout:      writeBudget,
		MaxHeaderBytes:    8 << 10,
		// late-status 的 superfluous WriteHeader 是预期危险对照；不污染终端。
		ErrorLog: log.New(io.Discard, "", 0),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			f.mu.Lock()
			f.nextID++
			id := f.nextID
			f.conns[c] = id
			f.mu.Unlock()
			// 自定义 key 避免与其他包字符串 key 冲突；本请求只读连接 ID 值副本。
			return context.WithValue(ctx, connKey{}, id)
		},
		ConnState: f.connState,
	}
	go func() {
		// Serve 管理 accept 与每条连接的 HTTP goroutine。owner 保留 buffered 完成通道，
		// 即使清理暂时还没接收，Serve 也能报告返回，不会卡在发送完成消息。
		f.serveDone <- f.Server.Serve(ln)
	}()
	return f, nil
}

// connState 由 net/http 回调，记录实际新连接和关闭状态；HTTP/1.1 实验不用 Hijack。
// changed 容量 1 只表示“请重新检查状态”，允许合并通知，真实事实始终来自受锁保护的 live。
func (f *Fixture) connState(c net.Conn, state http.ConnState) {
	f.mu.Lock()
	if state == http.StateNew {
		f.live++
	} else if state == http.StateClosed {
		f.live--
		delete(f.conns, c)
	}
	f.mu.Unlock()
	select {
	case f.changed <- struct{}{}:
	default:
	}
}

// Gate 在发请求之前由 owner 注册，避免 handler 与 owner 各自拿到一个屏障。
func (f *Fixture) Gate(name string) *Gate {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := newGate()
	f.gates[name] = g
	return g
}

// gate 只供 handler 查询预先登记的屏障；没有登记时返回 nil 并拒绝该实验请求。
func (f *Fixture) gate(name string) *Gate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gates[name]
}

// Counts 供 owner/测试读取连接和实际进入 handler 的次数；三项在同一把锁下取快照。
func (f *Fixture) Counts() (accepted, live, handled int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nextID, f.live, f.handled
}

// Close 由实验 owner 在释放客户端 body/连接后调用。先通知所有 gate，保证失败分支
// 也不会遗留等待 handler；Shutdown 等活动请求退出，再接收 Serve 返回，最后等 live=0。
// Server.Close 是有界 graceful shutdown 失败后的兜底，不把 Shutdown 返回误当成 Serve join。
func (f *Fixture) Close() error {
	close(f.stop)
	f.mu.Lock()
	for _, g := range f.gates {
		g.Open()
	}
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := f.Server.Shutdown(ctx)
	if err != nil {
		_ = f.Server.Close()
	}
	select {
	case serveErr := <-f.serveDone:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			err = errors.Join(err, serveErr)
		}
	case <-ctx.Done():
		return errors.Join(err, fmt.Errorf("Serve 未退出: %w", ctx.Err()))
	}
	for {
		_, live, _ := f.Counts()
		if live == 0 {
			return err
		}
		select {
		case <-f.changed:
		case <-ctx.Done():
			return errors.Join(err, fmt.Errorf("仍有 %d 条服务器连接: %w", live, ctx.Err()))
		}
	}
}

// waitGate 是正常 handler 的协作取消点。它等待 owner Release、请求取消或 fixture 停止，
// 并有独立安全上限；请求取消被观察后返回 false，上层据此放弃后续副作用。
func (f *Fixture) waitGate(r *http.Request, g *Gate) bool {
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-g.Release:
		return true
	case <-r.Context().Done():
		g.markCanceled()
		return false
	case <-f.stop:
		return false
	case <-timer.C:
		g.setError(errors.New("handler 安全上限到期"))
		return false
	}
}

// writeJSON 只在 handler 尚未返回时同步使用 writer。先编码再选状态，使编码错误仍可
// 返回 500；一旦 WriteHeader/Write 选择最终状态，再调用 WriteHeader 无法改成另一状态。
func writeJSON(w http.ResponseWriter, status int, value Envelope) {
	data, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "编码失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// readOrder 把不可信网络输入变成已验证的 Order 值。服务端拥有 Request.Body，通常由
// net/http 在 handler 结束后关闭；这里用 MaxBytesReader 包装并立即安排 Close。
// Content-Length 可以缺失/伪造，真正大小限制必须施加在实际读取上。
func readOrder(w http.ResponseWriter, r *http.Request) (Order, bool) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, Envelope{Code: "METHOD"})
		return Order{}, false
	}
	if r.Header.Get("Authorization") != token {
		writeJSON(w, http.StatusUnauthorized, Envelope{Code: "AUTH"})
		return Order{}, false
	}
	if r.Header.Get("Content-Type") != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, Envelope{Code: "CONTENT_TYPE"})
		return Order{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var order Order
	err := decoder.Decode(&order)
	if err == nil {
		// 第二次 Decode 要求 EOF：只读第一个 JSON 会悄悄接受尾随第二个对象。
		var extra any
		if trailing := decoder.Decode(&extra); trailing != io.EOF {
			if trailing == nil {
				err = errors.New("多个 JSON 对象")
			} else {
				err = trailing
			}
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		status := http.StatusBadRequest
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, Envelope{Code: "INPUT"})
		return Order{}, false
	}
	if order.SKU != "sku-m17" || order.Qty < 1 || order.Qty > 3 {
		writeJSON(w, http.StatusUnprocessableEntity, Envelope{Code: "ORDER_INVALID"})
		return Order{}, false
	}
	return order, true
}

// serveHTTP 是真实 net/http.HandlerFunc 的目标。每条请求由 HTTP server 调用它，
// 它同步执行到返回；下面的 Request 指针/ResponseWriter 都属于本次请求，不能存起来
// 交给 handler 返回后的 goroutine 继续写。共享 Ledger 的线程安全由 Ledger 自己承担。
func (f *Fixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.handled++
	f.mu.Unlock()
	id, _ := r.Context().Value(connKey{}).(int)
	w.Header().Set("X-Conn-ID", fmt.Sprint(id))
	g := f.gate(r.URL.Query().Get("gate"))
	switch r.URL.Path {
	case "/ok":
		writeJSON(w, 200, Envelope{Code: "OK", Committed: true})
	case "/bad-json":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":`)
	case "/large", "/leak":
		// 固定 1 MiB 响应让超限策略可以被实际字节证明；不信任响应头做唯一防线。
		w.Header().Set("Content-Length", fmt.Sprint(1<<20))
		_, _ = io.WriteString(w, strings.Repeat("x", 1<<20))
	case "/early":
		// 声称 100 字节却只写 2 字节并返回。net/http 实际关闭 HTTP/1 连接，
		// 客户端读 body 得到 unexpected EOF；Do 仍可能已成功交付 200 响应头。
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "{}")
	case "/business-error":
		writeJSON(w, 200, Envelope{Code: "OUT_OF_STOCK"})
	case "/legacy":
		// 危险业务反例：写出 200/OK，却没有调用 Ledger.Commit，账本仍为 0。
		writeJSON(w, 200, Envelope{Code: "OK", Committed: false})
	case "/late-status":
		w.WriteHeader(http.StatusOK)
		w.WriteHeader(http.StatusInternalServerError) // 故意错误：最终状态已是 200。
		_, _ = io.WriteString(w, `{"code":"REJECTED","committed":false}`)
	case "/fixed-status":
		writeJSON(w, http.StatusConflict, Envelope{Code: "REJECTED"})
	case "/identity":
		// 没有可信反向代理部署配置，所以忽略用户可伪造的 X-Forwarded-For。
		writeJSON(w, 200, Envelope{Code: "OK", Committed: true, Peer: r.RemoteAddr})
	case "/orders", "/lost-order":
		order, ok := readOrder(w, r)
		if !ok {
			return
		}
		unsafe := r.URL.Query().Get("unsafe") == "1"
		key := r.Header.Get("Idempotency-Key")
		if !unsafe && key == "" {
			writeJSON(w, 400, Envelope{Code: "KEY_REQUIRED"})
			return
		}
		orderID, _ := f.Ledger.Commit(order, key, unsafe)
		if r.URL.Path == "/lost-order" {
			// 显式协议故障：已经提交却不发送响应。标准库捕获 ErrAbortHandler 后
			// 关闭本条 HTTP/1 连接，不打印panic栈；它不是取消/回滚 Ledger 的指令。
			// 此端点用实际 handler 次数检验 POST 没有被 Transport 自动重放。
			panic(http.ErrAbortHandler)
		}
		writeJSON(w, 201, Envelope{Code: "OK", Committed: true, OrderID: orderID})
	case "/headers", "/body", "/write-stall", "/stream", "/cancel-order", "/cooperative-order":
		if r.URL.Path == "/cancel-order" || r.URL.Path == "/cooperative-order" {
			if _, ok := readOrder(w, r); !ok {
				return
			}
		}
		if g == nil {
			writeJSON(w, 400, Envelope{Code: "GATE_REQUIRED"})
			return
		}
		defer close(g.Done) // Done 证明 handler 已走完；不是 Release 的别名。
		f.gatedHandler(w, r, g)
	default:
		writeJSON(w, 404, Envelope{Code: "NOT_FOUND"})
	}
}

// gatedHandler 只处理需要精确时间线的路由；owner 先等 Entered，再取消/释放请求。
// writer 始终在当前 handler 内使用，所有等待都有 request ctx、fixture stop 或安全 timer。
func (f *Fixture) gatedHandler(w http.ResponseWriter, r *http.Request, g *Gate) {
	if r.URL.Path == "/body" || r.URL.Path == "/stream" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		if r.URL.Path == "/stream" {
			// 长流应在每段写前设置新的写 deadline。本例是 2 段 NDJSON，
			// 固定总 WriteTimeout 对真实长流通常不合适，应隔离 listener/单独预算。
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = io.WriteString(w, "{\"part\":1}\n")
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			g.setError(err)
			close(g.Entered)
			return
		}
	}
	close(g.Entered)
	if r.URL.Path == "/cancel-order" {
		// 危险分支故意观察取消后仍执行订单。它有 fixture stop 与 3 秒保险，
		// 不把“故意忽略 ctx”实现为不可收回的永久 goroutine。
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			g.markCanceled()
		case <-f.stop:
			return
		case <-timer.C:
			return
		}
		select {
		case <-g.Release:
		case <-f.stop:
			return
		case <-timer.C:
			return
		}
		f.Ledger.Commit(Order{SKU: "sku-m17", Qty: 1}, "canceled-unsafe", true)
		writeJSON(w, 201, Envelope{Code: "OK", Committed: true})
		return
	}
	if !f.waitGate(r, g) {
		return
	}
	switch r.URL.Path {
	case "/cooperative-order":
		// Release 与取消可能同时就绪，select 允许选 Release；再检查 ctx 能覆盖
		// 这个可见取消窗口，但不能使“检查 + 外部提交”原子化，仍需幂等/事务设计。
		if r.Context().Err() != nil {
			g.markCanceled()
			return
		}
		f.Ledger.Commit(Order{SKU: "sku-m17", Qty: 1}, "cooperative", false)
		writeJSON(w, 201, Envelope{Code: "OK", Committed: true})
	case "/write-stall":
		_, _ = io.WriteString(w, "{\"code\":\"OK\"}")
		g.setError(http.NewResponseController(w).Flush())
	case "/stream":
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = io.WriteString(w, "{\"part\":2}\n")
		g.setError(http.NewResponseController(w).Flush())
	default:
		writeJSON(w, 200, Envelope{Code: "OK", Committed: true})
	}
}
