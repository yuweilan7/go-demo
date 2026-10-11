package orderbackend

import (
	"errors"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// TestInputBoundary直接测HTTP使用的同一个解码器，不接入数据库；JSON成功与业务
// 成功是两层，null/0可以解码但Command.Validate仍拒绝。失败候选不会成为持久订单。
func TestInputBoundary(t *testing.T) {
	for _, raw := range []string{`{"sku":"sku-m37","quantity":1,"quantity":2}`, `{"SKU":"sku-m37","quantity":1}`, `{"sku":"sku-m37","quantity":1,"owner":"bob"}`, `{"sku":"sku-m37","quantity":1} {}`, `{"sku":"sku-m37"}`, `{"sku":"sku-m37","quantity":1.5}`} {
		if _, e := DecodeInput([]byte(raw)); e == nil {
			t.Errorf("意外接受 %s", raw)
		}
	}
	for _, raw := range []string{`{"sku":"sku-m37","quantity":null}`, `{"sku":"sku-m37","quantity":0}`} {
		input, e := DecodeInput([]byte(raw))
		if e != nil {
			t.Fatal(e)
		}
		if e = (Command{Owner: "alice", Key: "key", RequestID: "req", Input: input}).Validate(); !errors.Is(e, ErrInvalid) {
			t.Fatal("缺失/null/0不可被默认为1")
		}
	}
	input, e := DecodeInput([]byte(`{"sku":"sku-m37","quantity":2}`))
	if e != nil || input.Quantity != 2 {
		t.Fatalf("正常具体整数: %v", e)
	}
}

// TestRecoverDuplicatePriority构造cause树，验证实际Create失败出口的分类和身份保留。
// 构造MySQLError是辅助分类证据，不冒称这里发生真实rollback/Commit网络失败。
func TestRecoverDuplicatePriority(t *testing.T) {
	c := Command{Owner: "alice", Key: "k", RequestID: "r", Input: Input{SKU: SKU, Quantity: 1}}
	o := Order{ID: OrderID(c.Owner, c.Key), Owner: c.Owner, Key: c.Key, SKU: SKU, Quantity: 1}
	duplicate := &mysql.MySQLError{Number: 1062, Message: "synthetic duplicate"}
	for _, tc := range []struct {
		name    string
		cause   error
		queries int
		recover bool
	}{
		{"safe-1062", duplicate, 1, true},
		{"1062-cleanup", errors.Join(duplicate, ErrCleanup), 0, false},
		{"1062-unknown", errors.Join(duplicate, ErrUnknown), 0, false},
		{"1062-both", errors.Join(duplicate, ErrCleanup, ErrUnknown), 0, false},
		{"ordinary", ErrStock, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, recovered, e := RecoverDuplicate(c, tc.cause, func() (Order, bool, error) { calls++; return o, true, nil })
			if calls != tc.queries || recovered != tc.recover {
				t.Fatalf("calls=%d recovered=%t err=%v", calls, recovered, e)
			}
			if recovered {
				if e != nil || got.ID != o.ID {
					t.Fatal("未返回原单")
				}
			} else if e != tc.cause {
				t.Fatal("严重cause未原样保留")
			}
		})
	}
	queryFailure := errors.New("query failure")
	_, _, e := RecoverDuplicate(c, duplicate, func() (Order, bool, error) { return Order{}, false, queryFailure })
	if !errors.Is(e, duplicate) || !errors.Is(e, queryFailure) {
		t.Fatal("查询失败丢cause")
	}
	o.Quantity = 2
	_, _, e = RecoverDuplicate(c, duplicate, func() (Order, bool, error) { return o, true, nil })
	if !errors.Is(e, duplicate) || !errors.Is(e, ErrConflict) {
		t.Fatal("payload冲突丢cause")
	}
}

// TestIDsAndValues验证规范ID作用域和值复制。ID不包含requestID，重试跟踪号不同
// 仍是同一订单；owner改变必须是另一订单，绝不能只以key为全局幂等命名空间。
func TestIDsAndValues(t *testing.T) {
	if OrderID("alice", "k") == OrderID("bob", "k") || OrderID("alice", "k") != OrderID("alice", "k") {
		t.Fatal("ID scope不成立")
	}
	a := Order{ID: OrderID("alice", "k"), Quantity: 1, Version: 1}
	copy := a
	copy.Quantity = 2
	if a.Quantity != 1 {
		t.Fatal("值副本被污染")
	}
	if EventFor(a).EventID != EventFor(copy).EventID {
		t.Fatal("事件ID不应因非法重投payload而被重生成")
	}
}
