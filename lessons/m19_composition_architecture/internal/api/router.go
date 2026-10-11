// Package api 是传输适配器：读取网络、建立可信请求状态、把业务错误映射成 HTTP。
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"go-demo/lessons/m19_composition_architecture/internal/config"
	"go-demo/lessons/m19_composition_architecture/internal/order"
)

type Envelope struct {
	Code      string `json:"code"`
	Committed bool   `json:"committed"`
	OrderID   int    `json:"order_id,omitempty"`
	RequestID uint64 `json:"request_id"`
}

// State 只包含可信身份与服务器生成的编号；每个请求构造一次，绝不写回共享 Service。
type State struct {
	ID    uint64
	Owner string
}

const stateKey = "m19.request-state"

// NewRouter 由 root 构造一次，接收显式 Service 指针和配置值。cfg 被闭包保存为值副本；
// 请求到来不会再次查环境变量，也不会从 context.Value 查 DSN/token/全局容器。
func NewRouter(service *order.Service, cfg config.Config) (*gin.Engine, error) {
	if service == nil {
		return nil, errors.New("service pointer is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	engine := gin.New() // 不安装默认 Logger，避免记录任意 URL/query/token。
	if err := engine.SetTrustedProxies(nil); err != nil {
		return nil, err
	}
	var sequence atomic.Uint64
	engine.Use(gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, _ any) { reject(c, 500, "PANIC") }))
	engine.GET("/version", func(c *gin.Context) { c.JSON(200, cfg.Safe()) })
	group := engine.Group("/api")
	group.Use(func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer "+cfg.Token {
			reject(c, 401, "AUTH")
			return
		}
		// c.Set 保存的是本次 State 值。Gin Context 只借到当前 handler 返回，不能逃逸进后台。
		c.Set(stateKey, State{ID: sequence.Add(1), Owner: "alice"})
		ctx, cancel := context.WithTimeout(c.Request.Context(), cfg.RequestTimeout())
		defer cancel() // 请求预算的 timer 由本中间件拥有；Next 返回就释放，不等 root Close。
		c.Request = c.Request.WithContext(ctx)
		c.Next() // 同一请求 goroutine 同步运行后续 handler，再回到此处执行 defer。
	})
	group.POST("/orders", func(c *gin.Context) { create(c, service) })
	return engine, nil
}

// create 由 Gin c.Next 调用。body 在当前请求最多读 256 字节，函数退出关闭包装器；
// net/http 也负责入站 body，重复 Close 对这个包装器安全。它不启动 goroutine。
func create(c *gin.Context, service *order.Service) {
	if c.ContentType() != "application/json" {
		reject(c, 415, "CONTENT_TYPE")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256)
	defer c.Request.Body.Close()
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		SKU string `json:"sku"`
		Qty int    `json:"qty"`
	}
	if err := decoder.Decode(&input); err != nil {
		rejectInput(c, err)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		rejectInput(c, err)
		return
	}
	value, exists := c.Get(stateKey)
	state, ok := value.(State) // comma-ok 的两个结果防止缺失/类型错误变成断言 panic。
	if !exists || !ok {
		reject(c, 500, "STATE")
		return
	}
	key := c.GetHeader("Idempotency-Key")
	if key == "" {
		reject(c, 400, "KEY_REQUIRED")
		return
	} // 保留M18的网络协议；业务层仍独立校验非HTTP caller。
	record, err := service.Create(c.Request.Context(), order.Request{Owner: state.Owner, Key: key, SKU: input.SKU, Qty: input.Qty})
	switch {
	case errors.Is(err, order.ErrInvalid):
		reject(c, 422, "ORDER_INVALID")
	case errors.Is(err, order.ErrConflict):
		reject(c, 409, "CONFLICT")
	case errors.Is(err, context.DeadlineExceeded):
		reject(c, 504, "BUDGET")
	case errors.Is(err, context.Canceled):
		reject(c, 499, "CANCELED")
	case err != nil:
		reject(c, 503, "REPOSITORY")
	default:
		c.JSON(201, Envelope{Code: "OK", Committed: true, OrderID: record.ID, RequestID: state.ID})
	}
}

// rejectInput 由严格JSON解码的两次错误路径调用；大小超限保留M18的413/INPUT契约。
// 第二次Decode也可能在读取尾随空白时超限，所以不能只检查第一次Decode。
func rejectInput(c *gin.Context, err error) {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		reject(c, 413, "INPUT")
		return
	}
	reject(c, 400, "INPUT")
}

// reject 只中止 Gin 后续链，当前 Go 函数仍需 return；此处所有 caller 都立即返回/结束。
// error 原因不直接写给网络，避免未来仓储 error 包含连接串或秘密。
func reject(c *gin.Context, status int, code string) {
	c.AbortWithStatusJSON(status, Envelope{Code: code})
}
