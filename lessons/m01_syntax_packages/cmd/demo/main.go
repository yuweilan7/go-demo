// package main 与 func main 构成可执行入口；文件名 main.go 只是惯例。
package main

import (
	"fmt"
	"os"
	"strconv"

	"go-demo/lessons/m01_syntax_packages/input"
	"go-demo/lessons/m01_syntax_packages/order"
	"go-demo/lessons/m01_syntax_packages/service"
)

// main 是所有 init 完成后由 Go 调用的入口。课程不接受外部参数，不创建服务或 goroutine。
// run 返回实验失败时，入口输出诊断并退出 1；成功时正常返回，没有待关闭连接。
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "实验失败：", err)
		os.Exit(1)
	}
}

// run 是按 L00~L03 顺序组织输出的 caller；返回值交给 main，而不是吞掉 assertion error。
// 每个 Store 都是本次实验专用对象，进程结束后由 Go 管理内存，没有文件/端口/数据库副作用。
func run() error {
	fmt.Printf("[L00 main] names=%v\n", startupNames)
	// 独立核对启动后的业务可见状态，不只依赖几条 init 日志看起来“像是成功”。
	if len(startupNames) != 1 || startupNames[0] != "demo-validator" {
		return fmt.Errorf("L00 空白导入应在 main 前恰好注册一次 demo-validator")
	}
	fmt.Println("\n[L01] 请求文本 → 校验 → 出库记录")
	// Go 的局部 var 也立即有零值；Java 局部变量的确定赋值规则不能直接套用。
	var count int
	var text string
	var enabled bool
	var initialErr error
	fmt.Printf("[L01 zero] count=%d text=%q enabled=%v err=%v status=%s\n", count, text, enabled, initialErr, order.Unknown)

	raw := `{"id":"ORD-100","status":"paid","quantity":"2","unit_cents":"1250"}`
	parsed, err := input.Parse(raw) // 本函数体第一次声明 parsed 和 err。
	if err != nil {
		return err
	}
	var store service.Store
	// shipper 是同块新变量，因此此处 := 复用原有 err；err 的绑定没有变化。
	shipper, err := service.New(&store, os.Stdout)
	if err != nil {
		return err
	}
	// 函数声明不执行函数；这里只将函数值保存到 policy，Ship/Validate 才会调用它。
	var policy order.Policy = allowSmallOrder
	receipt, err := shipper.Ship(parsed, policy)
	if err != nil {
		return err
	}
	fmt.Printf("[L01 accepted] id=%s quantity=%d total_cents=%d records=%d\n", receipt.ID, receipt.Quantity, receipt.TotalCents, len(store.Snapshot()))
	if receipt.TotalCents != 2500 || len(store.Snapshot()) != 1 {
		return fmt.Errorf("L01 应只有一次出库，总价必须为 2500 分")
	}

	// 三段式 for 的 i 只在循环内可见；i++ 是语句，不能当作 Java 的赋值表达式使用。
	// 同一组业务订单按 slice 索引顺序处理，不依赖 map 遍历顺序。
	invalid := []order.Order{
		{ID: "ORD-100", Status: order.Paid, Quantity: 0, UnitCents: 1250},
		{ID: "ORD-101", Status: order.Created, Quantity: 2, UnitCents: 1250},
		{ID: "ORD-102", Status: order.Status(99), Quantity: 2, UnitCents: 1250},
	}
	for i := 0; i < len(invalid); i++ {
		// E_if 是新的 if 初始化变量，遮住函数体的 err，但失败立即在此分支处理并检查。
		// 出了整个 if，它不可见；不能依赖它自动更新外层 err。
		if _, err := shipper.Ship(invalid[i], policy); err == nil {
			return fmt.Errorf("L01 危险订单 %s 被接受", invalid[i].ID)
		} else {
			fmt.Printf("[L01 rejected] id=%s err=%v records=%d\n", invalid[i].ID, err, len(store.Snapshot()))
		}
	}
	if len(store.Snapshot()) != 1 {
		return fmt.Errorf("L01 拒绝后仓库仍应只有最初那条记录")
	}

	if err := compareScope(invalid[0]); err != nil {
		return err
	}
	return numericAndAssignment()
}

