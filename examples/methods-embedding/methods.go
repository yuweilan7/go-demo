package main

import "fmt"

// Passed 的接收者是 ExamScore 值，说明方法并不局限于结构体。
// 但接收者的基类型必须是在当前包定义的类型；不能直接给内置 int 加方法。
func (score ExamScore) Passed() bool {
	return score >= 60
}

// GetName 定义在 Person 值上。每次调用都会把 Person 值复制给接收者 p。
// 本方法只读取很小的结构体，使用值接收者清晰且安全。
func (p Person) GetName() string {
	return p.Name
}

// Rename 需要修改原来的 Person，所以使用指针接收者。
func (p *Person) Rename(name string) {
	p.Name = name
}

// GetScore 使用值接收者，只读取副本中的分数。
func (student Student) GetScore() int {
	return student.Score
}

// SetScore 使用指针接收者，通过地址修改原 Student。
func (student *Student) SetScore(score int) {
	student.Score = score
}

// SetScoreOnCopy 故意在值接收者上赋值，用来观察复制语义。
// 函数内的 student 只是调用者的一份副本，因此这个方法通常没有实际用途。
func (student Student) SetScoreOnCopy(score int) {
	fmt.Printf("值接收者拿到的副本地址=%p\n", &student)
	student.Score = score
	fmt.Printf("方法内部的副本分数=%d\n", student.Score)
}

// ScoreReader 和 ScoreWriter 用来展示“能直接调用”与“属于方法集”并非一回事。
type ScoreReader interface {
	GetScore() int
}

type ScoreWriter interface {
	SetScore(int)
}

func printScore(reader ScoreReader) {
	fmt.Printf("通过 ScoreReader 读取=%d\n", reader.GetScore())
}

func overwriteScore(writer ScoreWriter, score int) {
	writer.SetScore(score)
}

// 编译期断言：Student 和 *Student 都拥有值接收者方法 GetScore；
// 只有 *Student 的方法集拥有指针接收者方法 SetScore。
var _ ScoreReader = Student{}
var _ ScoreReader = (*Student)(nil)
var _ ScoreWriter = (*Student)(nil)

// 下面一行若取消注释会编译失败：Student 本身不实现 ScoreWriter。
// var _ ScoreWriter = Student{}
