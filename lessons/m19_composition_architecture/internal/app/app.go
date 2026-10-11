// Package app 是 composition root：它知道具体仓储、HTTP adapter、配置及关闭顺序。
// 业务 order 与存储 memory 互不反向导入 app，因此 import 图没有循环。
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"go-demo/lessons/m19_composition_architecture/internal/api"
	"go-demo/lessons/m19_composition_architecture/internal/config"
	"go-demo/lessons/m19_composition_architecture/internal/memory"
	"go-demo/lessons/m19_composition_architecture/internal/order"
)

var _ order.Repository = (*memory.Repository)(nil) // 编译只证明方法集匹配，nil 值仍不可注入。

// Factories 是 root 的三个资源/适配器创建入口，不是请求期 service locator。
// nil 函数字段使用实际默认实现；实验替换入口仍创建真实 listener/仓储，只在边界注入失败。
// FinalizeServe 仅用于可观察的 Result/Done 实验；正常程序为 nil，不新增后台服务。
type Factories struct {
	OpenRepository       func() (*memory.Repository, error)
	Router               func(*order.Service, config.Config) (http.Handler, error)
	Listen               func(string) (net.Listener, error)
	FinalizeServe        func()
	AfterRepositoryClose func() error
}

type App struct {
	repo       *memory.Repository
	server     *http.Server
	handlers   *handlerOwner
	listener   net.Listener
	cancel     context.CancelFunc
	result     chan error
	done       chan struct{}
	finalize   func()
	afterClose func() error
	mu         sync.Mutex
	started    bool
	serveErr   error
	stopMu     sync.Mutex
	closed     bool
}

// WireService 是 root 的具体指针断言边界；实验同时展示直接调用接口构造器的漏洞。
// r 为 nil 时尚未转换成接口，所以 ==nil 有效；Ready 进一步拦住“非 nil 但未启动”。
func WireService(r *memory.Repository) (*order.Service, error) {
	if r == nil {
		return nil, errors.New("compose: repository pointer is nil")
	}
	if err := r.Ready(); err != nil {
		return nil, fmt.Errorf("compose ready: %w", err)
	}
	return order.NewService(r)
}

// New 是正常入口的构造器，返回具体 *App；调用者获得所有资源的 Close owner 职责。
func New(cfg config.Config) (*App, error) { return Build(cfg, Factories{}) }

// Build 由入口/构造故障实验调用，按“验证→仓储→Start→Service→Router→Listener”创建。
// 具名 err + defer 是未成功交接时的回滚；成功返回 app 后资源所有权才交给 caller。
// 如果 factory 返回“资源+error”，也先接住资源再处理 error，防止中途构造泄漏。
func Build(cfg config.Config, f Factories) (a *App, err error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	a = &App{cancel: cancel, result: make(chan error, 1), done: make(chan struct{}), finalize: f.FinalizeServe, afterClose: f.AfterRepositoryClose}
	transferred := false // 与 err 分开：panic 展开时 err 可能仍 nil，但资源尚未交给 caller。
	defer func() {
		if transferred {
			return
		} // 成功才把 a 交给 caller；失败仍由本函数清理局部对象。
		cancel()
		if a.listener != nil {
			err = errors.Join(err, a.listener.Close())
		}
		if a.repo != nil {
			err = errors.Join(err, a.repo.Close())
			if a.afterClose != nil {
				err = errors.Join(err, a.afterClose())
			}
		}
		a = nil
	}()
	open := f.OpenRepository
	if open == nil {
		open = func() (*memory.Repository, error) { return memory.New(nil), nil }
	}
	a.repo, err = open()
	if err != nil {
		return a, fmt.Errorf("open repository: %w", err)
	}
	if a.repo == nil {
		return a, errors.New("repository factory returned nil")
	}
	if err = a.repo.Start(); err != nil {
		return a, fmt.Errorf("start repository: %w", err)
	}
	service, wireErr := WireService(a.repo)
	if wireErr != nil {
		return a, wireErr
	}
	router := f.Router
	if router == nil {
		router = func(s *order.Service, c config.Config) (http.Handler, error) { return api.NewRouter(s, c) }
	}
	handler, routerErr := router(service, cfg)
	if routerErr != nil {
		return a, fmt.Errorf("build router: %w", routerErr)
	}
	if handler == nil {
		return a, errors.New("router factory returned nil")
	}
	listen := f.Listen
	if listen == nil {
		listen = func(address string) (net.Listener, error) { return net.Listen("tcp", address) }
	}
	a.listener, err = listen(cfg.Listen)
	if err != nil {
		return a, fmt.Errorf("listen: %w", err)
	}
	if a.listener == nil {
		return a, errors.New("listener factory returned nil")
	}
	a.handlers = newHandlerOwner(handler)
	a.server = &http.Server{Handler: a.handlers, ReadHeaderTimeout: time.Second, IdleTimeout: time.Second, BaseContext: func(net.Listener) context.Context { return root }}
	transferred = true
	return a, nil
}

