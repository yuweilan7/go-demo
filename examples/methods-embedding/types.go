// methods-embedding 用可运行的对比演示 Go 的方法、接收者和结构体嵌入。
package main

// Person 是一个独立的结构体类型。
// Student 嵌入 Person 后，可以用更短的选择器访问 Person 的字段和方法，
// 但 Student 和 Person 仍是两个不同的类型，不存在父类、子类关系。
type Person struct {
	Name string
	Age  int
}

// Student 通过匿名字段嵌入 Person。
// 字段没有显式字段名，但它的字段名实际就是类型名 Person，
// 所以既可以写 student.Person.Name，也可能简写为 student.Name。
type Student struct {
	ID    int
	Score int
	Person
}

// ExamScore 是以 int 为底层类型的新定义类型。
// Go 方法不只属于结构体，也可以属于这种在当前包定义的非结构体类型。
type ExamScore int

// ScoreAlias 只是 int 的别名，并没有创建新类型。
// 不能给它定义新方法；所以“能给 int 的别名绑定方法”这个说法并不准确。
type ScoreAlias = int
