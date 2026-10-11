package lesson

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

var ErrClientClosed = errors.New("订单客户端已关闭")

type orderResponse struct {
	ID     string `json:"id"`
	Amount int64  `json:"amount"`
}
type BackendSnapshot struct {
	Committed                        map[string]int64
	Accepted, Closed, ActiveHandlers int
}

// OrderBackend 是当前实验 owner 的真实 loopback HTTP/1.1 服务，不是 RoundTripper mock。
// committed 是合成订单账本，受 mu 保护；它证明本进程里的业务副作用，不声称进程崩溃后持久化。
// A 的 entered/release 门给调用方一个“handler 已进入、提交尚未发生”的可观察边界。
// connections 在 StateNew 登记、StateClosed 完成；先 join Serve，再 Wait 连接，最后 Wait handler，
// 防止“handler 计数暂时为零”却还有已 accept 的连接随后启动 handler 的竞态。
type OrderBackend struct {
	URL                              string
	server                           *http.Server
	listener                         net.Listener
	mu                               sync.Mutex
	committed                        map[string]int64
	accepted, closed, activeHandlers int
	entered                          *gate
	release                          *gate
	connections, handlers, serve     sync.WaitGroup
	serveDone                        chan struct{}
	serveErr                         error
	closeOnce                        sync.Once
	closeErr                         error
}

// NewOrderBackend 由 CoreDrain/HTTP 测试调用，操作系统分配独立端口，服务只接受合成 A/B 订单。
// Listen 成功后 owner 必须 Close；Serve 的 goroutine 和所有连接也由这个对象归属。
func NewOrderBackend() (*OrderBackend, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b := &OrderBackend{URL: "http://" + listener.Addr().String(), listener: listener,
		committed: make(map[string]int64), entered: newGate(), release: newGate(), serveDone: make(chan struct{})}
	b.server = &http.Server{Handler: http.HandlerFunc(b.handle), ReadHeaderTimeout: time.Second,
		ConnState: func(_ net.Conn, state http.ConnState) {
			b.mu.Lock()
			defer b.mu.Unlock()
			switch state {
			case http.StateNew:
				b.accepted++
				b.connections.Add(1)
			case http.StateClosed:
				b.closed++
				b.connections.Done()
			}
		}}
	b.serve.Go(func() {
		b.serveErr = b.server.Serve(listener)
		close(b.serveDone)
	})
	return b, nil
}

// handle 由 net/http 在真实连接的 goroutine 调用。先校验协议与 ID，再过 A 的业务门，最后
// 在锁内幂等登记金额。写 JSON 失败不能撤回已登记账本；调用端报错也不能反推“订单没提交”。
// r.Context 只管理这次 HTTP 请求，不能拿它给整个服务的 Shutdown/清理预算当父节点。
func (b *OrderBackend) handle(w http.ResponseWriter, r *http.Request) {
	b.handlers.Add(1)
	b.mu.Lock()
	b.activeHandlers++
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.activeHandlers--
		b.mu.Unlock()
		b.handlers.Done()
	}()
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	amounts := map[string]int64{"A": 12500, "B": 1800}
	amount, ok := amounts[id]
	if !ok {
		http.Error(w, "unknown order", http.StatusNotFound)
		return
	}
	if id == "A" {
		b.entered.release()
		select {
		case <-b.release.C:
		case <-r.Context().Done():
			return
		}
	}
	// 这是协作取消检查，不是与远端请求取消原子化的提交协议；检查后仍可能收到取消。
	if r.Context().Err() != nil {
		return
	}
	b.mu.Lock()
	if _, exists := b.committed[id]; !exists {
		b.committed[id] = amount
	}
	b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(orderResponse{id, amount})
}

func (b *OrderBackend) Entered() <-chan struct{} { return b.entered.C }
func (b *OrderBackend) Release()                 { b.release.release() }

// Snapshot 由 owner/测试读取账本和资源同一时刻的值副本；新 map 不借出可变共享账本。
func (b *OrderBackend) Snapshot() BackendSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	copyRows := make(map[string]int64, len(b.committed))
	for id, amount := range b.committed {
		copyRows[id] = amount
	}
	return BackendSnapshot{copyRows, b.accepted, b.closed, b.activeHandlers}
}

