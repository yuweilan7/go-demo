package lab

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"
)

// HTTPError/BusinessError/ErrTooLarge 区分 HTTP 状态、业务协议与读取边界；Do 的网络错误
// 仍由 %w 保留。它们让上层按语义分支，避免用字符串包含 “error” 猜失败原因。
type HTTPError struct {
	Status int
	Code   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP status=%d code=%s", e.Status, e.Code) }

type BusinessError struct{ Code string }

func (e *BusinessError) Error() string { return "业务失败: " + e.Code }

var ErrTooLarge = errors.New("响应超过字节上限")

// Client 拥有一个可并发复用的 http.Client/Transport；OpenBodies 是教学计数器，
// 只记录本 wrapper 收到却尚未 Close 的响应体，不声称等于运行时 goroutine 或 socket 数量。
type Client struct {
	HTTP       *http.Client
	Transport  *http.Transport
	OpenBodies atomic.Int64
	// OwnedRequests只数本课doAsync创建且尚未finished的请求goroutine，不等于Transport
	// 内部goroutine数；计数减到0发生在finished关闭之前，供最终独立断言检查。
	OwnedRequests atomic.Int64
}

// countedTransport 仍把真实网络调用交给 http.Transport，不生成假响应。它只在成功收到
// response 时包装 body，把“调用方拥有一个必须关闭的对象”变成可独立断言的事实。
type countedTransport struct {
	transport *http.Transport
	owner     *Client
}
type countedBody struct {
	io.ReadCloser
	owner *Client
	once  sync.Once
}

func (t *countedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// client 策略层同步调用此方法，但底层 Transport 会启动/复用自己的 I/O goroutine。
	resp, err := t.transport.RoundTrip(r)
	if err == nil {
		t.owner.OpenBodies.Add(1)
		resp.Body = &countedBody{ReadCloser: resp.Body, owner: t.owner}
	}
	return resp, err
}

// Close 调用底层 body.Close 后只减一次 ownership 计数；不让重复 Close 伪造负计数。
// Embed 的 io.ReadCloser 提供 Read 方法，读取仍走真实 HTTP body 的分帧/EOF 逻辑。
func (b *countedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { b.owner.OpenBodies.Add(-1) })
	return err
}

// NewClient 由实验 owner 创建一次并复用。MaxConnsPerHost 包括拨号/活动/空闲连接，
// MaxIdleConnsPerHost 只管空闲池；HTTP/2 一条连接可多路复用，二者都不是业务并发限流器。
// Proxy=nil 保证课堂请求直连自己的 loopback，不读取用户系统代理配置。
func NewClient(maxConns int, headerBudget, totalBudget time.Duration) *Client {
	dialer := &net.Dialer{Timeout: 500 * time.Millisecond, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy: nil, DialContext: dialer.DialContext,
		MaxConnsPerHost: maxConns, MaxIdleConns: 4, MaxIdleConnsPerHost: 2,
		IdleConnTimeout: 500 * time.Millisecond, TLSHandshakeTimeout: 150 * time.Millisecond,
		ResponseHeaderTimeout: headerBudget, DisableCompression: true,
	}
	c := &Client{Transport: tr}
	c.HTTP = &http.Client{
		Transport: &countedTransport{transport: tr, owner: c}, Timeout: totalBudget,
		// 下单不自动跟随重定向，不让 Authorization 或 POST 语义意外跨 origin。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return c
}

// CloseIdle 在整轮 owner 退出时调用，清理可空闲复用的 TCP 连接；它不关闭未消费的活动
// body，因此不能拿它修复泄漏，也不要在每次请求后调用而破坏正常池复用。
func (c *Client) CloseIdle() { c.Transport.CloseIdleConnections() }

// Trace 由每次请求的 owner 创建。Transport 回调可能在内部 goroutine 执行，故用锁保存
// 快照；Reused 来自实际 GotConn，Connection 来自服务端 ConnContext 编号，形成两端证据。
type Trace struct {
	mu           sync.Mutex
	Connected    bool
	Reused       bool
	TLSStarted   bool
	TLSCompleted bool
	FirstByte    bool
}

// AttachTrace 返回带 trace 的新 context 值；不会修改 caller 已持有的 context。
func AttachTrace(ctx context.Context, t *Trace) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		ConnectDone:          func(_, _ string, err error) { t.mu.Lock(); t.Connected = err == nil; t.mu.Unlock() },
		GotConn:              func(info httptrace.GotConnInfo) { t.mu.Lock(); t.Reused = info.Reused; t.mu.Unlock() },
		GotFirstResponseByte: func() { t.mu.Lock(); t.FirstByte = true; t.mu.Unlock() },
		TLSHandshakeStart:    func() { t.mu.Lock(); t.TLSStarted = true; t.mu.Unlock() },
		TLSHandshakeDone:     func(_ tls.ConnectionState, err error) { t.mu.Lock(); t.TLSCompleted = err == nil; t.mu.Unlock() },
	})
}

