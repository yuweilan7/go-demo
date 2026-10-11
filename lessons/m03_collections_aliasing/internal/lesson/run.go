package lesson

import (
	"fmt"
	"io"
	"runtime"
)

// transcript 由 Run 创建，汇总日志的第一个写入错误。
// out 的生命周期由调用方拥有：Run 不关闭 stdout，也不在 Writer 返回后启动后台写入。
type transcript struct {
	out io.Writer
	err error
}

// log 由每个实验同步调用，写出一条状态事件；Writer 失败后记录错误并停止后续输出。
// 实验仍完成本地步骤和清理，Run 在退出时把这个错误交给 main，不静默报告成功。
func (t *transcript) log(format string, args ...any) {
	if t.err != nil {
		return
	}
	_, t.err = fmt.Fprintf(t.out, format+"\n", args...)
}

// Run 由 cmd/demo 或集成测试调用，是唯一的实验分派入口。
// all/core 走逻辑错误与修复对照，nil-map 是显式故障；未知编号返回 error 而不 panic。
// 每个实验使用局部合成数据，没有 goroutine/锁/channel/连接，不存在取消或等待清理的隐含路径。
func Run(out io.Writer, caseName string) error {
	t := &transcript{out: out}
	t.log("M03 runtime=%s module-language=go1.27 case=%s", runtime.Version(), caseName)
	var experiments []func(*transcript) error
	switch caseName {
	case "all":
		experiments = []func(*transcript) error{coreExperiment, collectionsExperiment, loopsExperiment, textExperiment}
	case "core":
		experiments = []func(*transcript) error{coreExperiment}
	case "slices":
		experiments = []func(*transcript) error{slicesExperiment}
	case "cache":
		experiments = []func(*transcript) error{cacheExperiment}
	case "collections":
		experiments = []func(*transcript) error{collectionsExperiment}
	case "loops":
		experiments = []func(*transcript) error{loopsExperiment}
	case "text":
		experiments = []func(*transcript) error{textExperiment}
	case "nil-map":
		nilMapFailure(t)
	default:
		return fmt.Errorf("未知实验编号 %q", caseName)
	}
	for _, experiment := range experiments {
		if err := experiment(t); err != nil {
			// 包装后保留原因链；只要关键状态不成立，立即返回失败，main 会非零退出。
			return fmt.Errorf("%s: %w", caseName, err)
		}
	}
	if t.err != nil {
		return fmt.Errorf("写实验日志: %w", t.err)
	}
	t.log("\nM03 PASS case=%s；全部同步步骤完成，无外部资源或后台任务", caseName)
	return t.err
}