// Close 是 fixture owner 的独立收尾，不冒充订单服务的 drain。先放业务门再强制关闭连接，
// 新预算依次等待 Serve、已登记连接、handler，并加入负责发布各通知的 goroutine。
// Once 保证重复 Close 复用结果。失败只报告失败，不用进程退出或 NumGoroutine 猜已经清理。
func (b *OrderBackend) Close() error {
	b.closeOnce.Do(func() {
		b.Release()
		b.closeErr = b.server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), CleanupBudget)
		defer cancel()
		if err := await(ctx, b.serveDone, "HTTP Serve join"); err != nil {
			b.closeErr = errors.Join(b.closeErr, err)
			return
		}
		b.serve.Wait()
		if b.serveErr != nil && !errors.Is(b.serveErr, http.ErrServerClosed) {
			b.closeErr = errors.Join(b.closeErr, b.serveErr)
		}
		joined := make(chan struct{})
		var owner sync.WaitGroup
		owner.Go(func() {
			b.connections.Wait() // Serve 已退出，因此 StateNew 不再 Add；连接终态后 handler 也不再 Add。
			b.handlers.Wait()
			close(joined)
		})
		if err := await(ctx, joined, "HTTP 连接与 handler join"); err != nil {
			b.closeErr = errors.Join(b.closeErr, err)
			return
		}
		owner.Wait()
	})
	return b.closeErr
}

// OrderClient 由 Service owner 创建/关闭，worker 借用 Fetch。active 在请求最开始加一，
// 直到 response.Body 读完并 Close 才减一；Close 不允许在任何 active 调用中进行。
type OrderClient struct {
	baseURL   string
	http      *http.Client
	transport *http.Transport
	mu        sync.Mutex
	closed    bool
	active    int
}

// NewOrderClient 由实验owner调用，baseURL来自NewOrderBackend的动态地址；返回指针拥有
// 一个独立Transport。它随后借给Service的worker使用，最后由Service.Wait之后的Close回收。
func NewOrderClient(baseURL string) *OrderClient {
	// 明确不走系统代理；真实本机协议测试的端点就是动态 loopback。每请求关闭连接，
	// 使本课把重点放在任务生命周期，而不是连接复用；Body 仍必须读取/关闭。
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	return &OrderClient{baseURL: baseURL, transport: transport, http: &http.Client{Transport: transport, Timeout: 3 * time.Second}}
}

// Fetch 实现 M15.Fetch 的真实 HTTP adapter：id 来自已接纳订单，返回分给 Ticket.Row.Price。
// 请求成功不仅检查 200，还限制响应大小、检查 JSON ID/金额并拒绝多余对象。
// 具名 err 使 defer 能合并 Body.Close 错误；先注册 active 归还，后注册 Body.Close，因此
// Body.Close 先执行，active 最后才减一。资源所有权不会在读取响应前提前归还。
func (c *OrderClient) Fetch(ctx context.Context, id string) (amount int64, resultErr error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, ErrClientClosed
	}
	c.active++
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.active--; c.mu.Unlock() }()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/orders?id="+id, nil)
	if err != nil {
		return 0, err
	}
	response, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { resultErr = errors.Join(resultErr, response.Body.Close()) }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return 0, fmt.Errorf("订单 %s HTTP %d", id, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return 0, err
	}
	if len(data) > 4096 {
		return 0, errors.New("订单响应超过4096字节")
	}
	var row orderResponse
	if err := json.Unmarshal(data, &row); err != nil {
		return 0, err
	} // Unmarshal 要求单个完整 JSON。
	if row.ID != id || row.Amount <= 0 {
		return 0, errors.New("订单 ID/金额不匹配")
	}
	return row.Amount, nil
}

// Close 只有在 Service 真正 Wait worker 后调用才应该成功。锁内 closed=true 建立不能再借用
// 的边界，随后 CloseIdleConnections 清理 transport 的闲置资源；它本身不会取消 active 请求。
func (c *OrderClient) Close() error {
	c.mu.Lock()
	if c.active != 0 {
		active := c.active
		c.mu.Unlock()
		return fmt.Errorf("仍有 %d 个在途客户端调用", active)
	}
	c.closed = true
	c.mu.Unlock()
	c.transport.CloseIdleConnections()
	return nil
}

func (c *OrderClient) Closed() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.closed }
