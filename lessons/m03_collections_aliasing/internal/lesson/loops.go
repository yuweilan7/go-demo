package lesson

import (
	"fmt"
	"slices"
)

// LoopResult 把闭包的调用结果交给日志和独立测试，不暴露内部 func 切片供外部误用。
type LoopResult struct {
	Declared []string
	Assigned []string
	Fixed    []string
}

// ObserveLoops 由 E06 和测试调用，先保存函数，再在整个 range 结束后依次调用。
// 使用同一个 goroutine 明确“创建→结束循环→执行闭包”，不需要调度运气或并发等待。
func ObserveLoops() LoopResult {
	skus := []string{"A", "B", "C"}
	declared := make([]func() string, 0, len(skus))
	for _, sku := range skus {
		// 本 module go 1.27：:= 在 range 子句中声明，每轮有独立 sku 变量。
		// 闭包捕获变量，执行时读变量；这里各轮 sku 不再修改，所以得到 A/B/C。
		declared = append(declared, func() string { return sku })
	}

	assigned := make([]func() string, 0, len(skus))
	var sharedSKU string
	for _, sharedSKU = range skus {
		// = 是给外部已有变量赋值，没有每轮创建新变量；所有函数读同一个 sharedSKU。
		assigned = append(assigned, func() string { return sharedSKU })
	}

	fixed := make([]func() string, 0, len(skus))
	for _, sharedSKU = range skus {
		// 在循环体内创建快照变量，这条写法对旧语言版本也安全。
		// 短声明 snapshot := sharedSKU 的新变量属于本轮执行的块，各闭包各持有一份。
		snapshot := sharedSKU
		fixed = append(fixed, func() string { return snapshot })
	}
	return LoopResult{Declared: callStringFunctions(declared), Assigned: callStringFunctions(assigned), Fixed: callStringFunctions(fixed)}
}

// callStringFunctions 由闭包实验调用。函数调用同步返回，再 append 到独立结果数组。
func callStringFunctions(functions []func() string) []string {
	result := make([]string, 0, len(functions))
	for _, function := range functions {
		result = append(result, function())
	}
	return result
}

// validateSKU 模拟业务校验，故意让 B 失败；调用者必须把本次 error 归属到本次 SKU。
// 这里不做 I/O、不产生资源，无取消分支；error 由 fmt.Errorf 创建后交回调用者。
func validateSKU(sku string) error {
	if sku == "B" {
		return fmt.Errorf("B 缺货")
	}
	return nil
}

// errorCallbacks 同步构造业务回调，展示新循环变量规则没有替外部 err 创建副本。
// 返回 bad/fixed 两组文本，调用者可以独立核对 B 的错误是否保存。
func errorCallbacks() (bad, fixed []string) {
	badFunctions := make([]func() string, 0, 3)
	fixedFunctions := make([]func() string, 0, 3)
	var err error // 整个函数只有这一份 err，后续各轮赋值覆盖它。
	for _, sku := range []string{"A", "B", "C"} {
		err = validateSKU(sku)
		badFunctions = append(badFunctions, func() string {
			if err != nil {
				return err.Error()
			}
			return sku + ":ok"
		})
		currentErr := err // 保存本轮接口值；本例的 error 不包含随后会修改的可变业务对象。
		fixedFunctions = append(fixedFunctions, func() string {
			if currentErr != nil {
				return currentErr.Error()
			}
			return sku + ":ok"
		})
	}
	// 最后一轮 C 成功，外部 err=nil。此时 bad 的三个回调都读取最终 nil，吞掉 B 的失败。
	return callStringFunctions(badFunctions), callStringFunctions(fixedFunctions)
}

