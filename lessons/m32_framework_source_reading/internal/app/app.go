// Package app 是 composition root：创建实际框架服务、装配业务依赖并拥有最终 Close。
package app

import (
	"context"
	"errors"
	"fmt"
	kratos "github.com/go-kratos/kratos/v3"
	kerrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/transport"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	pb "go-demo/lessons/m32_framework_source_reading/gen/inventoryv1"
	"go-demo/lessons/m32_framework_source_reading/internal/biz"
	"go-demo/lessons/m32_framework_source_reading/internal/data"
	"go-demo/lessons/m32_framework_source_reading/internal/service"
	"go-demo/lessons/m32_framework_source_reading/internal/trace"
	"google.golang.org/grpc"
	"io"
	"log/slog"
	"net"
	stdhttp "net/http"
	"time"
)

// Config 是入口交给 root 的值快照；只含教学版本/预算，listener地址固定动态loopback，不接公网配置。
// Tail 仅课程故障fixture有共享指针，不能把这个参数开放给生产请求者。
type Config struct {
	Version                     string
	RequestTimeout, StopTimeout time.Duration
	Tail                        *Tail
}

func DefaultConfig() Config {
	return Config{Version: "m32-kratos-v3", RequestTimeout: 200 * time.Millisecond, StopTimeout: 80 * time.Millisecond}
}

// Validate 在任何资源创建前调用；避免非法配置先绑TCP再返回错误。接近Spring属性验证，但只是普通方法。
func (c Config) Validate() error {
	if c.Version == "" || c.RequestTimeout <= 0 || c.RequestTimeout > 5*time.Second || c.StopTimeout <= 0 || c.StopTimeout > 5*time.Second {
		return errors.New("version非空、request/stop budget必须在(0,5s]")
	}
	return nil
}

type Root struct {
	Config                   Config
	Repository               *data.Memory
	Usecase                  *biz.Usecase
	HTTP                     *khttp.Server
	GRPC                     *kgrpc.Server
	Framework                *kratos.App
	HTTPAddress, GRPCAddress string
	journal                  *trace.Journal
	active                   *activeOwner
	cancel                   context.CancelFunc
	transports               []*observedTransport
	listeners                []net.Listener
	RunOwner                 *Task
	stopped                  chan struct{}
}

