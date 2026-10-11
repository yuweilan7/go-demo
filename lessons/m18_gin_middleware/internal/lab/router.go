package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// router 由 NewRig 调用一次，注册完成以后再启动 HTTP server；不在请求期间改路由。
// HandlerFunc 是 func(*gin.Context) 的函数类型，和 net/http.HandlerFunc 的签名不同；
// engine 实现 ServeHTTP，故能直接赋给 http.Server.Handler，协议层仍由 net/http 拥有。
func (r *Rig) router(trusted []string) (*gin.Engine, error) {
	engine := gin.New() // 不隐式安装会输出敏感 URL 的默认 Logger。
	if err := engine.SetTrustedProxies(trusted); err != nil {
		return nil, err
	}
	engine.HandleMethodNotAllowed = true
	// A 在 Recovery 外层，所以 panic 被 Recovery 转成响应后，A.after 仍能记录最终状态。
	// Recovery 只包住本请求 goroutine 的 c.Next。nil writer 禁止 Gin 输出原请求/任意 panic 值。
	engine.Use(r.onion("A"), gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, _ any) {
		if c.Writer.Written() {
			// 已发出的状态/body 无法撤回；只停止后续链，不能承诺改成500或回滚订单。
			c.Abort()
			return
		}
		reject(c, http.StatusInternalServerError, "PANIC")
	}))
	engine.NoRoute(func(c *gin.Context) { reject(c, http.StatusNotFound, "NOT_FOUND") })
	engine.NoMethod(func(c *gin.Context) { reject(c, http.StatusMethodNotAllowed, "METHOD") })
	// Group 在注册阶段合并全局 handlers 与 B/auth，最后 POST 合并 createOrder。
	// 它不是运行时另起 goroutine，也不隐含事务/幂等/订单归属检查。
	api := engine.Group("/api", r.onion("B"), authentication)
	api.POST("/orders", r.createOrder)
	api.GET("/orders/:id", r.getOrder)
	api.POST("/slow-orders", r.slowOrder)
	api.GET("/identity", func(c *gin.Context) { c.JSON(200, Envelope{Code: "OK", User: c.GetString("user"), Peer: c.ClientIP()}) })
	api.GET("/panic", func(c *gin.Context) { r.trace(c.GetHeader("X-Run"), "panic.before-write"); panic("synthetic panic") })
	api.POST("/panic-after", func(c *gin.Context) {
		// Recovery 不撤销外部/内存提交。此反例先写账本再 panic，测试直接检查 rows=1。
		_, _ = r.Ledger.Commit("alice", "panic-effect", Order{SKU: "sku-m17", Qty: 1})
		panic("panic after submission")
	})
	api.GET("/lifecycle/:mode", r.lifecycle)
	api.GET("/pool", r.poolProbe)
	engine.POST("/fault/abort-bad", r.abortBad, func(c *gin.Context) { r.trace(c.GetHeader("X-Run"), "H.unexpected") })
	engine.POST("/fault/abort-fixed", authentication, r.createOrder)
	engine.POST("/fault/bind-bad", r.bindBad)
	engine.POST("/fault/validation-bad", r.validationBad)
	engine.POST("/fault/body-bad", r.preview(false), r.bodyAgain(false))
	engine.POST("/fault/body-fixed", authentication, r.preview(true), r.bodyAgain(true))
	return engine, nil
}

// onion 由 router 在注册时创建闭包；label 是该 middleware 的值，不会随请求变化。
// Gin 在请求 goroutine 调用返回的函数，c 是本次池化对象的临时借用；绝不存到后台任务。
func (r *Rig) onion(label string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Run")
		if label == "A" {
			// X-Run只用于实验屏障索引，不把用户给的标识写入审计；日志ID由服务器生成。
			c.Set("request-id", strconv.FormatInt(r.requests.Add(1), 10))
		}
		r.trace(id, label+".before")
		c.Next() // 同步进入剩余 handlers；下层返回以后，本函数才继续执行 after。
		r.trace(id, label+".after")
		if label == "A" {
			audit := Audit{Request: c.GetString("request-id"), Method: c.Request.Method, Route: c.FullPath(), User: c.GetString("user"), Peer: c.ClientIP(), Status: c.Writer.Status(), Code: c.GetString("result-code")}
			r.mu.Lock()
			r.audits[id] = audit
			r.mu.Unlock()
			select {
			case r.changed <- struct{}{}:
			default:
			}
		}
	}
}

