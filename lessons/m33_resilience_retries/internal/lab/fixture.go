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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Order 按值解码业务输入；本课仅 qty/SKU 合成数据，账本为本进程真实状态，绝非持久数据库。
type Order struct {
	SKU string `json:"sku"`
	Qty int    `json:"qty"`
}
type Receipt struct {
	ID        int   `json:"id"`
	Order     Order `json:"order"`
	Duplicate bool  `json:"duplicate"`
}

// Ledger 的一把锁包住查 key、参数绑定、append 效果和保存回执，避免并发 check-then-act。
// map/slice 是共享引用数据；对外返回 struct 值副本，禁止调用方修改底层 rows。
type Ledger struct {
	mu   sync.Mutex
	rows []Order
	keys map[string]Receipt
}

// commit 仅由真实订单 handler 调用，返回已确认的回执或 key/参数冲突。
// key为空是显式危险路径；相同key不同payload必须拒绝，否则会把另一个订单伪装成重试成功。
func (l *Ledger) commit(order Order, key string) (Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if key != "" {
		if old, ok := l.keys[key]; ok {
			if old.Order != order {
				return Receipt{}, errors.New("KEY_PAYLOAD_CONFLICT")
			}
			old.Duplicate = true
			return old, nil
		}
	}
	l.rows = append(l.rows, order) // 真正合成业务副作用在这里；响应丢失不会撤销它。
	r := Receipt{ID: len(l.rows), Order: order}
	if key != "" {
		l.keys[key] = r
	}
	return r, nil
}

// snapshot 由 owner/测试独立读取账本行数；不用客户端返回值推断副作用。
func (l *Ledger) snapshot() int { l.mu.Lock(); defer l.mu.Unlock(); return len(l.rows) }

// lookup 由真实 GET /status handler 调用，只查询稳定幂等键，没有新建订单副作用。
func (l *Ledger) lookup(key string) (Receipt, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.keys[key]
	return r, ok
}

// Fixture 是 listener、Serve goroutine、handler gates、连接和服务端重试client 的唯一 owner。
// 每组新实例/动态端口/新账本；mu 保护计数和连接，active 统计正在 ServeHTTP 的调用。
type Fixture struct {
	URL        string
	Ledger     Ledger
	server     *http.Server
	listener   net.Listener
	stop       context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	serveErr   error
	mu         sync.Mutex
	counts     map[string]int
	bodies     map[string][]string
	conns      map[net.Conn]struct{}
	gates      map[string]*Gate
	changed    chan struct{}
	sideErr    error
	active     atomic.Int64
	trace      *Trace
	downstream *Client
}

// newFixture 由 withFixture/测试创建；net.Listen 成功后资源归 Fixture，Serve 错误在 done 后读取。
func newFixture(trace *Trace) (*Fixture, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("本课 listener: %w", err)
	}
	stop, cancel := context.WithCancel(context.Background())
	f := &Fixture{URL: "http://" + ln.Addr().String(), listener: ln, stop: stop, cancel: cancel, done: make(chan struct{}), counts: make(map[string]int), bodies: make(map[string][]string), conns: make(map[net.Conn]struct{}), gates: make(map[string]*Gate), changed: make(chan struct{}, 1), trace: trace}
	f.Ledger.keys = make(map[string]Receipt)
	f.downstream = newClient(4)
	f.server = &http.Server{Handler: http.HandlerFunc(f.handle), ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 4 * time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 8 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	f.server.ConnState = func(conn net.Conn, state http.ConnState) {
		f.mu.Lock()
		if state == http.StateNew {
			f.conns[conn] = struct{}{}
		}
		// Hijack 后标准库不再管理连接；丢响应handler自己Close，此处移出server计数。
		if state == http.StateClosed || state == http.StateHijacked {
			delete(f.conns, conn)
		}
		f.mu.Unlock()
		select {
		case f.changed <- struct{}{}:
		default:
		}
	}
	go func() {
		defer close(f.done)
		f.serveErr = f.server.Serve(ln) // 真正阻塞在accept；Shutdown/Close关闭listener解除它。
	}()
	trace.event("HTTP-owner", "真实动态 listener=%s", f.URL)
	return f, nil
}

// gate 由 owner 在请求派发前登记；handler 只查同一个指针别名，不创建自己的协调对象。
func (f *Fixture) gate(name string) *Gate {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g := f.gates[name]; g != nil {
		return g
	}
	g := newGate()
	f.gates[name] = g
	return g
}

// count/bodyCopies 读取独立服务端证据。bodyCopies 复制slice，调用方不会别名修改共享数组。
func (f *Fixture) count(path string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.counts[path] }
func (f *Fixture) bodyCopies(path string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies[path]...)
}