// New 被实验/入口调用；构造成功才把listener、repo与框架交给Root。失败defer保留主因和回收因。
// 本课无需配置加载器/provider空壳，DefaultConfig→Validate→具体构造就是M19的显式装配路径。
func New(cfg Config, j *trace.Journal) (root *Root, err error) {
	if err = cfg.Validate(); err != nil {
		return nil, err
	}
	repo := data.New(j)
	root = &Root{Config: cfg, Repository: repo, Usecase: biz.New(repo, j), journal: j, active: newActiveOwner(), stopped: make(chan struct{})}
	var httpListener, grpcListener net.Listener
	transferred := false
	defer func() {
		if !transferred {
			if httpListener != nil {
				err = errors.Join(err, httpListener.Close())
			}
			if grpcListener != nil {
				err = errors.Join(err, grpcListener.Close())
			}
			err = errors.Join(err, repo.Close())
			root = nil
		}
	}()
	httpListener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	grpcListener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	root.HTTPAddress = httpListener.Addr().String()
	root.GRPCAddress = grpcListener.Addr().String()
	root.listeners = []net.Listener{httpListener, grpcListener}
	requestMiddleware := root.serverMiddleware()
	// recovery不是NewServer的默认配置：root明确安装官方Recovery。日志writer仅合成课例；panic栈可在测试诊断打开。
	recoveryLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	middlewares := []middleware.Middleware{requestMiddleware, recovery.Recovery(recovery.WithLogger(recoveryLogger)), authenticate()}
	root.HTTP = khttp.NewServer(khttp.Listener(httpListener), khttp.Address(root.HTTPAddress), khttp.Timeout(cfg.RequestTimeout), khttp.Middleware(middlewares...), khttp.Filter(root.httpOwnerFilter()))
	root.GRPC = kgrpc.NewServer(kgrpc.Listener(grpcListener), kgrpc.Address(root.GRPCAddress), kgrpc.Timeout(cfg.RequestTimeout), kgrpc.Middleware(middlewares...), kgrpc.UnaryInterceptor(root.grpcOwnerInterceptor()), kgrpc.StreamInterceptor(root.streamOwnerInterceptor()), kgrpc.DisableReflection())
	svc := service.New(root.Usecase)
	pb.RegisterInventoryHTTPServer(root.HTTP, svc) // 生成的HTTP Route 注册；不是手写伪框架路由。
	pb.RegisterInventoryServer(root.GRPC, svc)     // 真正的gRPC ServiceDesc/handlers注册到内嵌grpc.Server。
	if cfg.Tail != nil {
		root.HTTP.HandleFunc("/owned-tail", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
			// 明确Content-Length并Flush，让客户端能读完4字节，服务端outer filter仍可卡在自己的defer。
			// 这不是“响应Body关闭=handler退出”的保证，恰好用来复现两者不同。
			w.Header().Set("Content-Length", "4")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("tail"))
			w.(stdhttp.Flusher).Flush()
		})
	}
	// Endpoint 在并发Start之前求值，固定已取得的listener/endpoint；不把App的afterStart hook当readiness。
	if _, err = root.HTTP.Endpoint(); err != nil {
		return nil, err
	}
	if _, err = root.GRPC.Endpoint(); err != nil {
		return nil, err
	}
	base, cancel := context.WithCancel(context.Background())
	root.cancel = cancel
	root.transports = []*observedTransport{observe("HTTP", root.HTTP), observe("gRPC", root.GRPC)}
	root.Framework = kratos.New(kratos.ID("m32-local"), kratos.Name("m32-inventory"), kratos.Version(cfg.Version), kratos.Context(base), kratos.Server(root.transports[0], root.transports[1]), kratos.StopTimeout(cfg.StopTimeout), kratos.BeforeStop(func(context.Context) error {
		// Stop先封账，然后显式取消业务根。框架App取消它自己的ctx并不等价于取消opts.ctx里的所有请求。
		root.active.seal()
		root.cancel()
		j.Add("root", "beforeStop.sealed-and-canceled")
		return nil
	}))
	transferred = true
	return root, nil
}

// Start 只创建一次App.Run owner；网络ready随后由客户端真实连接/Inspect观察，Run goroutine启动不是健康证明。
func (r *Root) Start() {
	r.RunOwner = StartTask(func() error { err := r.Framework.Run(); r.journal.Add("root", "App.Run.return"); return err }, nil)
}

// StopNotification 是实验显式调用的框架停止入口。返回只证明hooks/取消通知已执行，仍需Close或JoinFramework。
func (r *Root) StopNotification() error { return r.Framework.Stop() }

// JoinFramework 被停止实验调用，独立等App.Run和真实Start的最后defer；它仍不等同于handler账本idle。
func (r *Root) JoinFramework(ctx context.Context) error {
	if r.RunOwner == nil {
		return nil
	}
	result := r.RunOwner.Join(ctx)
	for _, s := range r.transports {
		if err := waitDone(ctx, s.done, s.name+" Start"); err != nil {
			result = errors.Join(result, err)
		} else {
			result = errors.Join(result, s.result)
		}
	}
	return result
}
func (r *Root) Active() int { return r.active.Count() }

// Close 是唯一最终资源owner，实验的defer以及正常结尾均同步调用，不借用已取消的请求ctx。
// 每次关闭有独立4秒保险：放开所有故障屏障→Stop→join框架→join实际handler→Close仓储。
// 主请求错误由caller用errors.Join与本函数结果一起保留；框架Stop有可能吞掉Shutdown超时，README单独观察边界。
func (r *Root) Close() error {
	select {
	case <-r.stopped:
		return nil
	default:
	}
	r.Usecase.ReleaseAll()
	if r.Config.Tail != nil {
		r.Config.Tail.Release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var result error
	if r.RunOwner != nil {
		result = errors.Join(result, r.StopNotification(), r.JoinFramework(ctx))
	} else {
		// 尚未Start也必须回收已绑定listener/admin resources；Stop由当前调用方同步拥有。
		r.active.seal()
		r.cancel()
		result = errors.Join(result, r.HTTP.Stop(ctx), r.GRPC.Stop(ctx))
	}
	result = errors.Join(result, r.active.wait(ctx))
	// 尚未Serve过的外部listener不一定由Shutdown/GracefulStop关闭；root无论哪条路径都精确关闭自有句柄。
	for _, listener := range r.listeners {
		closeErr := listener.Close()
		if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			result = errors.Join(result, closeErr)
		}
	}
	if r.active.Count() != 0 {
		return errors.Join(result, fmt.Errorf("active=%d，仓储不能在handler仍借用时关闭", r.active.Count()))
	}
	result = errors.Join(result, r.Repository.Close())
	close(r.stopped)
	r.journal.Add("root", "Close.joined-and-closed")
	return result
}

