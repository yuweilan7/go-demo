package orderbackend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/twmb/franz-go/pkg/kgo"
	inv "go-demo/lessons/m31_grpc_protobuf/gen/inventoryv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Task由App在go之前登记Done。err仅工作goroutine写，caller在Done之后读；
// 入口defer recover保留panic为失败，最后close Done，不把预览Result当join。
type Task struct {
	Name string
	Done <-chan struct{}
	done chan struct{}
	err  error
}

func startTask(name string, work func() error) *Task {
	d := make(chan struct{})
	t := &Task{Name: name, Done: d, done: d}
	go func() {
		defer close(d)
		defer func() {
			if p := recover(); p != nil {
				if cause, ok := p.(error); ok {
					t.err = fmt.Errorf("%s panic: %w", name, cause)
				} else {
					t.err = fmt.Errorf("%s panic: %v", name, p)
				}
			}
		}()
		t.err = work()
	}()
	return t
}

// Join由App最终owner调用；fresh预算等真实Done以后才读任务err。
func (t *Task) Join() error {
	if e := JoinDone(t.Done, t.Name, 10*time.Second); e != nil {
		return e
	}
	return t.err
}

// admissions记录实际已接纳的handler。HTTP Close可能断TCP而函数仍活跃，gRPC
// Serve返回也不能替代业务handler退出。Stop与enter同锁，停后不会再借用仓储。
type admissions struct {
	mu       sync.Mutex
	stopping bool
	active   int
	idle     chan struct{}
}

// newAdmissions以已关闭idle表示零handler；还未Start的App也可立即收尾。
func newAdmissions() *admissions { d := make(chan struct{}); close(d); return &admissions{idle: d} }

// enter由HTTP/RPC入口调用；true才登记一次leave，停后拒绝借仓储。
func (h *admissions) enter() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopping {
		return false
	}
	if h.active == 0 {
		h.idle = make(chan struct{})
	}
	h.active++
	return true
}

// leave只由成功enter的handler defer调用，最后一个真实返回者关闭本轮idle。
func (h *admissions) leave() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active--
	if h.active == 0 {
		close(h.idle)
	}
}

// stop与enter同锁，建立“此后无新借用”的边界，不等待现有handler。
func (h *admissions) stop() { h.mu.Lock(); h.stopping = true; h.mu.Unlock() }

// wait由Close在stop后调用，固定当前idle；fresh预算避免沿用取消业务ctx。
func (h *admissions) wait() error {
	h.mu.Lock()
	d := h.idle
	h.mu.Unlock()
	return JoinDone(d, "accepted handlers", 10*time.Second)
}
func (h *admissions) count() int { h.mu.Lock(); defer h.mu.Unlock(); return h.active }

// Service是传输无关用例接线：共享Store/Cache指针，库存client是实际gRPC Conn。
// 请求ctx只用于这一次RPC+SQL；不会保存gin.Context，也不让请求取消后台relay。
type Service struct {
	Store     *Store
	Cache     *Cache
	Inventory inv.InventoryClient
	Hooks     Hooks
}

// Create由HTTP handler同步调用。Inspect只是当前观测，即便看见库存10，下一刻也
// 可能被别的请求扣走；所有库存决定仍由Store.Create同一MySQL Tx做。
func (s *Service) Create(ctx context.Context, c Command) (Order, error) {
	if e := c.Validate(); e != nil {
		return Order{}, e
	}
	reply, e := s.Inventory.Inspect(ctx, &inv.InspectRequest{Sku: c.Input.SKU})
	if e != nil {
		return Order{}, e
	}
	if reply.Sku != SKU || reply.Available < 0 {
		return Order{}, ErrInvalid
	}
	fmt.Fprintf(s.Store.Out, "RPC_HINT request=%s available=%d (not_a_lock)\n", c.RequestID, reply.Available)
	return s.Store.Create(ctx, c, s.Hooks)
}

// inventoryServer复用M31真实生成协议，但仅实现Inspect；嵌入Unimplemented使Reserve
// 和Watch明确返回Unimplemented。本课不存在RPC扣库存或跨服务事务。
type inventoryServer struct {
	inv.UnimplementedInventoryServer
	store *Store
}

