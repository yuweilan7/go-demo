package contract

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Minor 是 int64 最小单位数，JSON 使用规范十进制字符串。
// 数值计算仍然是整数；引号是跨语言接口契约，不是把业务计算改成字符串拼接。
type Minor int64

// UnmarshalJSON 由 Field[Minor] 调用，只接受 "299" 这样的字符串。
// "2.99"、"2e2"、"0299"、超出 int64 的文本都失败；负数在 Money.Validate 中再拒绝。
func (m *Minor) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return fmt.Errorf("amount_minor 必须是整数字符串: %w", err)
	}
	number, err := strconv.ParseInt(text, 10, 64)
	if err != nil || strconv.FormatInt(number, 10) != text {
		return fmt.Errorf("amount_minor 必须是 int64 范围内的规范十进制整数")
	}
	*m = Minor(number)
	return nil
}

// MarshalJSON 由 Order/Money 的 JSON 编码器调用，保证输出也遵守同一个字符串契约。
// 值接收者拿到 Minor 的值副本，不会更改原金额。
func (m Minor) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatInt(int64(m), 10))
}

// Money 同时携带币种和数量；脱离币种的 100 不知道是 1 CNY 还是 100 JPY。
type Money struct {
	Currency    string `json:"currency"`
	AmountMinor Minor  `json:"amount_minor"`
}

// CurrencyScale 是本课冻结的订单币种白名单，不是在线汇率/完整 ISO 4217 数据库。
// 将来支持新币种必须更新契约和测试，不能默认所有币种都“乘 100”。
func CurrencyScale(currency string) (int, error) {
	switch currency {
	case "CNY", "SGD", "USD":
		return 2, nil
	case "JPY":
		return 0, nil
	case "KWD":
		return 3, nil
	default:
		return 0, fmt.Errorf("不支持的币种 %q", currency)
	}
}

// Validate 在加法/乘法/汇率换算前共用；本订单模型不允许负余额/退款金额。
// 退款业务应另订方向与符号契约，不能直接删掉校验后复用全部结论。
func (m Money) Validate() error {
	if _, err := CurrencyScale(m.Currency); err != nil {
		return err
	}
	if m.AmountMinor < 0 {
		return fmt.Errorf("本订单契约不允许负金额")
	}
	return nil
}

// Add 由金额实验调用，返回新值；a/b 是值副本，不共享可写金额。
// 先检查再做加法，不能先相加再比较：Go 和 Java 的普通整数加法都不会自动报告溢出。
// Java Math.addExact 的显式检查不会因为使用 int64 就自动出现在 Go；这里自己检查上界。
func Add(a, b Money) (Money, error) {
	if err := a.Validate(); err != nil {
		return Money{}, err
	}
	if err := b.Validate(); err != nil {
		return Money{}, err
	}
	if a.Currency != b.Currency {
		return Money{}, fmt.Errorf("不能直接相加不同币种 %s/%s", a.Currency, b.Currency)
	}
	if int64(a.AmountMinor) > math.MaxInt64-int64(b.AmountMinor) {
		return Money{}, fmt.Errorf("最小单位加法溢出 int64")
	}
	return Money{a.Currency, a.AmountMinor + b.AmountMinor}, nil
}

// Multiply 在 PATCH 重算总额时使用。quantity 由业务范围校验，但这里仍防负数与溢出。
// Money 的非负契约允许用 MaxInt64/quantity 判断，避免先执行危险的乘法。
func Multiply(unitPrice Money, quantity int64) (Money, error) {
	if err := unitPrice.Validate(); err != nil {
		return Money{}, err
	}
	if quantity <= 0 {
		return Money{}, fmt.Errorf("乘法数量必须为正")
	}
	if int64(unitPrice.AmountMinor) > math.MaxInt64/quantity {
		return Money{}, fmt.Errorf("最小单位乘法溢出 int64")
	}
	return Money{unitPrice.Currency, unitPrice.AmountMinor * Minor(quantity)}, nil
}

// Rounding 是业务选择，不能由“转 int64 时默认截断”偷偷决定。
// 本课金额非负，所以 HalfUp 与 half-away-from-zero 在本契约范围内结果相同。
type Rounding int

const (
	RejectFraction Rounding = iota // 结果不能完整落在最小单位时拒绝。
	HalfUp                         // 正金额恰好一半时向上，例如 1.005 CNY -> 101 分。
)

