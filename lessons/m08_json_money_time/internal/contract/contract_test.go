// 这些测试从接口调用者角度验证订单事实，预期值直接来自 contract v1 的业务规则。
// 不用生产函数重新计算期望金额/时间；测试证明的是本地契约，不冒充数据库或线上验收。
package contract_test

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"go-demo/lessons/m08_json_money_time/internal/contract"
)

const validRequest = `{"contract_version":"order.v1","request_id":"test-001","order_id":"9007199254740993","user_id":"42","sku":"SKU-M08","quantity":1,"currency":"CNY","amount_minor":"299","expires_at":"2026-10-12T10:00:00+08:00","note":"gift","tags":["new"]}`

// originalOrder 由各 PATCH 测试调用，建立已经验证的原订单，而不是直接绕过 DTO 校验。
// t.Helper 使失败位置归到调用它的测试；没有网络/文件资源需要清理。
func originalOrder(t *testing.T) contract.Order {
	t.Helper()
	dto, err := contract.DecodeCreate([]byte(validRequest))
	if err != nil {
		t.Fatal(err)
	}
	order, err := contract.BuildOrder(dto, time.Date(2026, 10, 12, 1, 59, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return order
}

// TestPatchThreeStates 验证缺失/null/0没有被压成一种输入；拒绝分支不会改原订单。
func TestPatchThreeStates(t *testing.T) {
	for _, tc := range []struct {
		raw           string
		present, null bool
		value         int64
		wantError     bool
		quantity      int64
		amount        contract.Minor
	}{
		{`{}`, false, false, 0, false, 1, 299},
		{`{"quantity":null}`, true, true, 0, true, 0, 0},
		{`{"quantity":0}`, true, false, 0, true, 0, 0},
		{`{"quantity":2}`, true, false, 2, false, 2, 598},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			original := originalOrder(t)
			patch, err := contract.DecodePatch([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if patch.Quantity.Present != tc.present || patch.Quantity.Null != tc.null || patch.Quantity.Value != tc.value {
				t.Fatalf("丢失三态: %+v", patch.Quantity)
			}
			candidate, err := contract.ApplyPatch(original, patch, contract.Money{Currency: "CNY", AmountMinor: 299})
			if (err != nil) != tc.wantError {
				t.Fatalf("错误边界不符: %v", err)
			}
			if err == nil && (candidate.Quantity != tc.quantity || candidate.AmountMinor != tc.amount) {
				t.Fatalf("订单事实不符: %+v", candidate)
			}
			if original.Quantity != 1 || original.AmountMinor != 299 || *original.Note != "gift" || original.Tags[0] != "new" {
				t.Fatal("PATCH 计算候选值时改写了原订单")
			}
		})
	}
}

// TestPatchNullableNoteAndCollection 验证清除备注、设置空备注、清空标签的不同协议含义。
// 同时修改返回值的 slice/指针，证明它不与调用方已有订单共享可写存储。
func TestPatchNullableNoteAndCollection(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		noteNil   bool
		note      string
		tags      []string
		wantError bool
	}{
		{`{}`, false, "gift", []string{"new"}, false},
		{`{"note":null}`, true, "", []string{"new"}, false},
		{`{"note":""}`, false, "", []string{"new"}, false},
		{`{"tags":[]}`, false, "gift", []string{}, false},
		{`{"tags":null}`, false, "", nil, true},
		{`{"note":"changed","tags":null}`, false, "", nil, true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			original := originalOrder(t)
			patch, err := contract.DecodePatch([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := contract.ApplyPatch(original, patch, contract.Money{Currency: "CNY", AmountMinor: 299})
			if (err != nil) != tc.wantError {
				t.Fatalf("错误边界: %v", err)
			}
			if err == nil {
				if (candidate.Note == nil) != tc.noteNil || (!tc.noteNil && *candidate.Note != tc.note) || !reflect.DeepEqual(candidate.Tags, tc.tags) {
					t.Fatalf("备注/集合契约不符: %+v", candidate)
				}
				if candidate.Note != nil {
					*candidate.Note = "mutated-copy"
				}
				if len(candidate.Tags) > 0 {
					candidate.Tags[0] = "mutated-copy"
				}
			}
			if *original.Note != "gift" || original.Tags[0] != "new" {
				t.Fatal("返回值/失败分支别名改写了原订单")
			}
		})
	}
}

// TestStrictBoundary 验证原始协议歧义在 DTO 进入业务层前就被拒绝。
// 注意 escaped 名字会解转义后比较；普通 map 的重复检查无法通过这项断言。
func TestStrictBoundary(t *testing.T) {
	for _, raw := range []string{
		`{"quantity":1,"quantity":9}`, `{"quantity":1,"\u0071uantity":9}`,
		`{"QUANTITY":1}`, `{"quantitty":1}`, `{"quantity":1} {}`,
		`{"quantity":1} trailing`, `[]`, `null`, `{"quantity":1.0}`,
		`{"quantity":"2"}`, `{"quantity":9223372036854775808}`,
		`{"quantity":`, `{"note":"` + string([]byte{0xff}) + `"}`,
		strings.Repeat(" ", 16*1024+1) + `{}`,
	} {
		t.Run(raw[:min(len(raw), 80)], func(t *testing.T) {
			if _, err := contract.DecodePatch([]byte(raw)); err == nil {
				t.Fatalf("危险输入被接受: %q", raw)
			}
		})
	}
	var nested struct {
		Data map[string]any `json:"data"`
	}
	if err := contract.DecodeStrict([]byte(`{"data":{"x":1,"x":2}}`), &nested, []string{"data"}); err == nil {
		t.Fatal("嵌套对象的重复名字未被拒绝")
	}
	if err := contract.DecodeStrict([]byte(`{"data":`+strings.Repeat("[", 20)+`0`+strings.Repeat("]", 20)+`}`), &nested, []string{"data"}); err == nil {
		t.Fatal("过深输入未被拒绝")
	}
	if _, err := contract.DecodePatch([]byte(" { \"quantity\":2 } \n\t")); err != nil {
		t.Fatalf("合法尾随空白被误拒绝: %v", err)
	}
}

// TestCreateValidation 把“JSON 类型正确但业务无效”和“JSON 解码错误”都纳入验收。
// replacement 对原请求只改一个事实，避免失败来自无关缺字段而掩盖真正边界。
func TestCreateValidation(t *testing.T) {
	for _, tc := range []struct{ old, replacement string }{
		{`"order.v1"`, `"order.v2"`}, {`"test-001"`, `""`},
		{`"order_id":"9007199254740993"`, `"order_id":9007199254740993`},
		{`"order_id":"9007199254740993"`, `"order_id":"09007199254740993"`},
		{`"order_id":"9007199254740993"`, `"order_id":"18446744073709551616"`},
		{`"quantity":1`, `"quantity":0`}, {`"quantity":1`, `"quantity":null`},
		{`"quantity":1,`, ``}, {`"quantity":1`, `"quantity":1001`},
		{`"currency":"CNY"`, `"currency":"cny"`},
		{`"amount_minor":"299"`, `"amount_minor":299`},
		{`"amount_minor":"299"`, `"amount_minor":"2.99"`},
		{`"amount_minor":"299"`, `"amount_minor":"0"`},
		{`"amount_minor":"299"`, `"amount_minor":"-1"`},
		{`"amount_minor":"299"`, `"amount_minor":"9223372036854775808"`},
		{`"amount_minor":"299"`, `"amount_minor":null`},
		{`"expires_at":"2026-10-12T10:00:00+08:00"`, `"expires_at":"2026-10-12T01:59:00Z"`},
		{`"expires_at":"2026-10-12T10:00:00+08:00"`, `"expires_at":null`},
		{`"tags":["new"]`, `"tags":null`}, {`,"tags":["new"]`, ``},
	} {
		t.Run(tc.replacement, func(t *testing.T) {
			raw := strings.Replace(validRequest, tc.old, tc.replacement, 1)
			dto, err := contract.DecodeCreate([]byte(raw))
			if err == nil {
				_, err = contract.BuildOrder(dto, time.Date(2026, 10, 12, 1, 59, 0, 0, time.UTC))
			}
			if err == nil {
				t.Fatalf("无效业务输入被接受: %s", raw)
			}
		})
	}
	order := originalOrder(t)
	encoded, err := json.Marshal(order)
	if err != nil {
		t.Fatal(err)
	}
	// 预期 ID/金额/UTC 文本是硬编码合同事实，独立于生产编码函数。
	for _, fragment := range []string{`"order_id":"9007199254740993"`, `"amount_minor":"299"`, `"expires_at":"2026-10-12T02:00:00Z"`} {
		if !strings.Contains(string(encoded), fragment) {
			t.Fatalf("输出未遵守字段契约: %s", encoded)
		}
	}
}

// TestDecimalAndOverflow 验证真实的分/日元/fils 值，以及边界值上的舍入和溢出。
func TestDecimalAndOverflow(t *testing.T) {
	for _, tc := range []struct {
		currency, text string
		mode           contract.Rounding
		want           contract.Minor
		wantError      bool
	}{
		{"CNY", "0.29", contract.RejectFraction, 29, false},
		{"CNY", "1.005", contract.RejectFraction, 0, true},
		{"CNY", "1.005", contract.HalfUp, 101, false},
		{"CNY", "1.004", contract.HalfUp, 100, false},
		{"CNY", "1.999", contract.HalfUp, 200, false},
		{"JPY", "1.50", contract.HalfUp, 2, false},
		{"KWD", "1.234", contract.RejectFraction, 1234, false},
		{"CNY", "92233720368547758.07", contract.RejectFraction, contract.Minor(math.MaxInt64), false},
		{"CNY", "92233720368547758.075", contract.HalfUp, 0, true},
		{"CNY", "-1", contract.HalfUp, 0, true}, {"CNY", "1e2", contract.HalfUp, 0, true},
		{"CNY", "1.", contract.HalfUp, 0, true}, {"CNY", ".5", contract.HalfUp, 0, true},
		{"CNY", "NaN", contract.HalfUp, 0, true}, {"XXX", "1", contract.HalfUp, 0, true},
		{"CNY", "1", contract.Rounding(99), 0, true},
	} {
		t.Run(tc.currency+"/"+tc.text, func(t *testing.T) {
			actual, err := contract.ParseMajor(tc.currency, tc.text, tc.mode)
			if (err != nil) != tc.wantError || (err == nil && actual.AmountMinor != tc.want) {
				t.Fatalf("want=%d error=%t actual=%+v err=%v", tc.want, tc.wantError, actual, err)
			}
		})
	}
	a, b := contract.Money{Currency: "CNY", AmountMinor: 10}, contract.Money{Currency: "CNY", AmountMinor: 20}
	sum, err := contract.Add(a, b)
	if err != nil || sum.AmountMinor != 30 {
		t.Fatalf("整数金额相加: %+v %v", sum, err)
	}
	maxMoney := contract.Money{Currency: "CNY", AmountMinor: contract.Minor(math.MaxInt64)}
	if _, err := contract.Add(maxMoney, b); err == nil {
		t.Fatal("加法溢出未拦截")
	}
	if _, err := contract.Multiply(maxMoney, 2); err == nil {
		t.Fatal("乘法溢出未拦截")
	}
	if _, err := contract.Add(a, contract.Money{Currency: "USD", AmountMinor: 10}); err == nil {
		t.Fatal("不同币种直接相加未拦截")
	}
}

// TestFXBoundaries 独立验证主单位汇率、两币种精度、有效窗口和最终 int64 范围。
func TestFXBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 12, 2, 0, 0, 0, time.UTC)
	quote := contract.FXQuote{Source: "CNY", Target: "USD", Numerator: 7, Denominator: 50, AsOf: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}
	source := contract.Money{Currency: "CNY", AmountMinor: 105}
	actual, err := contract.Convert(source, quote, now, contract.HalfUp)
	if err != nil || actual.Currency != "USD" || actual.AmountMinor != 15 {
		t.Fatalf("1.05 CNY -> 0.15 USD 的舍入契约: %+v %v", actual, err)
	}
	if _, err := contract.Convert(source, quote, now, contract.RejectFraction); err == nil {
		t.Fatal("不允许小单位分数时未拒绝")
	}
	for _, timestamp := range []time.Time{quote.AsOf.Add(-time.Nanosecond), quote.ValidUntil} {
		if _, err := contract.Convert(source, quote, timestamp, contract.HalfUp); err == nil {
			t.Fatal("汇率有效窗口边界未拒绝")
		}
	}
	for _, mutate := range []func(*contract.FXQuote){
		func(q *contract.FXQuote) { q.Denominator = 0 },
		func(q *contract.FXQuote) { q.Numerator = -1 },
		func(q *contract.FXQuote) { q.Source = "SGD" },
		func(q *contract.FXQuote) { q.Target = "XXX" },
		func(q *contract.FXQuote) { q.ValidUntil = q.AsOf },
	} {
		bad := quote
		mutate(&bad) // 每次只改本地值副本，不污染下一报价反例。
		if _, err := contract.Convert(source, bad, now, contract.HalfUp); err == nil {
			t.Fatal("无效报价被接受")
		}
	}
	yenQuote := contract.FXQuote{Source: "SGD", Target: "JPY", Numerator: 113, Denominator: 1, AsOf: quote.AsOf, ValidUntil: quote.ValidUntil}
	yen, err := contract.Convert(contract.Money{Currency: "SGD", AmountMinor: 100}, yenQuote, now, contract.RejectFraction)
	if err != nil || yen.AmountMinor != 113 {
		t.Fatalf("跨币种精度换算: %+v %v", yen, err)
	}
	quote.Numerator, quote.Denominator = 2, 1
	if _, err := contract.Convert(contract.Money{Currency: "CNY", AmountMinor: contract.Minor(math.MaxInt64)}, quote, now, contract.HalfUp); err == nil {
		t.Fatal("汇率换算最终 int64 溢出未拦截")
	}
}