// writeReply 在最终状态决定之后写出小JSON；写入error由fixture收集，不伪装业务成功。
func (f *Fixture) writeReply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		f.mu.Lock()
		f.sideErr = errors.Join(f.sideErr, err)
		f.mu.Unlock()
	}
}

// handle 由 net/http 的连接goroutine调用。它拥有 ResponseWriter 的使用期，到返回即结束。
// count记录实际进入handler的次数；HTTP 200只是协议状态，订单成功还需回执和Ledger证据。
func (f *Fixture) handle(w http.ResponseWriter, r *http.Request) {
	f.active.Add(1)
	defer f.active.Add(-1)
	path := r.URL.Path
	f.mu.Lock()
	f.counts[path]++
	n := f.counts[path]
	g := f.gates[r.URL.Query().Get("gate")]
	f.mu.Unlock()
	f.trace.event("handler", "%s #%d method=%s", path, n, r.Method)
	// 限制真实请求body，避免课堂资源无界；读取成功后保存原始字节供重放断言。
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		f.writeReply(w, 400, map[string]string{"code": "BAD_BODY"})
		return
	}
	f.mu.Lock()
	f.bodies[path] = append(f.bodies[path], string(data))
	f.mu.Unlock()
	switch path {
	case "/slow", "/probe":
		if g == nil {
			f.writeReply(w, 400, map[string]string{"code": "NO_GATE"})
			return
		}
		g.enter.Do(func() { close(g.Entered) })
		defer g.finish.Do(func() { close(g.Done) })
		// 真实等待点是Release/request.Done/fixture.stop，不是sleep猜客户端是否已开始。
		select {
		case <-g.Release:
			if r.Context().Err() != nil {
				return
			}
		case <-r.Context().Done():
			g.observed.Do(func() { close(g.Canceled) })
			return
		case <-f.stop.Done():
			return
		}
		f.writeReply(w, 200, map[string]string{"code": "OK"})
	case "/flaky":
		if n <= 2 {
			f.writeReply(w, 503, map[string]string{"code": "TEMPORARY"})
			return
		}
		f.writeReply(w, 200, map[string]string{"code": "OK"})
	case "/replay":
		if n%2 == 1 {
			f.writeReply(w, 503, map[string]string{"code": "TEMPORARY"})
			return
		}
		f.writeReply(w, 200, map[string]string{"code": "OK"})
	case "/always503":
		f.writeReply(w, 503, map[string]string{"code": "TEMPORARY"})
	case "/retry-after":
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			f.writeReply(w, 503, map[string]string{"code": "BUSY"})
			return
		}
		f.writeReply(w, 200, map[string]string{"code": "OK"})
	case "/retry-after-long":
		w.Header().Set("Retry-After", "1")
		f.writeReply(w, 503, map[string]string{"code": "BUSY"})
	case "/reject":
		f.writeReply(w, 422, map[string]string{"code": "OUT_OF_STOCK"})
	case "/healthy":
		f.writeReply(w, 200, map[string]string{"code": "OK"})
	case "/body-fault":
		f.writeReply(w, 200, map[string]string{"code": "OK"})
	case "/gateway":
		// 这是真实服务端二次HTTP调用，不是“n*2”虚构计数。caller ctx没有跨网络传deadline，
		// 因此当前handler额外收紧到2s；客户端断连会通过r.Context取消正在执行的下游请求。
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		serverAttempts := 2
		if raw := r.URL.Query().Get("server_attempts"); raw != "" {
			value, parseErr := strconv.Atoi(raw)
			if parseErr != nil || value < 1 || value > 2 {
				f.writeReply(w, 400, map[string]string{"code": "BAD_ATTEMPTS"})
				return
			}
			serverAttempts = value
		}
		_, _, callErr := Retry(ctx, f.downstream, Spec{Method: "GET", URL: f.URL + "/always503", Idempotent: true}, Policy{Total: 2 * time.Second, MaxAttempts: serverAttempts, Base: time.Millisecond, Cap: time.Millisecond}, f.trace)
		if callErr == nil {
			f.writeReply(w, 200, map[string]string{"code": "OK"})
			return
		}
		f.writeReply(w, 503, map[string]string{"code": "DOWNSTREAM_FAILED"})
	case "/orders", "/lost-order", "/corrupt-order", "/large-order", "/no-receipt-order", "/mismatched-order", "/empty-lost-order":
		if r.Method != "POST" {
			f.writeReply(w, 405, map[string]string{"code": "METHOD"})
			return
		}
		var order Order
		if path == "/empty-lost-order" && len(data) == 0 {
			// 显式反例：无请求body仍可能由服务端默认参数产生效果，不能据len(Body)==0跳过安全key。
			order = Order{SKU: "server-default-m33", Qty: 1}
		} else if json.Unmarshal(data, &order) != nil || order.SKU == "" || order.Qty <= 0 {
			f.writeReply(w, 422, map[string]string{"code": "BAD_ORDER"})
			return
		}
		receipt, err := f.Ledger.commit(order, r.Header.Get("Idempotency-Key"))
		if err != nil {
			f.writeReply(w, 409, map[string]string{"code": "KEY_PAYLOAD_CONFLICT"})
			return
		}
		f.trace.event("ledger", "id=%d duplicate=%t rows=%d", receipt.ID, receipt.Duplicate, f.Ledger.snapshot())
		if path == "/lost-order" || path == "/empty-lost-order" {
			// 效果先完成，随后真实接管TCP并断开，故意让客户端收不到任何response headers。
			// Hijack不是回滚；此连接转为handler所有，必须自己Close并收集错误。
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				err = conn.Close()
			}
			if err != nil {
				f.mu.Lock()
				f.sideErr = errors.Join(f.sideErr, err)
				f.mu.Unlock()
			}
			return
		}
		// 这四个故障都在真实commit之后选择201。client看见成功HTTP但响应不可确认时，
		// 必须UNKNOWN并查原key；handler次数与账本行数是独立证据，不用响应中的ID推断commit。
		switch path {
		case "/corrupt-order":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(201)
			if _, err := io.WriteString(w, `{"code":"OK","receipt":`); err != nil {
				f.mu.Lock()
				f.sideErr = errors.Join(f.sideErr, err)
				f.mu.Unlock()
			}
			return
		case "/large-order":
			f.writeReply(w, 201, map[string]any{"code": "OK", "receipt": receipt, "padding": strings.Repeat("x", 6000)})
			return
		case "/no-receipt-order":
			f.writeReply(w, 201, map[string]string{"code": "OK"})
			return
		case "/mismatched-order":
			wrong := receipt
			wrong.Order.Qty++ // struct值副本只损坏响应，账本原回执仍是实际参数。
			f.writeReply(w, 201, map[string]any{"code": "OK", "receipt": wrong})
			return
		}
		f.writeReply(w, 201, map[string]any{"code": "OK", "receipt": receipt})
	case "/status":
		receipt, exists := f.Ledger.lookup(r.URL.Query().Get("key"))
		if !exists {
			f.writeReply(w, 404, map[string]string{"code": "NOT_FOUND"})
			return
		}
		f.writeReply(w, 200, map[string]any{"code": "OK", "receipt": receipt})
	default:
		f.writeReply(w, 404, map[string]string{"code": "UNKNOWN_PATH"})
	}
}

