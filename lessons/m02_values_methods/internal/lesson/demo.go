package lesson

import (
	"fmt"
	"io"
)

// Run 由 cmd/demo/main.go 调用，只运行用户指定的实验；测试也可以向 io.Discard 输出。
// writer 只是日志目的地，不代表业务对象的拥有者；本包不会关闭 caller 提供的 writer。
// 正常实验中的危险操作由当前 goroutine 内的 observePanic 有界恢复，复制锁入口单独运行。
func Run(writer io.Writer, experiment string) error {
	// []struct{...} 是匿名结构体组成的 slice；每项保存名称和 func(io.Writer) 函数值。
	// 这个局部 slice 固定实验的阅读顺序，不借助 map 迭代顺序或 goroutine 调度。
	cases := []struct {
		name string
		run  func(io.Writer)
	}{
		{"core", runCore},
		{"methods", runMethods},
		{"allocation", runAllocation},
		{"clone", runClone},
		{"counter", runCounter},
		{"nil", runNil},
	}
	// range 逐项取值，_ 丢弃索引；current 是本轮条目的副本，其中 run 仍是同一函数值。
	for _, current := range cases {
		if experiment == "all" || experiment == current.name {
			current.run(writer) // 同步调用：当前实验返回后才进入下一个实验，没有后台工作。
			if experiment != "all" {
				return nil
			}
		}
	}
	if experiment != "all" {
		return fmt.Errorf("unknown case %q: use all/core/methods/allocation/clone/counter/nil", experiment)
	}
	return nil
}

// showState 是实验日志的共同视图，caller 传入订单地址以避免日志函数偷偷再复制顶层 Order。
// 它只用于已初始化的样本，不承担通用序列化或 nil 校验。日志目的就是观察实际副作用。
func showState(writer io.Writer, label string, order *Order) {
	fmt.Fprintf(writer, "%s id=%s status=%s qty=%d review=%s buyer=%s\n",
		label, order.ID, order.Status, order.Lines[0].Qty, order.Labels["review"], order.Buyer.Name)
}

// runCore 是 10～20 分钟可讲清的核心实验，由 Run 在当前 goroutine 串行调用。
// 每一步先建立 caller 变量，再调用三个修改函数，对照返回值与 caller 的最终对象。
func runCore(writer io.Writer) {
	fmt.Fprintln(writer, "\n[E01 core] Order 值、地址值与 caller 变量")
	// := 在本函数块声明局部 order，右侧 NewOrder 的返回类型使它成为 Order 值变量。
	order := NewOrder()
	pointer := &order // pointer 的类型是 *Order；&pointer 则是这个指针变量自身的地址，类型是 **Order。
	fmt.Fprintf(writer, "caller-order=%p pointer-target=%p pointer-variable=%p\n", &order, pointer, &pointer)
	showState(writer, "before", &order)

	changedCopy := ModifyValue(order)
	// changedCopy 是函数返回的 Order 值，再赋给 caller 的新变量；这里不是打印 callee 参数的地址。
	// 顶层地址不同，Lines[0] 和 Buyer 的地址相同，再用状态证明内部数据确实共享。
	fmt.Fprintf(writer, "top-level-distinct=%t line-shared=%t buyer-shared=%t\n",
		&order != &changedCopy, &order.Lines[0] == &changedCopy.Lines[0], order.Buyer == changedCopy.Buyer)
	showState(writer, "after ModifyValue caller", &order)
	showState(writer, "after ModifyValue returned-copy", &changedCopy)

	modified := ModifyPointer(pointer)
	fmt.Fprintf(writer, "ModifyPointer non-nil=%t nil=%t\n", modified, ModifyPointer(nil))
	showState(writer, "after ModifyPointer caller", &order)

	replacement := RebindPointer(pointer)
	fmt.Fprintf(writer, "after RebindPointer caller-still-original=%t returned-new-target=%t\n",
		pointer == &order, replacement != pointer)
	showState(writer, "caller target", pointer)
	showState(writer, "returned target", replacement)
	pointer = replacement // 修复发生在 caller：显式给自己持有的指针变量赋新地址。
	fmt.Fprintf(writer, "caller assigned return: target=%s old-order=%s\n", pointer.ID, order.ID)

	// 数组值包含元素；复制 [1]Line 会复制数组元素，和复制 []Line 描述的共享边界不同。
	array := [1]Line{{SKU: "SKU-A", Qty: 1}}
	arrayCopy := array
	arrayCopy[0].Qty = 7
	fmt.Fprintf(writer, "array-copy original-qty=%d copied-qty=%d element-distinct=%t\n",
		array[0].Qty, arrayCopy[0].Qty, &array[0] != &arrayCopy[0])
}

