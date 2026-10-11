package review

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
	inv "go-demo/lessons/m31_grpc_protobuf/gen/inventoryv1"
	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// admission保护“允许借用仓储”与最后idle。Mutex保护计数，不跨网络/SQL等待持锁。
// seal与enter同一把锁，所以seal以后不会新增借用；已入站的函数仍需leave。
type admission struct {
	mu     sync.Mutex
	sealed bool
	active int
	idle   chan struct{}
}

func newAdmission() *admission { d := make(chan struct{}); close(d); return &admission{idle: d} }

// enter由网络handler入口同步调用，true才获得一次leave责任；临界区仅改变账本。
func (a *admission) enter() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sealed {
		return false
	}
	if a.active == 0 {
		a.idle = make(chan struct{})
	}
	a.active++
	return true
}

// leave是成功enter后的defer，最后真实函数退出者发布idle；HTTP Body完成不是此信号。
func (a *admission) leave() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active--
	if a.active == 0 {
		close(a.idle)
	}
}
func (a *admission) seal() { a.mu.Lock(); a.sealed = true; a.mu.Unlock() }

// wait只在seal后由close调用。取idle指针快照后放锁，再等待，不堵住handler的leave。
func (a *admission) wait() error {
	a.mu.Lock()
	d := a.idle
	a.mu.Unlock()
	return b.JoinDone(d, "HTTP/RPC handlers", 10*time.Second)
}

// gateway是新切片的组合根。Store/Cache来自session共享指针，本对象只借用；
// 自己拥有listeners、网络Serve任务、grpc.Conn及所有异步任务，close以后才准删数据。
type gateway struct {
	r                 *b.Resources
	service           *b.Service
	ctx               context.Context
	cancel            context.CancelFunc
	url               string
	http              *http.Server
	grpc              *grpc.Server
	conn              *grpc.ClientConn
	listeners         []net.Listener
	serves            []*task
	owner             *admission
	mu                sync.Mutex
	jobs              []*task
	badGate, goodGate *gate
	badCanceled       chan struct{}
	safe              bool
	started           bool
}

// inventoryRead复用已验M31生成协议，只实现Inspect。它读真实SQL；观察数量不是锁。
// pointer接收者借Store，不创建业务状态；Reserve/Watch由嵌入类型明确Unimplemented。
type inventoryRead struct {
	inv.UnimplementedInventoryServer
	store *b.Store
}

func (r *inventoryRead) Inspect(ctx context.Context, q *inv.InspectRequest) (*inv.Stock, error) {
	if q.Sku != b.SKU {
		return nil, status.Error(codes.InvalidArgument, "unknown sku")
	}
	n, e := r.store.Stock(ctx)
	if e != nil {
		return nil, status.Error(codes.Unavailable, "inventory read failed")
	}
	return &inv.Stock{Sku: b.SKU, Available: int32(n)}, nil
}

// newGateway在任何goroutine之前完成接线，错误/panic由defer关闭已取得资源。
// Hooks是值副本；其中闭包共享实验门，HTTP输入不能选故障或替换这些hook。
func newGateway(r *b.Resources, h b.Hooks) (g *gateway, err error) {
	ctx, cancel := context.WithCancel(context.Background())
	g = &gateway{r: r, ctx: ctx, cancel: cancel, owner: newAdmission(), badCanceled: make(chan struct{})}
	ok := false
	defer func() {
		if !ok {
			if p := recover(); p != nil {
				err = errors.Join(err, panicError("网关构造", p))
			}
			err = errors.Join(err, g.close())
			g = nil
		}
	}()
	hl, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return g, e
	}
	g.listeners = append(g.listeners, hl)
	rl, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return g, e
	}
	g.listeners = append(g.listeners, rl)
	g.url = "http://" + hl.Addr().String()
	g.grpc = grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		if !g.owner.enter() {
			return nil, status.Error(codes.Unavailable, "stopping")
		}
		defer g.owner.leave()
		return next(ctx, req)
	}))
	inv.RegisterInventoryServer(g.grpc, &inventoryRead{store: r.Store})
	g.conn, e = grpc.NewClient(rl.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithNoProxy())
	if e != nil {
		return g, e
	}
	g.service = &b.Service{Store: r.Store, Cache: r.Cache, Inventory: inv.NewInventoryClient(g.conn), Hooks: h}
	router := g.router()
	g.http = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if !g.owner.enter() {
			http.Error(w, "stopping", 503)
			return
		}
		defer g.owner.leave() // Gin归还Context之前/之后的实际外层handler都在此账本里。
		router.ServeHTTP(w, q)
	}), BaseContext: func(net.Listener) context.Context { return g.ctx }, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: time.Second}
	ok = true
	return g, nil
}

