//go:build faults

// Package faults 只在显式 -tags=faults 时构建；默认 go test 不包含这些预期失败测试。
package faults

import (
	"errors"
	"testing"
	"time"

	"go-demo/lessons/m10_testing_debugging/internal/order"
)

// TestWrongCommitMustNotMutateOnRejection 是 E02 的独立验收断言：拒绝后订单必须仍然待支付。
// 预期 FAIL，失败事实是错误金额返回时 State 已经 paid；所有访问加锁，-race 不会替我们理解业务。
func TestWrongCommitMustNotMutateOnRejection(t *testing.T) {
	store, err := order.NewStore("order-42", 1299)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyPaymentWrong(order.Payment{ID: "order-42", Cents: 1300})
	if !errors.Is(err, order.ErrAmountMismatch) {
		t.Fatalf("error=%v; want rejected mismatching payment", err)
	}
	value, err := store.View("order-42")
	if err != nil {
		t.Fatal(err)
	}
	if value.State != "pending" || value.CreditedCents != 0 || value.CreditCount != 0 {
		t.Fatalf("rejected payment mutated business: %+v; want pending, credit=0, count=0", value)
	}
}

// TestSharedFixtureContaminatesSibling 是 E03：两个 parallel 测试共享同一 Store，锁正确也会污染期望。
// changed 是先后屏障，pay 关闭、expect_pending 接收；失败不依赖 shuffle 恰好排到哪一顺序。
func TestSharedFixtureContaminatesSibling(t *testing.T) {
	shared, err := order.NewStore("order-42", 1299)
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan struct{})
	t.Run("pay", func(t *testing.T) {
		t.Parallel()
		defer close(changed) // 即使下面 Fatal 退出 pay 子测试，也放开观察者，避免诊断变成挂死。
		if _, err := shared.ApplyPayment(order.Payment{ID: "order-42", Cents: 1299}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("expect_pending", func(t *testing.T) {
		t.Parallel()
		select {
		case <-changed:
		case <-time.After(2 * time.Second):
			t.Fatal("pay sibling did not complete within diagnostic limit")
		}
		value, err := shared.View("order-42")
		if err != nil {
			t.Fatal(err)
		}
		if value.State != "pending" {
			t.Fatalf("shared fixture state=%s count=%d; this test wanted an independent pending order", value.State, value.CreditCount)
		}
	})
}
