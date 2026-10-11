package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Client 拥有一份真实 Transport/Client；Bodies 和 Calls 仅计本课 wrapper 的资源所有权，
// 不把它们称为整个Go runtime的goroutine/socket数。每个实验有独立Client和Rig。
type Client struct {
	HTTP          *http.Client
	transport     *http.Transport
	Bodies, Calls atomic.Int64
}

type Reply struct {
	Status int
	Header http.Header
	Body   []byte
}

// NewClient 由实验owner调用，Proxy=nil直连本课loopback；Do不会把401/409自动变成error。
// 整体3秒预算包含body读取；调用者ctx仍能更早取消。POST取消后不会自动业务重试。
func NewClient() *Client {
	tr := &http.Transport{Proxy: nil, MaxConnsPerHost: 8, MaxIdleConnsPerHost: 4, ResponseHeaderTimeout: 2500 * time.Millisecond}
	return &Client{transport: tr, HTTP: &http.Client{Transport: tr, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *Client) Close() { c.transport.CloseIdleConnections() }

// Request 是所有普通实验的同步HTTP调用：构造request -> 实际TCP/HTTP -> 收响应头 ->
// 有界完整读body -> Close。返回Reply供caller独立解析协议，err只表达网络/资源错误。
// token/key/id来自实验参数，额外headers仅用于可信代理/伪造身份反例，不会输出秘密。
func (c *Client) Request(ctx context.Context, method, url, body, token, key, id string, headers map[string]string) (reply Reply, err error) {
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, url, strings.NewReader(body))
	if err != nil {
		return reply, err
	}
	if method == http.MethodPost {
		req.GetBody = nil
	} // 禁用可回放POST；保留M17“不自动再下单”边界。
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", token)
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("X-Run", id)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return reply, fmt.Errorf("真实HTTP请求: %w", err)
	}
	c.Bodies.Add(1)
	defer func() { err = errors.Join(err, resp.Body.Close()); c.Bodies.Add(-1) }()
	reply.Status, reply.Header = resp.StatusCode, resp.Header.Clone()
	reply.Body, err = io.ReadAll(io.LimitReader(resp.Body, 4097))
	if len(reply.Body) > 4096 {
		cancel()
		return reply, errors.Join(err, errors.New("响应超4096字节"))
	}
	return reply, err
}

// OwnedCall 是启动异步真实HTTP的owner句柄。Reply写入发生在Job.result发送之前，
// caller成功消费Result以后才能读Reply；StopAndJoin等的是Finished而非结果槽位。
type OwnedCall struct {
	Job   *Job
	Reply Reply
}

// StartRequest 用于要同时控制依赖gate的实验。创建后caller立即defer Job.StopAndJoin，
// 覆盖等Entered失败等早退；afterResult只给“结果已到但清理未完”的实验注入清理屏障。
func (c *Client) StartRequest(ctx context.Context, url, id string, afterResult func() error) *OwnedCall {
	call := &OwnedCall{}
	call.Job = startJob(ctx, 3*time.Second, &c.Calls, func(requestCtx context.Context) error {
		var err error
		call.Reply, err = c.Request(requestCtx, http.MethodPost, url, OrderJSON, AliceToken, id, id, nil)
		return err
	}, afterResult)
	return call
}
