// Package lab 把 M17 的订单 HTTP 契约接到真实 Gin，并用本轮独占状态证明框架边界。
package lab

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

// AliceToken 沿用 M17 的教学 token；它只是当前实验的身份映射，不是生产 token 校验器。
const AliceToken = "Bearer m17-local-learning"
const BobToken = "Bearer m18-local-bob"
const OrderJSON = `{"sku":"sku-m17","qty":1}`

// Envelope 的四个核心字段直接遵守 M17 的协议：状态码、code、committed、order_id。
// 后三个可选字段仅供本课观察身份、客户端 IP 和 Context.Copy，不改变订单成功判断。
type Envelope struct {
	Code      string `json:"code"`
	Committed bool   `json:"committed"`
	OrderID   int    `json:"order_id,omitempty"`
	Peer      string `json:"peer,omitempty"`
	User      string `json:"user,omitempty"`
}

// Order 是每次绑定得到的值，不持有 Request/Context。required 只拒绝零值，
// 不会自动知道 sku-m17 的业务含义，也不会拒绝 qty=-1 这种非零但非法的数量。
type Order struct {
	SKU string `json:"sku" binding:"required"`
	Qty int    `json:"qty" binding:"required"`
}

var ErrConflict = errors.New("同一幂等键对应不同订单内容")
var ErrForbidden = errors.New("订单不属于当前身份")
var ErrNotFound = errors.New("订单不存在")

type storedOrder struct {
	ID    int
	Owner string
	Order Order
}

// Ledger 是本课真实的进程内副作用：rows 的 append 是订单写入点。
// mu 同时保护查重、内容冲突检查与 append，避免把三个独立动作误当原子业务。
// 这里没有数据库；课程只证明当前进程，不声称持久事务、跨实例幂等或崩溃恢复。
type Ledger struct {
	mu   sync.Mutex
	rows []storedOrder
	keys map[string]storedOrder
}

// Commit 由正常/故障 handler 同步调用。owner 来自已校验身份，order 是局部值副本；
// key 来自网络头部。返回 ID 给响应层；冲突时返回 error 并且绝不 append。
func (l *Ledger) Commit(owner, key string, order Order) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock() // return 先确定 ID/error，随后释放账本锁，再回到 handler。
	if l.keys == nil {
		l.keys = make(map[string]storedOrder)
	}
	// namespace 包含可信身份，因此 Alice 和 Bob 的相同 key 不会共享订单。
	namespace := owner + "|" + key
	if previous, exists := l.keys[namespace]; exists {
		if previous.Order != order {
			return 0, ErrConflict
		}
		return previous.ID, nil
	}
	record := storedOrder{ID: len(l.rows) + 1, Owner: owner, Order: order}
	l.rows = append(l.rows, record)
	l.keys[namespace] = record
	return record.ID, nil
}

// Lookup 由 GET handler 调用；先找到真实订单，再校验 owner，不能只凭 URL 中的 ID 授权。
// 返回 struct 值副本，不把共享 slice 或内部 map 交给调用者修改。
func (l *Ledger) Lookup(owner string, id int) (storedOrder, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if id < 1 || id > len(l.rows) {
		return storedOrder{}, ErrNotFound
	}
	record := l.rows[id-1]
	if record.Owner != owner {
		return storedOrder{}, ErrForbidden
	}
	return record, nil
}

// Count 由实验和独立测试读取业务事实；受同一锁保护，返回的是整数值副本。
func (l *Ledger) Count() int { l.mu.Lock(); defer l.mu.Unlock(); return len(l.rows) }

