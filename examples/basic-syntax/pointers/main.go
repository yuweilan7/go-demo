// 本示例把 Go 指针的四个核心动作放在一起观察：
// 1. 声明指针类型 *T；2. 用 & 取得地址；3. 用 * 读写目标；4. 用 new(T) 创建零值变量。
package main

import (
	"fmt"
	"unsafe"
)

func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}

// setByValue 接收的是 int 的副本。修改副本不会影响调用方的 number。
func setByValue(value int) {
	value = 100
	fmt.Printf("setByValue 内部：value=%d，副本地址=%p\n", value, &value)
}

// setByPointer 接收的是 *int：它保存某个 int 的地址。
// 对 pointer 解引用后赋值，就能修改调用方的那个 int。
func setByPointer(pointer *int) {
	*pointer = 200
	fmt.Printf("setByPointer 内部：pointer=%p，目标值=%d\n", pointer, *pointer)
}

func main() {
	section("1. *int 是类型，nil 是指针零值")

	// *int 整体是一个类型，意思是“指向 int 的指针”。
	// 更准确地说，不是“在变量 a 前加 *”，而是声明 a 的类型为 *int。
	// 指针带有目标类型：*int 不能直接接收 *string，编译器会阻止类型混用。
	var a *int
	fmt.Printf("刚声明：a=%v，类型=%T，a == nil 为 %v\n", a, a, a == nil)

	// nil 指针没有指向 int。比较或打印 nil 是安全的；解引用 nil 会 panic。
	// 下面这行语法正确，但若取消注释，运行时会报 invalid memory address：
	// fmt.Println(*a)

	section("2. & 取地址，* 解引用")

	b := 3
	a = &b // &b 取得变量 b 的地址，结果的类型是 *int。

	fmt.Printf("b 的值=%d，b 的地址=%p\n", b, &b)
	fmt.Printf("a 保存的地址=%p，*a 读出的值=%d\n", a, *a)

	// 表达式中的 *a 是解引用：沿着 a 中的地址找到 b。
	// *a 不仅可以读取，还可以作为赋值左侧修改目标变量。
	*a = 4
	fmt.Printf("执行 *a = 4 后：b=%d，*a=%d\n", b, *a)

	section("3. 复制指针只复制地址")

	alias := a
	fmt.Printf("a=%p，alias=%p，两个指针是否相等：%v\n", a, alias, a == alias)

	// alias 和 a 是两个指针变量，但保存了同一个地址，所以它们共享同一个目标 b。
	*alias = 5
	fmt.Printf("通过 alias 修改后：b=%d，*a=%d，*alias=%d\n", b, *a, *alias)

	section("4. 值传递与指针传递")

	number := 10
	fmt.Printf("调用前：number=%d，地址=%p\n", number, &number)
	setByValue(number)
	fmt.Printf("值参数返回后：number=%d（调用方没有改变）\n", number)
	setByPointer(&number)
	fmt.Printf("指针参数返回后：number=%d（调用方已被修改）\n", number)

	section("5. new(T) 创建零值变量并返回 *T")

	// new(string) 会创建一个匿名的 string 变量，把它初始化为 string 的零值 ""，
	// 再返回指向该变量的 *string。它返回的不是 string，而是指针。
	message := new(string)
	fmt.Printf("new 后：message=%p，类型=%T，*message=%q\n", message, message, *message)

	*message = "Golang 学习之路"
	fmt.Printf("赋值后：message=%p，*message=%q\n", message, *message)

	// 从语言语义上看，下面两种写法都得到“指向零值 int 的指针”。
	// 不要把 new 理解成必须直接向操作系统申请堆内存：编译器会通过逃逸分析
	// 决定存储位置，运行时与垃圾回收器负责生命周期，程序员不需要手动 free。
	createdByNew := new(int)
	var zero int
	createdByAddress := &zero
	fmt.Printf("new(int)：值=%d，地址=%p；&zero：值=%d，地址=%p\n",
		*createdByNew, createdByNew, *createdByAddress, createdByAddress)

	section("6. 指针大小是平台观察，不是目标值大小")

	intPointer := new(int)
	stringPointer := new(string)
	largeArrayPointer := new([1024]byte)

	// unsafe.Sizeof 展示当前编译目标下指针值占多少字节。
	// 常见 32/64 位目标分别是 4/8 字节；这是实现和目标平台的性质，
	// 不应把具体数字写成 Go 语言规范对所有实现的永久保证。
	fmt.Printf("当前平台：*int=%d 字节，*string=%d 字节，*[1024]byte=%d 字节\n",
		unsafe.Sizeof(intPointer), unsafe.Sizeof(stringPointer), unsafe.Sizeof(largeArrayPointer))
	fmt.Printf("目标本身：[1024]byte=%d 字节；指针大小与目标大小无关\n",
		unsafe.Sizeof(*largeArrayPointer))

	section("7. Go 指针的边界")

	fmt.Println("普通 Go 代码不能做指针加减，也不需要手动释放内存。")
	fmt.Println("优先传值；需要修改调用方数据或共享同一对象身份时，再考虑指针。")
}
