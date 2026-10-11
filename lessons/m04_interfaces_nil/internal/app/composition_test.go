package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"go-demo/lessons/m04_interfaces_nil/internal/adapter"
	"go-demo/lessons/m04_interfaces_nil/internal/app"
	"go-demo/lessons/m04_interfaces_nil/internal/domain"
)

// TestRealReadPath 由 go test 调用，从公开组合入口读取真实 JSON 文件。
// 断言业务结果/错误分类/最终日志，不把某个“PASS”字符串当成订单正确性的证明。
func TestRealReadPath(t *testing.T) {
	repo, err := adapter.NewRepository("../../testdata/orders.json")
	if err != nil {
		t.Fatal(err)
	}
	logger := adapter.NewLogger()
	service, err := app.Compose(repo, logger)
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.Read(context.Background(), "ORDER-1001")
	want := domain.Order{ID: "ORDER-1001", Status: "PAID", AmountCents: 12900}
	// 订单字段均可比较；这里比较已知具体类型，而非对任意接口值执行 ==。
	if err != nil || got != want {
		t.Fatalf("real order got=%+v err=%v, want=%+v", got, err, want)
	}
	got.Status = "LOCAL_CHANGE"
	again, err := service.Read(context.Background(), "ORDER-1001")
	// caller 修改返回值不应修改文件内容；第二次真正读取应仍得到 PAID。
	if err != nil || again.Status != "PAID" {
		t.Fatalf("read result leaked mutation: order=%+v err=%v", again, err)
	}
	_, err = service.Read(context.Background(), "ORDER-404")
	var missing *domain.DomainError
	if !errors.As(err, &missing) || missing == nil || missing.Code != "ORDER_NOT_FOUND" || missing.OrderID != "ORDER-404" {
		t.Fatalf("missing order classification lost through wrapper: %v", err)
	}
	expected := []string{
		"order.read.begin id=ORDER-1001", "order.read.success id=ORDER-1001",
		"order.read.begin id=ORDER-1001", "order.read.success id=ORDER-1001",
		"order.read.begin id=ORDER-404", "order.read.failed id=ORDER-404",
	}
	if !slices.Equal(logger.Entries(), expected) {
		t.Fatalf("unexpected synchronous event order: %v", logger.Entries())
	}
	// Entries 返回副本：观察者修改日志快照不能改写服务已记录的历史。
	events := logger.Entries()
	events[0] = "observer changed snapshot"
	if logger.Entries()[0] != expected[0] {
		t.Fatal("logger exposed a mutable internal event slice")
	}
}

// TestValidateAndCancellationBeforeIO 用不存在的路径区分“已停止”与“已尝试读取”。
// 这里没有时序猜测：ctx 在请求之前已经取消，Find 的第一次检查必须直接返回。
func TestValidateAndCancellationBeforeIO(t *testing.T) {
	repo, err := adapter.NewRepository("../../testdata/this-file-does-not-exist.json")
	if err != nil {
		t.Fatal(err)
	}
	logger := adapter.NewLogger()
	service, err := app.Compose(repo, logger)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Read(context.Background(), "")
	var invalid *domain.DomainError
	if !errors.As(err, &invalid) || invalid == nil || invalid.Code != "ORDER_ID_REQUIRED" {
		t.Fatalf("empty id should be rejected before I/O: %v", err)
	}
	if len(logger.Entries()) != 0 {
		t.Fatal("invalid request reached read.begin")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // 本测试是 ctx 的 owner；退出仍调用，覆盖未来提前失败的路径。
	cancel()
	_, err = service.Read(ctx, "ORDER-1001")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled rather than file-not-found: %v", err)
	}
	if !slices.Equal(logger.Entries(), []string{"order.read.begin id=ORDER-1001", "order.read.failed id=ORDER-1001"}) {
		t.Fatalf("canceled request should have no success event: %v", logger.Entries())
	}
}

// TestConcreteDependencyBoundary 证明必需依赖检查发生在转换成接口之前。
// 参数零指针来自坏接线，返回错误属于启动配置失败，不属于订单未找到。
func TestConcreteDependencyBoundary(t *testing.T) {
	repo, err := adapter.NewRepository("../../testdata/orders.json")
	if err != nil {
		t.Fatal(err)
	}
	if service, err := app.Compose(nil, adapter.NewLogger()); service != nil || err == nil {
		t.Fatalf("nil repo accepted: service=%v err=%v", service, err)
	}
	if service, err := app.Compose(repo, nil); service != nil || err == nil {
		t.Fatalf("nil logger accepted: service=%v err=%v", service, err)
	}
}