// runMethods 由 Run 调用，对照直接调用的自动取址与方法值保存接收者的时机。
// 每个 map/slice/变量在这个实验内创建，只对当前进程的对象产生修改。
func runMethods(writer io.Writer) {
	fmt.Fprintln(writer, "\n[E02 methods] 直接调用、方法集、方法值与方法表达式")
	order := NewOrder()
	localStatus := order.SetStatusOnCopy("paid")
	fmt.Fprintf(writer, "value receiver returned=%s caller=%s\n", localStatus, order.Status)
	order.SetStatus("paid") // order 可寻址，这次调用等价于 (&order).SetStatus("paid")。
	pointer := &order
	_ = pointer.SetStatusOnCopy("lost") // *Order 调用值方法会取目标值作为接收者副本，仍不会改 Status。
	fmt.Fprintf(writer, "auto-address/auto-deref status=%s\n", pointer.StatusText())

	byValue := map[string]Order{"primary": order}
	// byValue["primary"].SetStatus("shipped") 不能编译：map 元素不可寻址，不能自动取得 *Order。
	// &byValue["primary"] 和 byValue["primary"].Status = "shipped" 也不能编译。
	temporary := byValue["primary"] // 得到一个 Order 副本；temporary 是可寻址的局部变量。
	temporary.SetStatus("shipped")
	fmt.Fprintf(writer, "map value before writeback=%s temporary=%s\n", byValue["primary"].Status, temporary.Status)
	byValue["primary"] = temporary // map 下标虽不可寻址，但允许整项赋值；这一步才替换 map 中的值。
	byPointer := map[string]*Order{"primary": LocalOrder()}
	byPointer["primary"].SetStatus("shipped") // 取出的值已经是 *Order，不需要对 map 元素取地址。
	items := []Order{NewOrder()}
	items[0].SetStatus("slice-element") // slice 元素可寻址，因此这个位置允许自动取址。
	fmt.Fprintf(writer, "map value after writeback=%s pointer-map=%s slice=%s\n",
		byValue["primary"].Status, byPointer["primary"].Status, items[0].Status)
	// NewOrder().SetStatus("paid") 也不能编译：函数返回的 Order 临时值不可寻址。
	// 修复是先保存到局部变量再调用，或让构造函数返回 *Order；上面的 LocalOrder 是后一种形式。

	var setter StatusSetter = &order
	setter.SetStatus("interface-write")
	fmt.Fprintf(writer, "interface dynamic-type=%T status=%s\n", setter, order.Status)

	valueMethod := order.StatusText // 此刻求值并保存 Order 接收者副本，Status 是 interface-write。
	buyerMethod := order.BuyerName  // 也是保存 Order 副本，但保存的 Buyer 地址仍与原订单相同。
	order.Status = "latest"
	order.Buyer.Name = "UpdatedBuyer"
	statusExpression := Order.StatusText // 类型开头的是方法表达式，函数签名为 func(Order) string，没有绑定对象。
	fmt.Fprintf(writer, "method value status=%s expression-now=%s shared-buyer=%s\n",
		valueMethod(), statusExpression(order), buyerMethod())

	first := NewOrder()
	second := NewOrder()
	current := &first
	boundSetter := current.SetStatus // 此时保存 *Order 地址值，绑定的是 first，不是变量 current 的未来值。
	current = &second
	boundSetter("bound-first")
	expressionSetter := (*Order).SetStatus // 签名 func(*Order, string)，调用时才明确传哪个接收者。
	expressionSetter(current, "expression-second")
	fmt.Fprintf(writer, "pointer method value first=%s method expression second=%s\n", first.Status, second.Status)
	// Order.SetStatus 不能作为表达式：指针方法不属于 Order 的方法集。表达式不会替类型自动取址。
	// (*Order).StatusText 则合法，签名为 func(*Order) string，调用时会读取并复制目标 Order。
	fmt.Fprintf(writer, "pointer expression to value method=%s\n", (*Order).StatusText(&second))
}