// close 由owner的defer在所有退出路径调用。先cancel/unblock，再独立Shutdown/join Serve与连接。
// Serve返回不能证明handler全退出；Shutdown成功与active/conn归零分别检查，不读取result冒充join。
func (f *Fixture) close() error {
	f.cancel()
	f.mu.Lock()
	gates := make([]*Gate, 0, len(f.gates))
	for _, g := range f.gates {
		gates = append(gates, g)
	}
	f.mu.Unlock()
	for _, g := range gates {
		g.Open()
	}
	f.downstream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := f.server.Shutdown(ctx)
	if err != nil {
		err = errors.Join(err, f.server.Close())
	}
	err = errors.Join(err, join(f.done, "Server.Serve Done"))
	if closed(f.done) && f.serveErr != nil && !errors.Is(f.serveErr, http.ErrServerClosed) {
		err = errors.Join(err, f.serveErr)
	}
	// ConnState通知避免sleep猜socket是否已经关闭；清理ctx有独立上限。
	for {
		f.mu.Lock()
		live := len(f.conns)
		f.mu.Unlock()
		if live == 0 {
			break
		}
		select {
		case <-f.changed:
		case <-ctx.Done():
			return errors.Join(err, fmt.Errorf("连接未归零: %w", ctx.Err()))
		}
	}
	f.mu.Lock()
	sideErr := f.sideErr
	f.mu.Unlock()
	err = errors.Join(err, sideErr, check(f.active.Load() == 0, "handler active=%d", f.active.Load()), check(f.downstream.bodies.Load() == 0, "服务端client body未归零"))
	f.trace.event("HTTP-owner", "清理 active=%d conns=0 Serve=joined listener=closed err=%v", f.active.Load(), err)
	return err
}
