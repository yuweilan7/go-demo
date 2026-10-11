package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var ErrUnsafeRetry = errors.New("没有安全重试协议，保留 UNKNOWN")
var ErrBodyNotReplayable = errors.New("请求 body 不能重放")
var ErrResponseTooLarge = errors.New("响应超4096字节")
var ErrInvalidOrderResponse = errors.New("订单响应协议不可信")
var ErrInvalidReceipt = errors.New("订单回执缺失或与请求不匹配")

// HTTPError保存收到的HTTP状态/code，并不自行保证业务确定失败；不可信POST状态会再包Unknown。
// transport error仍保留原始error链；caller通过errors.As分别检查协议与业务确定性，不能只看状态码。
type HTTPError struct {
	Status     int
	Code       string
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP=%d code=%s Retry-After=%s", e.Status, e.Code, e.RetryAfter)
}

// Unknown 表示写请求结果未知；它既包网络原因，也包超限/坏JSON/无效回执等不可信响应原因。
// 2xx不能证明订单成功，协议损坏也不能证明没提交。caller按原key查询，不把零值Receipt当失败。
type Unknown struct{ Cause error }

func (e *Unknown) Error() string { return "UNKNOWN: " + e.Cause.Error() }
func (e *Unknown) Unwrap() error { return e.Cause }

// Spec 是一次业务调用的值描述。复制Spec会复制slice头，Body底层字节仍共享，只读即可。
// Replayable表示能重新得到相同字节；Idempotent表示业务允许重复，是审查重试的两个不同条件。
// 满足它们也不会自动重放Unknown；本课先交caller按原key对账，再由caller选择后续协议动作。
// 本课所有POST都按订单写协议处理：安全重试需要非空key，与body是否为空无关；没有查询POST。
type Spec struct {
	Method, URL, Key       string
	Body                   []byte
	Replayable, Idempotent bool
}

// Policy 由调用方提供，总预算只在Retry入口创建一次。MaxAttempts包括第一次，不是重试次数。
// OnBackoff是实验通知钩子，不能阻塞业务；生产通常在这里记录指标而非改变控制流。
type Policy struct {
	Total, Attempt, Base, Cap time.Duration
	MaxAttempts               int
	OnBackoff                 func(time.Duration)
}

// Attempt保存本次真实请求的开始和有效deadline。Stats只由当前Retry goroutine写，返回后交owner。
type Attempt struct {
	Number, Status  int
	Start, Deadline time.Time
	Err             error
}
type Stats struct {
	Deadline time.Time
	Attempts []Attempt
	Waits    []time.Duration
}
type Reply struct {
	Code    string  `json:"code"`
	Receipt Receipt `json:"receipt"`
	Status  int     `json:"-"`
}

// Client拥有可复用Transport；wrapper只计算真实Response.Body所有权，不伪造任何响应。
type Client struct {
	HTTP      *http.Client
	transport *http.Transport
	bodies    atomic.Int64
}
type countedTransport struct{ owner *Client }
type countedBody struct {
	io.ReadCloser
	owner *Client
	once  sync.Once
}

// RoundTrip接收真实网络响应后登记body owner；Do即便收到503仍err=nil，状态分类在oneAttempt做。
func (t *countedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r, err := t.owner.transport.RoundTrip(req)
	if err == nil {
		t.owner.bodies.Add(1)
		r.Body = &countedBody{ReadCloser: r.Body, owner: t.owner}
	}
	return r, err
}

// Close转发真实关闭，只减一次计数；EOF与Close不是同一事件，所有响应分支都必须Close。
func (b *countedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { b.owner.bodies.Add(-1) })
	return err
}

// newClient由实验owner创建一次。禁keepalive使每次为新连接，排除Transport隐式重发影响调用计数。
// 这是为了归因教学，不是生产建议；真实生产通常复用连接，并核对底层自动重发契约。
func newClient(maxConns int) *Client {
	dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
	c := &Client{transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext, DisableKeepAlives: true, DisableCompression: true, MaxConnsPerHost: maxConns, ResponseHeaderTimeout: 2 * time.Second}}
	c.HTTP = &http.Client{Transport: &countedTransport{owner: c}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return c
}

// Close由整组owner收尾调用，只关空闲连接；活动body必须由oneAttempt关闭，并由任务owner join。
func (c *Client) Close() { c.transport.CloseIdleConnections() }

// unsafeBodyReplay只由显式教学反例调用：两份Request共享同一个可消耗bytes.Buffer。
// 这不是正常Retry策略。第一次HTTP读空buffer，第二次虽创建新Request却只能读到EOF。
// 每次Do仍是真实网络，每个response同步有界读完并关闭，错误返回后不遗留body。
func unsafeBodyReplay(ctx context.Context, c *Client, url string, payload string) error {
	shared := bytes.NewBufferString(payload)
	for range 2 {
		if err := unsafeBodyAttempt(ctx, c, url, shared); err != nil {
			return err
		}
	}
	return nil
}