// start只有实验组合根调用一次；先记录每个task再允许网络caller请求。
func (g *gateway) start() {
	g.started = true
	g.serves = append(g.serves, startTask("Gin HTTP Serve", func() error {
		e := g.http.Serve(g.listeners[0])
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	}))
	g.serves = append(g.serves, startTask("generated gRPC Serve", func() error {
		e := g.grpc.Serve(g.listeners[1])
		if errors.Is(e, grpc.ErrServerStopped) {
			return nil
		}
		return e
	}))
}

// startJob由handler在go以前登记。command仅字符串/int值，复制不共享可写map/slice；
// 捕获*gin.Context会共享其被池复用的对象，因此这里从来不把它传给work。
func (g *gateway) startJob(name string, work func() error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.jobs = append(g.jobs, startTask(name, work))
}

// job让串行驱动在HTTP202返回后取得已登记任务。返回共享句柄，err仍须done之后读。
func (g *gateway) job(name string) *task {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, t := range g.jobs {
		if t.name == name {
			return t
		}
	}
	return nil
}

// router只装配本课新业务入口。认证身份来自header映射，不接受body中的owner。
// CustomRecovery在同goroutine恢复业务panic；不能捕获另一个goroutine的panic。
func (g *gateway) router() *gin.Engine {
	e := gin.New()
	if err := e.SetTrustedProxies(nil); err != nil {
		panic(err)
	}
	e.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, p any) {
		fmt.Fprintf(g.r.Out, "GATEWAY_RECOVER cause_is_cleanup=%t\n", errors.Is(panicError("handler", p), b.ErrCleanup))
		c.AbortWithStatusJSON(500, b.Envelope{Code: "PANIC", Outcome: "UNKNOWN"})
	}))
	e.Use(func(c *gin.Context) {
		owner := ""
		switch c.GetHeader("Authorization") {
		case "Bearer m39-alice":
			owner = "alice"
		case "Bearer m39-bob":
			owner = "bob"
		}
		if owner == "" {
			c.AbortWithStatusJSON(401, b.Envelope{Code: "AUTH", Outcome: "NOT_STARTED"})
			return
		}
		c.Set("owner", owner)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 4*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next() // 同goroutine同步执行后续handler，返回时cancel。
	})
	e.POST("/orders", func(c *gin.Context) {
		command, err := g.decode(c)
		if err != nil {
			writeError(c, err)
			return
		}
		o, err := g.service.Create(c.Request.Context(), command)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(201, b.Envelope{Code: "OK", Outcome: "COMMITTED", Order: &o})
	})
	for _, route := range []struct {
		path string
		bad  bool
	}{{"/candidate/status/:key", true}, {"/status/:key", false}} {
		// Go1.27每轮range变量独立；handler闭包保存这一轮route值，不在请求期间改它。
		e.GET(route.path, func(c *gin.Context) {
			v, hit, err := lookupStatus(c.Request.Context(), g.r, c.GetString("owner"), c.Param("key"), route.bad)
			if err != nil {
				writeError(c, err)
				return
			}
			c.JSON(200, b.Envelope{Code: "OK", Outcome: "READ", View: &v, CacheHit: hit})
		})
	}
	for _, mode := range []string{"bad", "fixed"} {
		e.POST("/async/"+mode, func(c *gin.Context) {
			command, err := g.decode(c)
			if err != nil {
				writeError(c, err)
				return
			}
			requestCtx := c.Request.Context() // interface值复制仍引用同一取消树；不保存gin.Context。
			// 后台任务以网关owner为父，但每项再有10秒上限。cancel由该任务的defer
			// 拥有，普通返回/error/panic都释放timer；root.stop也能更早通知它。
			workCtx, workCancel := context.WithTimeout(g.ctx, 10*time.Second)
			g.startJob(mode, func() error {
				defer workCancel()
				if mode == "bad" {
					// 真handler返回使middleware defer cancel生效；先观察真实Done才报告。
					if err := await(workCtx, requestCtx.Done(), "请求ctx实际取消"); err != nil {
						return err
					}
					close(g.badCanceled)
					if err := g.badGate.wait(workCtx); err != nil {
						return err
					}
					_, err := g.service.Create(requestCtx, command)
					return err
				}
				// 修复有明确后台owner及stop/join，预算来自owner；202不是持久队列保证。
				if err := g.goodGate.wait(workCtx); err != nil {
					return err
				}
				_, err := g.service.Create(workCtx, command)
				return err
			})
			c.JSON(202, b.Envelope{Code: "ACCEPTED_IN_MEMORY", Outcome: "PENDING"})
		})
	}
	return e
}