// TestTimeContract 验证时间点而非显示文本，截止时刻本身过期，秒误当毫秒被拒绝。
func TestTimeContract(t *testing.T) {
	deadline, err := contract.ParseInstant("2026-10-12T10:00:00+08:00")
	if err != nil {
		t.Fatal(err)
	}
	if deadline.Format(time.RFC3339) != "2026-10-12T02:00:00Z" || deadline.UnixMilli() != 1791770400000 {
		t.Fatalf("UTC/毫秒契约: %s %d", deadline.Time, deadline.UnixMilli())
	}
	for _, zone := range []string{"UTC", "Asia/Shanghai", "America/New_York"} {
		location, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			delta   time.Duration
			expired bool
		}{{-time.Nanosecond, false}, {0, true}, {time.Nanosecond, true}} {
			if actual := contract.Expired(deadline.Add(tc.delta).In(location), deadline.Time); actual != tc.expired {
				t.Fatalf("跨时区到期不一致 zone=%s delta=%s expired=%t", zone, tc.delta, actual)
			}
		}
	}
	for _, raw := range []string{"2026-10-12 10:00:00", "2026-10-12T10:00:00", "2026-02-30T10:00:00Z", "2026-10-12T10:00:00+24:00", "2026-10-12T10:00:00,1Z", "0001-01-01T00:00:00Z"} {
		if _, err := contract.ParseInstant(raw); err == nil {
			t.Fatalf("不符合订单时间契约的输入被接受: %s", raw)
		}
	}
	if _, err := contract.ParseUnixMillis(1791770400); err == nil {
		t.Fatal("秒数误传成毫秒未被范围约束拒绝")
	}
	parsed, err := contract.ParseUnixMillis(1791770400000)
	if err != nil || !parsed.Equal(deadline.Time) {
		t.Fatalf("正确毫秒失败: %v", err)
	}
	if _, err := json.Marshal(contract.UTCInstant{}); err == nil {
		t.Fatal("零值时间漏出协议")
	}
}

// TestMonotonicSerialization 验证序列化保存时刻却不保存单调钟。
// time.Now 的具体数值随运行变化；不使用精确耗时、不 sleep，也不改系统时钟。
func TestMonotonicSerialization(t *testing.T) {
	start := time.Now()
	encoded, err := json.Marshal(start)
	if err != nil {
		t.Fatal(err)
	}
	var restored time.Time
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if !start.Equal(restored) || strings.Contains(restored.String(), "m=") {
		t.Fatal("JSON 未保存墙钟时刻，或错误保存了单调钟")
	}
	if start.Add(30*time.Minute).Sub(start) != 30*time.Minute {
		t.Fatal("本进程 Add/Sub 的时长不符")
	}
	// 不以 start==restored 是否相等作为所有平台的必然结论：位置指针和单调部分都参与 ==。
}