// TLSSnapshot/Snapshot 由实验 owner 在事件发生后读取值副本；锁避免与 trace 回调竞争。
func (t *Trace) TLSSnapshot() (started, completed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.TLSStarted, t.TLSCompleted
}

func (t *Trace) Snapshot() (connected, reused, firstByte bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Connected, t.Reused, t.FirstByte
}

type Reply struct {
	Envelope   Envelope
	Status     int
	Connection string
	Bytes      int
}

// RequestJSON 是正常业务调用路径：入口 -> NewRequestWithContext -> Client.Do -> 有界读取
// -> HTTP/JSON/业务校验 -> Reply。调用同步等待到读取完成；Transport 自己管理底层 I/O。
// 本函数拥有 child cancel 和成功得到的 Response.Body，所有早返回都会经过 defer 清理。
func RequestJSON(ctx context.Context, client *Client, method, url string, body io.Reader, key string, maxBytes int64) (reply Reply, err error) {
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, url, body)
	if err != nil {
		return reply, fmt.Errorf("构造请求: %w", err)
	}
	if method == http.MethodPost {
		// NewRequest 会为 strings.Reader 自动设置 GetBody。有 Idempotency-Key 且
		// 可回放 body 时，标准 Transport 可能在已复用连接网络错误后自动重发 POST。
		// 本课订单选择“不自动重发”：要求非空 body，并去掉 GetBody。在当前 HTTP/1
		// Transport 的 shouldRetryRequest/isReplayable 路径中，这个 body 不可回放。
		if req.Body == nil || req.Body == http.NoBody {
			return reply, errors.New("本课 POST 要求非空 JSON body")
		}
		req.GetBody = nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", token)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := client.HTTP.Do(req)
	if err != nil {
		return reply, fmt.Errorf("发送/等待响应头: %w", err)
	}
	// 注册时保存本次 resp.Body 接口值，函数退出时调用其 Close。不能放在循环所在
	// 大函数里直到所有请求结束才关闭；本函数一次请求一次资源作用域。
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("关闭响应体: %w", closeErr)
		}
	}()
	reply.Status, reply.Connection = resp.StatusCode, resp.Header.Get("X-Conn-ID")
	// 多读 1 字节区分“恰好 maxBytes”与“真实超限”。LimitReader 自己的 EOF
	// 不代表网络 body EOF；因此超限时明确 cancel，结束交换而非无界 drain。
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	reply.Bytes = len(data)
	if err != nil {
		return reply, fmt.Errorf("读取响应体: %w", err)
	}
	if int64(len(data)) > maxBytes {
		cancel()
		return reply, fmt.Errorf("读到 %d 字节，上限 %d: %w", len(data), maxBytes, ErrTooLarge)
	}
	// 状态是独立错误层。错误响应即使不是 JSON，也先保留 HTTP 状态，不能让解析
	// 错误覆盖 401/413/500；合法 JSON 时补上业务 code，便于日志定位。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = json.Unmarshal(data, &reply.Envelope)
		return reply, &HTTPError{Status: resp.StatusCode, Code: reply.Envelope.Code}
	}
	if err := json.Unmarshal(data, &reply.Envelope); err != nil {
		return reply, fmt.Errorf("解析 JSON: %w", err)
	}
	if reply.Envelope.Code != "OK" {
		return reply, &BusinessError{Code: reply.Envelope.Code}
	}
	if !reply.Envelope.Committed {
		return reply, &BusinessError{Code: "NOT_COMMITTED"}
	}
	return reply, nil
}
