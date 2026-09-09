// interfaceassertions 用可运行的对照解释：
// 1. []T{...} 为什么是切片，以及它的底层数组从哪里来；
// 2. 接口值的类型断言 x.(T) 到底检查什么；
// 3. “装有 nil 指针的接口”为何不等于 nil。
package main

import "fmt"

type Phone interface {
	Call()
}

type Apple struct {
	Name string
}

func (a Apple) Call() {
	fmt.Printf("%s：Apple.Call()\n", a.Name)
}

type Huawei struct {
	Name string
}

func (h Huawei) Call() {
	fmt.Printf("%s：Huawei.Call()\n", h.Name)
}

func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}

func main() {
	section("1. []Phone{...} 是切片，不是数组")

	iphone := Apple{Name: "iPhone"}
	mate := Huawei{Name: "Mate"}

	// 类型写在花括号前面：
	//   []Phone    中括号里没有长度，因此是切片类型。
	//   [2]Phone   长度写死为 2，因此是数组类型。
	//   [...]Phone 让编译器根据元素个数推断数组长度，因此仍是数组类型。
	// 花括号里的 iphone、mate 只负责提供初始元素，不决定容器类型。
	phones := []Phone{iphone, mate}
	fixedArray := [2]Phone{iphone, mate}
	inferredArray := [...]Phone{iphone, mate}

	fmt.Printf("phones 的类型：%T，len=%d，cap=%d\n", phones, len(phones), cap(phones))
	fmt.Printf("fixedArray 的类型：%T\n", fixedArray)
	fmt.Printf("inferredArray 的类型：%T\n", inferredArray)

	// 切片不是元素本体，而是一份描述底层数组的“小结构”：地址、长度、容量。
	// []Phone{iphone, mate} 会创建一个没有变量名的底层数组，再返回覆盖它的切片。
	// 因此这里仍然有底层数组；只是我们只有 phones 这个切片变量，没有数组变量。
	alias := phones
	alias[0] = Huawei{Name: "被 alias 改掉的第 0 项"}
	fmt.Printf("alias 修改后，phones[0] 的动态类型也变成 %T\n", phones[0])

	section("2. 断言具体类型：动态类型必须相同")

	var value any = 8
	fmt.Printf("value：静态类型 any，动态类型 %T，动态值 %v\n", value, value)

	// x.(T) 叫类型断言，只能对接口类型的 x 使用。
	// 两返回值形式不会 panic：成功时得到 T 类型的值和 true；
	// 失败时得到 T 的零值和 false。
	number, ok := value.(int)
	fmt.Printf("value.(int)：number=%d，ok=%t\n", number, ok)

	text, ok := value.(string)
	fmt.Printf("value.(string)：text=%q，ok=%t\n", text, ok)

	// 只有一个返回值时，断言失败会 panic：
	// text = value.(string)

	section("3. any(...) 是类型转换，不是函数调用")

	apple := Apple{Name: "装进 any 的 Apple"}

	// 下面两行语义相同：都把 Apple 值装入静态类型为 any 的接口变量。
	// any 是 interface{} 的别名；any(apple) 使用的是 Go 的 T(x) 类型转换语法。
	var assigned any = apple
	converted := any(apple)
	fmt.Printf("assigned：静态类型 any，动态类型 %T\n", assigned)
	fmt.Printf("converted：静态类型 any，动态类型 %T\n", converted)

	// converted 的静态类型只是 any，编译器不能仅凭 any 保证它能打电话，
	// 所以不能直接把它赋给 Phone，必须在运行时检查其动态类型是否实现 Phone。
	// 目标 T 是接口 Phone 时，不要求动态类型“恰好等于 Phone”；
	// 而是检查动态类型 Apple 是否实现了 Phone。
	assertedPhone, ok := converted.(Phone)
	fmt.Printf("converted.(Phone)：动态类型=%T，ok=%t\n", assertedPhone, ok)
	if ok {
		assertedPhone.Call()
	}

	// 目标 T 是具体类型时，动态类型必须与它相同。
	appleValue, ok := converted.(Apple)
	fmt.Printf("converted.(Apple)：Name=%q，ok=%t\n", appleValue.Name, ok)
	applePointer, ok := converted.(*Apple)
	fmt.Printf("converted.(*Apple)：pointer=%v，ok=%t（动态类型是 Apple，不是 *Apple）\n", applePointer, ok)

	section("4. typed nil：类型在，指针值是 nil")

	var nilApple *Apple // *Apple 的零值是 nil。
	var typedNilPhone Phone = nilApple

	// 可把接口值想成（动态类型，动态值）这一对：
	//   typedNilPhone == (*Apple, nil)
	// 接口只有在“动态类型”和“动态值”都没有时才等于 nil，即 (nil, nil)。
	fmt.Printf("typedNilPhone：动态类型=%T，接口是否为 nil=%t\n",
		typedNilPhone, typedNilPhone == nil)

	// 这里的 (*Apple) 整体是断言的目标类型，“*”是类型的一部分。
	// 它不是对 nil 指针做解引用。动态类型确实是 *Apple，所以 ok=true；
	// 取出的动态值本来就是 nil，所以 extracted==nil 也为 true。
	extracted, ok := typedNilPhone.(*Apple)
	fmt.Printf("typedNilPhone.(*Apple)：ok=%t，extracted==nil=%t\n", ok, extracted == nil)

	// 真正的解引用要在断言结果外再写一个 *，下面这行才会因 nil 而 panic：
	// appleObject := *(typedNilPhone.(*Apple))
	// 当前 Apple.Call 是值接收者方法；通过 nil *Apple 调用它也需要隐式解引用，
	// 因而 typedNilPhone.Call() 同样会 panic，不要用“接口不为 nil”判断调用安全。
}