// allowSmallOrder 由 Validate 通过函数值调用，返回额外策略结果给 Validate。
// o 是订单值副本，函数只读它；失败不会在任何地方写仓库。
func allowSmallOrder(o order.Order) error {
	if o.Quantity > 10 {
		return fmt.Errorf("演示仓一次最多出库 10 件")
	}
	return nil
}

// compareScope 由 run 调用，对同一份零数量订单分别创建独立仓库，避免坏记录污染修复观察。
// 返回值是实验是否证明了预定行为；危险入口的 nil error 本身绝不代表业务正确。
func compareScope(o order.Order) error {
	fmt.Println("\n[L02] 同名 err 的三个路径")
	var badStore, assignedStore, goodStore service.Store
	bad, err := service.New(&badStore, os.Stdout)
	if err != nil {
		return err
	}
	badReceipt, badErr := bad.ShipShadowed(o)
	fmt.Printf("[L02 bad] caller_err=%v quantity=%d records=%d\n", badErr, badReceipt.Quantity, len(badStore.Snapshot()))
	// 这里断言坏版确实发生了危险副作用。它是反例的成立条件，不是生产应满足的规则。
	if badErr != nil || len(badStore.Snapshot()) != 1 || badReceipt.Quantity != 0 {
		return fmt.Errorf("L02 未复现内层 err 非 nil、外层 err nil 且非法出库")
	}
	assigned, err := service.New(&assignedStore, os.Stdout)
	if err != nil {
		return err
	}
	_, assignedErr := assigned.ShipAssigned(o)
	good, err := service.New(&goodStore, os.Stdout)
	if err != nil {
		return err
	}
	_, goodErr := good.Ship(o)
	fmt.Printf("[L02 fixed =] caller_err=%v records=%d\n", assignedErr, len(assignedStore.Snapshot()))
	fmt.Printf("[L02 fixed return] caller_err=%v records=%d\n", goodErr, len(goodStore.Snapshot()))
	if assignedErr == nil || goodErr == nil || len(assignedStore.Snapshot()) != 0 || len(goodStore.Snapshot()) != 0 {
		return fmt.Errorf("L02 修复后必须拒绝非法订单且没有出库记录")
	}
	return nil
}

// numericAndAssignment 由 run 最后调用，展示协议数值边界与多重赋值，不刷位运算。
// ParseUint 的 error 只属于此函数的 if；编译期常量溢出另放显式故障入口。
func numericAndAssignment() error {
	fmt.Println("\n[L03] 数值和多重赋值")
	fmt.Printf("[L03 widths] int=%d位 int64=固定64位 uint8=0..255\n", strconv.IntSize)
	const maximum = 255
	var safe uint8 = maximum // 无类型常量在赋值时检查能否由 uint8 表示。
	var incoming uint16 = 256
	narrowed := uint8(incoming) // 运行时转换不自动报错，截断后得到 0。
	fmt.Printf("[L03 conversion] constant=%d runtime uint16(256)->uint8=%d\n", safe, narrowed)
	if _, err := strconv.ParseUint("256", 10, 8); err != nil {
		fmt.Printf("[L03 checked] 256 被 8 位输入边界拒绝：%v\n", err)
	} else {
		return fmt.Errorf("L03 256 不应通过 8 位校验")
	}
	left, right := "ORD-100", "ORD-101"
	left, right = right, left // 先求右侧两个旧值，再从左到右赋值，不需要临时变量。
	fmt.Printf("[L03 assignment] left=%s right=%s\n", left, right)
	// named result 看签名就能认出返回两个值；这里结果按位置接收，caller 名字不必与 callee 一样。
	net, remainder := splitCents(2500)
	fmt.Printf("[L03 named] whole_yuan=%d remainder_cents=%d\n", net, remainder)
	if left != "ORD-101" || right != "ORD-100" || net != 25 || remainder != 0 || narrowed != 0 {
		return fmt.Errorf("L03 赋值/转换结果与输入边界不符")
	}
	fmt.Println("[DONE] 正常路径与危险/修复对照断言全部成立")
	return nil
}

// splitCents 由 numericAndAssignment 调用。具名结果进入函数时都为 0，可像局部变量一样赋值。
// / 与 % 对整数计算，不产生浮点分；本函数输入为非负分，金额业务校验在 order 层。
func splitCents(cents int64) (wholeYuan int64, remainderCents int64) {
	wholeYuan, remainderCents = cents/100, cents%100
	return wholeYuan, remainderCents
}