func (s *inventoryServer) Inspect(ctx context.Context, request *inv.InspectRequest) (*inv.Stock, error) {
	if request.Sku != SKU {
		return nil, status.Error(codes.InvalidArgument, "unknown sku")
	}
	n, e := s.store.Stock(ctx)
	if e != nil {
		return nil, status.Error(codes.Unavailable, "inventory read failed")
	}
	return &inv.Stock{Sku: SKU, Available: int32(n), Sequence: 0}, nil
}

// Envelope只含安全错误码，不把SQL/driver错误正文、token或Config交给客户端。
// Outcome=UNKNOWN优先于其他映射；Order保留已知ID供原key权威对账。
type Envelope struct {
	Code      string `json:"code"`
	Outcome   string `json:"outcome"`
	RequestID string `json:"request_id"`
	Order     *Order `json:"order,omitempty"`
	View      *View  `json:"view,omitempty"`
	CacheHit  bool   `json:"cache_hit"`
}

// App拥有listener、Serve/relay/consumer任务和gRPC客户端；借用Resources。
// Start一次，Stop是通知，Done才是关闭工作最终退出；r.SQL最后由组合根关闭。
type App struct {
	Resources                 *Resources
	Service                   *Service
	Hooks                     Hooks
	URL, RPCAddress           string
	ctx                       context.Context
	cancel                    context.CancelFunc
	http                      *http.Server
	grpc                      *grpc.Server
	httpListener, rpcListener net.Listener
	rpcConn                   *grpc.ClientConn
	Consumer                  *KafkaClient
	httpOwner, rpcOwner       *admissions
	mu                        sync.Mutex
	started                   bool
	tasks                     []*Task
	closeOnce                 sync.Once
	done                      chan struct{}
	closeErr                  error
	safeToRelease             bool
}

// NewApp在启动goroutine前完成接线。listener使用OS动态loopback端口；任一步失败
// 都由defer关闭已取得资源，不误等待没启动的Serve。Resources仍归caller，错误时
// caller可以CleanupOwned/Close。Hooks是值副本，其闭包门由实验owner拥有。
func NewApp(r *Resources, h Hooks) (a *App, err error) {
	if r == nil || r.Store == nil || r.Cache == nil || r.Broker == nil {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithCancel(context.Background())
	a = &App{Resources: r, Hooks: h, ctx: ctx, cancel: cancel, httpOwner: newAdmissions(), rpcOwner: newAdmissions(), done: make(chan struct{})}
	transferred := false
	defer func() {
		if !transferred {
			clean := a.Close()
			if p := recover(); p != nil {
				cause, ok := p.(error)
				if !ok {
					cause = fmt.Errorf("App构造panic: %v", p)
				}
				panic(errors.Join(cause, clean))
			}
			err = errors.Join(err, clean)
			a = nil
		}
	}()
	if a.rpcListener, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		return a, err
	}
	a.RPCAddress = a.rpcListener.Addr().String()
	a.grpc = grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !a.rpcOwner.enter() {
			return nil, status.Error(codes.Unavailable, "stopping")
		}
		defer a.rpcOwner.leave()
		request, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(a.ctx, cancel)
		defer func() { stop(); cancel() }()
		return handler(request, req)
	}))
	inv.RegisterInventoryServer(a.grpc, &inventoryServer{store: r.Store})
	if a.rpcConn, err = grpc.NewClient(a.RPCAddress, grpc.WithTransportCredentials(insecure.NewCredentials())); err != nil {
		return a, err
	}
	a.Service = &Service{Store: r.Store, Cache: r.Cache, Inventory: inv.NewInventoryClient(a.rpcConn), Hooks: h}
	if a.Consumer, err = NewKafkaClient(r.Config, r.Namespace+"-consumer", ConsumerOptions(r.Broker.Topic, r.Broker.Group)...); err != nil {
		return a, err
	}
	if a.httpListener, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		return a, err
	}
	a.URL = "http://" + a.httpListener.Addr().String()
	engine := a.router()
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if !a.httpOwner.enter() {
			http.Error(w, "stopping", 503)
			return
		}
		defer a.httpOwner.leave()
		engine.ServeHTTP(w, request)
	})
	a.http = &http.Server{Handler: wrapped, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 3 * time.Second, MaxHeaderBytes: 4096, BaseContext: func(net.Listener) context.Context { return a.ctx }}
	transferred = true
	return a, nil
}

