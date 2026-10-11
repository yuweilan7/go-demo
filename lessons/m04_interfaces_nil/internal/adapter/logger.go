package adapter

// Logger 拥有本次实验的日志切片。它只在当前 goroutine 使用，没有全局注册表。
// 零值 Logger 可用；nil *Logger 不可用。这是两个不同的状态。
type Logger struct {
	entries []string
}

// NewLogger 由实验/组合入口调用，返回具体指针供 caller 显式注入。
// 日志只留在内存，没有文件句柄、网络连接或必须关闭的后台任务。
func NewLogger() *Logger {
	return &Logger{}
}

// Log 由 Service 或嵌入实验调用，message 是 caller 已生成的业务事件。
// l 是指针副本，append 后把新的切片描述符写回同一个 Logger 对象。
func (l *Logger) Log(message string) {
	// 解引用 l 是实际日志副作用。nil *Logger 会在这里 panic，而非静默丢日志。
	l.entries = append(l.entries, message)
}

// Entries 由入口与独立测试读取最终事件。返回新切片，避免 caller 改坏 Logger 内部状态。
// 字符串元素按值复制；本课没有深层可变对象需要再复制。
func (l *Logger) Entries() []string {
	return append([]string(nil), l.entries...)
}

// Name 故意与 Repository.Name 同名，用于同一深度的方法提升冲突。
// 值接收者不会复制切片底层数据；这里只返回固定名字，不修改 entries。
func (l Logger) Name() string {
	return "memory-log"
}
