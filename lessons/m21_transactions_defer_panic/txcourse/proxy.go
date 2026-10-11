package txcourse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// faultMode 决定只破坏本课这一条连接的哪一个 COMMIT 边界。
type faultMode int

const (
	dropBeforeCommit faultMode = iota
	dropCommitResponse
)

// ProtocolEvidence 是代理完成后交给 owner 的值快照；没有 SQL 值或认证内容。
// CommitSeen 是看到 COM_QUERY COMMIT，Forwarded 是交给真实 MySQL，ServerOK 是
// 收到服务器成功包，Dropped 是主动屏蔽响应。它们仍需业务 SELECT 交叉验证。
type ProtocolEvidence struct {
	CommitSeen, Forwarded, ServerOK, Dropped bool
}

// faultProxy 的唯一 owner 是 unknownCase。listener/两条 socket/accept goroutine/
// 两个 packet pump 都属于它；finished 只在 serve 收到两个 pump 的退出结果之后关闭。
// mutex 保护连接登记与证据，atomic 只协调方向间“当前命令是 COMMIT”的状态。
type faultProxy struct {
	listener   net.Listener
	target     string
	mode       faultMode
	cancel     context.CancelFunc
	finished   chan struct{}
	mu         sync.Mutex
	client     net.Conn
	server     net.Conn
	stopped    bool
	evidence   ProtocolEvidence
	serveErr   error
	cleanupErr error
	commit     atomic.Bool
}

// startProxy 创建动态 loopback listener 后启动唯一 accept wrapper。
// 它不是共享 MySQL 的替代服务：认证、SQL、事务都由原真实 MySQL 完成。
func startProxy(target string, mode faultMode) (*faultProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if err := ln.(*net.TCPListener).SetDeadline(time.Now().Add(18 * time.Second)); err != nil {
		ln.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	p := &faultProxy{listener: ln, target: target, mode: mode, cancel: cancel, finished: make(chan struct{})}
	go p.serve(ctx)
	return p, nil
}

// addr 供 caller 构造 proxyDB DSN，不包含凭据。
func (p *faultProxy) addr() string { return p.listener.Addr().String() }

// serve 负责 Accept/Dial 和两个方向 pump 的 join。任何一个方向结束先关闭双方，
// 使另一个方向的 ReadFull 解阻，再收第二个结果；result不是wrapper最后defer完成。
func (p *faultProxy) serve(ctx context.Context) {
	defer close(p.finished)
	defer p.cancel()
	defer p.closeOne(p.listener)
	client, err := p.listener.Accept()
	if err != nil {
		p.serveErr = err
		return
	}
	defer p.closeOne(client)
	if !p.register(client, true) {
		return
	}
	server, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", p.target)
	if err != nil {
		p.serveErr = err
		return
	}
	defer p.closeOne(server)
	if !p.register(server, false) {
		return
	}
	// 每次实验包很小、串行请求；socket期限是有界兜底，不是触发故障的机制。
	deadline := time.Now().Add(15 * time.Second)
	if err := errors.Join(client.SetDeadline(deadline), server.SetDeadline(deadline)); err != nil {
		p.serveErr = err
		return
	}
	results := make(chan error, 2)
	clientDone, serverDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(clientDone)
		results <- p.pumpClient(client, server)
	}()
	go func() {
		defer close(serverDone)
		results <- p.pumpServer(server, client)
	}()
	first := <-results
	p.closeSockets()
	second := <-results // 关闭 socket 解阻后必须真正等两个方向结束。
	<-clientDone
	<-serverDone // 两个结果均收到仍要等 wrapper 最后 defer 完成，再关闭总 finished。
	// 预期故障对应的 EOF/closed 不作为代理自身失败；协议证据不齐仍由 owner 拒绝。
	if !benignSocketEnd(first) || !benignSocketEnd(second) {
		p.serveErr = errors.Join(first, second)
	}
}

// register 在 owner Close 与 Accept/Dial 竞争时也不漏 socket：若已停止，立即关闭
// 新对象；否则登记给 owner。指针别名只在 mutex 保护下写入/读取。
func (p *faultProxy) register(conn net.Conn, client bool) bool {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		p.closeOne(conn)
		return false
	}
	if client {
		p.client = conn
	} else {
		p.server = conn
	}
	p.mu.Unlock()
	return true
}

// closeSockets 可由 owner/serve 调用，net.Conn.Close 可重复；它是停止通知。
// 真正完成由 finished 表示，不能用 Close 返回代替双向任务 join。
func (p *faultProxy) closeSockets() {
	p.mu.Lock()
	p.stopped = true
	client, server := p.client, p.server
	p.mu.Unlock()
	if client != nil {
		p.closeOne(client)
	}
	if server != nil {
		p.closeOne(server)
	}
}

