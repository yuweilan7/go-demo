package gormcourse

import (
	"net"
	"sync"
	"time"
)

// boundedConn是AMQP Dial交给库的真实socket装饰器，没有模拟协议或读写数据。
// amqp091-go openComplete会SetDeadline(零值)清握手期限，heartbeater又会延长读期限；
// 因此只在Dial里设置一次原net.Conn deadline不能保证实验有上限。
// owner保存绝对budget，库的任何clear/extend都截到它；只有owner能换成独立清理预算。
type boundedConn struct {
	net.Conn
	mu     sync.Mutex
	budget time.Time
}

// capped在已持有mu时使用；零值表示库请求无限期，这里仍采用owner的有限budget。
func (c *boundedConn) capped(requested time.Time) time.Time {
	if requested.IsZero() || c.budget.Before(requested) {
		return c.budget
	}
	return requested
}

// SetDeadline由AMQP握手调用。互斥只保护deadline配置，不锁住Read/Write，
// 不能让正在等网络的调用阻止owner设置deadline以解除等待。
func (c *boundedConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.SetDeadline(c.capped(deadline))
}

// SetReadDeadline由库heartbeater调用；允许更短的心跳检测，禁止超出owner总上限。
func (c *boundedConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.SetReadDeadline(c.capped(deadline))
}

// SetWriteDeadline同样不能被库清成无限写等待；读写仍走嵌入的真实net.Conn方法。
func (c *boundedConn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.SetWriteDeadline(c.capped(deadline))
}

// resetBudget仅由broker.close的owner调用，建立不继承失败业务预算的清理上限。
// 在同一锁里修改budget/底层deadline，让heartbeater无法在中间把它恢复成旧上限。
func (c *boundedConn) resetBudget(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.budget = deadline
	return c.Conn.SetDeadline(deadline)
}