// unsafeBodyAttempt隔离一次body资源作用域；io.NopCloser隐藏GetBody自动构建入口，但仍别名shared。
// caller传入的是指针，复制接口不会复制Buffer的字节/游标；这里故意演示第二次的空body。
func unsafeBodyAttempt(ctx context.Context, c *Client, url string, shared *bytes.Buffer) (err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, io.NopCloser(shared))
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 4097))
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("body反例状态=%d", resp.StatusCode)
	}
	return nil
}

// retryAfter实现RFC9110的delta-seconds或HTTP-date。非法/过去时间视作0，真正等待仍受总ctx限制。
// 不把服务端建议偷偷截短后提前发请求；如果建议超出总预算，等待取消并返回deadline。
func retryAfter(raw string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseUint(raw, 10, 31); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if target, err := http.ParseTime(raw); err == nil && target.After(now) {
		return target.Sub(now)
	}
	return 0
}

// responseFailure由oneAttempt在已经派发请求后调用，把不可信响应的原因交给业务caller。
// POST统一为本课订单写：server可能已commit，故返回Unknown；GET仍返回普通协议error。
func responseFailure(spec Spec, cause error) error {
	if spec.Method == http.MethodPost {
		return &Unknown{Cause: cause}
	}
	return cause
}

// definitiveOrderRejection只识别本课handler明确约定“在commit前拒绝”的状态与code组合。
// 合法JSON只是必要条件，不能把任意code/任意非2xx当确定失败；不在此协议里的写响应保留UNKNOWN。
func definitiveOrderRejection(status int, code string) bool {
	switch status {
	case http.StatusBadRequest:
		return code == "BAD_BODY"
	case http.StatusMethodNotAllowed:
		return code == "METHOD"
	case http.StatusConflict:
		return code == "KEY_PAYLOAD_CONFLICT"
	case http.StatusUnprocessableEntity:
		return code == "BAD_ORDER" || code == "OUT_OF_STOCK"
	default:
		return false
	}
}

// oneAttempt由Retry同步调用：构造新request -> Do等头 -> ReadAll等body -> 状态/JSON -> Close。
// 它拥有派生ctx的cancel和成功收到的body；具名err让defer将关闭错误与主错误一起返回。
func oneAttempt(ctx context.Context, c *Client, spec Spec, budget time.Duration) (reply Reply, a Attempt, err error) {
	child, cancel := context.WithCancel(ctx)
	if budget > 0 {
		cancel()
		child, cancel = context.WithTimeout(ctx, budget)
	}
	defer cancel()
	a.Start = time.Now()
	a.Deadline, _ = child.Deadline()
	var body io.Reader
	if len(spec.Body) > 0 {
		if spec.Replayable {
			body = bytes.NewReader(spec.Body)
		} else {
			body = io.NopCloser(bytes.NewReader(spec.Body))
		}
	}
	req, err := http.NewRequestWithContext(child, spec.Method, spec.URL, body)
	if err != nil {
		return reply, a, err
	}
	// 自己显式重建request，而非依赖Transport的GetBody。订单特别要求不发生隐藏自动重放。
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	if spec.Key != "" {
		req.Header.Set("Idempotency-Key", spec.Key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if spec.Method == "POST" {
			err = &Unknown{Cause: err}
		}
		return reply, a, err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	a.Status, reply.Status = resp.StatusCode, resp.StatusCode
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil {
		if spec.Method == "POST" {
			err = &Unknown{Cause: err}
		}
		return reply, a, err
	}
	if len(data) > 4096 {
		cancel()
		// 大小上限保护当前client，但已派发写的业务效果不能由“没读完整”推断不存在。
		return reply, a, responseFailure(spec, ErrResponseTooLarge)
	}
	decodeErr := json.Unmarshal(data, &reply)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		statusErr := &HTTPError{Status: resp.StatusCode, Code: reply.Code, RetryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now())}
		if spec.Method == http.MethodPost && (decodeErr != nil || !definitiveOrderRejection(resp.StatusCode, reply.Code)) {
			return reply, a, responseFailure(spec, errors.Join(statusErr, decodeErr))
		}
		return reply, a, statusErr
	}
	if decodeErr != nil {
		return reply, a, responseFailure(spec, errors.Join(ErrInvalidOrderResponse, fmt.Errorf("响应JSON: %w", decodeErr)))
	}
	if reply.Code != "OK" {
		// 本课的确定订单拒绝采用上面的4xx/code契约；任意2xx中的未知code不能代替可靠拒绝。
		return reply, a, responseFailure(spec, fmt.Errorf("响应code=%q不符合成功协议: %w", reply.Code, ErrInvalidOrderResponse))
	}
	if spec.Method == http.MethodPost {
		var requested Order
		// 这是校验收到的回执，与调用前本地参数解析不同：请求已派发，失败时必须保留UNKNOWN。
		// ID>0且SKU/qty与输入一致才可确认。Duplicate只是回执标记，不允许替换另一个订单参数。
		if json.Unmarshal(spec.Body, &requested) != nil || requested.SKU == "" || requested.Qty <= 0 || reply.Receipt.ID < 1 || reply.Receipt.Order != requested {
			return reply, a, responseFailure(spec, ErrInvalidReceipt)
		}
	}
	return reply, a, nil
}

