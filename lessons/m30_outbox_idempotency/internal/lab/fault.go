package lab

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
)

// ProduceFault是本课独占producer的Dialer仪器，不监听共享端口、不停止broker。
// Before使真实TCP连接建立后立即关闭；LostACK则正常转发全部请求，只吞一个
// API key=0 Produce的真实响应。Forwarded/Dropped是仪器证据，业务caller仍只有UNKNOWN。
type ProduceFault struct {
	Before                        bool
	Connected, Forwarded, Dropped atomic.Bool
}

// Dial传给kgo.Dialer。它只包装本client创建的连接；metadata advertised仍是原broker。
// 第一次丢ACK后拒绝该fault producer后续新连接，防止故障实验无边界地产生重试消息。
func (f *ProduceFault) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	if f.Dropped.Load() {
		return nil, errors.New("M30独占producer ACK丢失后拒绝重连")
	}
	c, e := (&net.Dialer{}).DialContext(ctx, network, address)
	if e != nil {
		return nil, e
	}
	f.Connected.Store(true)
	if f.Before {
		e = c.Close()
		if e != nil {
			return nil, e
		}
		return nil, errors.New("M30独占TCP连接主动关闭，尚未发Produce")
	}
	return &faultConn{Conn: c, fault: f, requests: make(map[int32]int16)}, nil
}

// faultConn只属于一次真实连接。mu保护Write跟踪的correlation map；readBuffer
// 只有kgo的单一reader使用。嵌入net.Conn保留deadline/Close等真实socket行为。
type faultConn struct {
	net.Conn
	fault                   *ProduceFault
	mu                      sync.Mutex
	writeBuffer, readBuffer []byte
	requests                map[int32]int16
}

// Write先记录frame里API/correlation，再向真实broker发送原字节；reader可能很快
// 收到响应，不能在Write返回之后才登记。暂存用于解析分段write，不改任何消息内容。
func (c *faultConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.writeBuffer = append(c.writeBuffer, p...)
	produceFrame := false
	for len(c.writeBuffer) >= 4 {
		n := int(binary.BigEndian.Uint32(c.writeBuffer[:4]))
		if n < 8 || n > 1<<20 {
			c.mu.Unlock()
			return 0, errors.New("Kafka request长度不在课程范围")
		}
		if len(c.writeBuffer) < n+4 {
			break
		}
		// frame前4字节是长度；后续API key占2、版本占2、correlation占4。
		// [a:b]是借同一底层数组的slice视图，BigEndian按Kafka协议字节序读整数。
		key := int16(binary.BigEndian.Uint16(c.writeBuffer[4:6]))
		id := int32(binary.BigEndian.Uint32(c.writeBuffer[8:12]))
		c.requests[id] = key
		if key == 0 {
			produceFrame = true
		}
		c.writeBuffer = c.writeBuffer[n+4:] // 消费已解析frame；下一片只保留未完成尾部。
	}
	c.mu.Unlock()
	n, err := c.Conn.Write(p)
	if produceFrame && err == nil && n == len(p) {
		c.fault.Forwarded.Store(true)
	}
	return n, err
}

// Read从真实socket先读取完整响应frame；响应的首4字节correlation对应原请求。
// 遇Produce真实响应才关闭本连接并返回EOF，caller没有得到这个ACK，broker仍可能
// 已接受。最终Records按event ID/原value读取才能判定接受事实，仪器不是生产查询接口。
func (c *faultConn) Read(p []byte) (int, error) {
	if len(c.readBuffer) == 0 {
		header := make([]byte, 4)
		if _, e := io.ReadFull(c.Conn, header); e != nil {
			return 0, e
		}
		n := int(binary.BigEndian.Uint32(header))
		if n < 4 || n > 1<<20 {
			return 0, errors.New("Kafka response长度不在课程范围")
		}
		body := make([]byte, n)
		if _, e := io.ReadFull(c.Conn, body); e != nil {
			return 0, e
		}
		id := int32(binary.BigEndian.Uint32(body[:4]))
		c.mu.Lock()
		key, ok := c.requests[id]
		delete(c.requests, id)
		c.mu.Unlock()
		if ok && key == 0 {
			c.fault.Dropped.Store(true)
			return 0, errors.Join(io.EOF, c.Conn.Close())
		}
		c.readBuffer = append(header, body...)
	}
	// net.Conn.Read可只交出调用方缓冲容得下的一部分，copy返回实际字节数。
	// 不把内部slice直接借给caller，余下frame在下一次Read继续交出。
	n := copy(p, c.readBuffer)
	c.readBuffer = c.readBuffer[n:]
	return n, nil
}
