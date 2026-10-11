// Package input 把外部 JSON 文本转换成 order.Order；解析和业务出库职责分开。
package input

import (
	"fmt"
	"strconv"

	"github.com/tidwall/gjson"
	"go-demo/lessons/m01_syntax_packages/order"
)

// Parse 由 cmd/demo 调用；raw 是合成请求文本，结果按值交给 service。
// 为集中观察数值边界，quantity、unit_cents 用十进制字符串传递，不用浮点 JSON 金额。
// gjson v1.18.0 的 Valid/Get/Result.Type 是实际使用的 API，版本固定在 go.mod/go.sum。
func Parse(raw string) (order.Order, error) {
	// Get 不自动证明整份 JSON 合法，因此先 Valid；非法文本不能只因 Get 得到零值就被接受。
	if !gjson.Valid(raw) {
		return order.Order{}, fmt.Errorf("请求不是合法 JSON")
	}
	id := gjson.Get(raw, "id")
	status := gjson.Get(raw, "status")
	quantity := gjson.Get(raw, "quantity")
	price := gjson.Get(raw, "unit_cents")
	// Type 区分字段不存在、JSON null、字符串及数值；不能让 String() 的转换掩盖类型错误。
	if id.Type != gjson.String || status.Type != gjson.String || quantity.Type != gjson.String || price.Type != gjson.String {
		return order.Order{}, fmt.Errorf("id/status/quantity/unit_cents 必须都是 JSON 字符串")
	}

	// qty 和 err 都在 Parse 的函数体作用域声明；ParseUint 限定 8 位，256/负数/非数字会失败。
	// 函数返回的 uint64 只是 API 统一返回类型，不意味着已经允许完整 uint64 数量范围。
	qty, err := strconv.ParseUint(quantity.String(), 10, 8)
	if err != nil {
		return order.Order{}, fmt.Errorf("quantity=%q：%w", quantity.String(), err)
	}
	// cents 是同块新名称，所以 := 合法；err 已在同一块中声明，复用那个 err 并重新赋值。
	// 若把这一行放入新 { } 块，err 就会变成另一变量，不能仅看拼写判断它绑定谁。
	cents, err := strconv.ParseInt(price.String(), 10, 64)
	if err != nil {
		return order.Order{}, fmt.Errorf("unit_cents=%q：%w", price.String(), err)
	}

	// 状态映射也是值，不把 iota 编码直接当外部协议，避免新增枚举项改变 JSON 含义。
	states := map[string]order.Status{
		"created": order.Created, "paid": order.Paid,
		"shipped": order.Shipped, "cancelled": order.Cancelled,
	}
	// ok 是“键存在”，与取出的状态是否零值是不同事实。它只在这个 if/else 内可见。
	if state, ok := states[status.String()]; ok {
		return order.Order{ID: id.String(), Status: state, Quantity: uint8(qty), UnitCents: cents}, nil
	}
	return order.Order{}, fmt.Errorf("status=%q 不受支持", status.String())
}