// runAllocation 由 Run 调用，对照变量初始化与底层数据初始化。没有从地址形式推导栈/堆。
// 唯一的 channel 是容量 1 的局部演示：恰好发送一次再接收一次，不会依赖另一个 goroutine。
func runAllocation(writer io.Writer) {
	fmt.Fprintln(writer, "\n[E03 allocation] 返回局部地址、new 两种形式、make 与零值")
	local := LocalOrder()
	local.SetStatus("after-return")
	fmt.Fprintf(writer, "local address remains usable: id=%s status=%s\n", local.ID, local.Status)

	zero := new(Order) // 旧版本常见形式：*Order，目标的每个字段都是零值；没有自动创建 Buyer/map/数组。
	initialized := new(Order{ID: "ORDER-NEW", Status: "initialized"})
	answer := new(42) // new(表达式) 对未定型常量取默认类型；这里的类型是 *int，目标值为 42。
	fmt.Fprintf(writer, "new(T) type=%T status=%q buyer-nil=%t map-nil=%t slice-nil=%t\n",
		zero, zero.Status, zero.Buyer == nil, zero.Labels == nil, zero.Lines == nil)
	fmt.Fprintf(writer, "new(expr) order=%s answer-type=%T answer=%d\n", initialized.Status, answer, *answer)
	base := NewOrder()
	initializedCopy := new(base) // 初始化按 Order 的普通值复制规则进行，new 不会把内部对象自动深克隆。
	initializedCopy.Lines[0].Qty = 8
	fmt.Fprintf(writer, "new(existing-order) top-level-distinct=%t line-shared=%t original-qty=%d\n",
		initializedCopy != &base, &initializedCopy.Lines[0] == &base.Lines[0], base.Lines[0].Qty)

	slicePointer := new([]int) // 返回 *[]int，指向的 slice 值为 nil；并没有创建元素数组。
	mapPointer := new(map[string]string)
	channelPointer := new(chan int)
	readySlice := make([]int, 2, 3) // 返回 []int，不是 *[]int；底层数组已初始化，长度 2，容量 3。
	readyMap := make(map[string]string)
	readyMap["state"] = "ready"
	fmt.Fprintf(writer, "new container types=%T/%T/%T targets-nil=%t/%t/%t\n",
		slicePointer, mapPointer, channelPointer, *slicePointer == nil, *mapPointer == nil, *channelPointer == nil)
	fmt.Fprintf(writer, "make types=%T/%T slice=%v len=%d cap=%d map=%s\n",
		readySlice, readyMap, readySlice, len(readySlice), cap(readySlice), readyMap["state"])

	// nil slice 可以 len/range/append；append 的结果必须接回相应 slice 变量。
	*slicePointer = append(*slicePointer, 5)
	nilMapPanicked, _ := observePanic(func() { (*mapPointer)["state"] = "unsafe" })
	*mapPointer = make(map[string]string) // 修复：初始化的是被指向的 map 值，而非仅创建一个 map 指针。
	(*mapPointer)["state"] = "fixed"
	fmt.Fprintf(writer, "nil slice append=%v nil map read=%q write-panicked=%t repaired-map=%s\n",
		*slicePointer, zero.Labels["missing"], nilMapPanicked, (*mapPointer)["state"])

	readyChannel := make(chan int, 1)
	readyChannel <- 7         // 容量 1 且尚为空，本 goroutine 的一次发送可以立即进入缓冲。
	value := <-readyChannel   // 已有一项值，接收立即取得它；没有用 sleep 预测调度。
	close(readyChannel)       // 当前函数拥有并关闭 channel；这表示发送方结束，不是释放 OS 资源。
	_, open := <-readyChannel // 已关闭且已排空，立即返回 int 零值和 false。
	fmt.Fprintf(writer, "make channel type=%T received=%d drained-closed=%t\n", readyChannel, value, !open)
	// 对 nil channel 直接发送或接收会一直等待，因此不在正常入口执行；new(chan int) 不能替代 make。
}