// ParseMajor 从人工输入的十进制主单位文本生成 Money，实验用它对照 float64 转换。
// 用 math/big 只做有界中间计算，最终必须回到 int64，不依赖第三方 decimal。
// 这不是数据库 decimal 类型；未来 SQL 列应配对 currency 与 BIGINT amount_minor。
func ParseMajor(currency, text string, rounding Rounding) (Money, error) {
	scale, err := CurrencyScale(currency)
	if err != nil {
		return Money{}, err
	}
	if len(text) == 0 || len(text) > 64 {
		return Money{}, fmt.Errorf("主单位文本长度必须为 1..64")
	}
	parts := strings.Split(text, ".")
	if len(parts) > 2 || !decimalDigits(parts[0]) {
		return Money{}, fmt.Errorf("主单位只接受非负普通十进制文本，不接受指数/符号")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if !decimalDigits(fraction) {
			return Money{}, fmt.Errorf("小数点后必须有十进制数字")
		}
	}
	if rounding == RejectFraction && len(fraction) > scale {
		return Money{}, fmt.Errorf("%s 最多 %d 位小数，额外精度必须显式选择舍入", currency, scale)
	}
	numerator, _ := new(big.Int).SetString(parts[0]+fraction, 10)
	numerator.Mul(numerator, pow10(scale))
	denominator := pow10(len(fraction))
	minor, err := roundRatio(numerator, denominator, rounding)
	if err != nil {
		return Money{}, err
	}
	return Money{currency, Minor(minor)}, nil
}

// FXQuote 定义“1 主单位 Source 等于 Numerator/Denominator 主单位 Target”。
// 报价窗口是 [AsOf, ValidUntil)，now==ValidUntil 时已过期；不从浮点汇率推导精确值。
// 本课提供合成报价，既不调用实时汇率服务，也不宣称报价可用于真实交易。
type FXQuote struct {
	Source, Target         string
	Numerator, Denominator int64
	AsOf, ValidUntil       time.Time
}

// Convert 由汇率实验调用，将币种、主/最小单位、报价时效和舍入同时纳入计算。
// 公式：目标最小单位 = 源最小单位 * rateNumerator * 10^目标精度 / (rateDenominator * 10^源精度)。
// big.Int 中间值允许分子超过 int64；roundRatio 检查最终值，不能让溢出混入订单。
func Convert(source Money, quote FXQuote, now time.Time, rounding Rounding) (Money, error) {
	if err := source.Validate(); err != nil {
		return Money{}, err
	}
	if quote.Source != source.Currency || quote.Numerator <= 0 || quote.Denominator <= 0 {
		return Money{}, fmt.Errorf("汇率币种或正数分子/分母不符合契约")
	}
	if !quote.AsOf.Before(quote.ValidUntil) || now.Before(quote.AsOf) || !now.Before(quote.ValidUntil) {
		return Money{}, fmt.Errorf("汇率报价尚未生效或已经到期")
	}
	sourceScale, _ := CurrencyScale(quote.Source) // source.Validate 已验证源币种。
	targetScale, err := CurrencyScale(quote.Target)
	if err != nil {
		return Money{}, err
	}
	numerator := big.NewInt(int64(source.AmountMinor))
	numerator.Mul(numerator, big.NewInt(quote.Numerator))
	numerator.Mul(numerator, pow10(targetScale))
	denominator := big.NewInt(quote.Denominator)
	denominator.Mul(denominator, pow10(sourceScale))
	minor, err := roundRatio(numerator, denominator, rounding)
	if err != nil {
		return Money{}, fmt.Errorf("汇率换算: %w", err)
	}
	return Money{quote.Target, Minor(minor)}, nil
}

// roundRatio 只接受调用方已验证的非负分子、正分母，不更改调用方 big.Int。
// QuoRem 同步计算商和余数：余数是“尚不能用一个最小单位表达”的业务事实。
func roundRatio(numerator, denominator *big.Int, rounding Rounding) (int64, error) {
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	switch rounding {
	case RejectFraction:
		if remainder.Sign() != 0 {
			return 0, fmt.Errorf("结果包含不足一个最小单位的部分")
		}
	case HalfUp:
		if new(big.Int).Lsh(remainder, 1).Cmp(denominator) >= 0 {
			quotient.Add(quotient, big.NewInt(1))
		}
	default:
		return 0, fmt.Errorf("未知舍入策略")
	}
	if !quotient.IsInt64() {
		return 0, fmt.Errorf("舍入后金额溢出 int64")
	}
	return quotient.Int64(), nil
}

// pow10 创建本次计算私有的整数；调用方可以 Mul 修改它，不会改共享的倍率对象。
func pow10(exponent int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil)
}

// decimalDigits 是 ASCII 契约校验，不能让 Unicode 数字、空串或指数进入金额解析。
func decimalDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, ch := range text {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
