package lab

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sony/gobreaker/v2"
)

var ErrQueueFull = errors.New("依赖隔离队列已满")
var ErrRateLimited = errors.New("入口速率限制")

// Bulkhead拥有业务槽位和排队人数，不拥有调用方ctx，也不启动goroutine。
// 一个已获得槽位的慢请求最多占一个token；queueLimit控制等待人数，连接池单独限制socket。
type Bulkhead struct {
	slots               chan struct{}
	mu                  sync.Mutex
	waiters, queueLimit int
}

// newBulkhead由依赖的composition owner创建；健康依赖与故障依赖应各有自己的实例。
func newBulkhead(active, queued int) *Bulkhead {
	return &Bulkhead{slots: make(chan struct{}, active), queueLimit: queued}
}

// acquire同步等待容量，返回release由成功调用者defer执行。ctx已取消先拒绝，避免无谓派发。
// queued通知当前函数已保留队列位置；它证明排队状态，不证明已执行HTTP或已经完成。
func (b *Bulkhead) acquire(ctx context.Context, queued chan<- struct{}) (release func(), err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case b.slots <- struct{}{}:
		return func() { <-b.slots }, nil
	default:
	}
	b.mu.Lock()
	if b.waiters >= b.queueLimit {
		b.mu.Unlock()
		return nil, ErrQueueFull
	}
	b.waiters++
	b.mu.Unlock()
	defer func() { b.mu.Lock(); b.waiters--; b.mu.Unlock() }()
	if queued != nil {
		close(queued)
	} // 每个caller传专属channel，仅一次调用关闭它。
	select {
	case b.slots <- struct{}{}:
		if err = ctx.Err(); err != nil {
			<-b.slots
			return nil, err
		}
		return func() { <-b.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// call由当前任务同步执行：入队 -> 获取业务槽 -> HTTP -> 归还槽，队列ctx就是总工作预算。
func (b *Bulkhead) call(ctx context.Context, c *Client, spec Spec, queued chan<- struct{}, trace *Trace) taskResult {
	release, err := b.acquire(ctx, queued)
	if err != nil {
		return taskResult{Err: err}
	}
	defer release()
	r, s, err := Retry(ctx, c, spec, Policy{Total: 2 * time.Second, MaxAttempts: 1}, trace)
	return taskResult{Reply: r, Stats: s, Err: err}
}

// snapshot返回整数值副本，只在gate锁定阶段或所有任务join后用于断言容量/最终归零。
func (b *Bulkhead) snapshot() (active, queued int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.slots), b.waiters
}

// SpacingLimiter 是透明的本机速率限制：两次准入至少间隔 period，容量1、超额立即拒绝。
// 它限制开始频率，不关心请求执行多久；不像Bulkhead那样在完成时release。
type SpacingLimiter struct {
	mu     sync.Mutex
	next   time.Time
	period time.Duration
}

// allow由入口caller在派发HTTP前调用；只拿锁做决策，无排队和后台补token任务。
func (l *SpacingLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.next) {
		return false
	}
	l.next = now.Add(l.period)
	return true
}

// rateCall把准入和HTTP明确放在同一调用链。拒绝时handler次数不增加，不能用429当成功响应。
func rateCall(ctx context.Context, l *SpacingLimiter, c *Client, spec Spec, trace *Trace) taskResult {
	if ctx.Err() != nil {
		return taskResult{Err: ctx.Err()}
	}
	if !l.allow(time.Now()) {
		return taskResult{Err: ErrRateLimited}
	}
	r, s, err := Retry(ctx, c, spec, Policy{Total: time.Second, MaxAttempts: 1}, trace)
	return taskResult{Reply: r, Stats: s, Err: err}
}

// breaker由实验owner创建库实例；泛型[T]指定Execute成功时返回Reply值，不是interface强转。
// 选定固定统计窗口，连续两次故障打开；业务拒绝/主动取消从依赖故障统计中排除。
func breaker(trace *Trace, opened chan<- struct{}, timeout, interval time.Duration) *gobreaker.CircuitBreaker[Reply] {
	var once sync.Once
	return gobreaker.NewCircuitBreaker[Reply](gobreaker.Settings{
		Name: "inventory-m33", MaxRequests: 1, Interval: interval, Timeout: timeout,
		ReadyToTrip: func(c gobreaker.Counts) bool { return c.ConsecutiveFailures >= 2 },
		IsExcluded: func(err error) bool {
			if errors.Is(err, context.Canceled) {
				return true
			}
			var h *HTTPError
			return errors.As(err, &h) && h.Status >= 400 && h.Status < 500 && h.Status != 429
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			// 库在自己的Mutex内调用回调，不能在这里再调cb.State/Counts造成重入死锁。
			trace.event("breaker", "%s %s -> %s", name, from, to)
			if to == gobreaker.StateOpen && opened != nil {
				once.Do(func() { close(opened) })
			}
		},
	})
}

// throughBreaker由业务caller调用，库只包住一次真实HTTP attempt，避免把重试总结果当每个attempt统计。
// Execute拒绝时不会调用闭包，error交回业务层。库不主动取消已在执行的请求，不管理任务join。
func throughBreaker(ctx context.Context, cb *gobreaker.CircuitBreaker[Reply], c *Client, spec Spec, trace *Trace) (Reply, error) {
	return cb.Execute(func() (Reply, error) {
		r, _, err := Retry(ctx, c, spec, Policy{Total: 2 * time.Second, MaxAttempts: 1}, trace)
		return r, err
	})
}

// unavailable是业务明确允许的降级：库存无法确认，保留error且不返回库存/支付成功。
// Java的fallback常放代理层，Go这里就是显式函数调用；合法降级范围仍需业务契约决定。
func unavailable(cause error) (Reply, error) { return Reply{Code: "INVENTORY_UNAVAILABLE"}, cause }
