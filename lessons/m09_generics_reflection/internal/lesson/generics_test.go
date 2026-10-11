package lesson_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"go-demo/lessons/m09_generics_reflection/internal/lesson"
)

// TestResultBusinessFacts 以固定业务金额与调用次数验证成功/失败边界，不复写 Map 算法。
// 失败值必须是输出类型零值，映射函数不能产生意外副作用，原始错误身份必须保留。
func TestResultBusinessFacts(t *testing.T) {
	orders := lesson.SampleOrders()
	amounts := lesson.MapSlice(orders, func(order lesson.Order) lesson.Cents { return order.Amount })
	if got := lesson.Sum(amounts); got != lesson.Cents(4200) {
		t.Fatalf("业务总金额=%d，want=4200", got)
	}
	text := lesson.Success(orders[0]).Map(func(order lesson.Order) string { return order.Customer })
	if text.Value != "alice" || !text.OK() {
		t.Fatalf("成功结果=%+v", text)
	}
	if reflect.TypeFor[lesson.Result[lesson.Order]]() == reflect.TypeFor[lesson.Result[string]]() {
		t.Fatal("不同实参的实例化类型不应具有同一运行时 Type 身份")
	}
	original := errors.New("not found")
	calls := 0
	failure := lesson.Failure[lesson.Order](original).Map(func(order lesson.Order) string {
		calls++
		return order.Customer
	})
	if calls != 0 || failure.Value != "" || !errors.Is(failure.Err, original) {
		t.Fatalf("失败映射边界：calls=%d result=%+v", calls, failure)
	}
}

// TestUniqueConcreteKeys 验证保持首次出现的顺序；不是 map 遍历顺序的偶然观察。
func TestUniqueConcreteKeys(t *testing.T) {
	got := lesson.Unique([]lesson.OrderID{102, 101, 102, 103, 101})
	if !slices.Equal(got, []lesson.OrderID{102, 101, 103}) {
		t.Fatalf("去重顺序=%v", got)
	}
	// any 满足 comparable 不证明其动态值都可做 key；只需一个 []int 就会 panic。
	if caught := lesson.CatchPanic(func() { lesson.Unique([]any{[]int{1}}) }); caught == nil {
		t.Fatal("动态 slice 作为 map key 应被运行时拒绝")
	}
}

// TestCloneShallowBoundary 检查标准库对外契约：命名切片身份保留，但指针元素仍共享。
func TestCloneShallowBoundary(t *testing.T) {
	type Orders []lesson.Order
	source := Orders(lesson.SampleOrders())
	copied := slices.Clone(source)
	copied[0].Customer = "bob"
	if reflect.TypeOf(copied) != reflect.TypeOf(source) || source[0].Customer != "alice" {
		t.Fatal("Clone 应保留切片类型且复制值元素")
	}
	pointerSource := []*lesson.Order{&source[0]}
	pointerCopy := slices.Clone(pointerSource)
	pointerCopy[0].Customer = "shared"
	if source[0].Customer != "shared" {
		t.Fatal("Clone 不应被误解为指针对象的深复制")
	}
}
