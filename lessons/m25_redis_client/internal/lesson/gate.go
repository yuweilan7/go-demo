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

// replyGate 是本课自己的连接响应代理，扣留下一次真实 socket.Read 返回的字节。
// Redis 已实际执行请求，测试只是延迟本课 caller 看见响应；不生成 Redis 假回复、不暂停服务。
// 该结构本身不启动 goroutine，Read 使用的 timer 在返回时停止。
type replyGate struct {
	mu       sync.Mutex
	armed    bool
	captured chan struct{}
	release  chan struct{}
	open     func()
}

// newReplyGate 被独占故障 client 的 owner 调用，只注入一次响应延迟。
func newReplyGate() *replyGate {
	gate := &replyGate{captured: make(chan struct{}), release: make(chan struct{})}
	gate.open = onceRelease(gate.release)
	return gate
}

// arm 在真实 Ping/认证完成后调用，确保扣留的是业务命令，不是 HELLO AUTH 的秘密字节。
func (g *replyGate) arm() {
	g.mu.Lock()
	g.armed = true
	g.mu.Unlock()
}

// take 由真实 Read 调用，一次性消费 armed。mutex 防止 owner 与连接 reader 竞争。
func (g *replyGate) take() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.armed {
		return false
	}
	g.armed = false
	return true
}

// gatedConn 嵌入真实 net.Conn，覆盖 Read、读/整体deadline及Close以维持延迟代理契约。
// deadline 是 go-redis 在本次读之前实际给出的绝对时间；不能自己把 ctx 当 socket deadline。
type gatedConn struct {
	net.Conn
	gate         *replyGate
	mu           sync.Mutex
	readDeadline time.Time
}

// SetReadDeadline 由 go-redis pool.Conn.WithReader 调用，既传给真实 socket，也记录给延迟代理。
func (c *gatedConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.readDeadline = deadline
	c.mu.Unlock()
	return c.Conn.SetReadDeadline(deadline)
}

// SetDeadline 维持net.Conn接口完整契约：同时设读写期限时，延迟代理也要观察同一读期限。
// 当前driver使用SetReadDeadline；仍不能让包装在其他调用方式下丢掉deadline语义。
func (c *gatedConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.readDeadline = deadline
	c.mu.Unlock()
	return c.Conn.SetDeadline(deadline)
}

// Read 在 driver 的命令 goroutine 中同步执行。先从真实 Redis 取字节，后在 release/deadline 等待。
// timeout 时已取的字节被丢弃，返回 net.Error；driver 必须淘汰该连接而不能复用错位协议流。
func (c *gatedConn) Read(buffer []byte) (int, error) {
	n, err := c.Conn.Read(buffer)
	if n == 0 || !c.gate.take() {
		return n, err
	}
	c.mu.Lock()
	deadline := c.readDeadline
	c.mu.Unlock()
	// 所有课程客户端配置有 ReadTimeout；额外上限保证错误配置也不能无期限扣住任务。
	if deadline.IsZero() {
		deadline = time.Now().Add(2 * time.Second)
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	close(c.gate.captured) // 证明真实 response bytes 已到本机，caller 正停在此 Read 的 gate。
	select {
	case <-c.gate.release:
		return n, err
	case <-timer.C:
		return 0, gateTimeout{}
	}
}

// Close 只作用本课保存的连接，同时解除代理等待，帮助 owner 在失败路径等到 Done。
func (c *gatedConn) Close() error {
	c.gate.open()
	return c.Conn.Close()
}

// gateTimeout 是独占代理的故障类型，契合 net.Error，代表响应读取阶段超时。
type gateTimeout struct{}

func (gateTimeout) Error() string   { return "m25 private response gate: i/o timeout" }
func (gateTimeout) Timeout() bool   { return true }
func (gateTimeout) Temporary() bool { return true }
func (gateTimeout) Unwrap() error   { return os.ErrDeadlineExceeded }

// faultClient 被 pool/socket 实验调用，返回本课独占 client 与 gate。
// client 的 main pool 为1，无预拨号；warmup 后 Arm 才能控制确切业务回复。
func (l *lab) faultClient(ctx context.Context, contextTimeout bool, readTimeout, poolTimeout time.Duration) (*redis.Client, *replyGate, error) {
	gate := newReplyGate()
	opt := options(l.cfg, l.stats)
	opt.PoolSize, opt.MaxActiveConns = 1, 1
	opt.ContextTimeoutEnabled, opt.ReadTimeout, opt.PoolTimeout = contextTimeout, readTimeout, poolTimeout
	baseDial := opt.Dialer
	opt.Dialer = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := baseDial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &gatedConn{Conn: conn, gate: gate}, nil
	}
	client := redis.NewClient(opt)
	if err := client.Ping(ctx).Err(); err != nil {
		closeErr := client.Close()
		return nil, nil, fmt.Errorf("故障 client warmup/close：%w", errors.Join(err, closeErr))
	}
	return client, gate, nil
}

// awaitCaptured 在响应屏障、任务 Done 与预算之间等待。任务提前出错时直接报告，
// 不只等一个再也不会关闭的 captured；caller 的 defer 会释放 gate 并真实 join。
func awaitCaptured(ctx context.Context, gate *replyGate, t *task) error {
	select {
	case <-gate.captured:
		return nil
	case <-t.done:
		return fmt.Errorf("任务在响应屏障前退出：%v", t.err)
	case <-ctx.Done():
		return fmt.Errorf("等待真实响应字节：%w", ctx.Err())
	}
}
