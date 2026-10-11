package lab

import (
	"context"
	"fmt"
	"io"

	"go-demo/lessons/m04_interfaces_nil/internal/adapter"
	"go-demo/lessons/m04_interfaces_nil/internal/usecase"
)

// Combined 嵌入两个指针字段；字段名实际为 Logger 和 Repository。
// 没歧义时 Combined.Log/Find 是提升后的选择器，实际接收者仍分别为 *Logger/*Repository。
// Name 同时出现在两个同深度字段的方法集中，Combined.Name 有歧义，不能直接调用。
type Combined struct {
	*adapter.Logger
	*adapter.Repository
}

// NewCombined 由 embedding 实验调用，在具体指针位置拒绝零值嵌入的缺失依赖。
// 组合本身没有新建资源；它持有与 caller 同一 Logger/Repository 对象的指针。
func NewCombined(logger *adapter.Logger, repo *adapter.Repository) (*Combined, error) {
	if logger == nil || repo == nil {
		return nil, fmt.Errorf("combined logger/repository pointers must be non-nil")
	}
	return &Combined{Logger: logger, Repository: repo}, nil
}

// BaseStep 嵌入 Logger，让 b.Log 可以简写为 b.Logger.Log。
// 它仍是一个独立类型，Outer 不是 BaseStep 的子类。
type BaseStep struct {
	*adapter.Logger
}

// Run 由 outer.Run 的提升选择器调用，b 接收到的是 outer.BaseStep 指针。
// b.Log 从 BaseStep 的静态结构寻找 Logger.Log，不会回头搜索 Outer 是否声明同名方法。
func (b *BaseStep) Run() {
	b.Log("inner.run")
}

// Outer 在外层提供 Log，用来复现“像覆写，实际没有接管内层内部调用”的路径。
type Outer struct {
	*BaseStep
}

// Log 由直接 outer.Log 或显式 LogSink 分派调用，给日志添加 outer 前缀。
// 显式选 o.BaseStep.Logger.Log 避免递归调用当前 Outer.Log。
func (o *Outer) Log(message string) {
	o.BaseStep.Logger.Log("outer:" + message)
}

// RunWith 是相同业务步骤的修复：把需要变化的行为明确放在参数契约里。
// caller 传 Outer 时，sink 动态类型为 *Outer，sink.Log 才会实际分派到 Outer.Log。
// 此处要求 caller 提供可用 LogSink，所有权仍归 caller；它不负责关闭或替换依赖。
func RunWith(sink usecase.LogSink) {
	sink.Log("explicit.run")
}

// 编译期断言证明 *Combined 的提升方法满足读取/日志契约，*Outer 的自有方法满足日志契约。
// 即使零值 Combined 的字段都为 nil，它的方法集也不改变；“实现接口”不等于“对象可用”。
var (
	_ usecase.OrderRepository = (*Combined)(nil)
	_ usecase.LogSink         = (*Combined)(nil)
	_ usecase.LogSink         = (*Outer)(nil)
)

// RunEmbedding 由 demo 的 embedding/all 场景调用，先观察反例，再用同一记录器核对绑定。
// 所有动作同步且有限；panic 仅在命名明确的零值故障操作中捕获。
func RunEmbedding(out io.Writer, repo *adapter.Repository) error {
	fmt.Fprintln(out, "\n[E05] embedding：提升、冲突、零值和实际方法绑定")
	var zero Combined // 两个嵌入指针都是 nil；并非已经创建 Logger/Repository 对象。
	fault := ObservePanic(func() { zero.Log("lost") })
	fmt.Fprintf(out, "zero embedded *Logger: panicked=%t\n", fault != "")
	_, err := NewCombined(nil, repo)
	fmt.Fprintf(out, "constructor rejects nil embedded dependency: %t\n", err != nil)

	logger := adapter.NewLogger()
	combined, err := NewCombined(logger, repo)
	if err != nil {
		return err
	}
	combined.Log("combined.begin") // 等价于 combined.Logger.Log；修改的是同一个 logger。
	order, err := combined.Find(context.Background(), "ORDER-1001")
	if err != nil {
		return err
	}
	// combined.Name() 编译错误：ambiguous selector combined.Name。
	// 修复是显式选所属字段，不猜“先嵌入谁就调用谁”；两个名字都保留了各自含义。
	fmt.Fprintf(out, "promoted Find: id=%s; names resolved explicitly: %s | %s\n",
		order.ID, combined.Logger.Name(), combined.Repository.Name())

	outer := &Outer{BaseStep: &BaseStep{Logger: logger}}
	outer.Log("direct")
	outer.Run()    // 提升到 BaseStep.Run；内部 b.Log 仍是 Logger.Log，输出没有 outer 前缀。
	RunWith(outer) // 显式把 *Outer 交给 LogSink；这里才有接口动态分派。
	for index, event := range logger.Entries() {
		fmt.Fprintf(out, "binding event[%d]=%s\n", index, event)
	}
	return nil
}
