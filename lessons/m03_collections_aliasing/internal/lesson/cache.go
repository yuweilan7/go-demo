package lesson

import (
	"maps"
	"slices"
)

// coreExperiment 是 10～20 分钟讲读的核心路径，由 Run 同步调用。
// 它先让库存过滤污染别名，再用快照修复，再展示两次 append；输入全部在本函数族内创建。
func coreExperiment(t *transcript) error {
	t.log("\n[E02] 过滤库存：缓存还持有输入时，复用数组会污染缓存")
	input := NewInventory()
	badCache := input // 只是描述值副本；不存在“把 A/B/C 全部复制到缓存”这一步。
	filtered := FilterInPlaceBad(input)
	t.log("错误过滤: filtered SKU=%v; input SKU=%v; cache SKU=%v", skus(filtered), skus(input), skus(badCache))
	if err := require(slices.Equal(skus(badCache), []string{"A", "C", "C"}), "E02: 原列表下标1应被 C 覆盖"); err != nil {
		return err
	}
	// filtered len=2、cap=3，追加 D 写入第三槽；之后从同一 len=2 的前缀追加 E 再覆盖它。
	withD := append(filtered, Product{SKU: "D", Stock: 9})
	withE := append(filtered, Product{SKU: "E", Stock: 9})
	t.log("错误结果两次 append: withD=%v withE=%v cache=%v", skus(withD), skus(withE), skus(badCache))
	if err := require(withD[2].SKU == "E" && badCache[2].SKU == "E", "E02: 缓存和第一份结果都应观察到第二次覆盖"); err != nil {
		return err
	}

	original := NewInventory()
	snapshot := FilterSnapshot(original)
	// 深入到 Tags/Meta 的写入也必须独立，不能只用 SKU 顺序证明全部修复。
	snapshot[0].Tags[0] = "请求标签"
	snapshot[0].Meta["warehouse"] = "请求仓"
	snapshot[0].Stock = 99
	t.log("快照修复: original SKU=%v A=%+v; snapshot SKU=%v A=%+v", skus(original), original[0], skus(snapshot), snapshot[0])
	if err := require(original[0].Stock == 5 && original[0].Tags[0] == "热卖" && original[0].Meta["warehouse"] == "深圳", "E02: 调用者保留的库存和嵌套内容必须不变"); err != nil {
		return err
	}
	// 两个业务分支都要独立可变列表，就各自领取完整快照，而非重复 append 同一描述值。
	// CloneProducts 的 len=cap=2，下面追加时换数组；Tags/Meta 也在 clone 边界分开。
	fixedD := append(CloneProducts(snapshot), Product{SKU: "D", Stock: 9})
	fixedE := append(CloneProducts(snapshot), Product{SKU: "E", Stock: 9})
	fixedD[0].Tags[0] = "D 分支标签"
	t.log("两分支快照修复: D=%v E=%v original=%v; D标签=%v E标签=%v", skus(fixedD), skus(fixedE), skus(original), fixedD[0].Tags, fixedE[0].Tags)
	if err := require(fixedD[2].SKU == "D" && fixedE[2].SKU == "E" && fixedE[0].Tags[0] == "请求标签", "E02: 两次追加及嵌套修改不得污染另一个业务分支"); err != nil {
		return err
	}
	nilResult := FilterSnapshot(nil)
	emptyResult := FilterSnapshot([]Product{})
	noneAvailable := FilterSnapshot([]Product{{SKU: "B", Stock: 0}})
	t.log("空列表过滤: nil结果 nil=%v; 空输入结果 nil=%v len=%d; 全缺货结果 nil=%v len=%d", nilResult == nil, emptyResult == nil, len(emptyResult), noneAvailable == nil, len(noneAvailable))
	if err := require(nilResult == nil && emptyResult != nil && len(emptyResult) == 0 && noneAvailable != nil && len(noneAvailable) == 0, "E02: 空列表不能丢失 nil/非nil 契约，也不能越界"); err != nil {
		return err
	}

	ownedInput := NewInventory()
	owned := FilterOwned(ownedInput)
	t.log("独占修复: owned=%v len/cap=%d/%d; 丢弃尾槽=%+v", skus(owned), len(owned), cap(owned), ownedInput[2])
	// 这里仅为教学检查已经移交的数组尾槽。真实调用者完成移交后不继续业务读取 ownedInput。
	if err := require(ownedInput[2].SKU == "" && ownedInput[2].Tags == nil && ownedInput[2].Meta == nil, "E02: 独占过滤的废弃尾部引用应清零"); err != nil {
		return err
	}
	t.log("E02 PASS：共享输入选 FilterSnapshot；独占输入可选 FilterOwned")
	if err := cacheExperiment(t); err != nil {
		return err
	}
	return slicesExperiment(t)
}

