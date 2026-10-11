package input_test

import (
	"testing"

	"go-demo/lessons/m01_syntax_packages/input"
	"go-demo/lessons/m01_syntax_packages/order"
)

// TestInputBoundaries 由 go test 调用，检查真实 JSON 库和整数解析 API 组合后的输入事实。
// 这里没有 mock；数量 255 可以表示、256 不可表示，金额还要由下一层业务校验限额。
func TestInputBoundaries(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"maximum quantity", `{"id":"ORD-100","status":"paid","quantity":"255","unit_cents":"1250"}`, true},
		{"overflow", `{"id":"ORD-100","status":"paid","quantity":"256","unit_cents":"1250"}`, false},
		{"negative quantity", `{"id":"ORD-100","status":"paid","quantity":"-1","unit_cents":"1250"}`, false},
		{"nonnumeric", `{"id":"ORD-100","status":"paid","quantity":"two","unit_cents":"1250"}`, false},
		{"int64 overflow", `{"id":"ORD-100","status":"paid","quantity":"2","unit_cents":"9223372036854775808"}`, false},
		{"JSON number", `{"id":"ORD-100","status":"paid","quantity":2,"unit_cents":"1250"}`, false},
		{"missing field", `{"id":"ORD-100","status":"paid","quantity":"2"}`, false},
		{"unknown status", `{"id":"ORD-100","status":"other","quantity":"2","unit_cents":"1250"}`, false},
		{"malformed", `{"id":"ORD-100"`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := input.Parse(c.raw)
			if !c.ok {
				if err == nil || o != (order.Order{}) {
					t.Fatalf("非法输入未被拒绝：order=%+v err=%v", o, err)
				}
				return
			}
			if err != nil || o.ID != "ORD-100" || o.Status != order.Paid || o.Quantity != 255 || o.UnitCents != 1250 {
				t.Fatalf("输入事实不正确：order=%+v err=%v", o, err)
			}
		})
	}
}
