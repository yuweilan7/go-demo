package course

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"
)

// UnknownReport 保存“请求返回失败”和“后来查到业务结果”两类独立证据。
// OriginalErr 始终保留第一次 POST 的原始失败；确认成功不篡改过去的通信事实。
type UnknownReport struct {
	OriginalErr  error
	BeforeQuery  Decision
	Confirmed    bool
	OrderID      string
	StoredOrders int
	PostAttempts int
	Queries      int
	Events       []string
}

// orderStatus 是 loopback 服务 GET 的实际 JSON 返回结构，不向外暴露底层错误文案。
// 反引号内的 json 标签告诉编码器用 request_id 等字段名，客户端解码也按这些名称匹配。
type orderStatus struct {
	RequestID string `json:"request_id"`
	OrderID   string `json:"order_id"`
	Stored    int    `json:"stored_orders"`
}

// localLedger 由 RunUnknownExperiment 创建，handler goroutine 与入口查询共享它。
// mu 保护 orders、计数、events 和 handlerErr；锁内只做内存操作，不进行网络 I/O。
// 数据只活在本次实际 HTTP server 进程中，不是持久数据库，也没有支付副作用。
type localLedger struct {
	mu         sync.Mutex
	orders     map[string]string
	posts      int
	queries    int
	events     []string
	handlerErr error
	completed  chan struct{}
}

// serveHTTP 被 net/http 在服务端请求 goroutine 中调用。
// RunUnknownExperiment 是 server owner；server.Close 等待正常 handler 退出。
// Hijack 后的 conn 不再归 net/http 管理，本 handler 必须主动 Close，不能交给 server.Close。
func (s *localLedger) serveHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		// handler 退出时发送完成通知；缓冲让它不用等待入口正在执行 HTTP I/O。
		// channel 由实验 owner 创建、接收，没人关闭它；这不是取消信号。
		select {
		case s.completed <- struct{}{}:
		default:
			s.recordHandlerError(errors.New("unexpected extra HTTP request"))
		}
	}()
	key := r.URL.Query().Get("request_id")
	if key == "" {
		http.Error(w, "missing request_id", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/orders" {
		s.mu.Lock()
		s.posts++
		// 本次实验只发送一次 POST。键仍记录在服务端，以便后续 GET 查询同一事实。
		s.orders[key] = "ORDER-05"
		s.events = append(s.events, "server: POST #1 stored ORDER-05 before response")
		s.mu.Unlock()

		// HTTP/1 的 ResponseWriter 支持 Hijacker；NewServer 未启用 HTTP/2。
		// Hijack 获得真实 TCP 连接，然后在写出任何 HTTP 响应前关闭它。
		// caller 的 EOF 与已发生的 map 写入在此形成一个可观测的未知结果窗口。
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			s.recordHandlerError(errors.New("loopback fixture lacks HTTP/1 hijacking"))
			http.Error(w, "fixture unavailable", http.StatusInternalServerError)
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			s.recordHandlerError(fmt.Errorf("hijack connection: %w", err))
			return
		}
		// 这里是同步关闭，不另外启动 goroutine，也不 sleep 猜测客户端时机。
		if err := conn.Close(); err != nil {
			s.recordHandlerError(fmt.Errorf("close hijacked connection: %w", err))
		}
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/orders" {
		s.mu.Lock()
		s.queries++
		orderID, exists := s.orders[key]
		count := len(s.orders)
		s.events = append(s.events, "server: GET request_id found ORDER-05")
		s.mu.Unlock()
		if !exists {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(orderStatus{key, orderID, count}); err != nil {
			// handler 只保存诊断，最终由实验边界打印；避免在 service/repository 每层重复日志。
			s.recordHandlerError(fmt.Errorf("write query response: %w", err))
		}
		return
	}
	http.Error(w, "unsupported operation", http.StatusMethodNotAllowed)
}

// recordHandlerError 被 handler 调用，把异步错误交给 server owner 的最终验收。
// 这里没有日志和重试；Join 保留可能出现的多个诊断。
func (s *localLedger) recordHandlerError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlerErr = errors.Join(s.handlerErr, err)
}

