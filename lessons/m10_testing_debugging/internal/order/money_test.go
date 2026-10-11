package order_test

import (
	"errors"
	"math/big"
	"regexp"
	"testing"

	"go-demo/lessons/m10_testing_debugging/internal/order"
)

// TestParseCents 用 table-driven 子测试覆盖业务批准的金额边界。
// 表里的 want 是人工给出的“分”事实，不再次调用 ParseCents/FormatCents 计算 expected。
func TestParseCents(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{"zero", "0.00", 0, false},
		{"one_cent", "0.01", 1, false},
		{"order_1299", "12.99", 1299, false},
		{"upper_bound", "1000000.00", 100000000, false},
		{"above_bound", "1000000.01", 0, true},
		{"missing_fraction", "12", 0, true},
		{"one_fraction_digit", "12.9", 0, true},
		{"leading_zero", "012.99", 0, true},
		{"negative", "-12.99", 0, true},
		{"exponent", "1e2.00", 0, true},
		{"whitespace", " 12.99", 0, true},
		{"unicode", "１２.９９", 0, true},
		{"huge_input", "99999999999999999999.00", 0, true},
	}
	for _, tc := range cases {
		// Go 1.27、go 指令 >=1.22 的 range 变量逐轮独立；本课不复制旧版“所有闭包捕获同一 tc”的结论。
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel() // 这些用例只读自己的 string 和表值，没有进程环境或可变全局夹具。
			got, err := order.ParseCents(tc.input)
			if tc.wantErr {
				if !errors.Is(err, order.ErrInvalidAmount) || got != 0 {
					t.Fatalf("ParseCents(%q): got=%d err=%v; want rejected amount and zero result", tc.input, got, err)
				}
				return // 错误分支已经核验；继续正常断言会混淆哪条契约失败。
			}
			if err != nil {
				t.Fatalf("ParseCents(%q): unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseCents(%q): got cents=%d; want cents=%d", tc.input, got, tc.want)
			}
		})
	}
}

// TestDecodePaymentTable 验证请求边界；错误值的链和成功的业务值都在当前测试 goroutine 断言。
func TestDecodePaymentTable(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"valid", `{"id":"order-42","amount":"12.99"}`, true},
		{"number_not_string", `{"id":"order-42","amount":12.99}`, false},
		{"zero_payment", `{"id":"order-42","amount":"0.00"}`, false},
		{"unknown_field", `{"id":"order-42","amount":"12.99","amout":"13.00"}`, false},
		{"second_document", `{"id":"order-42","amount":"12.99"}{}`, false},
		{"empty_id", `{"id":"","amount":"12.99"}`, false},
		{"bad_json", `{"id":`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := order.DecodePayment([]byte(tc.raw))
			if !tc.ok {
				if !errors.Is(err, order.ErrInvalidOrder) {
					t.Fatalf("input=%q: err=%v; want ErrInvalidOrder", tc.raw, err)
				}
				return
			}
			if err != nil || got.ID != "order-42" || got.Cents != 1299 {
				t.Fatalf("decoded=%+v err=%v; want order-42 with 1299 cents", got, err)
			}
		})
	}
}

var amountGrammar = regexp.MustCompile(`^(0|[1-9][0-9]{0,6})\.[0-9]{2}$`)

// decimalOracle 只用于 fuzz 判定：用正则描述批准语法，再用 math/big 的有理数做独立精确计算。
// 它不用被测循环或 FormatCents 生成 expected；这样实现和断言不共享同一种逐位累加错误。
func decimalOracle(text string) (int64, bool) {
	if !amountGrammar.MatchString(text) {
		return 0, false
	}
	rational, ok := new(big.Rat).SetString(text)
	if !ok {
		return 0, false
	}
	rational.Mul(rational, big.NewRat(100, 1))
	if !rational.IsInt() || !rational.Num().IsInt64() {
		return 0, false
	}
	cents := rational.Num().Int64()
	return cents, cents >= 0 && cents <= order.MaxCents
}

// FuzzParseCents 默认 go test 只跑这些 seed；加 -fuzz 才启用随机变异和覆盖引导。
// 时间和 worker 数由命令限制，fuzz 成功只表示这次有限探索没有找到反例。
func FuzzParseCents(f *testing.F) {
	for _, seed := range []string{"0.00", "0.01", "12.99", "1000000.00", "1000000.01", "01.00", "1e2.00", "", "１２.００", "\x00.00"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		want, valid := decimalOracle(input)
		got, err := order.ParseCents(input)
		if !valid {
			if !errors.Is(err, order.ErrInvalidAmount) {
				t.Fatalf("invalid input=%q accepted as %d; err=%v", input, got, err)
			}
			return
		}
		if err != nil || got != want {
			t.Fatalf("input=%q: cents=%d err=%v; independent rational want=%d", input, got, err, want)
		}
		// round-trip 是辅助性质；真正的金额数值证明来自上面的独立有理数 oracle。
		canonical, ok := order.FormatCents(got)
		if !ok || canonical != input {
			t.Fatalf("accepted canonical input=%q formatted=%q ok=%t", input, canonical, ok)
		}
	})
}
