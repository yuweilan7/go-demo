package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go-demo/lessons/m19_composition_architecture/internal/api"
	"go-demo/lessons/m19_composition_architecture/internal/app"
)

// Client 是实验 owner 的真实 HTTP 客户端；每个实例拥有独立连接池，不共享全局 DefaultClient。
type Client struct {
	HTTP      *http.Client
	Transport *http.Transport
}

func NewClient() *Client {
	t := &http.Transport{Proxy: nil, MaxIdleConns: 8}
	return &Client{HTTP: &http.Client{Transport: t, Timeout: 2 * time.Second}, Transport: t}
}
func (c *Client) Close() { c.Transport.CloseIdleConnections() }

type Reply struct {
	Status   int
	Body     []byte
	Envelope api.Envelope
}

// Call 由 demo/测试 owner 同步调用；返回前一定关闭 response.Body，包括 Read/Decode 失败。
// 网络错误不是“服务器未提交”的事实；caller 还需检查本课仓储快照。
func (c *Client) Call(ctx context.Context, url, method, token, key, body string) (reply Reply, err error) {
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		return Reply{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	response, err := c.HTTP.Do(req)
	if err != nil {
		return Reply{}, err
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return Reply{}, err
	}
	if len(data) > 4096 {
		return Reply{}, errors.New("response exceeded local limit")
	}
	reply = Reply{Status: response.StatusCode, Body: data}
	if method == http.MethodPost {
		if err := json.Unmarshal(data, &reply.Envelope); err != nil {
			return Reply{}, err
		}
	}
	return reply, nil
}

// Cleanup 由实验函数的 defer 调用，预算独立于请求/任务 ctx，保存原业务错误与清理错误。
// 在 Call 已返回/body 关闭后，先关池，再通知 App/Shutdown/join/仓储关闭。
func Cleanup(a *app.App, c *Client, cause error) error {
	if c != nil {
		c.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	closeErr := a.Close(ctx)
	if closeErr == nil && (a.ActiveHandlers() != 0 || a.Repository().State() != "closed") {
		closeErr = errors.New("cleanup returned but handler/repository ownership is incomplete")
	}
	return errors.Join(cause, closeErr)
}

// RequireReply 检查完整响应协议而非只看 HTTP 201；业务行数另由调用者直接断言。
func RequireReply(r Reply, status int, code string, committed bool) error {
	if r.Status != status || r.Envelope.Code != code || r.Envelope.Committed != committed {
		return fmt.Errorf("unexpected reply: status=%d body=%s", r.Status, r.Body)
	}
	return nil
}

// Wait 只用于接收可观察屏障/Done，超时是观察失败，不替代被观察对象的 cancel 与 join。
func Wait(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
