package lesson

import (
	"context"
	"errors"
	"io"
	"testing"

	v1 "go-demo/lessons/m31_grpc_protobuf/gen/compatv1"
	v2 "go-demo/lessons/m31_grpc_protobuf/gen/compatv2"
	"google.golang.org/protobuf/proto"
)

// TestRealRPCInventoryAndCleanup 从公开 Run 进入真实 TCP 协议，不用 fake client/server。
// 测试断言最终库存守恒、超时/取消后的服务端写入事实、流完成和连接释放，不把 Ready 当业务验收。
func TestRealRPCInventoryAndCleanup(t *testing.T) {
	report, err := Run(context.Background(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if report.FinalStock != 12 || report.ConflictAvailable != 18 {
		t.Fatalf("库存/冲突事实错误: %+v", report)
	}
	// 三项不是同一个错误码的复述：它们检查服务端真实副作用是否发生以及重试是否双扣。
	if report.LostContextWrites != 1 || report.PropagatedWrites != 0 || report.CommittedWrites != 2 {
		t.Fatalf("取消或重复写事实错误: %+v", report)
	}
	if report.StreamReceived != 2 || report.BlockedSend < 1 || !report.ConnectionReleased {
		t.Fatalf("流或资源最终状态错误: %+v", report)
	}
}

// TestCanceledOwnerReleasesRealResources 验证 owner 已取消的失败入口：实际 listener/Conn 仍创建后收尾。
// 它不验证库存副作用，专门检查 waitReady 的错误保留原因、Run defer 和连接释放结果。
func TestCanceledOwnerReleasesRealResources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := Run(ctx, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应保留 owner 取消原因，实际 %v", err)
	}
	if !report.ConnectionReleased {
		t.Fatal("取消失败路径未释放真实 ClientConn/Server/listener")
	}
}

// TestUnknownFieldRoundTrip 使用与 demo 不同的 owner 分支和金额，独立验证旧版本中转不抹掉未知字段。
// 另给 DiscardUnknown 反例，明确“未知字段默认保留”不是所有解码配置的保证。
func TestUnknownFieldRoundTrip(t *testing.T) {
	source := &v2.Record{Sku: "0009", Quantity: 7, Owner: &v2.Record_WarehouseId{WarehouseId: "wh-01"}, PriceMinor: 9007199254740993, Currency: "CNY", DiscountBasisPoints: proto.Int32(0)}
	wire, err := proto.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	old := new(v1.Record)
	if err := proto.Unmarshal(wire, old); err != nil {
		t.Fatal(err)
	}
	forwarded, err := proto.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	restored := new(v2.Record)
	if err := proto.Unmarshal(forwarded, restored); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(source, restored) {
		t.Fatalf("已知+未知字段未保真: %v", restored)
	}
	// 金额超过 float64 可逐整数精确表示的范围，仍用 int64 完整保留，证明本契约没有中途转浮点。
	if restored.GetPriceMinor() != 9007199254740993 || restored.GetSku() != "0009" {
		t.Fatal("金额精度/字符串 ID 前导零丢失")
	}
	discarded := new(v1.Record)
	if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(wire, discarded); err != nil {
		t.Fatal(err)
	}
	if len(discarded.ProtoReflect().GetUnknown()) != 0 {
		t.Fatal("DiscardUnknown 配置未生效")
	}
	cleaned, err := proto.Marshal(discarded)
	if err != nil {
		t.Fatal(err)
	}
	lost := new(v2.Record)
	if err := proto.Unmarshal(cleaned, lost); err != nil {
		t.Fatal(err)
	}
	if lost.Owner != nil || lost.DiscountBasisPoints != nil || lost.GetPriceMinor() != 0 {
		t.Fatal("旧读者丢弃 unknown 后不应还保留新字段")
	}
}
