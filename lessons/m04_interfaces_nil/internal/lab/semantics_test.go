package lab_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"go-demo/lessons/m04_interfaces_nil/internal/adapter"
	"go-demo/lessons/m04_interfaces_nil/internal/domain"
	"go-demo/lessons/m04_interfaces_nil/internal/lab"
	"go-demo/lessons/m04_interfaces_nil/internal/usecase"
)

// TestTypedNilBoundaries 从 caller 视角同时检查分支事实和动态值，不只检查显示文本。
// 比较具体指针和 nil 是安全的；fmt 输出为 <nil> 不改变 broken != nil 的事实。
func TestTypedNilBoundaries(t *testing.T) {
	broken := domain.ValidateBad("ORDER-1001")
	if broken == nil || fmt.Sprint(broken) != "<nil>" {
		t.Fatalf("bad boundary did not reproduce deceptive result: %v", broken)
	}
	inner, ok := broken.(*domain.DomainError)
	if !ok || inner != nil {
		t.Fatalf("want typed nil DomainError, got type=%T value=%v", broken, broken)
	}
	if err := domain.ValidateFixed("ORDER-1001"); err != nil {
		t.Fatalf("fixed success returned a non-nil interface: %v", err)
	}
	var invalid *domain.DomainError
	if err := domain.ValidateFixed(""); !errors.As(err, &invalid) || invalid == nil || invalid.Code != "ORDER_ID_REQUIRED" {
		t.Fatalf("fixed failure lost its business error: %v", err)
	}

	repo, err := adapter.NewRepository("../../testdata/orders.json")
	if err != nil {
		t.Fatal(err)
	}
	brokenRepo := lab.BadOptionalRepository(false, repo)
	if brokenRepo == nil {
		t.Fatal("bad repository boundary should retain the dynamic pointer type")
	}
	if message := lab.ObservePanic(func() { _, _ = brokenRepo.Find(context.Background(), "ORDER-1001") }); message == "" {
		t.Fatal("nil repository was expected to fail at dereference")
	}
	// 这不是赞成接受坏依赖，而是锁定通用接口 == nil 检查的能力边界。
	unsafeService, err := usecase.NewOrderService(brokenRepo, adapter.NewLogger())
	if err != nil || unsafeService == nil {
		t.Fatalf("interface nil comparison unexpectedly rejected typed nil: %v", err)
	}
	if lab.ObservePanic(func() { _, _ = unsafeService.Read(context.Background(), "ORDER-1001") }) == "" {
		t.Fatal("accepted typed nil must still fail when the dependency is used")
	}
	if lab.FixedOptionalRepository(false, repo) != nil || lab.FixedOptionalRepository(true, nil) != nil {
		t.Fatal("fixed repository boundary did not return a nil interface for missing dependency")
	}
	// 不是永远返回 nil 的假修复：启用的仓储必须仍读取真实文件并返回订单。
	order, err := lab.FixedOptionalRepository(true, repo).Find(context.Background(), "ORDER-1001")
	if err != nil || order.AmountCents != 12900 {
		t.Fatalf("enabled dependency broken by fix: order=%+v err=%v", order, err)
	}
}

// TestMethodSetsAndCopies 断言“直接调用可行”与“实现接口”两种规则，以及复制/共享的区别。
// 各断言检查业务订单状态，避免把接口内部内存布局当语言保证。
func TestMethodSetsAndCopies(t *testing.T) {
	repo, err := adapter.NewRepository("../../testdata/orders.json")
	if err != nil {
		t.Fatal(err)
	}
	value := *repo
	if _, ok := any(value).(usecase.OrderRepository); ok {
		t.Fatal("Repository value unexpectedly satisfies pointer-receiver Find")
	}
	if _, ok := any(&value).(usecase.OrderRepository); !ok {
		t.Fatal("*Repository should satisfy Find")
	}
	if order, err := value.Find(context.Background(), "ORDER-1001"); err != nil || order.ID != "ORDER-1001" {
		t.Fatalf("addressable direct value call should work: %+v %v", order, err)
	}
	snapshot := adapter.Snapshot{Order: domain.Order{ID: "ORDER-1001", Status: "PAID", AmountCents: 12900}}
	var copied usecase.OrderRepository = snapshot
	var shared usecase.OrderRepository = &snapshot
	snapshot.Order.Status = "SHIPPED"
	old, err := copied.Find(context.Background(), "ORDER-1001")
	if err != nil || old.Status != "PAID" {
		t.Fatalf("value interface failed to retain its own copy: %+v %v", old, err)
	}
	current, err := shared.Find(context.Background(), "ORDER-1001")
	if err != nil || current.Status != "SHIPPED" {
		t.Fatalf("pointer interface failed to share the original object: %+v %v", current, err)
	}
	result, err := lab.ReadSnapshot(snapshot.Order)
	if err != nil || result != snapshot.Order {
		t.Fatalf("same business service cannot consume replacement repository: %+v %v", result, err)
	}
}

