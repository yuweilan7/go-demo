// Package service 连接校验与仓库写入。业务依赖 order，命令入口依赖 service，order 不反向导入。
package service

import (
	"fmt"
	"io"

	"go-demo/lessons/m01_syntax_packages/order"
)

// Service 的 store 是 caller 拥有的共享仓库地址。out 仅用于实验日志，io.Writer 只需能 Write。
// 接口实现细节留到 M04；这里读签名即可：调用 Printf 时会同步把日志交给这个 writer。
type Service struct {
	store *Store
	out   io.Writer
}

// New 由 cmd/demo 或测试调用，显式传入仓库与日志输出，不能从 init 偷取隐含业务依赖。
// nil 依赖在此拒绝，防止后续出库时才解引用失败。调用方收到普通 error，决定是否继续。
func New(store *Store, out io.Writer) (*Service, error) {
	if store == nil || out == nil {
		return nil, fmt.Errorf("仓库和日志 writer 必须显式提供")
	}
	return &Service{store: store, out: out}, nil
}

// Ship 由 cmd/demo 调用，是修复后的入口。o 来自 input.Parse，checks 来自入口传入的函数值。
// (receipt Receipt, err error) 是两个具名结果变量，进入函数时分别为 Receipt{} 和 nil。
// 名字方便阅读签名；这里仍使用显式 return，避免读者追踪很长的裸 return。
// 本函数不拥有需要 Close 的资源；仓库属于 caller，写入必须发生在全部校验成功之后。
func (s *Service) Ship(o order.Order, checks ...order.Policy) (receipt Receipt, err error) {
	// = 给本函数的具名结果 err 赋值，不创建另一 err；checks... 把 slice 展开成可变实参。
	err = order.Validate(o, checks...)
	if err != nil {
		// %w 保留 Validate 产生的原因；返回零 receipt 说明没有成功出库结果。
		return Receipt{}, fmt.Errorf("出库拒绝：%w", err)
	}
	receipt = Receipt{ID: o.ID, Quantity: o.Quantity, TotalCents: order.TotalCents(o)}
	s.store.save(receipt)
	return receipt, nil
}

// ShipShadowed 是 L02 显式调用的危险版本，正确入口不会调用它。
// 复现的是“看到了错误日志，却把外层 nil 当作成功，并真的写入仓库”。
// 不使用 panic 或 goroutine，运行有界；测试会断言这个坏结果，然后与 Ship 的修复对照。
func (s *Service) ShipShadowed(o order.Order, checks ...order.Policy) (Receipt, error) {
	var err error // E_outer：函数体里的 error 变量，初始为 nil。
	{
		// E_inner：新代码块的 := 新建同名变量；不是更新 E_outer。
		// 当前故意在失败后仅记录日志，缺少 return，这是实际吞错点。
		err := order.Validate(o, checks...)
		fmt.Fprintf(s.out, "[L02 bad] inner_err=%v\n", err)
	}
	// 离开内层块后，err 又指向 E_outer，它从未被赋值，所以仍是 nil。
	if err != nil {
		return Receipt{}, err
	}
	// 因为错误检查读错变量，零数量订单也被当成成功写入；日志不是控制流。
	receipt := Receipt{ID: o.ID, Quantity: o.Quantity, TotalCents: order.TotalCents(o)}
	s.store.save(receipt)
	return receipt, err
}

// ShipAssigned 由 L02 调用，保留内层代码块但把 := 改成 =，展示最小语法修复。
// 它与 Ship 的早返回版本验证同一业务不变量：校验失败时仓库记录数不能增加。
func (s *Service) ShipAssigned(o order.Order, checks ...order.Policy) (Receipt, error) {
	var err error
	{
		err = order.Validate(o, checks...) // 更新外层已有 err，失败原因跨越块边界保留下来。
	}
	if err != nil {
		return Receipt{}, fmt.Errorf("出库拒绝：%w", err)
	}
	receipt := Receipt{ID: o.ID, Quantity: o.Quantity, TotalCents: order.TotalCents(o)}
	s.store.save(receipt)
	return receipt, nil
}