// RunUnknownExperiment 由 unknown 演示和独立测试调用，拥有 server、transport 和请求上下文。
// 一次真实 POST + 一次 GET；没有重放 POST，不实现 M33 的完整请求重试。
// 客户端超时 2 秒、整体子上下文 3 秒；handler 无等待循环，所有锁都在短内存段释放。
func RunUnknownExperiment(parent context.Context) (report UnknownReport, err error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel() // owner 在结束时释放截止时间资源，通知仍在等待 I/O 的请求。
	ledger := &localLedger{orders: make(map[string]string), completed: make(chan struct{}, 2)}
	server := httptest.NewServer(http.HandlerFunc(ledger.serveHTTP))
	defer server.Close() // 正常请求 handler 归 server 所有；Hijack 连接已在 handler 中关掉。

	// Proxy:nil 只让本实验的 loopback 客户端直接连接，不修改全局代理或用户系统设置。
	// 每次新建连接，不复用连接；不设置可触发幂等自动重放的请求头。
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	// url.Values 是 map[string][]string：一个查询参数可以有多个字符串值。
	// 内层 {"REQ-M05-001"} 省略了已知的 []string 类型，Encode 负责正确 URL 编码。
	query := url.Values{"request_id": {"REQ-M05-001"}}
	endpoint := server.URL + "/orders?" + query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return report, fmt.Errorf("build create request: %w", err)
	}
	response, postErr := client.Do(request) // 当前入口在此等待 HTTP 响应或 ctx/timeout；handler 并发执行。
	if postErr == nil {
		// 期望故障是响应彻底丢失；成功收到 HTTP 响应不能当作故障复现通过。
		closeErr := response.Body.Close()
		return report, errors.Join(errors.New("expected lost response, but received one"), closeErr)
	}
	// 出站请求已经交给客户端，服务端是否完成不能从通信失败推导。
	// 保留 *url.Error/EOF 和独立业务语义，使 Classify 选择 reconcile。
	report.OriginalErr = fmt.Errorf("create request_id=REQ-M05-001: %w", errors.Join(ErrOutcomeUnknown, postErr))
	report.BeforeQuery = Classify(report.OriginalErr)
	ledger.mu.Lock()
	ledger.events = append(ledger.events, "client: POST failed; outcome unknown; no POST replay")
	ledger.mu.Unlock()

	status, err := queryOrder(ctx, client, endpoint)
	if err != nil {
		// 核实也失败时，未知结果仍存在；不能由失败查询证明不存在。
		return report, errors.Join(report.OriginalErr, err)
	}
	report.Confirmed = status.RequestID == "REQ-M05-001" && status.OrderID == "ORDER-05" && status.Stored == 1
	report.OrderID = status.OrderID
	report.StoredOrders = status.Stored
	// 收到字节与业务工作段结束是两个时刻。owner 等待两个 handler 的 defer 通知，
	// 此时业务与连接清理段已结束、诊断不会再变化；通知不承诺 net/http goroutine
	// 在这一瞬间已退出。正常请求由 server.Close 等待，Hijack 连接已由 handler 关闭。
	for completed := 0; completed < 2; completed++ {
		select {
		case <-ledger.completed:
		case <-ctx.Done():
			return report, fmt.Errorf("wait for HTTP handlers: %w", ctx.Err())
		}
	}
	ledger.mu.Lock()
	report.PostAttempts = ledger.posts
	report.Queries = ledger.queries
	report.Events = append([]string(nil), ledger.events...) // 复制 slice，owner 离开后不会共享可变底层数组。
	handlerErr := ledger.handlerErr
	ledger.mu.Unlock()
	if handlerErr != nil {
		return report, handlerErr
	}
	if !report.Confirmed || report.PostAttempts != 1 || report.Queries != 1 {
		return report, fmt.Errorf("reconciliation invariant failed: %+v", report)
	}
	return report, nil // 返回前各 defer 清理资源；OriginalErr 仍作为历史证据留在 report。
}

// queryOrder 由 RunUnknownExperiment 调用，只读查询原请求键，避免重复业务写入。
// 成功获得 Response 后，本函数拥有 Body 的关闭责任，并把关闭失败并入具名结果 err。
func queryOrder(ctx context.Context, client *http.Client, endpoint string) (status orderStatus, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return status, fmt.Errorf("build reconcile request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return status, fmt.Errorf("query original request: %w", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close query response: %w", closeErr))
		}
	}()
	if response.StatusCode != http.StatusOK {
		return status, fmt.Errorf("query original request HTTP=%d", response.StatusCode)
	}
	// 本实验响应小于 1024 字节；限制读取量，不能用 HTTP 200 代替内容验收。
	if err := json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&status); err != nil {
		return status, fmt.Errorf("decode reconcile result: %w", err)
	}
	return status, nil
}