// authentication 由已注册路由链调用。header 只在当前请求中检查，不写入日志；
// c.Set 保存可信身份 string 值。拒绝后必须 return，Abort 不能替代 Go 函数返回。
func authentication(c *gin.Context) {
	user := identity(c.GetHeader("Authorization"))
	if user == "" {
		reject(c, http.StatusUnauthorized, "AUTH")
		return
	}
	c.Set("user", user)
	c.Next()
}

// createOrder 是正常 POST handler，由 Gin 链同步调用。bindOrder 的 false 已写错误响应；
// caller return 防止重复响应/副作用。业务 key 校验后才进入唯一实际写入点 commitReply。
func (r *Rig) createOrder(c *gin.Context) {
	r.trace(c.GetHeader("X-Run"), "H")
	order, ok := bindOrder(c)
	if !ok {
		return
	}
	key := c.GetHeader("Idempotency-Key")
	if key == "" {
		reject(c, 400, "KEY_REQUIRED")
		return
	}
	r.commitReply(c, order, key)
}

// getOrder 把 URL 字符串 ID 变成 int，然后同时核对存在性与当前身份归属。
// c.Param("id") 是此次路由参数；不能把 X-User 的自报身份代替认证后的 user。
func (r *Rig) getOrder(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		reject(c, 400, "INPUT")
		return
	}
	order, err := r.Ledger.Lookup(c.GetString("user"), id)
	switch {
	case errors.Is(err, ErrForbidden):
		reject(c, 403, "FORBIDDEN")
	case errors.Is(err, ErrNotFound):
		reject(c, 404, "NOT_FOUND")
	case err != nil:
		reject(c, 500, "INTERNAL")
	default:
		c.JSON(200, Envelope{Code: "OK", Committed: true, OrderID: order.ID})
	}
}

// abortBad 是显式危险入口：Abort 已让后面的 H 不会执行，但当前函数还在运行。
// 先发401，再非法提交并追加第二个JSON。账本 rows 与两个 JSON 对象证明问题，不能仅看401。
func (r *Rig) abortBad(c *gin.Context) {
	if identity(c.GetHeader("Authorization")) == "" {
		reject(c, 401, "AUTH")
		// 故意缺少 return；修复在 authentication 中，直接交付而非留练习。
	}
	id, _ := r.Ledger.Commit("unauthorized", "abort-bad", Order{SKU: "sku-m17", Qty: 1})
	r.trace(c.GetHeader("X-Run"), "bad.current-handler-write")
	c.JSON(201, Envelope{Code: "OK", Committed: true, OrderID: id})
}

// bindBad 是另一显式错误入口。Bind 自动在失败时写400并 Abort，但返回的 error
// 仍交给当前函数。这里企图改422、忽略失败继续提交，又写201，结果仍400并拼接JSON。
func (r *Rig) bindBad(c *gin.Context) {
	var order Order
	if err := c.Bind(&order); err != nil {
		c.JSON(422, Envelope{Code: "INPUT"})
		// 故意不 return；bindOrder 的 ShouldBind + reject + return 是完整修复。
	}
	id, _ := r.Ledger.Commit("bad-bind", "bad", Order{SKU: "sku-m17", Qty: 1})
	c.JSON(201, Envelope{Code: "OK", Committed: true, OrderID: id})
}

// validationBad 对 qty=-1 直接 ShouldBindJSON，required 会成功，因为 -1 非零。
// 本函数故意绕过业务范围检查；修复由 bindOrder 在绑定成功以后独立校验1..3。
func (r *Rig) validationBad(c *gin.Context) {
	var order Order
	if err := c.ShouldBindJSON(&order); err != nil {
		reject(c, 400, "INPUT")
		return
	}
	id, _ := r.Ledger.Commit("bad-validation", "bad", order)
	c.JSON(201, Envelope{Code: "OK", Committed: true, OrderID: id})
}

