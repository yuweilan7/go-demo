package service_test

import (
	"errors"
	"io"
	"testing"

	"go-demo/lessons/m01_syntax_packages/order"
	"go-demo/lessons/m01_syntax_packages/service"
)

// TestShipBusinessFacts 由 go test 调用，从包外检查真实内存记录，不只检查 error 或日志。
// 每项独立仓库，证明合法订单的金额/件数与非法订单的“无出库”不变量。
func TestShipBusinessFacts(t *testing.T) {
	cases := []struct {
		name string
		o    order.Order
		ok   bool
	}{
		{"paid", order.Order{ID: "ORD-100", Status: order.Paid, Quantity: 2, UnitCents: 1250}, true},
		{"zero quantity", order.Order{ID: "ORD-100", Status: order.Paid, Quantity: 0, UnitCents: 1250}, false},
		{"created", order.Order{ID: "ORD-100", Status: order.Created, Quantity: 2, UnitCents: 1250}, false},
		{"cancelled", order.Order{ID: "ORD-100", Status: order.Cancelled, Quantity: 2, UnitCents: 1250}, false},
		{"already shipped", order.Order{ID: "ORD-100", Status: order.Shipped, Quantity: 2, UnitCents: 1250}, false},
		{"unknown code", order.Order{ID: "ORD-100", Status: order.Status(99), Quantity: 2, UnitCents: 1250}, false},
		{"empty ID", order.Order{Status: order.Paid, Quantity: 2, UnitCents: 1250}, false},
		{"negative price", order.Order{ID: "ORD-100", Status: order.Paid, Quantity: 2, UnitCents: -1}, false},
		{"too large price", order.Order{ID: "ORD-100", Status: order.Paid, Quantity: 2, UnitCents: order.MaxUnitCents + 1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var store service.Store
			s, err := service.New(&store, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := s.Ship(c.o)
			records := store.Snapshot()
			if !c.ok {
				// 无出库是业务事实：即使 error 非 nil，也必须检查仓库确实没被提前写入。
				if err == nil || len(records) != 0 || receipt != (service.Receipt{}) {
					t.Fatalf("拒绝事实不成立：receipt=%+v err=%v records=%v", receipt, err, records)
				}
				return
			}
			if err != nil || len(records) != 1 || records[0].ID != "ORD-100" || records[0].Quantity != 2 || records[0].TotalCents != 2500 {
				t.Fatalf("合法出库事实不成立：receipt=%+v err=%v records=%v", receipt, err, records)
			}
			// Snapshot 返回独立结果；外部改写不能倒改已记录的订单金额。
			records[0].TotalCents = 0
			if store.Snapshot()[0].TotalCents != 2500 {
				t.Fatal("观察者改写了仓库内部出库事实")
			}
		})
	}
}

// TestShadowAndBothRepairs 由 go test 调用，同一输入核对坏版和两个修复版本。
// 测试名称标明反例意图：坏版应复现错误，绝不将它当成合法的生产入口。
func TestShadowAndBothRepairs(t *testing.T) {
	o := order.Order{ID: "ORD-100", Status: order.Paid, Quantity: 0, UnitCents: 1250}
	var badStore service.Store
	bad, _ := service.New(&badStore, io.Discard) // 本测试显式提供两个已知非 nil 依赖。
	_, err := bad.ShipShadowed(o)
	if err != nil || len(badStore.Snapshot()) != 1 || badStore.Snapshot()[0].Quantity != 0 {
		t.Fatal("危险版本没有复现吞错后非法出库")
	}
	var fixedStore service.Store
	fixed, _ := service.New(&fixedStore, io.Discard)
	if _, err := fixed.ShipAssigned(o); err == nil || len(fixedStore.Snapshot()) != 0 {
		t.Fatal("赋值修复未阻止非法出库")
	}
	if _, err := fixed.Ship(o); err == nil || len(fixedStore.Snapshot()) != 0 {
		t.Fatal("早返回修复未阻止非法出库")
	}
}

// TestPolicyOrderAndCause 检查可变实参按传入顺序同步执行、失败后停止，以及错误原因没有丢失。
// observed 是本测试专属 slice；闭包读取/更新同一个变量，没有并发访问或后台清理。
func TestPolicyOrderAndCause(t *testing.T) {
	var store service.Store
	s, _ := service.New(&store, io.Discard)
	o := order.Order{ID: "ORD-100", Status: order.Paid, Quantity: 2, UnitCents: 1250}
	observed := ""
	rejected := errors.New("演示仓额度不足")
	first := func(order.Order) error { observed += "A"; return nil }
	second := func(order.Order) error { observed += "B"; return rejected }
	third := func(order.Order) error { observed += "C"; return nil }
	_, err := s.Ship(o, first, second, third)
	if observed != "AB" || !errors.Is(err, rejected) || len(store.Snapshot()) != 0 {
		t.Fatalf("策略边界错误：observed=%q err=%v records=%v", observed, err, store.Snapshot())
	}
	// nil 策略是配置错误，应返回 error 并保持无出库，不靠 recover 掩盖 nil func panic。
	if _, err := s.Ship(o, nil); err == nil || len(store.Snapshot()) != 0 {
		t.Fatal("nil 策略没有被安全拒绝")
	}
}

// TestDependencyBoundary 检查显式注入不完整时 New 返回失败，而不是隐藏到 init 或出库中。
func TestDependencyBoundary(t *testing.T) {
	if _, err := service.New(nil, io.Discard); err == nil {
		t.Fatal("nil 仓库应被拒绝")
	}
	if _, err := service.New(&service.Store{}, nil); err == nil {
		t.Fatal("零值日志接口应被拒绝")
	}
}
