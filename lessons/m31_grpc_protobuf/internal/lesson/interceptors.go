package lesson

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// tenantKey 是包内独立 key 类型，避免不同包都用字符串 tenant 时互相覆盖。
type tenantKey struct{}

// requestName 只从拥有 GetRequestId 方法的动态请求值取 ID；Inspect 用固定标签，不做危险的强制类型断言。
func requestName(req any) string {
	if named, ok := req.(interface{ GetRequestId() string }); ok {
		return named.GetRequestId()
	}
	return "inspect"
}

// serverTrace 返回一个闭包：创建时捕获共享 journal 和本层名，每次 RPC 调用时取得专属 ctx/req。
// handler 是链的下一层，必须同步调用它；这里的 enter/exit 证明 ChainUnaryInterceptor 的嵌套顺序。
func serverTrace(log *journal, name string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := requestName(req)
		log.add(id, "server."+name+".enter")
		defer log.add(id, "server."+name+".exit")
		return handler(ctx, req)
	}
}

// recoverUnary 是服务器 RPC goroutine 的 panic boundary，包围它后面的 identity/inner/handler。
// named 返回值允许 defer 在 recover 后设置最终 error；被 panic 中断的 handler 不会从原行继续。
// 它不覆盖外层 trace，也不能恢复 handler 自己启动的另一 goroutine 的 panic。
func recoverUnary(log *journal) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (reply any, err error) {
		id := requestName(req)
		log.add(id, "server.recovery.enter")
		defer func() {
			if value := recover(); value != nil {
				reply = nil
				err = status.Error(codes.Internal, "服务内部故障")
				log.add(id, "server.recovery.panic")
			}
			log.add(id, "server.recovery.exit")
		}()
		return handler(ctx, req)
	}
}

// identityUnary 把真实 incoming metadata 校验后写入本 RPC 的派生 ctx，并发 RPC 不共享这个值。
// 本课程没有 TLS/鉴权；x-tenant 是归属演示，用户可伪造的 metadata 不能当生产身份凭证。
func identityUnary(log *journal) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := requestName(req)
		log.add(id, "server.identity.enter")
		defer log.add(id, "server.identity.exit")
		md, _ := metadata.FromIncomingContext(ctx)
		values := md.Get("x-tenant")
		if len(values) != 1 || values[0] != "course-m31" {
			return nil, status.Error(codes.Unauthenticated, "x-tenant 缺失或不属于本课")
		}
		ctx = context.WithValue(ctx, tenantKey{}, values[0])
		// Header 是返回 metadata；设置不会自行发送业务响应，客户端用 grpc.Header 接收。
		if err := grpc.SetHeader(ctx, metadata.Pairs("x-tenant", values[0], "x-rpc-owner", "inventory-handler")); err != nil {
			return nil, err
		}
		if err := grpc.SetTrailer(ctx, metadata.Pairs("x-course", "m31")); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// clientTrace 的 invoker 是后续 client interceptor 或 ClientConn.Invoke；共享 Conn 不意味着共享请求 ctx。
func clientTrace(log *journal, name string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		id := requestName(req)
		log.add(id, "client."+name+".enter")
		defer log.add(id, "client."+name+".exit")
		// opts... 将 Header/Trailer/WaitForReady 等调用选项完整转给下一层，漏传会破坏实验契约。
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// identityStream 是独立的 stream interceptor：unary 链不会自动用于 Watch。
// recovery 在此流 goroutine 中包围 handler；header、ctx 都归属于当前 stream。
func identityStream(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	defer func() {
		if recover() != nil {
			err = status.Error(codes.Internal, "流内部故障")
		}
	}()
	md, _ := metadata.FromIncomingContext(stream.Context())
	tenant := md.Get("x-tenant")
	if len(tenant) != 1 || tenant[0] != "course-m31" {
		return status.Error(codes.Unauthenticated, "流 x-tenant 无效")
	}
	if err := stream.SetHeader(metadata.Pairs("x-tenant", tenant[0])); err != nil {
		return err
	}
	return handler(srv, stream)
}