// preview 由 body 路由先执行，用来演示中间件读 body 的真实消耗。
// cache=true 用 ShouldBindBodyWithJSON 缓存受限字节；即使缓存，也不会自动完成业务校验。
func (r *Rig) preview(cache bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.ContentType() != "application/json" {
			reject(c, 415, "CONTENT_TYPE")
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256)
		defer c.Request.Body.Close()
		var order Order
		var err error
		if cache {
			err = c.ShouldBindBodyWithJSON(&order)
		} else {
			err = c.ShouldBindJSON(&order)
		}
		if err != nil {
			var large *http.MaxBytesError
			if errors.As(err, &large) {
				reject(c, 413, "INPUT")
			} else {
				reject(c, 400, "INPUT")
			}
			return
		}
		r.trace(c.GetHeader("X-Run"), "preview.read")
		c.Next()
	}
}

// bodyAgain 由 preview 的 c.Next 调用。坏例子再读真实 body 得EOF；修复从 Gin 保存的
// 有限 byte slice 再绑定。它的输入仅用于本课同body两次读取实验，正常API用bindOrder严格契约。
func (r *Rig) bodyAgain(cache bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var order Order
		var err error
		if cache {
			err = c.ShouldBindBodyWithJSON(&order)
		} else {
			err = c.ShouldBindJSON(&order)
		}
		if err != nil {
			reject(c, 400, "BODY_ALREADY_READ")
			return
		}
		if cache {
			// 缓存只解决读取问题，仍保留严格JSON/业务key规则，避免“修复版”放松M17契约。
			cached, _ := c.Get(gin.BodyBytesKey)
			data, ok := cached.([]byte) // any的动态值必须确认为bytes，不能假定缓存key必定存在。
			if !ok || strictOrderJSON(data) != nil {
				reject(c, 400, "INPUT")
				return
			}
		}
		if order.SKU != "sku-m17" || order.Qty < 1 || order.Qty > 3 {
			reject(c, 422, "ORDER_INVALID")
			return
		}
		key := c.GetHeader("Idempotency-Key")
		if key == "" {
			reject(c, 400, "KEY_REQUIRED")
			return
		}
		r.commitReply(c, order, key)
	}
}

// slowOrder 从 Gin 当前请求拿 context.Context，传到真实 HTTP 依赖，再决定订单写入。
// mode=detached 是反例：用 Rig root 截断了请求取消链且没有提交前检查，客户端取消后仍写。
// mode=gin-context直接把默认Gin对象当context传入，Done=nil使入站取消不能传播，但提交前检查
// 仍阻止订单写入。mode=timeout用150ms预算；mode=forward传原请求取消。timer只表示预算。
func (r *Rig) slowOrder(c *gin.Context) {
	order, ok := bindOrder(c)
	if !ok {
		return
	}
	key := c.GetHeader("Idempotency-Key")
	if key == "" {
		reject(c, 400, "KEY_REQUIRED")
		return
	}
	g := r.gate(c.Query("gate"))
	if g == nil {
		reject(c, 400, "GATE_REQUIRED")
		return
	}
	g.inbound = c.Request.Context()
	g.AppEntered.Open()
	defer g.AppDone.Open()
	mode := c.Query("mode")
	parent := c.Request.Context()
	if mode == "detached" {
		parent = r.root
	}
	if mode == "gin-context" {
		// 编译能通过，因为*gin.Context满足context.Context接口；默认engine关闭fallback，
		// 所以parent.Done()==nil，不会随本次HTTP请求结束而通知依赖。c仍只在当前handler内使用。
		parent = c
	}
	budget := 2 * time.Second
	if mode == "timeout" {
		budget = 150 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	err := r.reserve(ctx, c.Query("gate"))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			reject(c, 504, "DEPENDENCY_TIMEOUT")
		} else {
			reject(c, 499, "CANCELED")
		}
		return
	}
	if mode != "detached" && c.Request.Context().Err() != nil {
		// select 同时就绪可能选成功，补查覆盖已经可见的取消；检查与外部提交并非原子。
		reject(c, 499, "CANCELED")
		return
	}
	r.commitReply(c, order, key)
}

