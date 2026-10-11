package lab

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
	"sync/atomic"
	"time"

	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// response 是一次网络调用的值结果。Envelope 内的 Order/View 指针仅属于这次
// JSON 解码，调用者不能把它误认为数据库里的可修改实体。
type response struct {
	status int
	body   b.Envelope
	err    error
}

// call 由实验 caller 同步调用；transport 由上层拥有。本次 request/body/response
// 都在本函数收尾，不留下响应 Body。POST 网络错保持 UNKNOWN，GET错仅是读失败。
func call(ctx context.Context, client *http.Client, base, method, path, key string) response {
	body := ""
	if method == "POST" {
		body = `{"sku":"sku-m37","quantity":1}`
	}
	req, e := http.NewRequestWithContext(ctx, method, base+path, bytes.NewBufferString(body))
	if e != nil {
		return response{err: e}
	}
	req.Header.Set("Authorization", "Bearer m37-alice")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	r, e := client.Do(req)
	if e != nil {
		if method == "POST" {
			e = errors.Join(b.ErrUnknown, e)
		}
		return response{err: e}
	}
	defer r.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(r.Body, 8193))
	if e != nil || len(raw) > 8192 {
		return response{status: r.StatusCode, err: errors.Join(e, errors.New("HTTP body 无效/超上限"))}
	}
	var env b.Envelope
	e = json.Unmarshal(raw, &env)
	return response{status: r.StatusCode, body: env, err: e}
}

// httpClient把Transport的关闭责任明确交给caller，Client只是共享它的请求门面。
// 最大连接3与business SQL预算一致，不能无限生成等待goroutine绕过池上限。
func httpClient() (*http.Client, *http.Transport) {
	t := &http.Transport{MaxConnsPerHost: 3, MaxIdleConnsPerHost: 2, IdleConnTimeout: time.Second}
	return &http.Client{Transport: t}, t
}

// verifyAPI 在恢复的同一个 key 及同一个 orderID 上查完整业务内容；201/200本身
// 不是证据。pending/recorded 是 receipt 的事实，不改变订单 ACCEPTED/v1。
func verifyAPI(ctx context.Context, client *http.Client, a *b.App, key, delivery string) error {
	lookup := call(ctx, client, a.URL, "GET", "/requests/"+key, "")
	id := b.OrderID("alice", key)
	if e := b.Assert(lookup.err == nil && lookup.status == 200 && lookup.body.Order != nil, "原key权威API content: %v", lookup.err); e != nil {
		return e
	}
	o := lookup.body.Order
	if e := b.Assert(o.ID == id && o.Owner == "alice" && o.SKU == b.SKU && o.Quantity == 1 && o.AmountMinor == 299 && o.Currency == "CNY" && o.Status == "ACCEPTED" && o.Version == 1 && o.RequestID != "", "完整订单字段应保持创建事实"); e != nil {
		return e
	}
	view := call(ctx, client, a.URL, "GET", "/orders/"+id, "")
	if e := b.Assert(view.err == nil && view.status == 200 && view.body.View != nil, "cache API完整响应: %v", view.err); e != nil {
		return e
	}
	v := view.body.View
	if e := b.Assert(v.Order == *o && v.DeliveryStatus == delivery, "恢复API应为原order+%s，actual=%+v", delivery, v); e != nil {
		return e
	}
	fmt.Fprintf(a.Resources.Out, "API_RECONCILED key=%s order=%s amount_minor=299 currency=CNY status=ACCEPTED version=1 delivery=%s cache_hit=%t\n", key, id, delivery, view.body.CacheHit)
	return nil
}

// httpDrop 是有意的协议坏路径：向真实 App 转发 POST，完整读取真实201及其订单
// 后截断下游 TCP；client 得到 EOF/UNKNOWN，而服务端订单不能撤销。只有本课
// server/listener/transport 属于它，upstream App 的生存期仍由 session 管理。
type httpDrop struct {
	url                    string
	server                 *http.Server
	listener               net.Listener
	transport              *http.Transport
	serveDone, handlerDone chan struct{}
	once                   sync.Once
	err                    error
	serveErr               error
	used                   atomic.Bool
	out                    io.Writer
}

// newHTTPDrop 注册 handler 的最终 Done 与 Serve Done。handlerDone 在所有 Body、
// upstream资源 defer 完成后关闭；captured201只有真实业务内容通过验证才记录。
func newHTTPDrop(upstream string, out io.Writer) (*httpDrop, error) {
	u, e := url.Parse(upstream)
	if e != nil {
		return nil, e
	}
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	p := &httpDrop{url: "http://" + l.Addr().String(), listener: l, transport: &http.Transport{MaxConnsPerHost: 1}, serveDone: make(chan struct{}), handlerDone: make(chan struct{}), out: out}
	p.server = &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.used.Store(true)
		defer p.once.Do(func() { close(p.handlerDone) })
		defer r.Body.Close()
		forward := r.Clone(r.Context())
		forward.URL.Scheme = u.Scheme
		forward.URL.Host = u.Host
		forward.RequestURI = ""
		resp, e := p.transport.RoundTrip(forward)
		if e != nil {
			p.err = e
			http.Error(w, "upstream", 502)
			return
		}
		defer resp.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(resp.Body, 8193))
		var env b.Envelope
		if e != nil || len(raw) > 8192 || json.Unmarshal(raw, &env) != nil || resp.StatusCode != 201 || env.Outcome != "COMMITTED" || env.Order == nil {
			p.err = errors.New("HTTP代理未捕获可信201，拒绝宣称响应丢失")
			http.Error(w, "stage", 502)
			return
		}
		fmt.Fprintf(out, "HTTP_201_CAPTURED key_order=%s outcome=COMMITTED amount_minor=%d delivered=false\n", env.Order.ID, env.Order.AmountMinor)
		conn, _, e := w.(http.Hijacker).Hijack()
		if e != nil {
			p.err = e
			return
		}
		p.err = conn.Close()
	})}
	go func() {
		defer close(p.serveDone)
		e := p.server.Serve(l)
		if !errors.Is(e, http.ErrServerClosed) {
			p.serveErr = e
		}
	}()
	return p, nil
}

// close 等真实 handler 收尾和 Serve 最终返回，再关 idle连接。一次性代理只接一个
// 调用；若正常路径尚无handler，Shutdown后无需等待不存在的handler。
func (p *httpDrop) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	e := p.server.Shutdown(ctx)
	if e != nil {
		e = errors.Join(e, p.server.Close())
	}
	e = errors.Join(e, b.JoinDone(p.serveDone, "HTTP drop Serve", 3*time.Second))
	if p.used.Load() {
		e = errors.Join(e, b.JoinDone(p.handlerDone, "HTTP drop hijacked handler", 3*time.Second))
	}
	p.transport.CloseIdleConnections()
	if e == nil {
		fmt.Fprintln(p.out, "HTTP_PROXY_DONE serve_joined=true handlers_joined=true")
	}
	if !p.safe() {
		return errors.Join(e, b.ErrCleanup)
	}
	return errors.Join(e, p.err, p.serveErr)
}

func (p *httpDrop) safe() bool {
	select {
	case <-p.serveDone:
	default:
		return false
	}
	if p.used.Load() {
		select {
		case <-p.handlerDone:
		default:
			return false
		}
	}
	return true
}
