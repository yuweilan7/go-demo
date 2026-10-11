package lab

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// wireProxy 拥有本课动态 listener、每对真实 TCP socket 和转发任务。mysql 模式
// 按 MySQL packet 看 COM_QUERY COMMIT 和服务端 OK；redis 模式按 RESP 看完整命令
// 与回复。绝不记录原包（握手可能含凭据），只输出被选中的协议阶段。
type wireProxy struct {
	address, upstream, protocol string
	listener                    net.Listener
	mu                          sync.Mutex
	sockets                     map[net.Conn]bool
	armed                       string
	hit                         chan struct{}
	release                     chan struct{}
	once                        sync.Once
	done                        chan struct{}
	wg                          sync.WaitGroup
	out                         io.Writer
	closeOnce                   sync.Once
	waitOnce                    sync.Once
	connectionsDone             chan struct{}
	joined                      bool
}

// newWire 在创建后即由 session 登记。Accept goroutine 是唯一向 wg.Add 的任务；
// close 先关闭 listener 并等 Accept Done，随后才 wg.Wait，避免 Add/Wait 竞态。
func newWire(protocol, upstream string, out io.Writer) (*wireProxy, error) {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	p := &wireProxy{address: l.Addr().String(), upstream: upstream, protocol: protocol, listener: l, sockets: map[net.Conn]bool{}, hit: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{}), connectionsDone: make(chan struct{}), out: out}
	go func() {
		defer close(p.done)
		for {
			client, e := l.Accept()
			if e != nil {
				return
			}
			p.mu.Lock()
			p.sockets[client] = true
			p.mu.Unlock()
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				defer p.remove(client)
				server, e := net.DialTimeout("tcp", upstream, 2*time.Second)
				if e != nil {
					return
				}
				p.mu.Lock()
				p.sockets[server] = true
				p.mu.Unlock()
				defer p.remove(server)
				if protocol == "mysql" {
					p.mysql(client, server)
				} else {
					p.redis(client, server)
				}
			}()
		}
	}()
	return p, nil
}

func (p *wireProxy) remove(c net.Conn) {
	_ = c.Close()
	p.mu.Lock()
	delete(p.sockets, c)
	p.mu.Unlock()
}
func (p *wireProxy) arm(command string) { p.mu.Lock(); p.armed = command; p.mu.Unlock() }

// selected 将同一次 arm 只消耗一次；连接 goroutine 同锁竞争，不能各自制造一次。
func (p *wireProxy) selected(command string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.armed != command {
		return false
	}
	p.armed = ""
	return true
}

// intercepted 在已经读取真实服务端整包后关 hit，然后等 owner 放行；放行的动作
// 是丢弃回复并结束该连接。hit 证明协议阶段，最后退出仍由 socket/WG Done 证明。
func (p *wireProxy) intercepted(command string) {
	fmt.Fprintf(p.out, "WIRE_REPLY_CAPTURED protocol=%s command=%s actual_server_reply=true delivered=false\n", p.protocol, command)
	p.once.Do(func() { close(p.hit) })
	<-p.release
}

// packet 由两方向的 MySQL 转发使用，包头3字节 little-endian 长度+1字节序号。
// 长度上限保护内存；返回值 raw 是本次私有 buffer，转发后不修改、不打印内容。
func packet(r io.Reader) ([]byte, error) {
	h := make([]byte, 4)
	if _, e := io.ReadFull(r, h); e != nil {
		return nil, e
	}
	n := int(h[0]) | int(h[1])<<8 | int(h[2])<<16
	if n > 1<<20 {
		return nil, errors.New("课程 MySQL packet 超上限")
	}
	raw := make([]byte, 4+n)
	copy(raw, h)
	_, e := io.ReadFull(r, raw[4:])
	return raw, e
}

// mysql 必须双向同时转发，因为握手由 server 先发。client 方向在发送 COMMIT
// 前记录本连接 commit 标记；server 方向只截获 seq1、payload首字节0的实际 OK。
// 它没有把 Tx hook 或 driver 返回错冒充 COMMIT wire 证据。
func (p *wireProxy) mysql(client, server net.Conn) {
	var commitMu sync.Mutex
	commit := false
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		for {
			raw, e := packet(client)
			if e != nil {
				return
			}
			if len(raw) > 5 && raw[4] == 3 && strings.EqualFold(strings.TrimSpace(string(raw[5:])), "COMMIT") && p.selected("COMMIT") {
				commitMu.Lock()
				commit = true
				commitMu.Unlock()
				fmt.Fprintln(p.out, "MYSQL_COMMIT_WIRE command=COM_QUERY bytes=6 forwarded=true")
			}
			if _, e = server.Write(raw); e != nil {
				return
			}
		}
	}()
	for {
		raw, e := packet(server)
		if e != nil {
			break
		}
		commitMu.Lock()
		intercept := commit && len(raw) > 4 && raw[3] == 1 && raw[4] == 0
		commitMu.Unlock()
		if intercept {
			p.intercepted("COMMIT_OK")
			break
		}
		if _, e = client.Write(raw); e != nil {
			break
		}
	}
	_ = client.Close()
	_ = server.Close()
	<-done // 一方向返回不能丢下另一个 goroutine；断 socket 后另方向读会退出。
}

