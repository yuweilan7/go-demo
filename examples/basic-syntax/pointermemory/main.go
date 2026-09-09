// pointermemory 演示两个容易混淆的问题：
//  1. new(T) 的语言语义，与编译器最终选择栈或堆，是两个层次的问题；
//  2. 指针值本身的大小，与指针所指目标的大小，是两个不同的 Sizeof 结果。
package main

import (
	"fmt"
	"unsafe"
)

func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}

// 这里禁止内联只是为了让编译器的逃逸分析诊断更容易观察。
// //go:noinline 是当前 Go 编译器支持的指令，不是理解 new 的必要条件。
//
//go:noinline
func useNewLocally() int {
	pointer := new(int)
	*pointer = 7

	// pointer 没有被返回，也没有被保存到函数外部。
	// 当前编译器可以证明 new(int) 不会逃出本函数，甚至可能直接消除这次分配。
	return *pointer
}

//go:noinline
func returnNewPointer() *int {
	pointer := new(int)
	*pointer = 8

	// 返回以后调用方仍然能通过 pointer 访问这个 int，所以它不能随着本次函数调用结束而失效。
	// 当前编译器的逃逸分析通常会报告 new(int) escapes to heap。
	return pointer
}

//go:noinline
func returnLocalAddress() *int {
	value := 9

	// Go 允许返回局部变量的地址。编译器和运行时会保证 value 活得足够久，
	// 不会像 C 中返回栈上局部变量地址那样形成悬空指针。
	return &value
}

func main() {
	section("1. new(int) 与 &局部变量的语言语义")

	createdByNew := new(int)
	var zero int
	createdByAddress := &zero

	// 两个表达式都得到 *int，目标都已经被初始化为 int 的零值 0。
	// 它们分别指向各自的 int；“语义相近”不表示两个指针相等。
	fmt.Printf("new(int)：类型=%T，初始值=%d\n", createdByNew, *createdByNew)
	fmt.Printf("&zero：  类型=%T，初始值=%d\n", createdByAddress, *createdByAddress)

	*createdByNew = 10
	*createdByAddress = 20
	fmt.Printf("分别修改后：new 的目标=%d，&zero 的目标=%d\n", *createdByNew, *createdByAddress)

	section("2. 返回局部变量地址在 Go 中是安全的")

	returnedByNew := returnNewPointer()
	returnedByAddress := returnLocalAddress()
	fmt.Printf("函数已经返回，指针仍可使用：new=%d，&局部变量=%d\n",
		*returnedByNew, *returnedByAddress)
	fmt.Printf("只在函数内部使用 new 的结果：%d\n", useNewLocally())

	section("3. 指针大小与目标大小是两次不同的测量")

	bytePointer := new(byte)
	arrayPointer := new([1024]byte)
	stringPointer := new(string)

	// unsafe.Sizeof(pointer) 测量 pointer 这个值本身；在当前 64 位平台上是 8 字节。
	// unsafe.Sizeof(*pointer) 先解引用，再测量目标值，所以结果取决于目标类型。
	fmt.Printf("*byte：      指针值=%d 字节，目标 byte=%d 字节\n",
		unsafe.Sizeof(bytePointer), unsafe.Sizeof(*bytePointer))
	fmt.Printf("*[1024]byte：指针值=%d 字节，目标数组=%d 字节\n",
		unsafe.Sizeof(arrayPointer), unsafe.Sizeof(*arrayPointer))
	fmt.Printf("*string：    指针值=%d 字节，目标 string 描述符=%d 字节\n",
		unsafe.Sizeof(stringPointer), unsafe.Sizeof(*stringPointer))

	// Sizeof 只计算这个值自身的浅层大小，不沿着内部指针继续计算数据大小。
	// string 值在当前 64 位实现中是一个包含数据指针和长度的描述符；字符串内容另有存储。
	text := "这段文本明显不止八个字节"
	fmt.Printf("字符串内容=%q，但 string 值自身仍是 %d 字节\n", text, unsafe.Sizeof(text))
}