// transient只分类确定可以继续审查重试条件的读取故障。Canceled不重试；单attempt deadline可以重试。
// Retry先单独处理写的Unknown并交caller查账；不会把协议损坏当成暂时故障。GET坏JSON也不重试。
func transient(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var status *HTTPError
	if errors.As(err, &status) {
		return status.Status == 429 || status.Status == 502 || status.Status == 503 || status.Status == 504
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// backoff实现有上限的指数equal jitter：[ceiling/2,ceiling]。同一Retry持有自己的RNG，避免共享竞争。
// 使用固定seed只使教材可复现；生产各请求要独立随机seed，不能所有实例同一序列再次同步。
func backoff(base, cap time.Duration, failed int, rng *rand.Rand) time.Duration {
	ceiling := base
	for i := 1; i < failed && ceiling < cap; i++ {
		if ceiling > cap/2 {
			ceiling = cap
		} else {
			ceiling *= 2
		}
	}
	if ceiling > cap {
		ceiling = cap
	}
	if ceiling <= 0 {
		return 0
	}
	half := ceiling / 2
	return half + time.Duration(rng.Int64N(int64(ceiling-half)+1))
}

// Retry由业务caller/SDK wrapper/server handler同步调用，不偷偷启动后台goroutine。
// 每次尝试继承同一总ctx；任何排队/网络/读取/退避都花同一预算，没有每轮Background重置。
func Retry(parent context.Context, c *Client, spec Spec, p Policy, trace *Trace) (reply Reply, stats Stats, err error) {
	if p.Total <= 0 || p.MaxAttempts < 1 || p.MaxAttempts > 16 || p.Base < 0 || p.Cap < p.Base {
		return reply, stats, errors.New("非法Policy")
	}
	ctx, cancel := context.WithTimeout(parent, p.Total)
	defer cancel()
	stats.Deadline, _ = ctx.Deadline()
	rng := rand.New(rand.NewPCG(33, 17))
	for n := 1; n <= p.MaxAttempts; n++ {
		if ctx.Err() != nil {
			return reply, stats, ctx.Err()
		}
		if n > 1 && len(spec.Body) > 0 && !spec.Replayable {
			return reply, stats, errors.Join(err, ErrBodyNotReplayable)
		}
		var a Attempt
		reply, a, err = oneAttempt(ctx, c, spec, p.Attempt)
		a.Number, a.Err = n, err
		stats.Attempts = append(stats.Attempts, a)
		trace.event("retry", "attempt=%d HTTP=%d remaining=%s err=%v", n, a.Status, time.Until(stats.Deadline).Round(time.Millisecond), err)
		if err == nil {
			return reply, stats, nil
		}
		if ctx.Err() != nil {
			return reply, stats, errors.Join(err, ctx.Err())
		}
		var unknown *Unknown
		if errors.As(err, &unknown) {
			// 未知写先返回给caller查原操作；稳定key/可重放字节也不等于可以盲发下一POST。
			// 如果caller配置了额外attempt却缺安全协议，再附加ErrUnsafeRetry说明为何禁止重试。
			// 空body可能由server默认参数产生效果，key guard绝不能取决于len(Body)。
			if n < p.MaxAttempts && (!spec.Idempotent || (spec.Method == http.MethodPost && spec.Key == "")) {
				return reply, stats, errors.Join(err, ErrUnsafeRetry)
			}
			return reply, stats, err
		}
		if !transient(err) || n == p.MaxAttempts {
			return reply, stats, err
		}
		if !spec.Idempotent {
			return reply, stats, errors.Join(err, ErrUnsafeRetry)
		}
		if spec.Method == "POST" && spec.Key == "" {
			// 空body不意味着无副作用；server可能使用默认参数。本课POST写统一要求稳定key。
			return reply, stats, errors.Join(err, ErrUnsafeRetry)
		}
		delay := backoff(p.Base, p.Cap, n, rng)
		var status *HTTPError
		if errors.As(err, &status) && status.RetryAfter > delay {
			delay = status.RetryAfter
		}
		stats.Waits = append(stats.Waits, delay)
		trace.event("backoff", "wait=%s（Retry-After与jitter取较大值）", delay)
		if p.OnBackoff != nil {
			p.OnBackoff(delay)
		}
		if waitErr := pause(ctx, delay); waitErr != nil {
			return reply, stats, errors.Join(err, waitErr)
		}
	}
	return reply, stats, err
}
