package app

import (
	"context"
	"net/http"
	"sync"
)

// handlerOwner 是 root 的实际 handler 所有权账本，不能用 TCP 连接状态替代它。
// Server.Close 可以断开仍在运行的 handler 连接；再次 Shutdown 的连接列表可能已空，
// 但那个 goroutine 还在用业务依赖。本账本等待真正的 ServeHTTP 返回。
type handlerOwner struct {
	next     http.Handler
	mu       sync.Mutex
	stopping bool
	active   int
	idle     chan struct{}
}

// newHandlerOwner 由 Build 包装实际 Gin engine；初始 idle 已关闭，表示没有进入的请求。
// 以后首次进入时重新创建 idle；每轮最后一个退出者关闭它，closed channel 可重复等待。
func newHandlerOwner(next http.Handler) *handlerOwner {
	idle := make(chan struct{})
	close(idle)
	return &handlerOwner{next: next, idle: idle}
}

// ServeHTTP 由真实 net/http 请求 goroutine 调用，进入原始 engine 前先取得依赖使用资格。
// Stop 以后新的请求只得到503，不会再接触已准备关闭的Service/Repository；进入和Stop同锁。
func (h *handlerOwner) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.stopping {
		h.mu.Unlock()
		http.Error(w, "application stopping", http.StatusServiceUnavailable)
		return
	}
	if h.active == 0 {
		h.idle = make(chan struct{})
	}
	h.active++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.active--
		if h.active == 0 {
			close(h.idle)
		} // 原始engine已返回，包括其所有同步defer；现在才是真正退出。
	}()
	h.next.ServeHTTP(w, r)
}

// Stop 由 App.Close 在cancel/Shutdown前调用，阻止新业务handler注册。
func (h *handlerOwner) Stop() { h.mu.Lock(); h.stopping = true; h.mu.Unlock() }

// Wait 由 App.Close 在停止注册后调用；快照的 idle 不会再被新请求替换。
// ctx 超时只是owner尚未等完，不能把active goroutine变成已退出。
func (h *handlerOwner) Wait(ctx context.Context) error {
	h.mu.Lock()
	idle := h.idle
	h.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (h *handlerOwner) Active() int { h.mu.Lock(); defer h.mu.Unlock(); return h.active }
