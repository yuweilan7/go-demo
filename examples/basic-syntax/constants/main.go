// 本示例集中讲解 Go 常量、用常量表达“枚举”、iota 和位移。
//
// 运行方式：
//
//	go run ./examples/basic-syntax/constants
package main

import (
	"fmt"
	"unicode/utf8"
	"unsafe"
)

// 显式类型和省略类型都合法。省略类型得到的是“无类型常量”，而不是没有类型信息。
// 无类型常量会一直保留较高的表达精度，到需要落入变量、参数或 interface{} 时，
// 才根据上下文选定具体类型；这与运行时动态类型无关。
const explicitString string = "abc"
const inferredString = "abc"
const firstText, secondText = "abc", "def"

const (
	untypedInteger       = 42
	typedInteger   int64 = 42

	// Go 编译器可以用任意精度表示尚未落入具体类型的整数常量。
	// huge 本身太大，不能直接作为默认 int 传给 fmt.Println；右移后得到 2，才可安全打印。
	huge        = 1 << 100
	backToSmall = huge >> 99
)

// Go 没有 enum 关键字。“命名类型 + 一组常量”只是 Go 社区表达枚举意图的惯用组合，
// 编译器不会生成 Java enum 那样的类、单例对象、values() 或 valueOf()。
// type Status uint8 只创建一个底层为 uint8 的新类型；下面三个常量仍是独立的包级名称。
// 使用命名类型的价值是：Status 不会和普通 uint8 变量在没有显式转换时随意混用。
type Status uint8

const (
	// iota 可以先粗略理解成“这一条常量声明在本 const 块里的编号”，从 0 开始。
	// 后两条省略了类型和表达式，会复用上一条的“Status = iota”模板，
	// 但 iota 会换成各自当前的编号，所以结果依次是 Status(0)、Status(1)、Status(2)。
	// 如果不喜欢这种缩写，完全可以显式写成 0、1、2，语义没有区别。
	StatusUnknown Status = iota
	StatusSuccess
	StatusFail
)

// String 让教学输出显示可读名称。注意：Status 仍不是“封闭枚举”，
// Status(99) 这样的值完全合法，所以接收 Status 时仍要考虑未知值。
func (s Status) String() string {
	switch s {
	case StatusUnknown:
		return "Unknown"
	case StatusSuccess:
		return "Success"
	case StatusFail:
		return "Fail"
	default:
		return fmt.Sprintf("Status(%d)", uint8(s))
	}
}

const (
	compileText = "abc"

	// len、cap 和 unsafe.Sizeof 的写法像函数调用，但它们是编译器认识的内建操作。
	// 当实参满足语言规定时，结果在编译期即可确定，因此可以用来声明常量。
	compileTextLength   = len(compileText)
	fixedArrayCapacity  = cap([5]int{})
	stringValueSize     = unsafe.Sizeof(compileText)
	longStringValueSize = unsafe.Sizeof(
		"这段内容明显比 abc 长，但 string 值本身的大小不随内容长度变化",
	)
)

// 下面是不能作为常量的典型写法。它们只放在注释里，以保证示例可以运行：
//
//	const badSlice = []int{1, 2, 3}          // invalid constant type []int
//	const badLength = len([]int{1, 2, 3})    // len(slice) 不是常量
//	const badValue = ordinaryFunctionCall()  // 普通函数要到运行时调用
//
// 数组、slice、map、struct、channel、func 和指针值都不能直接成为常量。
// Go 常量的值只属于布尔、数字和字符串这几类基本值。
// 这首先是语言规范刻意限定的常量模型，不应简单归因为“这些类型都可变”：
// 数组和 struct 即使字面量完全写死，也仍然不是 Go 常量。
// Go const 更接近 Java 的“编译期常量”（基本类型/String + 常量表达式），
// 不等于一般的 Java final。final List 只是引用不能重新赋值，List 内容仍能修改；
// Go 没有一个 const 可以只冻结 slice/map/struct 变量绑定而继续允许内部变化。

