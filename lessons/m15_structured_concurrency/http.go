package lesson

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
)

// Catalog是本课共同合成数据。每次返回一个新map；单价是分，不做浮点金额比较。
func Catalog() map[string]int64 {
	return map[string]int64{"SKU-A": 12500, "SKU-B": 1800, "SKU-D": 4200}
}

var DemoIDs = []string{"SKU-A", "SKU-B", "SKU-C", "SKU-D"}

type wirePrice struct {
	ID    string `json:"id"`
	Price int64  `json:"price"`
	Code  string `json:"code"`
}

// HTTPFixture是实际loopback HTTP/1.1价格服务，不是Fetch替身或数据库。
// Server拥有handler goroutine，fixture owner拥有listener、Serve、请求gate及最终Shutdown。
// gates固定不再改map，所以并发读取无需锁；每个Gate内部Once只保护关闭权限。
type HTTPFixture struct {
	URL                         string
	Entered                     <-chan string // caller只能观察，不能关闭handler的发送通道。
	Finished                    <-chan string
	entered                     chan string
	finished                    chan string
	gates                       map[string]*Gate
	server                      *http.Server
	serve                       sync.WaitGroup
	serveDone                   chan struct{}
	serveErr                    error
	handlers                    sync.WaitGroup
	tracker                     Tracker
	mu                          sync.Mutex
	completed                   []string
	connections                 sync.WaitGroup
	connMu                      sync.Mutex
	live                        map[net.Conn]struct{}
	accepted, closedConnections int
	Accepted                    <-chan struct{}
	connectionEntered           chan struct{}
}
type ConnectionCounts struct{ Active, Accepted, Closed int }

// NewHTTPFixture由demo/test调用，hold=true让owner逐ID放行，false用于顺序基线。
// 只绑定系统分配的127.0.0.1端口，不占共用服务端口；成功后caller必须Close。
func NewHTTPFixture(hold bool) (*HTTPFixture, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	entered, finished := make(chan string, 128), make(chan string, 128)
	accepted := make(chan struct{}, 128)
	f := &HTTPFixture{URL: "http://" + listener.Addr().String(), Entered: entered, Finished: finished, entered: entered, finished: finished, gates: make(map[string]*Gate), serveDone: make(chan struct{}), live: make(map[net.Conn]struct{}), Accepted: accepted, connectionEntered: accepted}
	for _, id := range DemoIDs {
		f.gates[id] = NewGate()
		if !hold {
			f.gates[id].Release()
		}
	}
	f.server = &http.Server{Handler: http.HandlerFunc(f.handle), ConnState: f.connectionState}
	f.serve.Go(func() { f.serveErr = f.server.Serve(listener); close(f.serveDone) })
	return f, nil
}

// connectionState由net/http调用，覆盖“TCP已接纳却还没进入handler”的连接owner。
// Go1.27 Serve在返回前同步调用StateNew，然后才go conn.serve；所以先serve.Wait后不会再Add。
// HTTP/1.1的StateClosed位于conn.serve的退出defer，发生在handler返回与连接关闭之后。
// 本服务无Hijack/WebSocket；这类外借连接要建立自己的owner协议，不能套用这里的Closed等待。
func (f *HTTPFixture) connectionState(conn net.Conn, state http.ConnState) {
	f.connMu.Lock()
	defer f.connMu.Unlock()
	switch state {
	case http.StateNew:
		f.connections.Add(1)
		f.live[conn] = struct{}{}
		f.accepted++
		select {
		case f.connectionEntered <- struct{}{}:
		default:
		} // 观察通知不阻塞Serve，不参与连接计数责任。
	case http.StateClosed:
		if _, owned := f.live[conn]; owned {
			delete(f.live, conn)
			f.closedConnections++
			f.connections.Done()
		}
	}
}

// ConnectionCounts由owner在join后读取关联快照；Active是连接数，与活跃HTTP查询数分开。
func (f *HTTPFixture) ConnectionCounts() ConnectionCounts {
	f.connMu.Lock()
	defer f.connMu.Unlock()
	return ConnectionCounts{len(f.live), f.accepted, f.closedConnections}
}

// handle由net/http在请求goroutine调用。URL中的SKU是此请求string值，catalog为独立只读快照。
// gate只延迟响应，ctx.Done则中止尚未返回的查询；读取没有扣库存/数据库写入副作用。
func (f *HTTPFixture) handle(w http.ResponseWriter, r *http.Request) {
	f.handlers.Add(1)
	defer f.handlers.Done()
	id := r.URL.Query().Get("id")
	gate, ok := f.gates[id]
	if !ok {
		http.Error(w, "unknown experiment SKU", http.StatusBadRequest)
		return
	}
	leave := f.tracker.Enter(1)
	defer leave()
	// 实验每组<=128项所以观察邮箱有已知上限；如果caller复用服务却不消费，取消仍能解除发送。
	select {
	case f.entered <- id:
	case <-r.Context().Done():
		return
	}
	select {
	case <-gate.C:
	case <-r.Context().Done():
		return
	}
	if r.Context().Err() != nil {
		return
	} // 门与取消同时就绪时，先检查可减少已取消请求的响应工作。
	price, found := Catalog()[id]
	payload := wirePrice{ID: id, Price: price}
	status := http.StatusOK
	if !found {
		payload.Code = "UNAVAILABLE"
		status = http.StatusNotFound
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		return
	}
	f.mu.Lock()
	f.completed = append(f.completed, id)
	f.mu.Unlock() // 仅本进程日志账本，非客户端接收证明。
	select {
	case f.finished <- id:
	case <-r.Context().Done():
	}
}