// runClone 由 Run 调用，以相同初始数据展示 DTO 浅复制泄漏与按字段隔离的修复。
// 没有并发读取/修改；CloneDTO 能证明数据隔离，不能凭这个实验推出线程安全。
func runClone(writer io.Writer) {
	fmt.Fprintln(writer, "\n[E04 clone] DTO 浅复制与隔离快照")
	original := NewOrder()
	shallow := ShallowClone(original)
	_ = ModifyValue(shallow)
	showState(writer, "shallow original leaked", &original)

	original = NewOrder()
	isolated := CloneDTO(original)
	changed := ModifyValue(isolated)
	fmt.Fprintf(writer, "deep line-distinct=%t buyer-distinct=%t\n",
		&original.Lines[0] != &isolated.Lines[0], original.Buyer != isolated.Buyer)
	showState(writer, "deep original isolated", &original)
	showState(writer, "deep changed-copy", &changed)

	// NewOrder 的 Lines 只有一项且 cap=1；此处明确耗尽容量，append 第二项必须得到新底层数组。
	// 这不是“append 总会隔离”：有剩余容量时 append 可以继续使用旧数组；返回的 len 仍只改本变量。
	headerCopy := original
	headerCopy.Lines = append(headerCopy.Lines, Line{SKU: "SKU-B", Qty: 1})
	headerCopy.Lines[0].Qty = 99
	fmt.Fprintf(writer, "append at full cap original-len=%d copy-len=%d original-qty=%d copy-qty=%d\n",
		len(original.Lines), len(headerCopy.Lines), original.Lines[0].Qty, headerCopy.Lines[0].Qty)

	emptyClone := CloneDTO(Order{})
	fmt.Fprintf(writer, "clone preserves nil slice/map/buyer=%t/%t/%t\n",
		emptyClone.Lines == nil, emptyClone.Labels == nil, emptyClone.Buyer == nil)
}

// runCounter 由 Run 调用，只做串行状态对照。真正的有限并发验证在 counter_test.go 中。
// 复制锁的错误实现放 testdata/copylocks，避免把故意违反 sync 契约的程序混入正常入口。
func runCounter(writer io.Writer) {
	fmt.Fprintln(writer, "\n[E05 counter] 一份对象、一把锁，所有方法用指针接收者")
	var counter Counter // Counter 的零值中 mu 未加锁，value 为 0，无需构造函数。
	alias := &counter
	alias.Add(1)
	counter.Add(1) // counter 可寻址，自动取址后与 alias 使用同一个目标。
	fmt.Fprintf(writer, "fixed caller=%d expected=2 alias-target-same=%t\n", counter.Value(), alias == &counter)
	fmt.Fprintln(writer, "bad entry: go run ./testdata/copylocks; diagnostic: go vet -copylocks ./testdata/copylocks")
}

// runNil 由 Run 调用。observePanic 的恢复边界在本 goroutine，不承担生产环境吞掉 bug 的策略。
// 修复通过 SafeStatus 的明确 nil 契约实现；故障入口 cmd/nilpanic 则保留真实进程非零退出。
func runNil(writer io.Writer) {
	fmt.Fprintln(writer, "\n[E06 nil] 能调用与能安全读取是两件事")
	var missing *Order
	fmt.Fprintf(writer, "guarded nil status=%s\n", missing.SafeStatus())
	fieldPanicked, _ := observePanic(func() { missing.SetStatus("paid") })
	valueMethodPanicked, _ := observePanic(func() { _ = missing.StatusText() })
	fmt.Fprintf(writer, "unguarded-pointer-method-panicked=%t nil-to-value-method-panicked=%t caller-continued=true\n",
		fieldPanicked, valueMethodPanicked)
	// SetStatus 已进入方法后访问字段出错；StatusText 的值接收者需要 Order 副本，nil 在取目标时就出错。
	// 恢复返回到 observePanic 的 caller，不能从 SetStatus 内出错那一行之后继续执行。
}