// bindOrder 由 createOrder/slowOrder 调用，把网络 body 转成已校验的订单值。
// 它返回 (Order,true) 才允许进入业务写入；失败自己写一次响应，caller 必须立即 return。
// 两次解析都针对最多 256 字节的本地数据，并非重新读取已消费的网络 body。
func bindOrder(c *gin.Context) (Order, bool) {
	if c.ContentType() != "application/json" {
		reject(c, http.StatusUnsupportedMediaType, "CONTENT_TYPE")
		return Order{}, false
	}
	// 不信任 Content-Length；MaxBytesReader 限制实际读取，defer 负责本次包装 body。
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256)
	defer c.Request.Body.Close()
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			reject(c, http.StatusRequestEntityTooLarge, "INPUT")
		} else {
			reject(c, http.StatusBadRequest, "INPUT")
		}
		return Order{}, false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(data))
	var order Order
	if err := c.ShouldBindJSON(&order); err != nil {
		// ShouldBindJSON 只返回 error，响应尚未被它写定，因此这里拥有状态/JSON 选择权。
		reject(c, http.StatusBadRequest, "INPUT")
		return Order{}, false
	}
	// Gin 的默认 JSON binder 只 Decode 一次，默认允许未知字段。本课保留 M17 的严格契约，
	// 用请求局部 decoder 检查未知字段和 EOF，避免改包级全局选项影响其他 router。
	err = strictOrderJSON(data)
	if err != nil {
		reject(c, http.StatusBadRequest, "INPUT")
		return Order{}, false
	}
	if order.SKU != "sku-m17" || order.Qty < 1 || order.Qty > 3 {
		// -1/4 能通过 required，但不能通过应用的数量/商品规则。
		reject(c, http.StatusUnprocessableEntity, "ORDER_INVALID")
		return Order{}, false
	}
	return order, true
}

// strictOrderJSON由普通绑定与缓存绑定调用，只检查有限本地bytes的结构/完整性。
// 第一Decode拒绝未知字段，第二Decode必须EOF；它不读取网络，不重复产生业务副作用。
func strictOrderJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var strict Order
	if err := decoder.Decode(&strict); err != nil {
		return err
	}
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return errors.New("尾随第二个 JSON 对象")
	}
	return err
}

// reject 由当前请求 goroutine 调用，选择业务错误协议并终止后续 handler 链。
// 它不会终止 caller 正在运行的函数；每个调用点仍要写 return。
func reject(c *gin.Context, status int, code string) {
	c.Set("result-code", code)
	c.AbortWithStatusJSON(status, Envelope{Code: code})
}

// commitReply 由已完成输入/身份校验的 handler 调用；写入后再发 201，与 M17 一致。
// 响应失败不撤销 append。重试和事务不是 Gin.Next/Recovery 的功能。
func (r *Rig) commitReply(c *gin.Context, order Order, key string) {
	user := c.GetString("user")
	id, err := r.Ledger.Commit(user, key, order)
	if errors.Is(err, ErrConflict) {
		reject(c, http.StatusConflict, "CONFLICT")
		return
	}
	if err != nil {
		reject(c, http.StatusInternalServerError, "INTERNAL")
		return
	}
	c.Set("result-code", "OK")
	c.JSON(http.StatusCreated, Envelope{Code: "OK", Committed: true, OrderID: id})
}

// identity 由 authentication 调用，只有固定的两个教学 token 能产生身份。
// X-User 等用户自报字段永远不参与归属校验；生产中应替换为校验过的签名/会话信息。
func identity(header string) string {
	switch header {
	case AliceToken:
		return "alice"
	case BobToken:
		return "bob"
	default:
		return ""
	}
}

// assertEnvelope 供 demo 对真实响应做完整协议断言。独立测试另外直接检查 JSON 与账本，
// 不是只检查这个函数返回 nil，也不是把 HTTP 200/201 当业务提交事实。
func assertEnvelope(reply Reply, status int, code string, committed bool) error {
	var value Envelope
	if err := json.Unmarshal(reply.Body, &value); err != nil {
		return fmt.Errorf("响应不是一个完整 JSON: %q: %w", reply.Body, err)
	}
	if reply.Status != status || value.Code != code || value.Committed != committed {
		return fmt.Errorf("协议不符 status=%d body=%s，期望 %d/%s/%t", reply.Status, reply.Body, status, code, committed)
	}
	return nil
}
