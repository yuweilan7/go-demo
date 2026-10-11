package lesson

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
)

// HTTPOutcome 保存真实本地 HTTP/1.1 请求的结果，并把响应事实与业务效果分开。
// Status=0 表示 client 连响应头都没拿到；200 加 ReadErr 不等于完整成功响应。
type HTTPOutcome struct {
	Mode           string
	Status         int
	Body           string
	RequestErr     string
	ReadErr        string
	ExternalSent   int64
	Failure        *PanicFailure
	ServerLogged   bool
	PartialAborted bool
}

// lockedLog 让 net/http 的 server goroutine 写日志和 owner 读日志通过同一把锁同步。
// bytes.Buffer 本身不支持并发访问；不能用“客户端都返回了”替代内存同步契约。
type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// Write 被 log.Logger 调用，锁的 owner 是这一段调用，退出时用 defer 释放。
func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// text 由实验 owner 调用，取得一份可安全读的日志字符串。
func (l *lockedLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// recoveryRecord 存储 middleware 的请求失败，mutex 避免与 client owner 读取竞争。
type recoveryRecord struct {
	mu      sync.Mutex
	failure *PanicFailure
	partial bool
}

// save 被请求 goroutine 调用，失败记录的字段此后不再改变。
func (r *recoveryRecord) save(failure *PanicFailure, partial bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failure, r.partial = failure, partial
}

// load 被 owner 调用，返回固定失败对象和“响应已开始”的事实。
func (r *recoveryRecord) load() (*PanicFailure, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failure, r.partial
}

// responseTracker 包装这一次请求的 writer，committed 表示 HTTP 响应头已提交。
// 嵌入的 ResponseWriter 提供 Header；我们显式实现 WriteHeader/Write/Flush 记录写入边界。
// 这是课程最小包装，生产框架还需保持 Hijacker/Pusher 等其所需可选接口。
type responseTracker struct {
	http.ResponseWriter
	committed bool
}

// WriteHeader 首次调用向网络交付状态码；以后不能把已经发出的 200 改成 500。
func (w *responseTracker) WriteHeader(status int) {
	w.committed = true
	w.ResponseWriter.WriteHeader(status)
}

// Write 在没有显式头时隐含 200，跟原始 ResponseWriter 保持相同行为。
func (w *responseTracker) Write(p []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

// Flush 将已缓冲的头与正文发出，确保 partial 实验是真正的网络前缀而非本地缓冲。
// 本实验使用 net/http HTTP/1.1 server，它的 writer 支持 Flusher。
func (w *responseTracker) Flush() {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	w.ResponseWriter.(http.Flusher).Flush()
}

// recoveryMiddleware 由 RunHTTP 给 handler 包一层，同步调用 next，未创建新 goroutine。
// deferred closure 与 next 的 panic 在同一请求 goroutine，故能 recover。
func recoveryMiddleware(next http.Handler, record *recoveryRecord) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tracked := &responseTracker{ResponseWriter: w}
		defer func() {
			if value := recover(); value != nil {
				record.save(captureFailure("order-7001", "HTTP.handler", value), tracked.committed)
				if tracked.committed {
					// 无法撤回已发送字节，也不能追加一段 500 装作完整响应；重新 panic 让 server 中断连接。
					// record 已保留失败，net/http 自身的更外层恢复会接住这次重新 panic。
					panic(value)
				}
				http.Error(tracked, "订单处理失败；需核对已发送的发货请求", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(tracked, req)
	})
}

// RunHTTP 由 demo/test 调用，创建独立 127.0.0.1 临时端口、真实 server/client 和请求。
// mode=builtin/middleware/partial，ctx 给网络等待截止时间，所有响应与 listener 在本函数收尾。
func RunHTTP(ctx context.Context, mode string) (HTTPOutcome, error) {
	if mode != "builtin" && mode != "middleware" && mode != "partial" {
		return HTTPOutcome{}, fmt.Errorf("未知 HTTP mode: %q", mode)
	}
	var sent atomic.Int64 // 实际 handler 与 owner 在不同 goroutine；原子计数明确提供并发同步。
	record := &recoveryRecord{}
	serverLog := &lockedLog{}
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent.Add(1) // 模拟已经被外部接受的请求；恢复不会让这个计数退回 0。
		if mode == "partial" {
			_, _ = io.WriteString(w, "accepted-prefix\n")
			w.(http.Flusher).Flush()
		}
		panic("handler 在发货后失败")
	})
	if mode != "builtin" {
		handler = recoveryMiddleware(handler, record)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(serverLog, "", 0)
	server.Start() // httptest 仍是实际监听 TCP 的 net/http server，非 ResponseRecorder 替身。
	defer server.Close()
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/order-7001", nil)
	if err != nil {
		return HTTPOutcome{}, err
	}
	outcome := HTTPOutcome{Mode: mode}
	response, requestErr := client.Do(request)
	if requestErr != nil {
		outcome.RequestErr = requestErr.Error()
	} else {
		outcome.Status = response.StatusCode
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		outcome.Body = string(body)
		if readErr != nil {
			outcome.ReadErr = readErr.Error()
		}
		if closeErr != nil && readErr == nil {
			return HTTPOutcome{}, fmt.Errorf("关闭 HTTP response body: %w", closeErr)
		}
	}
	// Close 等本实验请求退出，再取快照；额外 defer Close 幂等地保障提前返回时也释放 listener。
	server.Close()
	outcome.ExternalSent = sent.Load()
	outcome.Failure, outcome.PartialAborted = record.load()
	outcome.ServerLogged = strings.Contains(serverLog.text(), "http: panic serving")
	return outcome, nil
}
