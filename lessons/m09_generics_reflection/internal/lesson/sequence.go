package lesson

import (
	"bufio"
	"fmt"
	"iter"
	"os"
)

// SequenceTrace 是本次序列的观察对象，由消费入口创建并读出最终资源事实。
// FileLines 的闭包共享此指针；没有同步保护，契约是单个消费者顺序调用。
// OpenCount/CloseCount 记录真实 os.File 的打开与成功关闭，不是假 Rows 的替代证据。
type SequenceTrace struct {
	OpenCount  int
	CloseCount int
	LinesRead  int
	CloseErr   error
	Events     []string
}

// record 由迭代器同步追加观察事件。slice 本身属于 trace，不返回给可并发的写入者。
func (t *SequenceTrace) record(event string) { t.Events = append(t.Events, event) }

// FileLines 由 demo/test 创建序列，path 来自本模块合成文件，trace 来自消费入口。
// 返回 iter.Seq2[string,error] 是函数值；这里只捕获 path 与 trace，尚未打开文件。
// 真正取得 os.File 的是下面这个闭包，因而它负责 defer Close；消费者负责及时结束。
// 序列既可由 range 调用，也可交给 iter.Pull2；不能由多个消费者同时调用。
func FileLines(path string, trace *SequenceTrace) iter.Seq2[string, error] {
	// 没有观察者时也使用本次独立 trace，避免 nil 观察指针在取得文件后造成清理遗漏。
	if trace == nil {
		trace = &SequenceTrace{}
	}
	return func(yield func(string, error) bool) {
		file, err := os.Open(path)
		if err != nil {
			// open 失败未取得资源，只交付一次 error 项；不再调用 yield。
			yield("", fmt.Errorf("open orders: %w", err))
			return
		}
		trace.OpenCount++
		trace.record("open")
		// defer 在取得文件之后注册。捕获的 file 指针始终代表本次打开的文件。
		// 无论读完、yield=false，还是消费者 panic，这个闭包栈展开时都执行关闭。
		defer func() {
			trace.CloseErr = file.Close()
			if trace.CloseErr == nil {
				trace.CloseCount++
			}
			trace.record("close")
		}()

		scanner := bufio.NewScanner(file)
		// Scan 在当前调用链同步读文件，没有 producer goroutine 或 channel 缓冲。
		// 本课文件只有三条短记录；Scanner 默认 token 上限仍适用，超限进入 Err 分支。
		for scanner.Scan() {
			trace.LinesRead++
			trace.record(fmt.Sprintf("read:%d", trace.LinesRead))
			if !yield(scanner.Text(), nil) {
				// range 内 break/return 让编译器提供的 yield 返回 false。
				// 现在立刻 return，既不读第三行，也不在 false 之后再次调用 yield。
				trace.record("yield:false")
				return
			}
			trace.record("yield:true")
		}
		if err := scanner.Err(); err != nil {
			yield("", fmt.Errorf("scan orders: %w", err))
		}
		// Close 错误写入 trace.CloseErr，而不在停止之后再 yield。
		// 消费者应检查该字段；本课未注入 Close 失败，不把成功运行泛化为所有文件系统。
	}
}

// ContinueAfterStopBad 包装 FileLines，故意忽略消费者的停止决定。
// 外层 range 提供 yield，内层 range 继续拉取源；第二次调用已拒绝的 yield 会 panic。
// cmd/demo 用 CatchPanic 有界恢复，这不是生产迭代器可采用的恢复策略。
func ContinueAfterStopBad(source iter.Seq2[string, error]) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for line, err := range source {
			yield(line, err) // 错误就在此处：缺少 if !yield(...) { return }。
		}
	}
}

// CatchPanic 只由故障演示/断言调用，把当前调用链的 panic 值交还给观察入口。
// defer 的匿名函数直接调用 recover；action 中断后不会从出错行继续执行。
// 若 action 另起 goroutine，该 goroutine 的 panic 不在本恢复边界内；本课没有那条路径。
func CatchPanic(action func()) (caught any) {
	defer func() {
		caught = recover()
	}()
	action()
	return nil
}
