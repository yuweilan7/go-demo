package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"go-demo/lessons/m08_json_money_time/internal/contract"
)

// moneyDemo 由 run 调用，复现通用 JSON 数字与金额的不同精度风险。
// a、b、major 都是运行时 float64 变量，避免把 Go 精确常量表达式的结果误当成浮点加法。
func moneyDemo() error {
	fmt.Println("\n[E02-A] map[string]any 的大整数")
	raw := `{"order_id":9007199254740993}`
	var unsafe map[string]any
	if err := json.Unmarshal([]byte(raw), &unsafe); err != nil {
		return err
	}
	unsafeID := unsafe["order_id"].(float64) // 已知输入是 JSON number；动态类型默认就是 float64。
	fmt.Printf("原文=9007199254740993 map动态类型=%T 存储=%.0f int64=%d\n", unsafe["order_id"], unsafeID, int64(unsafeID))
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber() // 解码前设置，让 any 内保存原数值文本，而不是已经舍入的 float64。
	var numbered map[string]any
	if err := decoder.Decode(&numbered); err != nil {
		return err
	}
	preciseID, err := numbered["order_id"].(json.Number).Int64()
	if err != nil {
		return err
	}
	var typed struct {
		OrderID int64 `json:"order_id"`
	}
	if err := json.Unmarshal([]byte(raw), &typed); err != nil {
		return err
	}
	fmt.Printf("UseNumber=%d；具体int64=%d；order.v1线上ID用字符串\n", preciseID, typed.OrderID)

	fmt.Println("\n[E02-B] 二进制浮点金额与截断")
	a, b := 0.1, 0.2
	major := 0.29
	fmt.Printf("运行时0.1+0.2=%.17f ==0.3=%t\n", a+b, a+b == 0.3)
	fmt.Printf("运行时0.29*100=%.17f 转int64=%d（29分丢成28分）\n", major*100, int64(major*100))
	left, err := contract.ParseMajor("CNY", "0.10", contract.RejectFraction)
	if err != nil {
		return err
	}
	right, err := contract.ParseMajor("CNY", "0.20", contract.RejectFraction)
	if err != nil {
		return err
	}
	sum, err := contract.Add(left, right)
	if err != nil {
		return err
	}
	fmt.Printf("整数修复：10+20=%d 最小单位；币种=%s\n", sum.AmountMinor, sum.Currency)
	for _, input := range []struct{ currency, text string }{{"CNY", "0.29"}, {"CNY", "1.005"}, {"JPY", "1.50"}, {"KWD", "1.234"}, {"CNY", "92233720368547758.08"}} {
		strict, strictErr := contract.ParseMajor(input.currency, input.text, contract.RejectFraction)
		rounded, roundedErr := contract.ParseMajor(input.currency, input.text, contract.HalfUp)
		fmt.Printf("%s %s -> 拒额外精度=%d err=%v；HalfUp=%d err=%v\n", input.currency, input.text, strict.AmountMinor, strictErr, rounded.AmountMinor, roundedErr)
	}
	_, overflow := contract.Add(contract.Money{Currency: "CNY", AmountMinor: contract.Minor(math.MaxInt64)}, contract.Money{Currency: "CNY", AmountMinor: 1})
	_, mixedCurrency := contract.Add(left, contract.Money{Currency: "USD", AmountMinor: 20})
	_, multiplied := contract.Multiply(contract.Money{Currency: "CNY", AmountMinor: contract.Minor(math.MaxInt64)}, 2)
	fmt.Printf("MaxInt64+1=%v；不同币种=%v；MaxInt64*2=%v\n", overflow, mixedCurrency, multiplied)

	fmt.Println("\n[E02-C] 汇率是方向、精度、时效与舍入的组合契约")
	now := time.Date(2026, 10, 12, 2, 0, 0, 0, time.UTC)
	quote := contract.FXQuote{Source: "CNY", Target: "USD", Numerator: 7, Denominator: 50, AsOf: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}
	// 合成报价：1 CNY = 7/50 USD；105 分 CNY = 14.7 分 USD，不可直接截为14。
	source := contract.Money{Currency: "CNY", AmountMinor: 105}
	_, exactErr := contract.Convert(source, quote, now, contract.RejectFraction)
	converted, roundedErr := contract.Convert(source, quote, now, contract.HalfUp)
	_, staleErr := contract.Convert(source, quote, quote.ValidUntil, contract.HalfUp)
	fmt.Printf("105 CNY最小单位 * (7/50 主单位汇率) -> exact_err=%v；HalfUp=%s/%d err=%v；到期报价=%v\n", exactErr, converted.Currency, converted.AmountMinor, roundedErr, staleErr)
	// SGD 是两位精度，JPY 是零位精度；换币必须同时换最小单位倍率。
	quote = contract.FXQuote{Source: "SGD", Target: "JPY", Numerator: 113, Denominator: 1, AsOf: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}
	yen, err := contract.Convert(contract.Money{Currency: "SGD", AmountMinor: 100}, quote, now, contract.RejectFraction)
	if err != nil {
		return err
	}
	fmt.Printf("100 SGD最小单位（1 SGD）-> %d JPY最小单位（113 JPY）\n", yen.AmountMinor)
	return nil
}
