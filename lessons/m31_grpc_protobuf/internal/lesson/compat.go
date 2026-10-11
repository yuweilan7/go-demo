package lesson

import (
	"bytes"
	"fmt"
	"io"

	v1 "go-demo/lessons/m31_grpc_protobuf/gen/compatv1"
	v2 "go-demo/lessons/m31_grpc_protobuf/gen/compatv2"
	"google.golang.org/protobuf/proto"
)

// runCompatibility 由 Run 在 RPC 前调用，用真正 protobuf 二进制编码演示两版消息，不用 JSON 冒充 wire。
// 这里的 v1/v2 是本模块两种 descriptor；它们的 protobuf 全名不同，但相同字段编号/类型允许演示编码兼容。
func runCompatibility(out io.Writer) error {
	newMessage := &v2.Record{Sku: "001-SKU", Quantity: 3, DiscountBasisPoints: proto.Int32(0), Owner: &v2.Record_Email{Email: ""}, PriceMinor: 12345, Currency: "CNY"}
	wire, err := proto.Marshal(newMessage)
	if err != nil {
		return err
	}
	oldReader := new(v1.Record)
	if err := proto.Unmarshal(wire, oldReader); err != nil {
		return err
	}
	unknown := len(oldReader.ProtoReflect().GetUnknown())
	if err := check(oldReader.GetSku() == "001-SKU" && oldReader.GetQuantity() == 3 && unknown > 0, "旧读者未读到公共字段/未知字段"); err != nil {
		return err
	}
	// 默认 Unmarshal 保留 unknown；旧读者重新 Marshal 后，新读者还能恢复新字段，不能手写拷贝只保留已知字段。
	relayed, err := proto.Marshal(oldReader)
	if err != nil {
		return err
	}
	restored := new(v2.Record)
	if err := proto.Unmarshal(relayed, restored); err != nil {
		return err
	}
	if err := check(proto.Equal(newMessage, restored), "旧读者中转丢失新字段"); err != nil {
		return err
	}
	missing := &v2.Record{}
	explicitZero := &v2.Record{DiscountBasisPoints: proto.Int32(0), Owner: &v2.Record_Email{Email: ""}}
	// Get 方法方便取零值，却会把 optional nil 与显式 0 读成相同值；存在性必须看指针/反射。
	if err := check(missing.GetDiscountBasisPoints() == explicitZero.GetDiscountBasisPoints() && missing.DiscountBasisPoints == nil && explicitZero.DiscountBasisPoints != nil, "optional presence 未区分"); err != nil {
		return err
	}
	if err := check(missing.Owner == nil && explicitZero.Owner != nil, "oneof 未区分未选分支和空 email"); err != nil {
		return err
	}
	// 普通 proto3 scalar 使用隐式 presence：没有赋值和显式写 0 序列化成相同 wire，接收方无法推断意图。
	implicitMissing, err := proto.Marshal(&v1.Record{})
	if err != nil {
		return err
	}
	implicitZero, err := proto.Marshal(&v1.Record{Quantity: 0})
	if err != nil {
		return err
	}
	if err := check(bytes.Equal(implicitMissing, implicitZero), "普通 scalar 0 应与缺失使用相同编码"); err != nil {
		return err
	}
	legacy := &v1.Record{Sku: "001-SKU", Quantity: 3, LegacyNote: "旧标签"}
	oldWire, err := proto.Marshal(legacy)
	if err != nil {
		return err
	}
	newReader := new(v2.Record)
	if err := proto.Unmarshal(oldWire, newReader); err != nil {
		return err
	}
	if err := check(len(newReader.ProtoReflect().GetUnknown()) > 0, "reserved 的旧字段应由新读者保留为 unknown"); err != nil {
		return err
	}
	// wire 解码成功仍不满足新的业务规则：旧客户端没有 currency/price，不可按“编译成功”宣布可上线。
	if err := check(validatePrice(newReader) != nil && validatePrice(newMessage) == nil, "业务兼容检查未拒绝缺币种旧消息"); err != nil {
		return err
	}
	fmt.Fprintf(out, "[E07] V2→V1→V2 preserved=true unknown_bytes=%d; scalar missing==zero wire; optional missing!=zero; oneof unset!=empty; V1→V2 wire=OK business=REJECT(currency missing); price=12345 minor CNY\n", unknown)
	return nil
}

// validatePrice 是新消费者的业务契约，不由 protobuf 解码器替我们执行；金额按最小货币单位和币种一起解释。
func validatePrice(record *v2.Record) error {
	if record.GetCurrency() != "CNY" || record.GetPriceMinor() < 0 {
		return fmt.Errorf("价格必须指定 CNY 且最小单位金额非负")
	}
	return nil
}