// Release由owner调用；错误ID不会创建隐藏新gate，实验启动前已固定所有gate。
func (f *HTTPFixture) Release(id string) error {
	g, ok := f.gates[id]
	if !ok {
		return fmt.Errorf("未知gate %s", id)
	}
	g.Release()
	return nil
}
func (f *HTTPFixture) ReleaseAll() {
	for _, g := range f.gates {
		g.Release()
	}
}
func (f *HTTPFixture) Counts() Counts { return f.tracker.Counts() }
func (f *HTTPFixture) Completed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.completed...)
}

// Close由fixture owner收尾：先放门让handler不会因实验失败永久等待，再有界Shutdown。
// Shutdown失败则Close中断连接并发取消；之后不能直接handlers.Wait，因为已接纳连接可能还没Add。
// 先真正serve.Wait停止所有StateNew登记，再connections.Wait等全部已接纳连接Closed，
// 这时不会再进入handler，才handlers.Wait。每阶段独立预算，主Shutdown错与清理错都保留。
func (f *HTTPFixture) Close() (resultErr error) {
	f.ReleaseAll()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
	defer cancel()
	if err := f.server.Shutdown(ctx); err != nil {
		resultErr = errors.Join(err, f.server.Close())
	}
	serveCtx, stopServe := context.WithTimeout(context.Background(), cleanupBudget)
	defer stopServe()
	if err := Await(serveCtx, f.serveDone, "HTTP Serve停止接纳"); err != nil {
		return errors.Join(resultErr, err)
	}
	f.serve.Wait() // StateNew的唯一新增者真实返回，后面的connections.Wait不会先见0后又Add。
	if !errors.Is(f.serveErr, http.ErrServerClosed) {
		resultErr = errors.Join(resultErr, f.serveErr)
	}
	var finisher sync.WaitGroup
	done := make(chan struct{})
	finisher.Go(func() { f.connections.Wait(); f.handlers.Wait(); close(done) })
	Cleanup(&resultErr, func() {}, done, finisher.Wait)
	return resultErr
}

// HTTPClient由批次owner创建，Fetch把整批ctx传入每次真实HTTP，不在每项重置总预算。
// Transport可复用连接，但不能限制所有业务资源；活跃上限由FanOut/WeightedBatch决定。
type HTTPClient struct {
	base      string
	transport *http.Transport
	client    *http.Client
}

func NewHTTPClient(base string) *HTTPClient {
	tr := &http.Transport{}
	return &HTTPClient{base: base, transport: tr, client: &http.Client{Transport: tr}}
}
func (c *HTTPClient) Close() { c.transport.CloseIdleConnections() }

// Fetch由工作函数同步调用，返回的Price对应传入ID。成功HTTP status不等于语义成功，
// 还必须严格解码、验证响应ID与业务code；Body归本函数拥有，所有分支Close。
func (c *HTTPClient) Fetch(ctx context.Context, id string) (price int64, resultErr error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/?id="+url.QueryEscape(id), nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("HTTP SKU %s: %w", id, err)
	}
	defer func() { resultErr = errors.Join(resultErr, resp.Body.Close()) }()
	// LimitReader人为截断会伪造EOF。先实际读上限+1字节，确认没有超限，才能严格解码完整对象。
	// 最大分配4097字节；若服务无限发数据也不会无限读。超限仍由defer关闭Body、释放本次资源。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil {
		return 0, fmt.Errorf("读取价格响应: %w", err)
	}
	if len(raw) > 4096 {
		return 0, ErrResponseTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var body wirePrice
	if err := decoder.Decode(&body); err != nil {
		return 0, fmt.Errorf("价格JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return 0, fmt.Errorf("价格JSON应只有一个对象: %v", err)
	}
	if body.ID != id {
		return 0, fmt.Errorf("响应ID=%s与请求=%s不匹配", body.ID, id)
	}
	if resp.StatusCode == http.StatusNotFound && body.Code == "UNAVAILABLE" {
		return 0, fmt.Errorf("%s: %w", id, ErrUnavailable)
	}
	if resp.StatusCode != http.StatusOK || body.Code != "" || body.Price < 0 {
		return 0, fmt.Errorf("HTTP status=%d code=%q: %w", resp.StatusCode, body.Code, ErrBackend)
	}
	return body.Price, nil
}
