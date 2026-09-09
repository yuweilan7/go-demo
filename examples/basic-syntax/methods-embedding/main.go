package main

import "fmt"

func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}

func receivePerson(person Person) {
	fmt.Printf("receivePerson 收到：%s，%d 岁\n", person.Name, person.Age)
}

func newStudent() Student {
	return Student{ID: 200, Score: 80}
}

func main() {
	section("1. 方法也能绑定到自定义的非结构体类型")

	mathScore := ExamScore(59)
	fmt.Printf("分数=%d，及格=%t\n", mathScore, mathScore.Passed())
	mathScore = 60
	fmt.Printf("分数=%d，及格=%t\n", mathScore, mathScore.Passed())

	// 新定义类型和它的底层类型是两个不同的类型，变量之间通常要显式转换。
	// 别名则完全是同一个类型，只是多了一个拼写。
	var rawScore int = 75
	var aliasScore ScoreAlias = rawScore
	definedScore := ExamScore(rawScore)
	fmt.Printf("别名可直接接收 int：%T(%d)；新类型需转换：%T(%d)\n",
		aliasScore, aliasScore, definedScore, definedScore)

	// ExamScore 是新定义类型，而 ScoreAlias 只是 int 的另一个拼写。
	// 下面这种给内置类型或别名定义方法的代码不能通过编译：
	// func (score int) Passed() bool { ... }        // 不能给非本地类型 int 定义方法
	// func (score ScoreAlias) Passed() bool { ... } // ScoreAlias 仍然只是 int

	section("2. 值接收者复制值，指针接收者修改原值")

	student := Student{
		ID:    100,
		Score: 98,
		Person: Person{
			Name: "zhangsan",
			Age:  18,
		},
	}

	fmt.Printf("初始分数=%d，main 中 student 的地址=%p\n", student.GetScore(), &student)
	student.SetScoreOnCopy(50)
	fmt.Printf("值接收者方法结束后，原分数仍是=%d\n", student.GetScore())

	// 方法表达式会把接收者显式变成第一个参数。这是合法的 Go 写法，
	// 也最接近普通函数 SetScoreOnCopy(student, 60) 的观察角度。
	setScoreOnCopy := Student.SetScoreOnCopy
	setScoreOnCopy(student, 60)
	fmt.Printf("用方法表达式调用后，原分数仍是=%d\n", student.GetScore())

	// student 的类型是 Student，但变量是可寻址的，因此 Go 会把下面的调用
	// 便利地改写为 (&student).SetScore(100)。这不是在 Student 的方法集中
	// 凭空增加了 SetScore，只是一次调用语法糖。
	student.SetScore(100)
	fmt.Printf("指针接收者方法结束后，原分数变为=%d\n", student.GetScore())

	studentPointer := &student
	// *Student 调用值接收者方法时，Go 会自动取它指向的 Student 值；
	// GetScore 得到的接收者仍然是一份复制品。
	fmt.Printf("*Student 也能调用值接收者方法：%d\n", studentPointer.GetScore())
	studentPointer.SetScoreOnCopy(1)
	fmt.Printf("*Student 调用值接收者的写方法后，原分数仍是=%d\n", student.GetScore())

	// Go 当然可以直接创建指针。new(Student) 返回 *Student；更常见的写法是
	// &Student{...}，因为它可以在创建时顺便填写字段。
	allocatedPointer := new(Student)
	allocatedPointer.SetScore(70)
	literalPointer := &Student{ID: 300, Score: 80}
	literalPointer.SetScore(90)
	fmt.Printf("new(Student) 的分数=%d，&Student{...} 的分数=%d\n",
		allocatedPointer.GetScore(), literalPointer.GetScore())

	// 临时返回值和 map 元素通常不可寻址，不能依靠自动取地址调用指针接收者方法：
	// newStudent().SetScore(90) // 编译错误：cannot call pointer method on newStudent()
	// students := map[string]Student{"a": student}
	// students["a"].SetScore(90) // 编译错误：cannot call pointer method on map index expression
	// 修正方式之一是先放进可寻址变量；这里的 temporary 就是可寻址变量。
	temporary := newStudent()
	temporary.SetScore(90)
	fmt.Printf("临时值先赋给变量后可修改：%d\n", temporary.GetScore())

	// map 下标表达式不可寻址。若 map 保存的是值，需要“取出副本、修改、写回”。
	studentsByValue := map[string]Student{"a": student}
	studentFromMap := studentsByValue["a"]
	studentFromMap.SetScore(91)
	studentsByValue["a"] = studentFromMap
	fmt.Printf("map[string]Student 修改后写回：%d\n", studentsByValue["a"].GetScore())

	// 若业务需要频繁原地修改，也可以让 map 保存指针。此时 map 元素表达式虽然
	// 仍不可寻址，但取出的值本身已经是 *Student，不再需要编译器替它取地址。
	studentsByPointer := map[string]*Student{"a": &Student{ID: 400, Score: 81}}
	studentsByPointer["a"].SetScore(92)
	fmt.Printf("map[string]*Student 可直接修改指向的对象：%d\n",
		studentsByPointer["a"].GetScore())

	section("3. 直接调用规则与接口方法集")

	// 值接收者方法同时属于 Student 和 *Student 的方法集。
	printScore(student)
	printScore(&student)

	// 指针接收者方法只属于 *Student 的方法集，所以接口需要接收指针。
	overwriteScore(&student, 95)
	fmt.Printf("通过 ScoreWriter 修改后=%d\n", student.GetScore())
	// overwriteScore(student, 95) // 编译错误：Student 没有实现 ScoreWriter

	section("4. 嵌入带来字段和方法提升，不是继承")

	// 完整路径是 student.Person.Name；没有歧义时，选择器可以提升并简写。
	fmt.Printf("完整字段路径=%s，提升后的简写=%s\n",
		student.Person.Name, student.Name)

	// GetName 是 Person 的方法。由于 Person 被嵌入，它可以提升为 student.GetName。
	fmt.Printf("完整方法路径=%s，提升后的简写=%s\n",
		student.Person.GetName(), student.GetName())

	// Rename 是 *Person 的方法。student 可寻址，它内部的 Person 也可寻址，
	// 因而这里组合了“方法提升”和“自动取地址”两条便利规则。
	student.Rename("lisi")
	fmt.Printf("调用提升的 Rename 后：%s\n", student.Name)

	// 嵌入没有建立 is-a（是一个）关系：需要 Person 时不能直接传 Student。
	receivePerson(student.Person)
	// receivePerson(student) // 编译错误：Student 不能作为 Person 使用

	// Student 也可以声明自己的同名字段或方法来遮蔽提升成员；这不是 Java/C++
	// 意义上的 override。需要时仍可通过 student.Person 明确访问嵌入值。

	section("5. 类型和方法可以分散在同一个包的不同文件")

	fmt.Println("Person、Student 定义在 types.go，方法定义在 methods.go。")
	fmt.Println("三个文件都声明 package main，所以编译时会组成同一个包。")
}
