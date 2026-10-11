// Package lesson 把 defer 的语言规则与真实文件资源实验放在一个独立课程内。
// 全部实验同步执行，没有 goroutine 或 sleep；日志顺序来自实际调用顺序。
package lesson

import (
	"fmt"
	"io"
)

// Trace 是本次入口专属的日志记录器。w 由 caller 提供并拥有，Trace 不关闭它。
// err 保留第一次输出失败，防止输出设备故障时仍声称完整证据已经写出。
type Trace struct {
	w   io.Writer
	err error
}

// NewTrace 由 demo 或测试创建；测试传入内存缓冲区，不依赖控制台截图。
func NewTrace(w io.Writer) *Trace { return &Trace{w: w} }

// Event 由当前同步实验调用；label 指明实验/路径，格式化参数只是教学日志。
// 它不启动异步日志任务，因此每行输出的先后就是本次串行执行的先后。
func (t *Trace) Event(label, format string, args ...any) {
	if t.err != nil {
		return
	}
	_, t.err = fmt.Fprintf(t.w, "[%s] %s\n", label, fmt.Sprintf(format, args...))
}

// Err 由入口在所有资源已经清理后读取，把日志失败交回 main。
func (t *Trace) Err() error { return t.err }