const (
	// 理解这个块需要同时看两条互不干扰的规则：
	// 规则 A：iota 的编号每经过一个 ConstSpec 都加 1，无论表达式有没有写 iota。
	// 规则 B：某项省略类型和表达式时，复制上一条非空项的“表达式文本”，不是复制计算结果。
	sequenceA = iota // 当前 ConstSpec 的索引是 0
	sequenceB        // 重复上一条非空表达式 iota；在本条中重新求值得 1
	sequenceC        // 得 2

	sequenceText         = "ha" // 索引为 3；iota 仍为 3，只是本表达式没读取它
	sequenceTextAgain           // 重复 "ha"，索引为 4
	sequenceHundred      = 100  // 索引为 5
	sequenceHundredAgain        // 重复 100，索引为 6
	sequenceResume       = iota // 当前索引为 7，所以得到 7
	sequenceResumeAgain         // 重复 iota，并在当前索引 8 重新求值，所以得到 8
)

const (
	// iota 按 const 块中的 ConstSpec（常量声明项）计数，不是按物理“行数”计数。
	// 同一个声明项里出现多少次 iota，读到的都是同一个索引，因此两者都是 0。
	sameSpecA, sameSpecB = iota, iota

	// 空行和注释不产生新的 ConstSpec，所以这里是 1，而不是 2 或 3。
	nextSpec = iota
)

// 这个块专门回答“有的常量项带 iota，有的不带，会怎样”。
// iota 从未暂停或重启；每遇到一个新的 ConstSpec 就继续编号。
const (
	mixedFixedA = 10   // 索引 0；表达式不读取 iota，结果是 10
	mixedIotaB  = iota // 索引 1；结果是 1
	mixedIotaC         // 索引 2；复用 iota，结果是 2
	mixedFixedD = 99   // 索引 3；换成固定值模板，结果是 99
	mixedFixedE        // 索引 4；复用 99，结果仍是 99
	mixedIotaF  = iota // 索引 5；再次读取当前编号，结果是 5，不是从 0 重新开始

	// 逗号左边有两个名称，所以这一整项仍只算一个 ConstSpec，当前 iota 都是 6。
	mixedPairG, mixedPairH = iota, iota + 100 // 6, 106
	// 这里复用完整表达式列表“iota, iota + 100”；当前 iota 是 7。
	mixedPairI, mixedPairJ // 7, 107
)

const (
	shiftI = 1 << iota // 1 << 0 = 1
	shiftJ = 3 << iota // 3 << 1 = 6
	// shiftJ 显式写出新表达式，改变的是后续空白项复用的“模板”，不是 iota 的起点。
	shiftK // 重复完整表达式 3 << iota；此时 iota=2，结果为 12
	shiftL // 再次重复 3 << iota；此时 iota=3，结果为 24
)

func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}