// router由NewApp调用一次。每个middleware创建requestID和可信owner；c.Next同步
// 跑后续handler，defer在返回时取消请求timer。Gin Context会被池复用，绝不逃逸。
func (a *App) router() *gin.Engine {
	engine := gin.New()
	_ = engine.SetTrustedProxies(nil)
	engine.Use(gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, value any) {
		cause, ok := value.(error)
		if !ok {
			cause = fmt.Errorf("handler panic: %v", value)
		}
		fmt.Fprintf(a.Resources.Out, "HANDLER_PANIC request=%s cleanup_failed=%t\n", c.GetString("request_id"), errors.Is(cause, ErrCleanup))
		respondError(c, Envelope{RequestID: c.GetString("request_id")}, errors.Join(ErrUnknown, cause))
	}))
	engine.Use(func(c *gin.Context) {
		id := a.Resources.NextRequestID()
		c.Set("request_id", id)
		owner := ""
		switch c.GetHeader("Authorization") {
		case "Bearer m37-alice":
			owner = "alice"
		case "Bearer m37-bob":
			owner = "bob"
		}
		if owner == "" {
			c.AbortWithStatusJSON(401, Envelope{Code: "AUTH", Outcome: "NOT_STARTED", RequestID: id})
			return
		}
		c.Set("owner", owner)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	engine.POST("/orders", func(c *gin.Context) {
		env := Envelope{RequestID: c.GetString("request_id")}
		if c.ContentType() != "application/json" {
			c.AbortWithStatusJSON(415, Envelope{Code: "CONTENT_TYPE", Outcome: "NOT_STARTED", RequestID: env.RequestID})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
		defer c.Request.Body.Close()
		raw, e := io.ReadAll(c.Request.Body)
		if e != nil {
			c.AbortWithStatusJSON(413, Envelope{Code: "INPUT", Outcome: "NOT_STARTED", RequestID: env.RequestID})
			return
		}
		input, e := DecodeInput(raw)
		if e != nil {
			c.AbortWithStatusJSON(400, Envelope{Code: "INPUT", Outcome: "NOT_STARTED", RequestID: env.RequestID})
			return
		}
		key := c.GetHeader("Idempotency-Key")
		if key == "" {
			c.AbortWithStatusJSON(400, Envelope{Code: "KEY_REQUIRED", Outcome: "NOT_STARTED", RequestID: env.RequestID})
			return
		}
		o, e := a.Service.Create(c.Request.Context(), Command{Owner: c.GetString("owner"), Key: key, RequestID: env.RequestID, Input: input})
		if o.ID != "" {
			env.Order = &o
		}
		if e != nil {
			respondError(c, env, e)
			return
		}
		env.Code, env.Outcome = "OK", "COMMITTED"
		c.JSON(201, env)
	})
	engine.GET("/orders/:id", func(c *gin.Context) {
		env := Envelope{RequestID: c.GetString("request_id")}
		id := c.Param("id")
		if !idPattern.MatchString(id) {
			respondError(c, env, ErrInvalid)
			return
		}
		v, hit, e := a.Resources.Cache.Query(c.Request.Context(), a.Resources.Store, c.GetString("owner"), id, a.Hooks)
		if e != nil {
			respondError(c, env, e)
			return
		}
		env.Code, env.Outcome, env.View, env.CacheHit = "OK", "READ", &v, hit
		c.JSON(200, env)
	})
	// 查询原key直接MySQL，绕过缓存。它只读，不能根据found=false盲目重复写。
	engine.GET("/requests/:key", func(c *gin.Context) {
		env := Envelope{RequestID: c.GetString("request_id")}
		key := c.Param("key")
		if !keyPattern.MatchString(key) {
			respondError(c, env, ErrInvalid)
			return
		}
		o, found, e := a.Resources.Store.FindByKey(c.Request.Context(), c.GetString("owner"), key)
		if e != nil {
			respondError(c, env, e)
			return
		}
		if !found {
			respondError(c, env, ErrNotFound)
			return
		}
		env.Code, env.Outcome, env.Order = "OK", "COMMITTED", &o
		c.JSON(200, env)
	})
	return engine
}

// respondError先检查严重状态再检查普通冲突/输入。cleanup不能被1062映射成409后
// 丢失语义；ctx失败可能发生在已提交响应阶段，所以保守给UNKNOWN，原key对账。
func respondError(c *gin.Context, env Envelope, err error) {
	code, word, outcome := 503, "DEPENDENCY", "NOT_COMMITTED"
	switch {
	case errors.Is(err, ErrCleanup):
		word, outcome = "CLEANUP_FAILED", "UNKNOWN"
	case errors.Is(err, ErrUnknown):
		word, outcome = "UNKNOWN", "UNKNOWN"
	case errors.Is(err, context.DeadlineExceeded):
		code, word, outcome = 504, "BUDGET", "UNKNOWN"
	case errors.Is(err, context.Canceled):
		code, word, outcome = 499, "CANCELED", "UNKNOWN"
	case errors.Is(err, ErrConflict):
		code, word = 409, "CONFLICT"
	case errors.Is(err, ErrStock):
		code, word = 409, "STOCK"
	case errors.Is(err, ErrNotFound):
		code, word = 404, "NOT_FOUND"
	case errors.Is(err, ErrInvalid):
		code, word = 422, "ORDER_INVALID"
	}
	env.Code, env.Outcome = word, outcome
	c.AbortWithStatusJSON(code, env)
}

// Start由实验owner调用一次，在返回前保存每个Task。Serve任务等待网络请求，relay
// 等ticker/SQL/ACK，consumer等真实Poll或显式门；请求结束不会取消这两个后台任务。
func (a *App) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started || a.ctx.Err() != nil {
		return ErrInvalid
	}
	a.started = true
	a.tasks = append(a.tasks, startTask("http.Serve", func() error {
		e := a.http.Serve(a.httpListener)
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	}))
	a.tasks = append(a.tasks, startTask("grpc.Serve", func() error {
		e := a.grpc.Serve(a.rpcListener)
		if errors.Is(e, grpc.ErrServerStopped) {
			return nil
		}
		return e
	}))
	a.tasks = append(a.tasks, startTask("relay", func() error {
		t := time.NewTicker(30 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return nil
			case <-t.C:
				_, e := a.Resources.Broker.RelayOne(a.ctx, a.Resources.Store, a.Hooks)
				if e != nil {
					if a.ctx.Err() != nil && e == context.Canceled {
						return nil
					}
					return e
				}
			}
		}
	}))
	a.tasks = append(a.tasks, startTask("consumer", func() error {
		if a.Hooks.BeforeConsume != nil {
			if e := a.Hooks.BeforeConsume(a.ctx); e != nil {
				if e == context.Canceled {
					return nil
				}
				return e
			}
		}
		for {
			f := a.Consumer.K.PollRecords(a.ctx, 1)
			if a.ctx.Err() != nil {
				return nil
			}
			if es := f.Errors(); len(es) > 0 {
				return es[0].Err
			}
			for _, r := range f.Records() {
				if e := a.Resources.Broker.ProcessRecord(a.ctx, a.Consumer, a.Resources.Store, a.Resources.Cache, r, a.Hooks); e != nil {
					if a.ctx.Err() != nil && e == context.Canceled {
						return nil
					}
					return e
				}
			}
			a.Consumer.K.AllowRebalance()
		}
	}))
	return nil
}

