package lesson

import (
	"encoding/json"
	"slices"
)

// collectionsExperiment 由 Run 调用，检查空值边界和 map 的合法读写模型。
// rawKeys 是本次运行的观察，sortedKeys 才是适合日志/签名/断言的稳定业务顺序。
func collectionsExperiment(t *transcript) error {
	t.log("\n[E05] nil/空 slice、nil map、两值查找、无序遍历、map 元素副本")
	var nilSlice []string
	empty := make([]string, 0)
	nilJSON, err := json.Marshal(nilSlice)
	if err != nil {
		return err
	}
	emptyJSON, err := json.Marshal(empty)
	if err != nil {
		return err
	}
	t.log("nil slice: nil=%v len/cap=%d/%d JSON=%s; 空 slice: nil=%v len/cap=%d/%d JSON=%s", nilSlice == nil, len(nilSlice), cap(nilSlice), nilJSON, empty == nil, len(empty), cap(empty), emptyJSON)
	nilSlice = append(nilSlice, "A") // append 负责必要的分配，必须接返回的描述值。
	t.log("append nil slice: %v nil=%v", nilSlice, nilSlice == nil)
	if err := require(string(nilJSON) == "null" && string(emptyJSON) == "[]" && len(nilSlice) == 1, "E05: encoding/json 的 nil/空契约与 append 结果"); err != nil {
		return err
	}

	var nilMap map[string]int
	missing, exists := nilMap["A"]
	delete(nilMap, "A") // nil map 上 len/range/delete/clear 均安全；写入才要求初始化。
	clear(nilMap)
	iterations := 0
	for range nilMap {
		iterations++
	}
	t.log("nil map 读/删/遍历: value=%d ok=%v len=%d iterations=%d", missing, exists, len(nilMap), iterations)
	nilMap = make(map[string]int)
	nilMap["A"] = 0
	zero, zeroExists := nilMap["A"]
	absent, absentExists := nilMap["missing"]
	t.log("make 后写入 0: A=(%d,%v) missing=(%d,%v)", zero, zeroExists, absent, absentExists)
	if err := require(zeroExists && !absentExists && zero == absent && iterations == 0, "E05: 不能用 value==0 判断键是否存在"); err != nil {
		return err
	}

	stock := map[string]int{"A": 5, "B": 0, "C": 7}
	alias := stock // map 值被复制，但两者引用同一 map；通过 alias 写入可被 stock 读取。
	alias["A"] = 6
	rawKeys := make([]string, 0, len(stock))
	for key := range stock {
		rawKeys = append(rawKeys, key)
	}
	sortedKeys := slices.Clone(rawKeys)
	slices.Sort(sortedKeys)
	t.log("map 别名 A=%d; 本次 rawKeys=%v（顺序无保证）; 排序后=%v", stock["A"], rawKeys, sortedKeys)
	if err := require(stock["A"] == 6 && slices.Equal(sortedKeys, []string{"A", "B", "C"}), "E05: map 别名共享，排序结果覆盖所有键"); err != nil {
		return err
	}

	bySKU := map[string]Product{"A": NewInventory()[0]}
	// 以下两行若取消注释，会在编译时拒绝；map index 不可取址，也不能改其 struct 字段。
	// address := &bySKU["A"]       // invalid operation: cannot take address of bySKU["A"]
	// bySKU["A"].Stock = 8          // cannot assign to struct field ... in map
	p, ok := bySKU["A"]
	if !ok {
		return require(false, "E05: 合成数据缺少 A")
	}
	p.Stock = 8 // p 是 Product 副本，这一句尚未回写 map。
	t.log("map struct 读出后只改局部: p.Stock=%d map.Stock=%d", p.Stock, bySKU["A"].Stock)
	p.Tags[0] = "map 副本共享标签" // p.Tags 仍指向原数组，这一步已经改变 map 中对象可见的内容。
	bySKU["A"] = p           // 显式把 scalar 修改后的整个 Product 写回 map。
	t.log("显式回写后: map.Stock=%d map.Tags=%v", bySKU["A"].Stock, bySKU["A"].Tags)
	if err := require(bySKU["A"].Stock == 8 && bySKU["A"].Tags[0] == "map 副本共享标签", "E05: struct 回写与嵌套共享应区分"); err != nil {
		return err
	}
	t.log("E05 PASS：map[string]*Product 可取对象地址但会增加对象共享，不是默认修复")
	return nil
}

// nilMapFailure 只有 -case nil-map 才会调用。这是一次有限步骤的真实 panic 入口。
// defer 注册清理日志；同一 goroutine 展开调用栈时执行，然后 panic 继续交给 runtime 结束进程。
// 此函数刻意不 recover，让故障的退出状态和栈可观察；正常入口不走这里。
func nilMapFailure(t *transcript) {
	defer t.log("E05 故障清理: defer 已执行；没有外部资源需要关闭")
	var stock map[string]int
	t.log("E05 故障前: nil map len=%d；下一句尝试写 A=5", len(stock))
	stock["A"] = 5
	t.log("不可达：panic 之后不会从下一句继续")
}
