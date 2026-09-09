// 本示例用可运行的现象讲清楚数组、切片和 map 的基本用法与核心区别。
package main

import "fmt"

func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}

func main() {
	section("1. 数组：长度属于类型，未填写的位置取零值")

	// [10]string 是“恰好包含 10 个 string 的数组”。
	// 10 是类型的一部分，不是“当前放了几个值”。这里只写了 5 个初始值，
	// 剩余 5 个位置仍然真实存在，只是自动取得 string 的零值 ""。
	strArray := [10]string{"aa", "bb", "cc", "dd", "ee"}
	fmt.Printf("strArray=%q\n", strArray)
	fmt.Printf("类型=%T, len=%d, cap=%d\n", strArray, len(strArray), cap(strArray))
	// 对数组来说，容量没有第二层含义：一个 [10]string 就固定有 10 个位置，
	// 所以 len(strArray) 和 cap(strArray) 都必然是 10。

	// 数组的长度是类型的一部分：[10]string 和 [5]string 是不同类型，不能直接赋值。
	// 如果取消下一行注释，编译器会报告不能把 [5]string 用作 [10]string：
	// strArray = [5]string{"a", "b", "c", "d", "e"}

	// 数组赋值会复制全部元素。修改副本，不会影响原数组。
	arrayCopy := strArray
	arrayCopy[0] = "changed in copy"
	fmt.Printf("原数组第 0 项=%q，副本第 0 项=%q\n", strArray[0], arrayCopy[0])

	section("2. 切片：描述一段连续元素的窗口")

	// make([]string, 0) 创建一个长度和容量都为 0 的非 nil 切片。
	// nil 与 len/cap 是不同维度：nil 表示切片取零值；len=0 只表示当前没有可访问元素。
	// 因而 nil slice 和非 nil 空 slice 都可以是 len=0、cap=0，但二者和 nil 比较的结果不同。
	emptySlice := make([]string, 0)
	fmt.Printf("emptySlice=%v, len=%d, cap=%d, 是否为 nil=%v\n",
		emptySlice, len(emptySlice), cap(emptySlice), emptySlice == nil)
	var zeroSlice []string
	fmt.Printf("zeroSlice=%v, len=%d, cap=%d, 是否为 nil=%v\n",
		zeroSlice, len(zeroSlice), cap(zeroSlice), zeroSlice == nil)

	// 能否使用下标只看 len，不看切片是否为 nil，也不看 cap：
	// emptySlice[0] = "x" // 非 nil 也会 panic，因为 len=0，没有合法下标。
	reservedSlice := make([]string, 0, 3)
	// reservedSlice[0] = "x" // cap=3 仍会 panic，因为 len 依然是 0。
	fmt.Printf("预留容量但长度为 0：len=%d, cap=%d，仍不能访问下标 0\n",
		len(reservedSlice), cap(reservedSlice))

	// append 一个元素后 len 变成 1，此时只有下标 0 可以读写；下标 1 仍然越界。
	emptySlice = append(emptySlice, "first")
	emptySlice[0] = "changed"
	// emptySlice[1] = "x" // 会 panic，因为现在 len=1，合法范围只有 [0, 1)。
	fmt.Printf("append 一项后：emptySlice=%q, len=%d, 可读写下标只有 0\n",
		emptySlice, len(emptySlice))

	// make([]T, len) 省略容量时，cap 默认等于 len。
	// 如果希望创建后立刻能读写 3 个位置，应把长度设为 3；它们最初都是 string 零值 ""。
	twoArgumentSlice := make([]string, 3)
	twoArgumentSlice[0] = "safe"
	fmt.Printf("make([]string, 3)：值=%q, len=%d, cap=%d\n",
		twoArgumentSlice, len(twoArgumentSlice), cap(twoArgumentSlice))

	// make([]T, len, cap) 把“现在有几个逻辑元素”和“预留多少底层空间”分开指定。
	// 这里一开始已有 3 个零值元素可读写，同时预留到 10 项，后续 append 7 项前无需换底层数组。
	threeArgumentSlice := make([]string, 3, 10)
	threeArgumentSlice[0] = "safe"
	fmt.Printf("make([]string, 3, 10)：值=%q, len=%d, cap=%d\n",
		threeArgumentSlice, len(threeArgumentSlice), cap(threeArgumentSlice))

	// make([]string, 0, 3) 的典型用途是“结果还没有元素，但预计最多追加 3 项”。
	// 这类似 Java 的 new ArrayList<String>(3)：初始 size 仍为 0，set(0, x) 也会越界，
	// 但 add/append 前几项可以复用预留空间，避免反复分配数组和复制旧元素。
	growingSlice := make([]string, 0, 3)
	for _, value := range []string{"a", "b", "c", "d"} {
		oldCap := cap(growingSlice)
		growingSlice = append(growingSlice, value)
		fmt.Printf("append %q 后：len=%d, cap=%d, 本次是否扩容=%v\n",
			value, len(growingSlice), cap(growingSlice), cap(growingSlice) != oldCap)
	}
	// 前 3 次 append 使用预留的 3 个槽位，不扩容；第 4 次超过 cap 才扩容。
	// 扩容后的具体 cap 是运行时实现细节，业务代码不应依赖其精确数值。

	// cap 里的位置也不是永远不能使用：可以显式扩展 len，但随后这些位置只是零值。
	reservedSlice = reservedSlice[:3]
	reservedSlice[0] = "after reslice"
	fmt.Printf("显式扩展到 len=3 后：reservedSlice=%q，可以访问下标 0、1、2\n", reservedSlice)

	// 切片本身不是装元素的数组，而是“底层存储 + 起点 + len + cap”的小型描述值。
	// []string 这个类型不写死长度；不同的 []string 值可以有不同 len/cap，
	// 也可以像下面这样，成为同一个数组上彼此重叠的窗口。

	// strArray[1:3] 使用半开区间 [1, 3)：包含下标 1、2，不包含下标 3。
	// 它没有复制 bb、cc，而是建立一个观察同一底层数组的窗口。
	sliceArray := strArray[1:3]
	fmt.Printf("sliceArray=%q, len=%d, cap=%d\n",
		sliceArray, len(sliceArray), cap(sliceArray))
	// len = high-low = 3-1 = 2：当前可用下标只有 0、1，分别对应原数组下标 1、2。
	// cap = 原数组末端边界-low = 10-1 = 9：从原数组下标 1 到 9 总共有 9 个槽位。
	// cap 不是“当前切出了几个元素”；它表示从切片起点出发，不换底层数组最多能扩到多长。
	expandedWindow := sliceArray[:cap(sliceArray)]
	fmt.Printf("把窗口扩到 cap 后=%q（对应 strArray[1:10]）\n", expandedWindow)

	// 因为 sliceArray 与 strArray 共享存储，所以通过切片修改元素，数组也会变化。
	sliceArray[0] = "BB"
	fmt.Printf("修改切片后：sliceArray=%q, strArray[1]=%q\n", sliceArray, strArray[1])

	// 当前切片容量还有富余，append 可以继续使用原底层数组。
	// sliceArray 原来覆盖数组下标 1、2；append 的新元素会写到数组下标 3。
	sliceArray = append(sliceArray, "written by append")
	fmt.Printf("append 后：sliceArray=%q\n", sliceArray)
	fmt.Printf("共享底层数组，所以 strArray[3]=%q\n", strArray[3])

	// append 的签名可理解为 append(目标切片, 待追加的元素...)。
	// []string(nil) 把 nil 转成“类型明确的 nil string 切片”；sliceArray... 把切片展开成多个元素。
	// 目标的 cap=0，追加非空数据时必然分配底层数组并复制元素，所以结果与 sliceArray 独立。
	independent := append([]string(nil), sliceArray...)
	independent[0] = "independent"
	fmt.Printf("复制后修改：independent[0]=%q, sliceArray[0]=%q\n",
		independent[0], sliceArray[0])

	// 如果上面的紧凑写法不直观，make + copy 完全表达同一个意图，而且更适合初学时阅读。
	independentByCopy := make([]string, len(sliceArray))
	copiedCount := copy(independentByCopy, sliceArray)
	fmt.Printf("make+copy：复制了 %d 项，结果=%q\n", copiedCount, independentByCopy)

	// 三下标切片 [low:high:max] 中，high 和 max 都是“不包含”的原切片/数组边界：
	// len = high-low，cap = max-low。因此 [1:3:3] 的 len=3-1=2，cap=3-1=2。
	// 它仍只包含原数组下标 1、2；第三个 3 不是“最多取 3 项”，而是容量的末端边界。
	// 普通的 strArray[1:3] 可以理解成这里省略了 max，默认末端边界为 10，绝不是下标 9；
	// 所以它的 cap=10-1=9。容量受限后再 append 一个元素，所需 len=3 已超过 cap=2，
	// append 便分配新底层数组，原来的 strArray[3] 不会被覆盖。
	limited := strArray[1:3:3]
	fmt.Printf("追加前：limited=%q, len=%d, cap=%d\n", limited, len(limited), cap(limited))
	limited = append(limited, "new backing array")
	fmt.Printf("限制容量后 append：limited=%q, strArray[3] 仍为 %q\n",
		limited, strArray[3])

	section("3. map：按键查找的无序集合")

	// map[string]int 表示键是 string、值是 int；可粗略对照 Java 的 Map<String, Integer>，
	// 但这里的 [] 是 Go 的 map 类型语法，并不是泛型语法。
	// map 字面量已经完成初始化，可以直接读取和写入。
	dictionary := map[string]int{
		"apple":      1,
		"watermelon": 2,
	}
	fmt.Printf("dictionary=%v, 类型=%T, len=%d\n", dictionary, dictionary, len(dictionary))

	// 用 key 读取、写入；给已有 key 赋值就是更新。
	dictionary["banana"] = 3
	dictionary["apple"] = 10
	fmt.Printf("更新后：apple=%d, banana=%d\n", dictionary["apple"], dictionary["banana"])

	// 只接收一个结果时，不存在的 key 会返回值类型的零值。若业务上“不存在就当 0”，
	// 根本不需要每次都写 exists；例如计数代码可以直接写 counts[word]++。
	// “comma ok” 形式用第二个 bool 区分“key 不存在”和“key 存在但值恰好是 0”。
	missingValue, exists := dictionary["pear"]
	fmt.Printf("查询 pear：value=%d, exists=%v\n", missingValue, exists)

	dictionary["free"] = 0
	freeValue, exists := dictionary["free"]
	fmt.Printf("查询 free：value=%d, exists=%v\n", freeValue, exists)

	// delete 删除 key；删除一个本来就不存在的 key 也是安全的。
	delete(dictionary, "watermelon")
	delete(dictionary, "not-exist")
	_, exists = dictionary["watermelon"]
	fmt.Printf("删除 watermelon 后：exists=%v, len=%d\n", exists, len(dictionary))

	// Go 语言不保证 map 的遍历顺序；不要把 range 的先后次序当成业务规则。
	for key, value := range dictionary {
		fmt.Printf("range（顺序不保证）：%s -> %d\n", key, value)
	}

	section("4. nil slice 与 nil map 的重要区别")

	var nilSlice []string
	var nilMap map[string]int

	// nil slice 的 len/cap 都是 0，可以 len、range，也可以交给 append。
	// 但不能执行 nilSlice[0] 这样的元素读写——长度为 0，访问下标 0 会越界 panic。
	// append 不是在“空指针指向的位置”写数据：它是内建函数，会在需要时分配底层数组，
	// 然后返回一个新的切片描述值。因此一定要用 nilSlice = append(...) 接住返回值。
	fmt.Printf("append 前的 nil slice：len=%d, cap=%d, 是否为 nil=%v\n",
		len(nilSlice), cap(nilSlice), nilSlice == nil)
	// fmt.Println(nilSlice[0]) // 若取消注释，会 panic: index out of range。
	nilSlice = append(nilSlice, "safe")
	fmt.Printf("append nil slice：%q, 是否仍为 nil=%v\n", nilSlice, nilSlice == nil)

	// nil map 是“尚未初始化的 map”。Go 明确定义了：查询它得到值类型的零值和 ok=false，
	// len/range/delete 也安全；但赋值语句只能修改一个已经存在的 map，不能替你创建并返回 map。
	// 因此写入 nil map 会 panic: assignment to entry in nil map。
	fmt.Printf("读取 nil map：value=%d, 是否为 nil=%v\n", nilMap["Go"], nilMap == nil)
	// nilMap["Go"] = 1 // 若在 make 之前执行，会 panic。
	nilMap = make(map[string]int)
	nilMap["Go"] = 1
	fmt.Printf("make 后写入 nilMap：value=%d, 是否为 nil=%v\n", nilMap["Go"], nilMap == nil)
}