// Stop仅关闭接纳和发取消通知；调用者必须另等Close/Done，不能据此关闭Resources。
func (a *App) Stop() { a.httpOwner.stop(); a.rpcOwner.stop(); a.cancel() }

// Close启动一次最终收尾任务。使用新Background预算Shutdown，必要时强制断HTTP，
// 仍独立等已接纳handler真正返回；gRPC Stop/Serve结果同样不代替rpcOwner.wait。
// 任务/handlerjoin完成后才关App拥有的consumer和Conn，仓储始终留给外层owner。
func (a *App) Close() error {
	a.closeOnce.Do(func() {
		go func() {
			defer close(a.done)
			a.Stop()
			a.mu.Lock()
			started := a.started
			tasks := append([]*Task(nil), a.tasks...)
			a.mu.Unlock()
			if started {
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				if e := a.http.Shutdown(ctx); e != nil {
					a.closeErr = errors.Join(a.closeErr, CleanupCause(e), a.http.Close())
				}
				cancel()
				graceful := startTask("grpc.GracefulStop", func() error { a.grpc.GracefulStop(); return nil })
				ctx, cancel = context.WithTimeout(context.Background(), 4*time.Second)
				select {
				case <-graceful.Done:
				case <-ctx.Done():
					a.grpc.Stop()
				}
				cancel()
				a.closeErr = errors.Join(a.closeErr, graceful.Join())
			} else {
				if a.httpListener != nil {
					a.closeErr = errors.Join(a.closeErr, a.httpListener.Close())
				}
				if a.rpcListener != nil {
					a.closeErr = errors.Join(a.closeErr, a.rpcListener.Close())
				}
			}
			joined := errors.Join(a.httpOwner.wait(), a.rpcOwner.wait())
			for _, task := range tasks {
				e := task.Join()
				a.closeErr = errors.Join(a.closeErr, e)
				if errors.Is(e, ErrCleanup) {
					joined = errors.Join(joined, e)
				}
			}
			a.closeErr = errors.Join(a.closeErr, joined)
			if joined == nil {
				if a.Consumer != nil {
					a.closeErr = errors.Join(a.closeErr, a.Consumer.Close())
				}
				if a.rpcConn != nil {
					a.closeErr = errors.Join(a.closeErr, a.rpcConn.Close())
				}
				a.safeToRelease = true
			}
			fmt.Fprintf(a.Resources.Out, "APP_DONE started=%t http_active=%d rpc_active=%d tasks=%d safe_release=%t\n", started, a.httpOwner.count(), a.rpcOwner.count(), len(tasks), a.safeToRelease)
		}()
	})
	if e := JoinDone(a.done, "App.Close", 35*time.Second); e != nil {
		return e
	}
	return a.closeErr
}