// reserve 由 slowOrder 同步调用；实际阻塞点是 Client.Do/Body.Read，ctx 就放在出站request。
// 本函数拥有成功得到的响应体，所有返回分支 Close；状态与 allowed JSON 都校验才返回nil。
func (r *Rig) reserve(ctx context.Context, gate string) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.Dependency.URL+"/reserve?gate="+gate, nil)
	if err != nil {
		return err
	}
	resp, err := r.depClient.Do(req)
	if err != nil {
		return fmt.Errorf("真实依赖 HTTP: %w", err)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 257))
	if err != nil {
		return fmt.Errorf("依赖 body: %w", err)
	}
	var reply struct {
		Allowed bool `json:"allowed"`
	}
	if len(data) > 256 || resp.StatusCode != 200 {
		return errors.New("依赖响应状态/大小错误")
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return err
	}
	if !reply.Allowed {
		return errors.New("依赖未允许")
	}
	return nil
}

// lifecycle 由真实GET调用，先在当前handler里检查Copy，再把不可变Snapshot交给显式Job。
// copy.Request==c.Request；cp.Writer没有底层writer；Keys只是map结构复制，指针值仍共享。
// 这些bool现在提取，worker不会在请求结束以后读取原c或cp，更不会写已结束的response。
func (r *Rig) lifecycle(c *gin.Context) {
	name := c.GetHeader("X-Run")
	marker := &struct{ Name string }{Name: "before"}
	c.Set("marker", marker)
	cp := c.Copy()
	marker.Name = "after"
	value, _ := cp.Get("marker")
	underlying := cp.Writer.(interface{ Unwrap() http.ResponseWriter }).Unwrap()
	life := &Life{Started: newSignal(), RequestCanceled: newSignal(), Release: newSignal(), Cleanup: newSignal(), Snapshot: Snapshot{User: c.GetString("user"), Request: c.GetString("request-id"), Route: c.FullPath()}, SameRequest: cp.Request == c.Request, WriterUnavailable: underlying == nil, GinDoneNil: c.Done() == nil, ShallowValue: value == marker && value.(*struct{ Name string }).Name == "after"}
	requestCtx := c.Request.Context() // context本身可跨goroutine使用；它并不使Gin对象长期有效。
	parent := r.root
	if c.Param("mode") == "request" {
		parent = requestCtx
	}
	snapshot := life.Snapshot // string字段的值副本，无共享可变业务指针。
	life.Job = startJob(parent, 2*time.Second, &r.Jobs, func(ctx context.Context) error {
		life.Started.Open()
		// 等请求结束事实，再观察独立owner是否仍有效；这里不是延时猜“handler应该返回了”。
		select {
		case <-requestCtx.Done():
			life.RequestCanceled.Open()
		case <-ctx.Done():
			return ctx.Err()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-life.Release.C:
		case <-ctx.Done():
			return ctx.Err()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if snapshot.User == "" {
			return errors.New("缺少提取身份")
		}
		life.mu.Lock()
		life.effects++
		life.mu.Unlock()
		return nil
	}, func() error {
		// 结果之后还有清理屏障，用于独立证明result不是join；正常实验owner显式Open。
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return wait(cleanupCtx, life.Cleanup.C, "后台清理gate")
	})
	r.mu.Lock()
	r.lives[name] = life
	r.mu.Unlock()
	c.JSON(202, Envelope{Code: "ACCEPTED", User: snapshot.User})
}

// poolProbe 在当前请求goroutine比较上次借用地址。只比较指针身份，不读取已归还对象；
// 当前c的user由当前认证重新设置。sync.Pool不保证哪次复用，所以只报告观察，不断言必复用。
func (r *Rig) poolProbe(c *gin.Context) {
	r.mu.Lock()
	reused := r.previous == c
	r.previous = c
	r.mu.Unlock()
	c.JSON(200, struct {
		Reused bool   `json:"reused"`
		User   string `json:"user"`
	}{reused, c.GetString("user")})
}

// joinedJSONCount 仅用于危险响应解析：Decoder逐个读对象，证明它们不能当成一个JSON协议。
// 这个函数不替正常API放宽契约；正常响应仍用json.Unmarshal要求完整单一对象。
func joinedJSONCount(body []byte) (int, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	count := 0
	for {
		var value any
		err := decoder.Decode(&value)
		if err == io.EOF {
			return count, nil
		}
		if err != nil {
			return count, err
		}
		count++
	}
}