// decode由POST handler同步调用，读取真实Body有1024上限并立即defer Close。
// strict DecodeInput复用M37协议；key/owner/requestID各自来自header/认证/组合根。
func (g *gateway) decode(c *gin.Context) (b.Command, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	defer c.Request.Body.Close()
	if c.ContentType() != "application/json" {
		return b.Command{}, b.ErrInvalid
	}
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return b.Command{}, err
	}
	input, err := b.DecodeInput(raw)
	if err != nil {
		return b.Command{}, err
	}
	command := b.Command{Owner: c.GetString("owner"), Key: c.GetHeader("Idempotency-Key"), RequestID: g.r.NextRequestID(), Input: input}
	return command, command.Validate()
}

// lookupStatus是本PR唯一业务修复位置，handler已把认证owner交进来。
// candidateBad仅显式反例/失败回归选择；默认业务route始终false。
func lookupStatus(ctx context.Context, r *b.Resources, owner, key string, candidateBad bool) (b.View, bool, error) {
	var o b.Order
	q := r.Store.DB.WithContext(ctx).Table(r.Store.Table("orders")).Where("request_key=?", key)
	if !candidateBad {
		q = q.Where("owner=?", owner)
	} // PR①：授权条件进入实际SQL，不只在HTTP后验。
	// 坏例把request key错当全局ID；Order仅固定复现Alice排前，不靠DB偶然返回顺序。
	q = q.Order("owner ASC").Take(&o)
	if q.Error != nil {
		return b.View{}, false, q.Error
	}
	cacheOwner := o.Owner // BAD：凭查询结果选择cache身份，认证owner没有约束这条链。
	if !candidateBad {
		cacheOwner = owner
	} // PR②：认证范围继续传给Cache.Query及真实Redis键。
	return r.Cache.Query(ctx, r.Store, cacheOwner, o.ID, b.Hooks{})
}

// writeError只输出安全协议分类，不输出驱动/SQL/配置正文。UNKNOWN优先于Canceled；
// 此函数编码结果不消费原错误，实验另以errors.Is+SQL事实核对。
func writeError(c *gin.Context, err error) {
	code, word, outcome := 503, "DEPENDENCY", "NOT_COMMITTED"
	switch {
	case errors.Is(err, b.ErrUnknown) || errors.Is(err, b.ErrCleanup):
		word, outcome = "UNKNOWN", "UNKNOWN"
	case errors.Is(err, context.Canceled):
		code, word, outcome = 499, "CANCELED", "UNKNOWN"
	case errors.Is(err, b.ErrInvalid):
		code, word, outcome = 422, "INVALID", "NOT_STARTED"
	case errors.Is(err, b.ErrConflict):
		code, word = 409, "CONFLICT"
	case errors.Is(err, b.ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound):
		code, word = 404, "NOT_FOUND"
	}
	c.AbortWithStatusJSON(code, b.Envelope{Code: word, Outcome: outcome})
}

// stop只封入站并发取消通知，没有宣称Serve/业务退出。
func (g *gateway) stop() { g.owner.seal(); g.cancel() }

// close在调用者已经放开自有门后执行，独立预算停止网络，强制关闭也仍等handler。
// Jobs未消费error保留；只有所有Done/连接关闭成功才发布safe，session才会清对象。
func (g *gateway) close() (err error) {
	g.stop()
	safe := true
	if g.started {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		e := g.http.Shutdown(ctx)
		cancel()
		if e != nil {
			err = errors.Join(err, b.CleanupCause(e), g.http.Close())
		}
		g.grpc.Stop() // grpc网络取消后，下面仍等真实handler账本，不用Serve返回冒充。
	} else {
		for _, ln := range g.listeners {
			err = errors.Join(err, ln.Close())
		}
	}
	handlerJoinErr := g.owner.wait()
	err = errors.Join(err, handlerJoinErr)
	safe = safe && handlerJoinErr == nil
	var e error
	for _, t := range g.serves {
		e = t.join()
		err = errors.Join(err, e)
		safe = safe && !errors.Is(e, b.ErrCleanup)
	}
	g.mu.Lock()
	jobs := append([]*task(nil), g.jobs...)
	g.mu.Unlock()
	for _, t := range jobs {
		e = t.join()
		if errors.Is(e, b.ErrCleanup) {
			safe = false
			err = errors.Join(err, e)
		} else if !t.consumed {
			err = errors.Join(err, e)
		}
	}
	if safe && g.conn != nil {
		e = g.conn.Close()
		err = errors.Join(err, e)
		safe = safe && e == nil
	}
	g.safe = safe
	fmt.Fprintf(g.r.Out, "GATEWAY_DONE serves=%d jobs=%d handlers_idle=%t safe=%t\n", len(g.serves), len(jobs), handlerJoinErr == nil, safe)
	return err
}