// Done只在最终关闭任务返回时关闭；Stop通知和Serve结果均不能替代它。
func (a *App) Done() <-chan struct{} { return a.done }

// SafeToRelease由Resources唯一owner检查；Done后才读状态，避免读未完成写入。
func (a *App) SafeToRelease() bool {
	select {
	case <-a.done:
		return a.safeToRelease
	default:
		return false
	}
}
func (a *App) ActiveHandlers() (int, int) { return a.httpOwner.count(), a.rpcOwner.count() }

// BackgroundError只读取已经Done的任务；观察者可及时发现真实relay/consumer失败，
// 不从日志猜运行状态，不在任务尚活跃时读取它的err造成race。
func (a *App) BackgroundError() error {
	a.mu.Lock()
	tasks := append([]*Task(nil), a.tasks...)
	a.mu.Unlock()
	var err error
	for _, t := range tasks {
		select {
		case <-t.Done:
			err = errors.Join(err, t.err)
		default:
		}
	}
	return err
}

// InventoryClient供独立只读协议断言借用。Conn仍属于App，caller不能Close它。
func (a *App) InventoryClient() inv.InventoryClient { return inv.NewInventoryClient(a.rpcConn) }

// PollOne是M38可以复用的有界网络读取，不拥有client，调用者负责Close/进度确认。
func PollOne(ctx context.Context, c *KafkaClient) (*kgo.Record, error) {
	for {
		f := c.K.PollRecords(ctx, 1)
		if es := f.Errors(); len(es) > 0 {
			return nil, es[0].Err
		}
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		if rs := f.Records(); len(rs) > 0 {
			return rs[0], nil
		}
		c.K.AllowRebalance()
	}
}