// serverMiddleware 在真实Kratos middleware链最外层记事件，next是官方Recovery包装后的业务函数。
// error通过具名返回传给defer，panic先被内层Recovery转为UNKNOWN后返回，此处看见稳定协议错误。
func (r *Root) serverMiddleware() middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (reply any, err error) {
			id := "inspect"
			if value, ok := req.(*pb.ReserveRequest); ok {
				id = value.GetRequestId()
			}
			tr, ok := transport.FromServerContext(ctx)
			if !ok {
				return nil, kerrors.InternalServer("TRANSPORT", "缺少transport context")
			}
			r.journal.Add(id, "server.enter operation="+tr.Operation())
			defer func() {
				r.journal.Add(id, fmt.Sprintf("server.exit code=%d reason=%s", kerrors.Code(err), kerrors.Reason(err)))
			}()
			return next(ctx, req)
		}
	}
}

// authenticate读取transport统一header；同样的中间件适用于HTTP和gRPC，但不是生产认证方案。
// req参数不会保存；ctx.WithValue派生本次请求的可信tenant字符串，底层取消/deadline继续沿父ctx传播。
func authenticate() middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			tr, ok := transport.FromServerContext(ctx)
			if !ok || tr.RequestHeader().Get("x-course-tenant") != "m32" {
				return nil, kerrors.Unauthorized("AUTH", "缺少课程身份")
			}
			return next(biz.WithTenant(ctx, "m32"), req)
		}
	}
}

// httpOwnerFilter包住完整router，包括HTTP编码、错误encoder和框架timeout cancel。
// 它比业务plan.Done更外层；强制Close后仍可能执行本filter的defer，所以HTTP Shutdown不能替代账本。
func (r *Root) httpOwnerFilter() khttp.FilterFunc {
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			leave, ok := r.active.enter()
			if !ok {
				stdhttp.Error(w, "stopping", 503)
				return
			}
			defer leave()
			if tail := r.Config.Tail; tail != nil && req.URL.Path == "/owned-tail" {
				defer close(tail.Done) // tail.Done先于leave；真正最终使用范围还由active.wait确认。
				defer func() { close(tail.Returning); <-tail.Gate; r.journal.Add("tail", "handler.defer.released") }()
				close(tail.Entered)
			}
			next.ServeHTTP(w, req)
		})
	}
}

// grpcOwnerInterceptor登记generated handler到实际返回；外层Kratos interceptor还有context/header收尾，由GracefulStop join。
func (r *Root) grpcOwnerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		leave, ok := r.active.enter()
		if !ok {
			return nil, kerrors.ServiceUnavailable("STOPPING", "停服中")
		}
		defer leave()
		return next(ctx, req)
	}
}

// 流未进入unary链，所以独立登记其owner；客户端CloseSend并读到EOF以后，Close仍核对这个函数的退出。
func (r *Root) streamOwnerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		// 本课程认证和业务账本仅围绕库存流。框架自己的health.Watch由框架health.Shutdown拥有，
		// 不能误套课程tenant：那会让client的健康检查永远失败，connection一直无法Ready。
		if info.FullMethod != pb.Inventory_Watch_FullMethodName {
			return next(srv, stream)
		}
		leave, ok := r.active.enter()
		if !ok {
			return kerrors.ServiceUnavailable("STOPPING", "停服中")
		}
		defer leave()
		tr, ok := transport.FromServerContext(stream.Context())
		if !ok || tr.RequestHeader().Get("x-course-tenant") != "m32" {
			return kerrors.Unauthorized("AUTH", "缺少课程身份")
		}
		return next(srv, stream)
	}
}
