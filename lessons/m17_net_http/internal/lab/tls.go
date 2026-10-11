package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// tlsExperiment 观察三个真实阶段：loopback 成功 TCP+TLS，关闭端口上的 dial 拒绝，
// 接受 TCP/ClientHello 却不回答 TLS 的 peer 导致握手超时。最后一个 peer 是明确的
// 真实协议故障服务，不是假 RoundTripper；不向公网发送请求，不用不可控地址猜拨号超时。
func tlsExperiment(ctx context.Context) (result Observation, err error) {
	result.Name = "E09 TCP connect 与 TLSHandshakeTimeout"
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, Envelope{Code: "OK", Committed: true})
	}))
	ts.StartTLS() // httptest 仍使用真实 loopback listener 和 TCP/TLS/HTTP。
	c := NewClient(2, time.Second, 3*time.Second)
	// 只信任本次 httptest 的证书池，不设置 InsecureSkipVerify；正式服务应使用真实 CA。
	testTransport := ts.Client().Transport.(*http.Transport)
	c.Transport.TLSClientConfig = testTransport.TLSClientConfig.Clone()
	defer func() {
		c.CloseIdle()
		ts.Close() // 同步等待这个测试服务器的活动请求/连接退出。
		testTransport.CloseIdleConnections()
		if open := c.OpenBodies.Load(); open != 0 {
			err = errors.Join(err, fmt.Errorf("TLS body owner=%d", open))
		}
		if owned := c.OwnedRequests.Load(); owned != 0 {
			err = errors.Join(err, fmt.Errorf("TLS owned request未finished=%d", owned))
		}
	}()
	trace := &Trace{}
	if _, err = RequestJSON(AttachTrace(ctx, trace), c, "GET", ts.URL, nil, "", 2048); err != nil {
		return result, err
	}
	connected, _, firstByte := trace.Snapshot()
	started, completed := trace.TLSSnapshot()
	if err = require(connected && started && completed && firstByte, "正常 TLS 的 trace 阶段缺失"); err != nil {
		return result, err
	}
	result.Facts = append(result.Facts, "normal: ConnectDone=true -> TLSHandshakeStart -> TLSHandshakeDone=true -> HTTP200；校验证书有效")

	closedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return result, err
	}
	closedURL := "http://" + closedListener.Addr().String()
	if err = closedListener.Close(); err != nil {
		return result, err
	}
	_, dialErr := RequestJSON(ctx, c, "GET", closedURL, nil, "", 2048)
	var opErr *net.OpError
	if err = require(errors.As(dialErr, &opErr) && opErr.Op == "dial", "关闭端口应在真实 dial 阶段失败"); err != nil {
		return result, errors.Join(err, dialErr)
	}
	result.Facts = append(result.Facts, fmt.Sprintf("closed loopback port: stage=dial error=%v；Dialer.Timeout=500ms（本次是拒绝，不是OS拨号超时）", dialErr))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return result, err
	}
	defer ln.Close()
	// accept本身也设deadline；原始peer与HTTP请求是两项不同的owned goroutine，
	// 各自都有独立清理defer，任何Entered/Result等待失败都仍会取消、解除gate、join。
	if tcp, ok := ln.(*net.TCPListener); ok {
		if err = tcp.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			return result, err
		}
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	openRelease := func() { releaseOnce.Do(func() { close(release) }) }
	peerCtx, cancelPeer := context.WithCancel(ctx)
	peerFinished := make(chan struct{})
	var peerErr error
	go func() {
		// 内层先执行socket/timer清理，再写peerErr，最后关闭finished；owner只在
		// 接收关闭广播以后读取peerErr，不把工作ctx取消当peer已经退出。
		defer close(peerFinished)
		peerErr = func() error {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return acceptErr
			}
			defer conn.Close()
			if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				return err
			}
			oneByte := make([]byte, 1)
			if _, readErr := io.ReadFull(conn, oneByte); readErr != nil {
				return readErr
			}
			close(entered) // 客户端已开始发送 TLS record，TCP connect 已实际成功。
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			select {
			case <-release:
				return nil
			case <-peerCtx.Done():
				return peerCtx.Err()
			case <-timer.C:
				return errors.New("TLS peer 安全上限")
			}
		}()
	}()
	defer func() {
		cancelPeer()
		openRelease()
		_ = ln.Close() // 同时解除尚未accept的失败路径，不能只打开已accept后的gate。
		cleanupCtx, stopCleanup := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCleanup()
		joinErr := waitSignal(cleanupCtx, peerFinished, "raw TLS peer finished")
		err = errors.Join(err, joinErr)
		if joinErr == nil {
			err = errors.Join(err, peerErr)
		}
	}()
	stallTrace := &Trace{}
	request := doAsync(AttachTrace(ctx, stallTrace), c, "GET", "https://"+ln.Addr().String(), "")
	defer combineOwnedCleanup(&err, request, openRelease)
	if err = waitSignal(ctx, entered, "TLS peer 已读到 ClientHello 首字节"); err != nil {
		return result, err
	}
	tlsErr, waitErr := request.Result(ctx)
	if waitErr != nil {
		return result, waitErr
	}
	openRelease()
	if err = waitSignal(ctx, peerFinished, "raw TLS peer finished"); err != nil {
		return result, err
	}
	if peerErr != nil {
		return result, peerErr
	}
	connected, _, firstByte = stallTrace.Snapshot()
	started, completed = stallTrace.TLSSnapshot()
	var timeoutErr net.Error
	if err = require(connected && started && !completed && !firstByte && errors.As(tlsErr, &timeoutErr) && timeoutErr.Timeout(), "TLS stall 应在握手阶段超时"); err != nil {
		return result, errors.Join(err, tlsErr)
	}
	result.Facts = append(result.Facts, fmt.Sprintf("stall: TCP=true ClientHello_received=true TLS_started=true TLS_completed=false HTTP_first_byte=false budget=150ms error=%v", tlsErr), "cleanup: TLS/raw peer finished与request finished分别独立join，两个自有listener=closed，body/request owner=0")
	return result, nil
}