// closeOne 保留 listener/socket 真正的关闭错误，重复关闭的 net.ErrClosed 是
// owner/serve 共同保护产生的已结束结果。调用时不持有 p.mu，避免再登记错误死锁。
func (p *faultProxy) closeOne(resource io.Closer) {
	err := resource.Close()
	if errors.Is(err, net.ErrClosed) {
		return
	}
	p.mu.Lock()
	p.cleanupErr = errors.Join(p.cleanupErr, err)
	p.mu.Unlock()
}

// pumpClient 原样转发认证/prepared/普通命令，只识别单包 COM_QUERY COMMIT。
// payload[0]=0x03 是命令字，后面是文本；序号/长度也原样保留，绝不打印认证包。
func (p *faultProxy) pumpClient(client, server net.Conn) error {
	for {
		packet, err := readPacket(client)
		if err != nil {
			return err
		}
		payload := packet[4:]
		isCommit := len(payload) > 1 && payload[0] == 0x03 && strings.EqualFold(strings.TrimSpace(string(payload[1:])), "COMMIT")
		if isCommit {
			p.mu.Lock()
			p.evidence.CommitSeen = true
			p.mu.Unlock()
			if p.mode == dropBeforeCommit {
				return io.EOF // COMMIT 尚未发给服务端：断连接后服务端会回滚未提交业务。
			}
			p.commit.Store(true) // 先登记命令再写包，使返回方向能辨认对应响应。
		}
		if err := writePacket(server, packet); err != nil {
			return err
		}
		if isCommit {
			p.mu.Lock()
			p.evidence.Forwarded = true
			p.mu.Unlock()
		}
	}
}

// pumpServer 收到 COMMIT 的真实 OK 包后主动不转发，并返回使双方断开。
// 该包来自 MySQL，代理没有 fake Commit；客户端因此只看到通信失败。
func (p *faultProxy) pumpServer(server, client net.Conn) error {
	for {
		packet, err := readPacket(server)
		if err != nil {
			return err
		}
		if p.commit.Load() {
			payload := packet[4:]
			if len(payload) < 1 || payload[0] != 0x00 {
				return errors.New("COMMIT 没返回预期真实 OK 包，不能宣称已提交")
			}
			p.mu.Lock()
			p.evidence.ServerOK, p.evidence.Dropped = true, true
			p.mu.Unlock()
			return io.EOF
		}
		if err := writePacket(client, packet); err != nil {
			return err
		}
	}
}

// readPacket 由两个 pump 同步调用，MySQL header 为3字节小端长度+1字节序号。
// io.ReadFull 正确处理 TCP 拆包；4MiB 上限只适用于本课，拒绝协议/大包误用。
func readPacket(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	if n == 0 || n > 4<<20 {
		return nil, fmt.Errorf("本课代理拒绝 packet 长度%d", n)
	}
	packet := make([]byte, 4+n)
	copy(packet, header[:])
	_, err := io.ReadFull(r, packet[4:])
	return packet, err
}

// writePacket 对短 Write 继续写，不假定一次 TCP Write 恰好送完整包。
func writePacket(w io.Writer, packet []byte) error {
	for len(packet) > 0 {
		n, err := w.Write(packet)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		packet = packet[n:]
	}
	return nil
}

// benignSocketEnd 只接受本课主动断开的常见结束；超时/非法包保留为失败。
func benignSocketEnd(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}

// stopAndJoin 是代理 owner 的最终收尾：Close listener/连接解除 Accept/ReadFull，
// 再以新预算等 finished，之后才能读 serveErr 与证据；不继承取消的业务 ctx。
func (p *faultProxy) stopAndJoin() (ProtocolEvidence, error) {
	p.cancel()
	p.closeOne(p.listener)
	p.closeSockets()
	ctx, cancel := shortContext()
	defer cancel()
	select {
	case <-p.finished:
		p.mu.Lock()
		evidence := p.evidence
		cleanupErr := p.cleanupErr
		p.mu.Unlock()
		// owner 没发起连接就关闭 listener 是合法的提前清理。
		if errors.Is(p.serveErr, net.ErrClosed) {
			return evidence, cleanupErr
		}
		return evidence, errors.Join(p.serveErr, cleanupErr)
	case <-ctx.Done():
		return ProtocolEvidence{}, errors.Join(ErrJoin, ctx.Err())
	}
}
