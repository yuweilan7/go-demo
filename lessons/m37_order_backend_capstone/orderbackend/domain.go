// Package orderbackend 是 M37 唯一业务库。HTTP、RPC、relay、consumer 都调用这里的
// 同一 Store；M38 可以复用构造器与有限观察点，不必再复制一套“看起来类似”的业务。
package orderbackend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"
)

var (
	ErrInvalid   = errors.New("业务参数无效")
	ErrConflict  = errors.New("幂等键或事件ID已绑定不同payload")
	ErrStock     = errors.New("权威库存不足")
	ErrNotFound  = errors.New("当前用户没有该订单")
	ErrUnknown   = errors.New("UNKNOWN: 必须以原owner/key查权威库，不盲重试写")
	ErrCleanup   = errors.New("清理或最终join失败")
	ErrOwnership = errors.New("已有对象不在caller授权的精确owner账本中")
)

const SKU = "sku-m37"
const UnitMinor int64 = 299 // CNY 的分。不能用 float64(2.99)*100 再截断。

// Input 是网络可填写的全部字段；归属、金额、状态和ID都由服务端确定。
// Quantity 使用具体 int64，解码 null/缺失会得到0，业务层统一拒绝，不能默认为1。
type Input struct {
	SKU      string `json:"sku"`
	Quantity int64  `json:"quantity"`
}

// Command 是 handler 传给用例的值副本。没有 map/slice/指针字段，复制后互不污染。
// Key 是跨请求稳定幂等键；RequestID 是这一次网络调用的跟踪号，不能混成一个概念。
type Command struct {
	Owner, Key, RequestID string
	Input                 Input
}

// Order 是 MySQL 权威创建事实。Version 是“这个订单”的版本，本课只有创建v1。
// Status=ACCEPTED 不表示支付或发货。消费确认属于下面 View 的独立有限状态。
// AmountMinor 对外用字符串承载int64，避免通用JSON/JavaScript浮点精度损失。
type Order struct {
	ID          string `gorm:"column:order_id" json:"order_id"`
	Owner       string `json:"owner"`
	Key         string `gorm:"column:request_key" json:"-"`
	RequestID   string `json:"creation_request_id"`
	SKU         string `json:"sku"`
	Quantity    int64  `json:"quantity"`
	AmountMinor int64  `json:"amount_minor,string"`
	Currency    string `json:"currency"`
	Status      string `json:"status"`
	Version     int64  `gorm:"column:aggregate_version" json:"aggregate_version"`
}

// View 是可以重建的查询形状。DeliveryStatus 的状态机是 PENDING → RECORDED；
// RECORDED 意味着 inbox 与 receipt 已在消费事务提交，源订单仍为 ACCEPTED/v1。
// 它没有可变引用，缓存返回值也不会与其他请求共享可写对象。
type View struct {
	Order
	DeliveryStatus string `json:"delivery_status"`
}

// Event 不含 Kafka offset。EventID/order/version/payload 在 outbox 中固定，relay
// 重投不重新生成。RequestID记录创建调用的追踪信息，不用后续重试的跟踪号替换。
type Event struct {
	EventID string `json:"event_id"`
	Order   Order  `json:"order"`
}

// Hooks 是同步、可信、有限的观察边界；仅课程owner传入，HTTP用户不能选故障。
// 函数可能停在真实阶段，但必须观察ctx或由创建门的owner在清理时放行。
// AfterOrderCommit失败不撤销订单；AfterConsumeCommit失败不撤销消费效果。
type Hooks struct {
	AfterOrderWrites   func(context.Context, Order) error
	AfterOrderCommit   func(context.Context, Order) error
	BeforeConsume      func(context.Context) error
	AfterRelayACK      func(context.Context, Event) error
	AfterConsumeCommit func(context.Context, Event) error
	AfterRead          func(context.Context, View) error
}

var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var idPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Validate 由非HTTP caller与handler共用。即便解码已通过，数量0仍是业务错误。
// 单SKU/数量上限使金额乘法显然在int64范围内；推广到商品表必须保留溢出检查。
func (c Command) Validate() error {
	if (c.Owner != "alice" && c.Owner != "bob") || !keyPattern.MatchString(c.Key) || !keyPattern.MatchString(c.RequestID) || c.Input.SKU != SKU || c.Input.Quantity < 1 || c.Input.Quantity > 3 {
		return ErrInvalid
	}
	return nil
}

// OrderID 把用户+幂等键规范编码后散列，避免简单拼接的分隔符碰撞。
// 这是合成业务的确定ID策略，不是凭散列完成鉴权；SQL仍必须带owner条件。
func OrderID(owner, key string) string {
	b, _ := json.Marshal([2]string{owner, key})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// EventFor 由Create和消费者权威核对共用。v1创建事件的ID与订单ID/版本稳定绑定。
func EventFor(o Order) Event {
	h := sha256.Sum256([]byte(o.ID + ":created:v1"))
	return Event{EventID: hex.EncodeToString(h[:]), Order: o}
}

// DecodeInput 由HTTP读取最多1024字节后调用。逐token检查精确名字和重复，再解码
// 具体字段；encoding/json v1 的大小写匹配/后值覆盖不能偷偷改变订单协议。
func DecodeInput(raw []byte) (Input, error) {
	if len(raw) > 1024 || !utf8.Valid(raw) {
		return Input{}, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return Input{}, ErrInvalid
	}
	seen := map[string]bool{}
	for d.More() {
		t, err = d.Token()
		if err != nil {
			return Input{}, ErrInvalid
		}
		name, ok := t.(string)
		if !ok || seen[name] || (name != "sku" && name != "quantity") {
			return Input{}, ErrInvalid
		}
		seen[name] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return Input{}, ErrInvalid
		}
	}
	if _, err = d.Token(); err != nil {
		return Input{}, ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF || len(seen) != 2 {
		return Input{}, ErrInvalid
	}
	var input Input
	if json.Unmarshal(raw, &input) != nil {
		return Input{}, ErrInvalid
	}
	return input, nil
}

// SamePayload 比较同key的业务内容。价格/币种由服务器冻结，旧订单不因当前定价改变；
// 新版本增加业务字段时必须同步扩充这里和SQL/event/cache协议，README给完整影响面。
func SamePayload(o Order, c Command) bool {
	return o.Owner == c.Owner && o.Key == c.Key && o.SKU == c.Input.SKU && o.Quantity == c.Input.Quantity
}

// CleanupCause 将清理错误留在Join树中，errors.Is可同时识别原失败与资源失败。
func CleanupCause(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(ErrCleanup, err)
}

// Assert 是实验独立断言出口，描述预期业务事实；打印PASS不能代替这项比较。
func Assert(ok bool, format string, args ...any) error {
	if !ok {
		return fmt.Errorf("断言失败: "+format, args...)
	}
	return nil
}
