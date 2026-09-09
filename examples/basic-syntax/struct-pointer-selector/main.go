// 本示例专门解释：为什么 Student 值和 *Student 指针都能用“.字段”。
package main

import "fmt"

type Student struct {
	ID    int
	Name  string
	Age   int
	Score int
}

func main() {
	// lisi 的静态类型是 Student，变量中直接保存整个结构体值。
	lisi := Student{ID: 101, Name: "lisi", Age: 20, Score: 97}

	// studentPointer 的静态类型是 *Student，变量中保存 lisi 的地址。
	// 它没有保存第二份 Student；&lisi 也没有复制 lisi。
	studentPointer := &lisi

	fmt.Println("========== 类型和值确实不同 ==========")
	fmt.Printf("lisi 的类型：%T，值：%+v\n", lisi, lisi)
	fmt.Printf("studentPointer 的类型：%T，保存的地址：%p\n", studentPointer, studentPointer)
	fmt.Printf("lisi 自身的地址：%p\n", &lisi)

	fmt.Println("\n========== 相同的点号，不同的寻址过程 ==========")

	// lisi.Score：lisi 是 Student，直接选中 lisi 自己内部的 Score 字段。
	lisi.Score = 98
	fmt.Printf("执行 lisi.Score = 98 后：lisi.Score=%d\n", lisi.Score)

	// studentPointer.Score：studentPointer 是 *Student。
	// Go 规定，在“选择结构体字段”这个场景中，编译器会自动插入一次解引用，
	// 因而下面一行就是 (*studentPointer).Score = 99 的便捷写法。
	// 点号写法相同，不代表 lisi 和 studentPointer 的类型或含义相同。
	studentPointer.Score = 99
	fmt.Printf("执行 studentPointer.Score = 99 后：lisi.Score=%d\n", lisi.Score)

	// 显式解引用的写法得到完全相同的效果。括号不能省略：
	// *studentPointer.Score 会先把 studentPointer.Score 选成一个 int，再尝试解引用 int。
	(*studentPointer).Score = 100
	fmt.Printf("执行 (*studentPointer).Score = 100 后：lisi.Score=%d\n", lisi.Score)

	fmt.Println("\n========== 复制行为仍然不同 ==========")

	// 复制 Student 会产生互不影响的结构体副本。
	studentCopy := lisi
	studentCopy.Score = 60
	fmt.Printf("复制并修改结构体：lisi.Score=%d，studentCopy.Score=%d\n",
		lisi.Score, studentCopy.Score)

	// 复制 *Student 只会复制地址；两个指针仍指向同一个 lisi。
	pointerCopy := studentPointer
	pointerCopy.Score = 70
	fmt.Printf("复制并通过指针修改：lisi.Score=%d，两个地址分别为 %p、%p\n",
		lisi.Score, studentPointer, pointerCopy)

	// nil 也能是 *Student 类型，但它没有指向任何 Student。
	// 下面这行可以通过编译，却会在运行时因自动解引用 nil 而 panic，所以只放在注释中：
	// var nobody *Student
	// nobody.Score = 1
}
