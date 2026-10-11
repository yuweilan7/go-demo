// Package order 是本课的合成订单业务。它只在内存里保存数据，不能替代数据库事务验收。
package order

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxCents 是课程业务规则：单笔最多 1,000,000.00 元。金额存为整数分，避免二进制浮点舍入。
const MaxCents int64 = 100_000_000

var (
	ErrInvalidAmount = errors.New("金额格式或范围无效")
	ErrInvalidOrder  = errors.New("订单输入无效")
)

// ParseCents 由 JSON 边界和演示调用，把外部十进制文本转换为整数分。
// 本课接受的语法是 0.00 或不带前导零的非负金额，必须恰有两位小数；这里允许零，支付入口再拒绝零。
// 返回错误时结果为零，调用者必须先检查 err，不能把错误结果当成免费订单。
func ParseCents(text string) (int64, error) {
	// byte 长度先设上限，使累乘最多处理七位整数，既限制工作量，也避免 int64 溢出。
	// 下标读到的是字节，因此后面的范围判断有意只接受 ASCII 0..9，不接受全角数字。
	if len(text) < 4 || len(text) > 10 || text[len(text)-3] != '.' {
		return 0, fmt.Errorf("%w: %q 必须有两位小数", ErrInvalidAmount, text)
	}
	whole := text[:len(text)-3] // string 切片是只读视图；本函数不持有可变的外部缓冲。
	if len(whole) > 1 && whole[0] == '0' {
		return 0, fmt.Errorf("%w: 不接受前导零", ErrInvalidAmount)
	}
	var cents int64
	for i := range text {
		if i == len(text)-3 {
			continue // 小数点的位置已检查，累积其余字符就得到分，不经过 float64。
		}
		if text[i] < '0' || text[i] > '9' {
			return 0, fmt.Errorf("%w: 非 ASCII 数字", ErrInvalidAmount)
		}
		cents = cents*10 + int64(text[i]-'0')
	}
	if cents > MaxCents {
		return 0, fmt.Errorf("%w: 超过单笔上限", ErrInvalidAmount)
	}
	return cents, nil
}

// FormatCents 由演示、HTTP 成功响应和 fuzz 调用，只格式化本业务范围内的整数分。
// 返回 bool 是为了阻止负数/越界值被包装成合法外部金额，调用方拥有对 false 的处理。
func FormatCents(cents int64) (string, bool) {
	if cents < 0 || cents > MaxCents {
		return "", false
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100), true
}

// Payment 是解析后交给 Store 的值对象；传值会复制字段，不向 Store 暴露可变 slice/map。
type Payment struct {
	ID    string
	Cents int64
}

// DecodePayment 由 HTTP handler 和 fuzz 输入边界调用。raw 属于本次请求，最多允许 4096 字节。
// 外部金额使用 JSON string，例如 "12.99"；JSON number 12.99 会因类型不匹配被拒绝。
func DecodePayment(raw []byte) (Payment, error) {
	if len(raw) > 4096 {
		return Payment{}, fmt.Errorf("%w: 超过 4096 字节", ErrInvalidOrder)
	}
	var input struct {
		ID     string `json:"id"`
		Amount string `json:"amount"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields() // 拼错字段不能静默忽略，否则看似成功的测试可能根本没有测到预期输入。
	if err := decoder.Decode(&input); err != nil {
		return Payment{}, fmt.Errorf("%w: JSON: %w", ErrInvalidOrder, err)
	}
	// 首个 Decode 成功不代表整个请求只有一个 JSON 文档；再次读取必须得到 EOF。
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Payment{}, fmt.Errorf("%w: JSON 后还有内容", ErrInvalidOrder)
	}
	if !validID(input.ID) {
		return Payment{}, fmt.Errorf("%w: id 只能含 1..64 个 ASCII 字母、数字、- 或 _", ErrInvalidOrder)
	}
	cents, err := ParseCents(input.Amount) // := 声明本函数作用域的新 cents 和 err；金额错误保留在错误链中。
	if err != nil {
		return Payment{}, fmt.Errorf("%w: %w", ErrInvalidOrder, err)
	}
	if cents == 0 {
		return Payment{}, fmt.Errorf("%w: 支付金额必须大于零", ErrInvalidOrder)
	}
	return Payment{ID: input.ID, Cents: cents}, nil
}

// validID 只负责当前课程的订单编号契约；数据库是否存在这笔订单由 Store 的下一层判断。
func validID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := range id {
		b := id[i]
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_') {
			return false
		}
	}
	return true
}
