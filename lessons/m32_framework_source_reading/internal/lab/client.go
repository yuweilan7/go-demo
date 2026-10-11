package lab

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	pb "go-demo/lessons/m32_framework_source_reading/gen/inventoryv1"
	"go-demo/lessons/m32_framework_source_reading/internal/app"
	"go-demo/lessons/m32_framework_source_reading/internal/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"net/http"
	"time"
)

type clients struct {
	grpcConn      *grpc.ClientConn
	grpc          pb.InventoryClient
	http          pb.InventoryHTTPClient
	httpClient    *khttp.Client
	httpTransport *http.Transport
	closed        bool
}

// newClients 被实验owner调用；创建实际框架client，不用函数替身。ctx只限制建连观察，不成为连接生命周期owner。
// 构造成功不等于连通：gRPC WaitForStateChange 明确等Ready，然后真实Inspect才证明注册/业务链可用。
func newClients(ctx context.Context, r *app.Root, j *trace.Journal, tenant string) (c *clients, err error) {
	c = &clients{httpTransport: http.DefaultTransport.(*http.Transport).Clone()}
	owned := c // return nil会改具名c；rollback闭包必须保留实际已创建对象的指针。
	transferred := false
	defer func() {
		if !transferred {
			err = errors.Join(err, owned.close())
			c = nil
		}
	}()
	mw := clientMiddleware(j, tenant)
	// 动态loopback明确使用Kratos direct resolver，WithNoProxy仅作用此连接，避免本机HTTP代理截走gRPC。
	c.grpcConn, err = kgrpc.NewClient(ctx, kgrpc.WithEndpoint("direct:///"+r.GRPCAddress), kgrpc.WithTimeout(2*time.Second), kgrpc.WithMiddleware(mw), kgrpc.WithOptions(grpc.WithDisableRetry(), grpc.WithNoProxy()))
	if err != nil {
		return nil, err
	}
	c.grpc = pb.NewInventoryClient(c.grpcConn)
	for {
		state := c.grpcConn.GetState()
		if state == connectivity.Ready {
			break
		}
		if !c.grpcConn.WaitForStateChange(ctx, state) {
			return nil, fmt.Errorf("真实gRPC未Ready: %w", ctx.Err())
		}
	}
	c.httpClient, err = khttp.NewClient(ctx, khttp.WithEndpoint("http://"+r.HTTPAddress), khttp.WithTimeout(2*time.Second), khttp.WithTransport(c.httpTransport), khttp.WithMiddleware(mw))
	if err != nil {
		return nil, err
	}
	c.http = pb.NewInventoryHTTPClient(c.httpClient)
	transferred = true
	return c, nil
}

// close 由创建clients的实验在所有调用任务join后调用。Kratos HTTP Client.Close实际只关闭resolver，
// 所以独占标准Transport的闲连接另由我们CloseIdleConnections；不能误读第三方Close注释就认为全被回收。
func (c *clients) close() error {
	var err error
	if c == nil || c.closed {
		return nil
	}
	c.closed = true
	if c.grpcConn != nil {
		err = errors.Join(err, c.grpcConn.Close())
	}
	if c.httpClient != nil {
		err = errors.Join(err, c.httpClient.Close())
	}
	if c.httpTransport != nil {
		c.httpTransport.CloseIdleConnections()
	}
	return err
}

// clientMiddleware 被Kratos client interceptor/Invoke调用，ctx已经有transport client值与框架预算。
// next是实际invoker/HTTP请求；写header再进入网络，defer记录返回，闭包共享Journal但只读tenant字符串。
func clientMiddleware(j *trace.Journal, tenant string) middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (reply any, err error) {
			id := "inspect"
			if request, ok := req.(*pb.ReserveRequest); ok {
				id = request.GetRequestId()
			}
			tr, ok := transport.FromClientContext(ctx)
			if !ok {
				return nil, errors.New("client缺少transport ctx")
			}
			if tenant != "" {
				tr.RequestHeader().Set("x-course-tenant", tenant)
			}
			j.Add(id, "client.enter operation="+tr.Operation())
			defer func() { j.Add(id, fmt.Sprintf("client.exit error=%t", err != nil)) }()
			return next(ctx, req)
		}
	}
}
