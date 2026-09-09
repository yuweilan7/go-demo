// 本示例通过 Student 讲解 Go 结构体的定义、初始化、字段访问和复制语义。
package main

import "fmt"

// Student 是一个自定义的结构体类型。
//
// ID、Name、Age、Score 叫作字段（field），不是四个彼此分散的变量。
// 一个 Student 值会把这四项数据组合在一起，适合表达一个完整的业务概念。
// 字段名以大写字母开头，因此从其他包也可以访问；如果只在当前包使用，
// 也可以采用 id、name 这样的未导出字段名。
type Student struct {
	ID    int
	Name  string
	Age   int
	Score int
}

func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}

func main() {
	section("1. 结构体的零值")

	// 结构体不需要构造函数才能使用。只声明、不显式初始化时，
	// 每个字段分别取得自身类型的零值：int 是 0，string 是空字符串。
	var empty Student
	fmt.Printf("empty=%+v\n", empty)
	fmt.Printf("ID=%d, Name=%q, Age=%d, Score=%d\n",
		empty.ID, empty.Name, empty.Age, empty.Score)

	section("2. 按字段名初始化（推荐）")

	// 这种形式通常叫 keyed literal（带字段名的结构体字面量）。
	// 字段顺序可以改变；没写出的字段自动取零值。
	zhangsan := Student{
		Name: "zhangsan",
		ID:   100,
		Age:  18,
		// Score 没有写，所以是 0。
	}
	fmt.Printf("zhangsan=%+v\n", zhangsan)

	// %v 只显示字段值；%+v 会同时显示字段名，更适合观察结构体。
	fmt.Printf("使用 %%v：%v\n", zhangsan)
	fmt.Printf("使用 %%+v：%+v\n", zhangsan)

	section("3. 按字段顺序初始化（能用，但更脆弱）")

	// 不写字段名时，值必须与 Student 中的字段数量、顺序和类型一一对应。
	// 一旦 Student 增删字段或调整顺序，这里就可能编译失败，甚至在字段类型相同时
	// 悄悄表达错误含义。因此业务代码通常优先使用上面的带字段名写法。
	lisi := Student{101, "lisi", 20, 97}
	fmt.Printf("lisi=%+v\n", lisi)

	// 下面这些错误只保留在注释里，避免破坏可运行示例：
	// Student{101, "lisi"}              // 编译错误：too few values in struct literal
	// Student{101, "lisi", 20, 97, 1}  // 编译错误：too many values in struct literal
	// Student{"lisi", 101, 20, 97}     // 编译错误：string 不能用作 ID 的 int 值

	section("4. 使用点号访问和修改字段")

	// 结构体变量用“变量.字段”读取或修改。
	fmt.Printf("修改前：姓名=%s，分数=%d\n", lisi.Name, lisi.Score)
	lisi.Score = 99
	fmt.Printf("修改后：姓名=%s，分数=%d\n", lisi.Name, lisi.Score)

	// &Student{...} 创建一个 Student 值，并取得指向它的 *Student 指针。
	studentPointer := &Student{
		ID:    102,
		Name:  "wangwu",
		Age:   19,
		Score: 95,
	}

	// Go 允许直接写 studentPointer.Score；对于 *Student，编译器会自动解引用，
	// 其效果相当于 (*studentPointer).Score。这里只是语法便利，并没有复制结构体。
	studentPointer.Score = 96
	fmt.Printf("studentPointer 的类型=%T，指向的学生=%+v\n",
		studentPointer, *studentPointer)

	section("5. 结构体值会复制，指针会共享同一个值")

	// Student 是值类型。这里会把 lisi 的四个字段完整复制给 copied，
	// 修改 copied 不会影响 lisi。这个行为与 Java 中复制对象引用不同。
	copied := lisi
	copied.Name = "lisi-copy"
	copied.Score = 60
	fmt.Printf("原值 lisi：%+v\n", lisi)
	fmt.Printf("副本 copied：%+v\n", copied)

	// 复制 *Student 指针时只复制地址。alias 与 studentPointer 指向同一个 Student，
	// 因此通过 alias 修改字段后，从 studentPointer 也能看到变化。
	alias := studentPointer
	alias.Score = 100
	fmt.Printf("studentPointer=%p, alias=%p\n", studentPointer, alias)
	fmt.Printf("通过 alias 修改后：%+v\n", *studentPointer)
}