// cacheExperiment 由 core 或 -case cache 调用，用三种不同复制深度定位缓存污染。
// 所有调用同步完成；“请求1写入→请求1继续改→请求2读取”就是可保证的时间线，无需 sleep。
func cacheExperiment(t *transcript) error {
	t.log("\n[E03] 缓存写入后继续修改、浅拷贝嵌套对象与双向复制")
	request := NewInventory()
	bad := make(map[string][]Product)
	bad["stock"] = request
	request[0].Stock = 88
	request[0].Tags[0] = "污染标签"
	request[0].Meta["warehouse"] = "污染仓"
	t.log("缓存未复制: 下一请求读取 A=%+v", bad["stock"][0])

	request = NewInventory()
	shallow := slices.Clone(request)
	request[0].Stock = 88 // Product scalar 已复制，不会改变 shallow[0].Stock。
	request[0].Tags[0] = "浅拷贝标签"
	request[0].Meta["warehouse"] = "浅拷贝仓"
	t.log("只 Clone 外层: shallow A=%+v（Stock 独立，Tags/Meta 仍共享）", shallow[0])
	if err := require(shallow[0].Stock == 5 && shallow[0].Tags[0] == "浅拷贝标签" && shallow[0].Meta["warehouse"] == "浅拷贝仓", "E03: 外层 clone 不能误称递归深拷贝"); err != nil {
		return err
	}

	// maps.Clone 也浅复制。 map 的值若是 slice，新的 map 仍指向旧 slice 的数组。
	nestedMap := map[string][]string{"A": {"热卖"}}
	mapCopy := maps.Clone(nestedMap)
	nestedMap["A"][0] = "共享标签"
	t.log("maps.Clone 嵌套 slice: source[A]=%v clone[A]=%v", nestedMap["A"], mapCopy["A"])
	if err := require(mapCopy["A"][0] == "共享标签", "E03: map clone 的 slice 值仍共享"); err != nil {
		return err
	}

	cache := NewSnapshotCache()
	request = NewInventory()
	cache.Put("stock", request)
	request[0].Stock = 88
	request[0].Tags[0] = "请求1标签"
	request[0].Meta["warehouse"] = "请求1仓"
	reader1, ok := cache.Get("stock")
	if err := require(ok, "E03: 已写入键应存在"); err != nil {
		return err
	}
	reader1[0].Stock = 99
	reader1[0].Tags[0] = "请求2标签"
	reader1[0].Meta["warehouse"] = "请求2仓"
	reader2, _ := cache.Get("stock")
	t.log("Put/Get 双向复制: 后续读取 A=%+v", reader2[0])
	if err := require(reader2[0].Stock == 5 && reader2[0].Tags[0] == "热卖" && reader2[0].Meta["warehouse"] == "深圳", "E03: 写入方和读取方都不能污染缓存"); err != nil {
		return err
	}
	t.log("E03 PASS：复制深度与 Product 的可变字段一致，缓存自己拥有快照")
	return retentionExperiment(t)
}

// skus 只为日志提取标识；不返回 Product 别名，也不改变输入，空输入得到非 nil 空 []string。
func skus(products []Product) []string {
	result := make([]string, 0, len(products))
	for _, product := range products {
		result = append(result, product.SKU)
	}
	return result
}
