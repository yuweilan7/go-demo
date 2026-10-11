package lesson

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	pb "go-demo/lessons/m31_grpc_protobuf/gen/inventoryv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// harness 是整个实验资源的 owner：持有监听器、Server、ClientConn 和 Serve 的完成通知。
// ClientConn 是可复用逻辑通道，内部可管理传输连接；它不是“一次 RPC 一个 socket”。
type harness struct {
	server    *grpc.Server
	conn      *grpc.ClientConn
	client    pb.InventoryClient
	service   *inventory
	serveDone chan error
	log       *journal
	initial   connectivity.State
}

// startHarness 由 Run 同步调用，动态绑定 loopback :0 后保留同一监听器，避免“先查空闲端口再绑定”的竞争。
// 失败时立即关闭已经创建的资源，成功后把完整 owner 交给 Run。
func startHarness(log *journal) (*harness, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("建立独占 loopback listener: %w", err)
	}
	service := newInventory(log)
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(serverTrace(log, "outer"), recoverUnary(log), identityUnary(log), serverTrace(log, "inner")), grpc.StreamInterceptor(identityStream))
	pb.RegisterInventoryServer(srv, service)
	h := &harness{server: srv, service: service, serveDone: make(chan error, 1), log: log}
	// Serve 是本 harness 创建的 goroutine；buffer=1 保证即使创建 client 失败，它也能报告结束并退出。
	go func() { h.serveDone <- srv.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///"+lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithNoProxy(),
		grpc.WithChainUnaryInterceptor(clientTrace(log, "outer"), clientTrace(log, "inner")),
		// 固定客户端接收窗口，关闭动态窗口增长，使“不 Recv”的背压实验有可解释的上限。
		grpc.WithStaticStreamWindowSize(64*1024), grpc.WithStaticConnWindowSize(64*1024), grpc.WithWriteBufferSize(0))
	if err != nil {
		srv.Stop()
		<-h.serveDone
		return nil, fmt.Errorf("构造 ClientConn: %w", err)
	}
	h.conn = conn
	h.initial = conn.GetState()
	h.client = pb.NewInventoryClient(conn)
	log.add("E00", fmt.Sprintf("NewClient.state=%s; listener=%s", h.initial, lis.Addr()))
	return h, nil
}

// waitReady 观察真实连接状态；Connect 只触发建立连接，不等到 Ready，因此还要等待状态变化。
// WaitForStateChange 等“离开刚才状态”，不是承诺下一状态 Ready；必须在循环中重新读取。
func (h *harness) waitReady(ctx context.Context) error {
	// 已取消的 owner 不应再推进连接工作；startHarness 创建的资源仍由 Run 的 defer 清理。
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("等待连接前 owner 已结束: %w", err)
	}
	h.conn.Connect()
	for {
		state := h.conn.GetState()
		if state == connectivity.Ready {
			h.log.add("E00", "ClientConn.Ready")
			return nil
		}
		if state == connectivity.Shutdown {
			return errors.New("连接已 Shutdown")
		}
		if !h.conn.WaitForStateChange(ctx, state) {
			return fmt.Errorf("等待连接 Ready: %w", ctx.Err())
		}
	}
}

// close 由 Run 的 defer 调用：先关闭 client，再有界 GracefulStop，最后等待 Serve 真正返回。
// GracefulStop 可能被未结束的 RPC 阻塞；2 秒后 Stop 强制取消它们，然后仍等待 graceful goroutine 退出。
func (h *harness) close() error {
	h.service.mu.Lock()
	for _, p := range h.service.plans {
		p.release()
	}
	h.service.mu.Unlock()
	connErr := h.conn.Close()
	graceDone := make(chan struct{})
	go func() { h.server.GracefulStop(); close(graceDone) }()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-graceDone:
	case <-timer.C:
		h.server.Stop()
		<-graceDone
	}
	serveErr := <-h.serveDone
	if errors.Is(serveErr, grpc.ErrServerStopped) {
		serveErr = nil
	}
	h.log.add("E99", "client.Shutdown; server.Serve.returned; listener.closed")
	return errors.Join(connErr, serveErr, check(h.conn.GetState() == connectivity.Shutdown, "Close 后 ClientConn 未 Shutdown"))
}

// ownedContext 给每次请求附上 outgoing metadata；它必须由框架跨进程传输，不能靠 context.Value 直接穿过网络。
func ownedContext(parent context.Context) context.Context {
	return metadata.NewOutgoingContext(parent, metadata.Pairs("x-tenant", "course-m31"))
}

// inspect 以新的真实 RPC 查询库存，避免用过期的写请求 ctx，也避免只从写响应猜最终副作用。
func (h *harness) inspect(ctx context.Context) (int32, error) {
	stock, err := h.client.Inspect(ownedContext(ctx), &pb.InspectRequest{Sku: "001-SKU"})
	if err != nil {
		return 0, err
	}
	return stock.GetAvailable(), nil
}
