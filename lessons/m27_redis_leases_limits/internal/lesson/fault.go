package lesson

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// replyGate只包装本课独占连接。先读真实Redis响应字节，再扣住caller读取；
// 不生成RESP假消息、不暂停共享服务，不启动后台代理goroutine。
type replyGate struct {
	mu                 sync.Mutex
	armed              bool
	captured, released chan struct{}
	open               func()
}

// newReplyGate由acquireFailures创建，owner在所有正常/失败出口释放门。
func newReplyGate() *replyGate {
	g := &replyGate{captured: make(chan struct{}), released: make(chan struct{})}
	g.open = onceRelease(g.released)
	return g
}

// gatedConn嵌入真实连接；armed只在PING/AUTH之后设置，扣住的是业务SET回复。
// deadline由真实go-redis的WithReader设置；本包装专用于该“读前设期限”调用链。
type gatedConn struct {
	net.Conn
	gate     *replyGate
	mu       sync.Mutex
	deadline time.Time
}

// SetReadDeadline传递真实socket期限，同时保存给已经读取字节后的响应等待。
func (c *gatedConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadline = deadline
	c.mu.Unlock()
	return c.Conn.SetReadDeadline(deadline)
}

// SetDeadline维持当前包装使用的整体期限入口；不只更新底层而遗漏gate等待。
func (c *gatedConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadline = deadline
	c.mu.Unlock()
	return c.Conn.SetDeadline(deadline)
}

// Read同步运行在申请worker里。captured表示真实回复到达，timeout丢弃该回复，
// client收到net.Error却无法知道SET结果；驱动之后淘汰socket避免RESP响应错位。
func (c *gatedConn) Read(buffer []byte) (int, error) {
	n, err := c.Conn.Read(buffer)
	c.gate.mu.Lock()
	armed := c.gate.armed && n > 0
	if armed {
		c.gate.armed = false
	}
	c.gate.mu.Unlock()
	if !armed {
		return n, err
	}
	c.mu.Lock()
	deadline := c.deadline
	c.mu.Unlock()
	if deadline.IsZero() {
		deadline = time.Now().Add(time.Second)
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop() // 当前Read拥有timer；到期、放门或Close都会走这个defer。
	close(c.gate.captured)
	select {
	case <-c.gate.released:
		return n, err
	case <-timer.C:
		return 0, replyTimeout{}
	}
}

// Close解除本课额外等待并关闭真实socket；trackedConn在内层记录准确关闭次数。
func (c *gatedConn) Close() error { c.gate.open(); return c.Conn.Close() }

// replyTimeout保留标准deadline错误链，错误类型用于区分网络未知与Redis竞争回复。
type replyTimeout struct{}

func (replyTimeout) Error() string   { return "m27 private response gate: i/o timeout" }
func (replyTimeout) Timeout() bool   { return true }
func (replyTimeout) Temporary() bool { return true }
func (replyTimeout) Unwrap() error   { return os.ErrDeadlineExceeded }

// acquireFailures以真实SET非法PX复现服务端拒绝，再以专属socket响应门复现UNKNOWN。
// 任何提前失败先cancel/放门/join，最后Close独占client；主Redis客户端仍可使用。
func (l *lab) acquireFailures(ctx context.Context) (err error) {
	badKey := l.key("set-rejected")
	value, cmdErr := l.a.Do(ctx, "SET", badKey, "synthetic-owner", "NX", "PX", -1).Text()
	state := classifySET(value, cmdErr)
	exists, readErr := l.b.Exists(ctx, badKey).Result()
	if readErr != nil {
		return readErr
	}
	var remote redis.Error
	if err = l.check("command_rejected_not_contention", state.State == rejected && errors.As(cmdErr, &remote) && exists == 0,
		fmt.Sprintf("state=%s Redis_error=%v EXISTS=%d", state.State, cmdErr, exists)); err != nil {
		return err
	}
	gate := newReplyGate()
	opt := l.redisOptions()
	opt.PoolSize, opt.MaxActiveConns, opt.ReadTimeout = 1, 1, 350*time.Millisecond
	base := opt.Dialer
	opt.Dialer = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := base(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &gatedConn{Conn: conn, gate: gate}, nil
	}
	client := redis.NewClient(opt)
	defer func() { err = errors.Join(err, client.Close()) }()
	if err = client.Ping(ctx).Err(); err != nil {
		return err
	}
	g := newGroup(ctx, l.report)
	g.releases, g.forceClose = []func(){gate.open}, client.Close
	defer g.finish(&err) // LIFO先停止/等worker，再Close这个client。
	key := l.key("set-unknown")
	gate.mu.Lock()
	gate.armed = true
	gate.mu.Unlock()
	var got acquisition // worker唯一写；owner只在Finished同步之后读这一槽。
	t := g.start("SET reply unknown", func(workCtx context.Context) outcome {
		got = acquire(workCtx, client, key, 5*time.Second)
		return outcome{Err: got.Err}
	}, nil)
	select {
	case <-gate.captured:
	case <-t.finished:
		result, joinErr := g.join(ctx, t)
		return errors.Join(errors.New("SET回复未到捕获屏障"), result.Err, joinErr)
	case <-ctx.Done():
		return ctx.Err()
	}
	actual, readErr := l.b.Get(ctx, key).Result()
	if readErr != nil {
		return readErr
	}
	if err = l.check("real_SET_effect_before_reply", len(actual) == 24, "independent GET sees random owner while caller response held"); err != nil {
		return err
	}
	result, joinErr := g.join(ctx, t) // 不放门，等350ms的真实包装读deadline失败及全部defer。
	if joinErr != nil {
		return joinErr
	}
	var timeout net.Error
	if err = l.check("response_unknown_not_false", got.State == unknown && errors.As(result.Err, &timeout) && timeout.Timeout() && actual == got.Lease.Owner,
		fmt.Sprintf("state=%s timeout=%v; independent GET matched this attempt; no auto retry", got.State, result.Err)); err != nil {
		return err
	}
	// GET只用于本课核对“这次已执行”的事实，不授权后续业务；租约随后仍可能失效。
	removed, err := release(ctx, l.a, got.Lease)
	if err != nil {
		return err
	}
	return l.check("unknown_attempt_safe_cleanup", removed == 1, "no business dispatched; compare-delete exact attempt=1")
}