// loopsExperiment 由 Run 调用，用确定性状态检查变量、元素、副本中的嵌套对象三层。
func loopsExperiment(t *transcript) error {
	t.log("\n[E06] range、元素地址与闭包：本 module 的语言版本是 go 1.27")
	r := ObserveLoops()
	t.log("循环后调用: := 声明=%v; = 赋值=%v; 循环体快照=%v", r.Declared, r.Assigned, r.Fixed)
	if err := require(slices.Equal(r.Declared, []string{"A", "B", "C"}) && slices.Equal(r.Assigned, []string{"C", "C", "C"}) && slices.Equal(r.Fixed, []string{"A", "B", "C"}), "E06: 当前模块应采用 Go 1.22+ 的声明变量语义"); err != nil {
		return err
	}

	products := NewInventory()
	copyPointers := make([]*Product, 0, len(products))
	for _, value := range products {
		copyPointers = append(copyPointers, &value) // 每轮副本地址，绝不是 &products[i]。
	}
	copyPointers[0].Stock = 100
	copyPointers[0].Tags[0] = "副本仍共享标签"
	t.log("&v: 各轮地址不同=%v 元素地址相同=%v; 改副本 Stock=%d 原 Stock=%d; 原 Tags=%v", copyPointers[0] != copyPointers[1], copyPointers[0] == &products[0], copyPointers[0].Stock, products[0].Stock, products[0].Tags)
	if err := require(products[0].Stock == 5 && products[0].Tags[0] == "副本仍共享标签", "E06: range struct 副本的 scalar 独立，嵌套 slice 仍共享"); err != nil {
		return err
	}
	elementPointers := make([]*Product, 0, len(products))
	for i := range products {
		elementPointers = append(elementPointers, &products[i])
	}
	elementPointers[0].Stock = 101
	t.log("&slice[i]: 地址相同=%v; 原 Stock=%d", elementPointers[0] == &products[0], products[0].Stock)
	if err := require(products[0].Stock == 101, "E06: 元素地址修改必须回写原数组"); err != nil {
		return err
	}
	var reused Product
	reusedPointers := make([]*Product, 0, len(products))
	for _, reused = range products {
		reusedPointers = append(reusedPointers, &reused)
	}
	t.log("= 的 &v: 指针相同=%v; 三个 SKU=%s/%s/%s", reusedPointers[0] == reusedPointers[1], reusedPointers[0].SKU, reusedPointers[1].SKU, reusedPointers[2].SKU)

	sharedObject := &Product{SKU: "shared", Stock: 5}
	objectReaders := make([]func() string, 0, 3)
	for _, object := range []*Product{sharedObject, sharedObject, sharedObject} {
		// object 变量每轮独立，但它们的值是同一个对象地址。新循环规则不复制对象。
		objectReaders = append(objectReaders, func() string { return fmt.Sprintf("%d", object.Stock) })
	}
	sharedObject.Stock = 42
	t.log("独立变量捕获共享 *Product: %v", callStringFunctions(objectReaders))
	out := "before"
	outsideReader := func() string { return out }
	out = "after"
	t.log("捕获外层变量后继续改: %s", outsideReader())
	badErrs, fixedErrs := errorCallbacks()
	t.log("共享外部 err: bad=%v; 每轮保存 currentErr: fixed=%v", badErrs, fixedErrs)
	if err := require(slices.Equal(badErrs, []string{"A:ok", "B:ok", "C:ok"}) && slices.Equal(fixedErrs, []string{"A:ok", "B 缺货", "C:ok"}), "E06: 修复应保留 B 的失败归属"); err != nil {
		return err
	}

	numbers := make([]int, 2, 4)
	copy(numbers, []int{1, 2})
	seen := make([]int, 0, 2)
	for i, value := range numbers {
		seen = append(seen, value)
		if i == 0 {
			numbers[1] = 9               // range 不复制全部底层元素；下一轮从数组读取修改后的值。
			numbers = append(numbers, 3) // 描述值长度改变，不增加本次 range 的迭代次数。
		}
	}
	t.log("range 长度与数据: seen=%v iterations=%d final=%v", seen, len(seen), numbers)
	if err := require(slices.Equal(seen, []int{1, 9}) && len(numbers) == 3, "E06: range 固定原长度，但不是不可变内容快照"); err != nil {
		return err
	}
	t.log("E06 PASS：全程同步，无 goroutine；共享 err 在并发写读时还需同步或改成请求专属状态")
	return nil
}