// respFrame 读取 RESP2/RESP3 的一个完整值。go-redis 的 HELLO/AUTH/CLIENT握手也
// 原样经过这里；数组、map、push递归读，但不解释/输出值，秘密不会进入日志。
func respFrame(r *bufio.Reader, depth int) ([]byte, error) {
	if depth > 16 {
		return nil, errors.New("RESP 嵌套超上限")
	}
	line, e := r.ReadBytes('\n')
	if e != nil {
		return nil, e
	}
	if len(line) < 3 || len(line) > 8192 || line[len(line)-2] != '\r' {
		return nil, errors.New("RESP 行无效")
	}
	var out bytes.Buffer
	out.Write(line)
	num := func() (int, error) { return strconv.Atoi(string(line[1 : len(line)-2])) }
	switch line[0] {
	case '$', '!', '=':
		n, e := num()
		if e != nil || n > 1<<20 || n < -1 {
			return nil, errors.New("RESP bulk 上限")
		}
		if n >= 0 {
			payload := make([]byte, n+2)
			if _, e = io.ReadFull(r, payload); e != nil {
				return nil, e
			}
			out.Write(payload)
		}
	case '*', '%', '~', '>', '|':
		n, e := num()
		if e != nil || n > 64 || n < -1 {
			return nil, errors.New("RESP 容器上限")
		}
		if line[0] == '%' || line[0] == '|' {
			n *= 2
		}
		for range n {
			raw, e := respFrame(r, depth+1)
			if e != nil {
				return nil, e
			}
			out.Write(raw)
		}
	case '+', '-', ':', '_', '#', ',', '(':
	default:
		return nil, errors.New("RESP 类型不支持")
	}
	return out.Bytes(), nil
}

// redis 串行转发每条本课命令，然后读其实际完整回复。课程业务无 pipeline 的
// 并发共享连接；握手 pipeline 仍按 RESP 命令依次发送，回复对应关系明确。
func (p *wireProxy) redis(client, server net.Conn) {
	cr, sr := bufio.NewReader(client), bufio.NewReader(server)
	for {
		raw, e := respFrame(cr, 0)
		if e != nil {
			return
		}
		command := ""
		// 请求数组第一项是 bulk command；只提取无秘密的名字，不存其他参数。
		r := bufio.NewReader(bytes.NewReader(raw))
		_, _ = r.ReadBytes('\n')
		_, _ = r.ReadBytes('\n')
		name, _ := r.ReadBytes('\n')
		command = strings.ToUpper(strings.TrimSpace(string(name)))
		intercept := p.selected(command)
		if _, e = server.Write(raw); e != nil {
			return
		}
		reply, e := respFrame(sr, 0)
		if e != nil {
			return
		}
		if intercept {
			p.intercepted(command)
			return
		}
		if _, e = client.Write(reply); e != nil {
			return
		}
	}
}

// close 可反复调用；先让门可退出，再断全部自己登记的 socket，等 Accept 和连接
// 全部 Done。新鲜清理预算不沿用已超时的业务 ctx，避免把 ctx.Err 当作退出证据。
func (p *wireProxy) close() error {
	p.closeOnce.Do(func() { close(p.release); _ = p.listener.Close() })
	if e := b.JoinDone(p.done, "proxy Accept", 3*time.Second); e != nil {
		return e
	}
	p.mu.Lock()
	for c := range p.sockets {
		_ = c.Close()
	}
	p.mu.Unlock()
	p.waitOnce.Do(func() { go func() { defer close(p.connectionsDone); p.wg.Wait() }() })
	if e := b.JoinDone(p.connectionsDone, "proxy connections/directions", 5*time.Second); e != nil {
		return e
	}
	p.mu.Lock()
	n := len(p.sockets)
	p.mu.Unlock()
	p.mu.Lock()
	p.joined = n == 0
	p.mu.Unlock()
	fmt.Fprintf(p.out, "PROXY_DONE protocol=%s sockets=%d directions_joined=true\n", p.protocol, n)
	return b.Assert(n == 0, "代理所有真实 socket 应归零，actual=%d", n)
}

func (p *wireProxy) safe() bool {
	select {
	case <-p.done:
	default:
		return false
	}
	select {
	case <-p.connectionsDone:
	default:
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.joined && len(p.sockets) == 0
}
