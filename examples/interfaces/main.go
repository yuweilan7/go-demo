// interfaces 用可运行的对比演示 Go 接口的隐式实现、方法集、组合、断言和 nil 陷阱。
package main

import "fmt"

// Phone 只描述调用方需要的行为，不关心具体手机用什么字段、如何实现。
// Go 类型不需要写 implements；只要方法集包含下面两个方法，就自动满足 Phone。
type Phone interface {
	Call()
	SendMessage(string)
}

type Apple struct {
	Name  string
	muted bool
}

// Call 和 SendMessage 都使用值接收者，因此它们同时属于 Apple 和 *Apple 的方法集。
// 也就是说，Apple 值与 *Apple 指针都可以赋给 Phone。
func (a Apple) Call() {
	fmt.Printf("%s：打电话\n", a.Name)
}

func (a Apple) SendMessage(message string) {
	fmt.Printf("%s：发送 %q\n", a.Name, message)
}

type Huawei struct {
	Name string
}

func (h Huawei) Call() {
	fmt.Printf("%s：打电话\n", h.Name)
}

func (h Huawei) SendMessage(message string) {
	fmt.Printf("%s：发送 %q\n", h.Name, message)
}

// Muter 是第二个独立的小接口。
type Muter interface {
	Mute()
}

// Mute 需要修改原对象，所以使用指针接收者。
// 因此只有 *Apple 实现 Muter，Apple 值本身不实现 Muter。
func (a *Apple) Mute() {
	a.muted = true
}

// SmartPhone 通过嵌入组合两个接口。
// 实现它的类型必须同时拥有 Phone 和 Muter 的全部方法。
// 这通常称为接口组合；它不是包含两个“接口对象”的运行时嵌套结构。
type SmartPhone interface {
	Phone
	Muter
}

// 编译期接口断言：右侧只参与类型检查，不会在运行时创建有用对象。
// 以后若有人删掉所需方法，编译器会直接在这些行报告问题。
var _ Phone = Apple{}
var _ Phone = (*Apple)(nil)
var _ Phone = Huawei{}
var _ SmartPhone = (*Apple)(nil)

// 下面一行取消注释会编译失败：Mute 只在 *Apple 的方法集中。
// var _ SmartPhone = Apple{}

func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}

// usePhone 接受行为而不是具体结构体，所以 Apple、Huawei 以及将来的其他实现
// 都能复用这段逻辑。这是接口最常见、也最有价值的使用位置。
func usePhone(phone Phone) {
	fmt.Printf("接口中的动态类型：%T\n", phone)
	phone.Call()
	phone.SendMessage("你好，Go")

	// phone.Name // 编译错误：Phone 只暴露接口声明的方法，不暴露具体类型字段。
}

func useSmartPhone(phone SmartPhone) {
	phone.Mute()
	phone.Call()
}

func describe(value any) {
	// 类型 switch 适合对一个接口值区分多种可能的动态类型。
	switch current := value.(type) {
	case int:
		fmt.Printf("%v 的动态类型是 int，乘以 2 得 %d\n", current, current*2)
	case string:
		fmt.Printf("%q 的动态类型是 string，长度是 %d 字节\n", current, len(current))
	case Phone:
		fmt.Printf("%T 的动态类型满足 Phone 接口\n", current)
	case nil:
		fmt.Println("这是一个 nil 接口值")
	default:
		fmt.Printf("暂未处理的动态类型：%T\n", current)
	}
}

func main() {
	section("1. 接口由方法集隐式实现")

	iphone := Apple{Name: "iPhone"}
	mate := Huawei{Name: "Mate"}
	usePhone(iphone)
	usePhone(mate)

	// 接口值可理解为“动态类型 + 动态值”。这里静态类型都是 Phone，
	// 但循环中分别装入 Apple 和 Huawei，因此会分派到各自的方法。
	phones := []Phone{iphone, mate}
	for index, phone := range phones {
		fmt.Printf("phones[%d]：静态类型是 Phone，动态类型是 %T\n", index, phone)
	}

	section("2. new 只分配零值，不负责判断接口")

	// new(T) 对任何类型 T 都合法，返回指向 T 的零值的 *T。
	// 原教程说“new 的参数必须是接口实现”并不准确：是否满足 Phone，
	// 是把 *Apple 赋给 Phone 时才检查的，而不是调用 new 时检查的。
	createdWithNew := new(Apple)
	createdWithNew.Name = "new(Apple)"
	usePhone(createdWithNew)

	// new(int) 同样合法，这证明 new 并不要求参数实现某个接口。
	zeroNumber := new(int)
	fmt.Printf("new(int) 得到 %T，指向零值 %d\n", zeroNumber, *zeroNumber)

	// 已知具体类型时，通常直接构造再赋给接口，比先装进接口、再断言取出字段清晰。
	var phone Phone = &Apple{Name: "直接构造的 Apple"}
	phone.Call()

	section("3. 一个类型可实现多个接口，接口也可以组合")

	// *Apple 同时满足 Phone 和 Muter，所以也满足组合后的 SmartPhone。
	useSmartPhone(&iphone)
	fmt.Printf("调用 Muter 后，原 iphone.muted=%t\n", iphone.muted)

	// useSmartPhone(iphone) // 编译错误：Apple 的方法集没有指针接收者方法 Mute。
	// useSmartPhone(&mate)  // 编译错误：*Huawei 没有 Mute 方法。

	section("4. any 可以保存任意类型，但取回具体类型要断言")

	// any 是 interface{} 的预声明别名，两种拼写语义完全相同。
	// 现代 Go 和标准库源码通常使用更简洁的 any。
	var value any = 8
	fmt.Printf("value=%v，静态类型是 any，动态类型是 %T\n", value, value)

	number, ok := value.(int)
	fmt.Printf("断言 int：number=%d，ok=%t\n", number, ok)

	text, ok := value.(string)
	fmt.Printf("断言 string：text=%q（失败时为零值），ok=%t\n", text, ok)

	// var number2 int = value // 编译错误：any 不会自动还原为 int。
	// number3 := value.(string) // 编译通过，但运行时会 panic；不确定时应使用 value, ok 形式。

	// T 也可以是接口。这里检查动态类型 Apple 是否满足 Phone。
	phoneValue := any(Apple{Name: "断言得到的 Apple"})
	assertedPhone, ok := phoneValue.(Phone)
	fmt.Printf("断言 Phone：动态类型=%T，ok=%t\n", assertedPhone, ok)
	if ok {
		assertedPhone.Call()
	}

	describe(21)
	describe("golang")
	describe(mate)
	describe(nil)

	section("5. nil 接口与装有 nil 指针的接口不同")

	var empty Phone
	fmt.Printf("从未赋值的 Phone：值=%v，是否等于 nil：%t\n", empty, empty == nil)

	// nilApple 的值是 nil，但把它装进 Phone 后，接口仍保存动态类型 *Apple。
	// 所以 typedNilPhone 不是 nil；直接调用这里的值接收者方法还会因解引用 nil 而 panic。
	var nilApple *Apple
	var typedNilPhone Phone = nilApple
	fmt.Printf("装有 nil 指针的 Phone：动态类型=%T，是否等于 nil：%t\n",
		typedNilPhone, typedNilPhone == nil)

	extracted, ok := typedNilPhone.(*Apple)
	fmt.Printf("断言回 *Apple：ok=%t，指针是否为 nil：%t\n", ok, extracted == nil)
	// typedNilPhone.Call() // 会 panic；接口不为 nil，不代表其中的动态值不为 nil。
}