// TestAnyAssertionsAndComparison 断言失败返回零值、危险路径确实 panic，修复仍实现列表相等业务含义。
// 本测试不把 recover 用作业务类型检查；ObservePanic 只隔离这两个显式危险操作。
func TestAnyAssertionsAndComparison(t *testing.T) {
	order := domain.Order{ID: "ORDER-1001", Status: "PAID", AmountCents: 12900}
	if got, ok := lab.AssertOrder(order); !ok || got != order {
		t.Fatalf("value assertion failed: %+v %t", got, ok)
	}
	if got, ok := lab.AssertOrder(&order); ok || got != (domain.Order{}) {
		t.Fatalf("pointer must not silently become value: %+v %t", got, ok)
	}
	if lab.ObservePanic(func() { _ = lab.MustOrder(&order) }) == "" {
		t.Fatal("single-result assertion should panic for *Order -> Order")
	}
	var absent *domain.Order
	if lab.Describe(nil) != "nil interface" || lab.Describe(absent) != "typed nil *Order" {
		t.Fatal("type switch confused nil interface with typed nil pointer")
	}
	left, right := any([]string{order.ID}), any([]string{order.ID})
	if left == nil {
		t.Fatal("boxed slice should retain its dynamic type")
	}
	if lab.ObservePanic(func() { _ = lab.EqualAny(left, right) }) == "" {
		t.Fatal("same uncomparable dynamic slice types should panic when compared")
	}
	if !lab.EqualOrderIDs([]string{order.ID}, []string{order.ID}) || lab.EqualOrderIDs([]string{order.ID}, []string{"ORDER-1002"}) {
		t.Fatal("typed comparison lost the intended order-ID equality semantics")
	}
	if !lab.EqualOrderIDs(nil, []string{}) {
		t.Fatal("our list contract defines nil and empty as equal zero-order lists")
	}
}

// TestEmbeddingBinding 直接检查真实日志副作用，证明内层内部调用没有 Java 虚方法覆写语义。
// 修复要求调用明确的 LogSink 参数；它应在同一 Logger 里留下 outer 前缀。
func TestEmbeddingBinding(t *testing.T) {
	logger := adapter.NewLogger()
	outer := &lab.Outer{BaseStep: &lab.BaseStep{Logger: logger}}
	outer.Log("direct")
	outer.Run()
	lab.RunWith(outer)
	want := []string{"outer:direct", "inner.run", "outer:explicit.run"}
	if !slices.Equal(logger.Entries(), want) {
		t.Fatalf("actual method binding differs: got=%v want=%v", logger.Entries(), want)
	}
	var zero lab.Combined
	if lab.ObservePanic(func() { zero.Log("lost") }) == "" {
		t.Fatal("zero embedded pointer should not magically create a logger")
	}
	repo, err := adapter.NewRepository("../../testdata/orders.json")
	if err != nil {
		t.Fatal(err)
	}
	if combined, err := lab.NewCombined(nil, repo); combined != nil || err == nil {
		t.Fatalf("constructor accepted nil embedded pointer: combined=%v err=%v", combined, err)
	}
	combined, err := lab.NewCombined(logger, repo)
	if err != nil {
		t.Fatal(err)
	}
	if combined.Logger.Name() != "memory-log" || combined.Repository.Name() != "file:../../testdata/orders.json" {
		t.Fatal("explicit selectors must preserve both conflicting method meanings")
	}
	if got, err := combined.Find(context.Background(), "ORDER-1001"); err != nil || got.AmountCents != 12900 {
		t.Fatalf("promoted repository method failed: %+v %v", got, err)
	}
}

// TestFaultBoundaryDoesNotResumeAction 解释 recover 的返回位置，而非只记录“没崩溃”。
// after 表示故障语句之后是否继续；恢复后应返回 ObservePanic 的 caller，action 剩余语句不会执行。
func TestFaultBoundaryDoesNotResumeAction(t *testing.T) {
	after := false
	message := lab.ObservePanic(func() {
		_ = lab.MustOrder(&domain.Order{ID: "ORDER-1001"})
		after = true
	})
	if message == "" || after {
		t.Fatalf("unexpected recover path: message=%q resumed=%t", message, after)
	}
}
