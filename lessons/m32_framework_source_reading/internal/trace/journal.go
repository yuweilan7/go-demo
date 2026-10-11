// Package trace 只记录本进程合成课程事件；事件文本不包含 token、DSN 或用户输入正文。
package trace

import (
	"fmt"
	"io"
	"sync"
)

// Journal 被 root、仓储和实验共享。Mutex 同时保护序号、slice 和 Writer，race 检查不依赖打印碰巧串行。
type Journal struct {
	mu    sync.Mutex
	lines []string
	out   io.Writer
}

// New 由实验入口创建；out 可以为 nil（测试只保留事件），Writer 的生命周期仍归 caller。
func New(out io.Writer) *Journal { return &Journal{out: out} }

// Add 是各参与者同步调用的诊断记录，不创建 goroutine，也不把日志当业务提交证明。
// 同一锁内编号和追加，输出有一条总顺序；该顺序只记录本次运行，不能强制独立请求的调度先后。
func (j *Journal) Add(id, event string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	line := fmt.Sprintf("%03d [%s] %s", len(j.lines)+1, id, event)
	j.lines = append(j.lines, line)
	if j.out != nil {
		fmt.Fprintln(j.out, line)
	}
}

// Lines 返回诊断快照；append 到新 slice 复制底层数组，caller 无法改写 Journal 的内部事件。
func (j *Journal) Lines() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.lines...)
}