func main() {
	section("1. 显式常量、无类型常量与编译期精度")
	fmt.Printf("explicitString=%q(%T), inferredString=%q(%T)\n",
		explicitString, explicitString, inferredString, inferredString)
	fmt.Printf("多常量声明：firstText=%q, secondText=%q\n", firstText, secondText)

	// 把无类型常量传给 fmt.Printf 时，它进入 interface{}，会采用默认类型 int。
	// 显式声明的 typedInteger 则从一开始就是 int64。
	fmt.Printf("untypedInteger 进入接口后的默认类型=%T；typedInteger 的类型=%T\n",
		untypedInteger, typedInteger)
	fmt.Printf("(1 << 100) >> 99 = %d\n", backToSmall)

	section("2. 命名类型 + 常量模拟枚举")
	fmt.Printf("%d=%s, %d=%s, %d=%s\n",
		StatusUnknown, StatusUnknown,
		StatusSuccess, StatusSuccess,
		StatusFail, StatusFail)
	fmt.Println("上面三个值等价于手写 Status(0)、Status(1)、Status(2)；iota 没有生成任何类。")
	fmt.Printf("命名类型仍不是封闭枚举：%s\n", Status(99))

	section("3. 编译期可确定的 len、cap 与 unsafe.Sizeof")

	// 常量可用作数组长度；若 compileTextLength 不是编译期常量，这个类型声明就无法成立。
	var bytes [compileTextLength]byte
	fmt.Printf("len(%q)=%d，可用于数组类型 %T\n", compileText, compileTextLength, bytes)
	fmt.Printf("cap([5]int{})=%d\n", fixedArrayCapacity)

	// unsafe.Sizeof 返回的是“这个 string 值本身”占多少字节，不包含它引用的文本字节。
	// 在当前 Go 的 64 位实现中，string 值通常可理解为数据地址 + 长度，各 8 字节，合计 16。
	// 但 Go 语言规范没有承诺所有平台都必须是 16：在 32 位目标上通常是 8。
	wordSize := unsafe.Sizeof(uintptr(0))
	fmt.Printf("当前平台：一个机器字=%d 字节，string 值=%d 字节\n", wordSize, stringValueSize)
	fmt.Printf("短内容和长内容的 string 值大小：%d == %d（内容字节不计入）\n",
		stringValueSize, longStringValueSize)

	section("3.1 运行时 len 与 cap 到底数什么")

	// len 回答“现在有多少内容”，但不同类型中“内容”的单位不同：
	//   - string：字节数，不是 Unicode 字符数；
	//   - array/slice：元素数；map：键值对数；channel：缓冲区里当前排队的元素数。
	// cap 回答“当前这块存储最多能容纳多少”：适用于 array、slice 和有缓冲 channel。
	// string 和 map 没有可查询的 cap。
	unicodeText := "你好"
	fixedNumbers := [5]int{10, 20}
	dynamicNumbers := make([]int, 2, 5)
	scores := map[string]int{"Go": 100, "Java": 99}
	jobs := make(chan string, 3)
	jobs <- "first"
	jobs <- "second"

	fmt.Printf("string %q：len=%d 字节，Unicode 字符数=%d\n",
		unicodeText, len(unicodeText), utf8.RuneCountInString(unicodeText))
	fmt.Printf("array：len=%d, cap=%d（数组长度固定，二者相同）\n",
		len(fixedNumbers), cap(fixedNumbers))
	fmt.Printf("slice：len=%d, cap=%d，还可在不扩容的情况下 append %d 项\n",
		len(dynamicNumbers), cap(dynamicNumbers), cap(dynamicNumbers)-len(dynamicNumbers))
	fmt.Printf("map：len=%d；map 没有 cap 操作\n", len(scores))
	fmt.Printf("buffered channel：len=%d 项正在排队，cap=%d 个缓冲槽\n",
		len(jobs), cap(jobs))

	// append 到 len 达到 cap 之前，可以继续使用当前底层数组；超过 cap 时需要新的存储空间。
	// 新容量具体增长到多少是实现策略，不应写业务逻辑依赖那个数字。
	dynamicNumbers = append(dynamicNumbers, 30, 40, 50)
	fmt.Printf("填满 slice 后：len=%d, cap=%d\n", len(dynamicNumbers), cap(dynamicNumbers))
	dynamicNumbers = append(dynamicNumbers, 60)
	fmt.Printf("再 append 一项触发扩容后：len=%d, cap=%d（新 cap 的具体数值不保证）\n",
		len(dynamicNumbers), cap(dynamicNumbers))

	section("3.2 Go const 不是通用的 Java final")
	fmt.Println("Go 常量值的类别：bool、整数、浮点数、复数、字符串，以及底层为这些类型的命名类型。")
	fmt.Println("array/slice/map/struct/channel/func/指针要用 var；即使字面量写死也不能声明为 const。")

	section("4. iota 是 const 块内的声明项索引")
	fmt.Println(sequenceA, sequenceB, sequenceC,
		sequenceText, sequenceTextAgain,
		sequenceHundred, sequenceHundredAgain,
		sequenceResume, sequenceResumeAgain)
	fmt.Printf("同一 ConstSpec：sameSpecA=%d, sameSpecB=%d；下一项 nextSpec=%d\n",
		sameSpecA, sameSpecB, nextSpec)

	section("5. 混合写与不写 iota：编号从未中断")
	fmt.Printf("索引0 fixedA=%d；索引1 iotaB=%d；索引2 iotaC=%d\n",
		mixedFixedA, mixedIotaB, mixedIotaC)
	fmt.Printf("索引3 fixedD=%d；索引4 fixedE=%d；索引5 iotaF=%d\n",
		mixedFixedD, mixedFixedE, mixedIotaF)
	fmt.Printf("索引6 同一项 pairG=%d, pairH=%d；索引7 复用表达式 pairI=%d, pairJ=%d\n",
		mixedPairG, mixedPairH, mixedPairI, mixedPairJ)

	section("6. iota + 左移，以及省略表达式的真实含义")
	fmt.Printf("i=%d, j=%d, k=%d, l=%d\n", shiftI, shiftJ, shiftK, shiftL)
	fmt.Println("j 把复用模板换成 3 << iota；k、l 复制模板后，用各自的索引 2、3 重新计算。")
}