// Start 由 owner 在完整装配后调用一次。listener 已绑定真实端口，Serve goroutine 由 App 拥有。
// 不把“构造完成”当成“Serve 已处理请求”；核心实验用真实 GET/POST 成功证明可用。
func (a *App) Start() error {
	a.stopMu.Lock()
	defer a.stopMu.Unlock() // 与 Close 串行，构造/启动/关闭不会互相跨越。
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return errors.New("app already started")
	}
	if a.closed {
		return errors.New("app already closed")
	}
	a.started = true
	go func() {
		err := a.server.Serve(a.listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		a.mu.Lock()
		a.serveErr = err
		a.mu.Unlock()
		a.result <- err // 容量1：owner 即使尚未接收，Serve 也能发布结果，不会阻塞在发送。
		close(a.result)
		if a.finalize != nil {
			a.finalize()
		} // 实验屏障显示结果可读之后仍存在 owner 负责的尾部清理。
		close(a.done) // Done 在最终清理后关闭；任何 owner 都可重复等待这个最终状态。
	}()
	return nil
}

// Close 由入口 defer 或测试 cleanup 调用；ctx 必须是独立的清理预算，不能复用已取消请求。
// cancel 是通知，Shutdown 等请求结束，Done 等 Serve 尾部完成，最后才 Close 仓储。
// 超时保留错误且不假装 closed；caller 放开屏障后可再次 Close 完成剩余所有权收尾。
func (a *App) Close(ctx context.Context) error {
	a.stopMu.Lock()
	defer a.stopMu.Unlock()
	if a.closed {
		return nil
	}
	a.handlers.Stop() // 与进入handler同锁，阻止停止后的新业务调用。
	a.cancel()
	a.mu.Lock()
	started := a.started
	a.mu.Unlock()
	if !started {
		err := a.listener.Close()
		a.closed = true
		return errors.Join(err, a.closeRepository())
	}
	err := a.server.Shutdown(ctx)
	if err != nil {
		// Shutdown 超时后 Close 断开本 App 的连接；仍保留原超时，绝不能把它变成成功。
		err = errors.Join(err, a.server.Close())
		return err // 活跃 handler 可能尚未返回，仓储此时不能提前关闭。
	}
	// 曾经强制Server.Close以后，Shutdown的连接表可能已空；仍必须等独立handler账本。
	if err := a.handlers.Wait(ctx); err != nil {
		return err
	}
	select {
	case <-a.done:
		a.mu.Lock()
		serveErr := a.serveErr
		a.mu.Unlock()
		a.closed = true
		return errors.Join(serveErr, a.closeRepository())
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
}

// closeRepository 只在不再有 handler/Serve 使用资源时调用；afterClose 注入额外清理原因测试。
func (a *App) closeRepository() error {
	err := a.repo.Close()
	if a.afterClose != nil {
		err = errors.Join(err, a.afterClose())
	}
	return err
}
func (a *App) URL() string                    { return "http://" + a.listener.Addr().String() }
func (a *App) Repository() *memory.Repository { return a.repo }
func (a *App) Result() <-chan error           { return a.result }
func (a *App) Done() <-chan struct{}          { return a.done }
func (a *App) ActiveHandlers() int            { return a.handlers.Active() }
