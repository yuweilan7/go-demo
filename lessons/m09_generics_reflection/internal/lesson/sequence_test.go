package lesson_test

import (
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-demo/lessons/m09_generics_reflection/internal/lesson"
)

// fixture 只在本模块 .local 创建三行订单文件；测试入口拥有删除责任。
// Go 测试进程的 cwd 是 internal/lesson，所以 ../../.local 仍在本模块内。
func fixture(t *testing.T) string {
	t.Helper()
	path, err := lesson.CreateOrdersFile(filepath.Join("..", "..", ".local"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	return path
}

// TestBreakClosesRealFile 验证消费者停止后，迭代器没有读第三项且已经完成真实 Close。
// 先后关系由同步 yield/return/defer 保证，测试不使用 sleep 或 worker 调度猜测。
func TestBreakClosesRealFile(t *testing.T) {
	path := fixture(t)
	trace := &lesson.SequenceTrace{}
	count := 0
	for _, err := range lesson.FileLines(path, trace) {
		if err != nil {
			t.Fatal(err)
		}
		count++
		if count == 2 {
			break
		}
	}
	if count != 2 || trace.LinesRead != 2 || trace.OpenCount != 1 || trace.CloseCount != 1 || trace.CloseErr != nil {
		t.Fatalf("停止时资源事实: count=%d trace=%+v", count, trace)
	}
}

// TestPullOwnerMustStop 先观察未停止的 reader 仍打开，再由 owner 调 stop 等它返回。
// 第一次 next 不是“整个序列执行完成”；stop 可重复，之后 next 固定返回零值/false。
func TestPullOwnerMustStop(t *testing.T) {
	trace := &lesson.SequenceTrace{}
	next, stop := iter.Pull2(lesson.FileLines(fixture(t), trace))
	defer stop()
	_, err, ok := next()
	if !ok || err != nil || trace.CloseCount != 0 || trace.LinesRead != 1 {
		t.Fatalf("next 后序列仍停在 yield: err=%v ok=%t trace=%+v", err, ok, trace)
	}
	stop()
	stop()
	line, err, ok := next()
	if ok || line != "" || err != nil || trace.CloseCount != 1 || trace.LinesRead != 1 || trace.CloseErr != nil {
		t.Fatalf("stop 后资源/next 事实: line=%q err=%v ok=%t trace=%+v", line, err, ok, trace)
	}
}

// TestIteratorPanicStillCloses 检查错误协议和消费者主动 panic 两条不同的展开路径。
// CatchPanic 只隔离本测试调用链；文件 owner 的 defer 必须先执行，再把 panic 交回入口。
func TestIteratorPanicStillCloses(t *testing.T) {
	path := fixture(t)
	badTrace := &lesson.SequenceTrace{}
	caught := lesson.CatchPanic(func() {
		for range lesson.ContinueAfterStopBad(lesson.FileLines(path, badTrace)) {
			break
		}
	})
	if caught == nil || badTrace.LinesRead != 2 || badTrace.CloseCount != 1 {
		t.Fatalf("违反 yield 协议仍应关闭: caught=%v trace=%+v", caught, badTrace)
	}
	consumerTrace := &lesson.SequenceTrace{}
	caught = lesson.CatchPanic(func() {
		for range lesson.FileLines(path, consumerTrace) {
			panic("consumer rejected order")
		}
	})
	if caught != "consumer rejected order" || consumerTrace.LinesRead != 1 || consumerTrace.CloseCount != 1 {
		t.Fatalf("消费者 panic 的清理事实: caught=%v trace=%+v", caught, consumerTrace)
	}
}

// TestSequenceErrors 验证打开失败、完整消费和 Scanner token 超限都遵守资源边界。
// 错误以 Seq2 第二项交付，读完/出错后仍只关闭一次，不用“循环没报错”代替副作用断言。
func TestSequenceErrors(t *testing.T) {
	path := fixture(t)
	trace := &lesson.SequenceTrace{}
	items := 0
	for _, err := range lesson.FileLines(path, trace) {
		if err != nil {
			t.Fatal(err)
		}
		items++
	}
	if items != 3 || trace.LinesRead != 3 || trace.CloseCount != 1 {
		t.Fatalf("完整消费=%d trace=%+v", items, trace)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 100000)), 0o600); err != nil {
		t.Fatal(err)
	}
	trace = &lesson.SequenceTrace{}
	errorsSeen := 0
	for _, err := range lesson.FileLines(path, trace) {
		if err != nil {
			errorsSeen++
		}
	}
	if errorsSeen != 1 || trace.LinesRead != 0 || trace.CloseCount != 1 {
		t.Fatalf("Scanner 失败: errors=%d trace=%+v", errorsSeen, trace)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	trace = &lesson.SequenceTrace{}
	errorsSeen = 0
	for _, err := range lesson.FileLines(path, trace) {
		if err != nil {
			errorsSeen++
		}
	}
	if errorsSeen != 1 || trace.OpenCount != 0 || trace.CloseCount != 0 {
		t.Fatalf("open 失败未拥有文件: errors=%d trace=%+v", errorsSeen, trace)
	}
}

// TestEmptySequenceAndOptionalTrace 验证空文件也关闭 reader，无观察者不妨碍正确消费。
func TestEmptySequenceAndOptionalTrace(t *testing.T) {
	path := fixture(t)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	trace := &lesson.SequenceTrace{}
	count := 0
	for range lesson.FileLines(path, trace) {
		count++
	}
	if count != 0 || trace.OpenCount != 1 || trace.CloseCount != 1 || trace.CloseErr != nil {
		t.Fatalf("空文件的 reader 仍由 iterator 关闭: count=%d trace=%+v", count, trace)
	}
	for _, err := range lesson.FileLines(path, nil) {
		if err != nil {
			t.Fatal(err)
		}
	}
}
