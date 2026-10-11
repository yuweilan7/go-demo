// Package lesson 把恢复规则、任务结果和进程后果放在可断言的实验里。
// 它只使用标准库；这里的订单是合成数据，不连接数据库或生产服务。
package lesson

import (
	"errors"
	"fmt"
	"runtime/debug"
)

// ErrInvalidOrder 是可预期的业务校验失败，正常用 error 返回，避免启动 panic 展开。
var ErrInvalidOrder = errors.New("订单数量必须大于零")

// PanicFailure 是本课显式恢复边界向 caller 交付的失败记录。
// Value 保存 recover 返回的动态值；Stack 在恢复现场取得，而非返回后补拍调用栈。
// TaskID 和 Stage 表示哪项任务在哪里失败，不能把“进程还活着”当作“订单成功”。
type PanicFailure struct {
	TaskID string
	Stage  string
	Value  any
	Stack  string
}

// Error 让 *PanicFailure 实现 error 接口，caller 可以用 errors.As 取得详细状态。
// 指针接收者 p 指向本次失败记录；fmt 的 %v 展示 panic 值，%T 的动态类型见 demo。
func (p *PanicFailure) Error() string {
	return fmt.Sprintf("任务 %s 在 %s panic: %v", p.TaskID, p.Stage, p.Value)
}

// captureFailure 仅负责保存已经 recover 到的值，不能把 recover 藏进这个 helper。
// 调用者必须在直接 deferred function 内先调用 recover，再传入 value。
func captureFailure(taskID, stage string, value any) *PanicFailure {
	return &PanicFailure{TaskID: taskID, Stage: stage, Value: value, Stack: string(debug.Stack())}
}

// trace 是一个实验自己的事件容器。调用链同步使用同一 *trace，所以追加可被 caller 看见。
// 它没有锁，不能让不同 goroutine 同时追加；worker 实验用 channel 交付完成后的事件副本。
type trace struct{ events []string }

// add 由当前实验的函数记录一个已经发生的事件，不推测下一条调度顺序。
func (t *trace) add(event string) { t.events = append(t.events, event) }

// ChainOutcome 交给 demo/test：Events 是确定的单 goroutine 时间线，Err 是最终任务结果。
type ChainOutcome struct {
	Events []string
	Err    error
}

// RunChain 是第一层 caller，由 demo/test 同步调用，内部建立一次请求专属 trace。
// mode 取 success/error/panic：所有分支走同样三层函数，便于比较返回位置。
func RunChain(mode string) ChainOutcome {
	t := &trace{}
	t.add("L1.begin")
	err := service(t, mode) // := 声明本函数的 err；这里等待同一 goroutine 的 service 调用完成。
	// service 自己恢复后已经结束；因此 caller 的这一句可运行。
	// service 内调用 leaf 之后的普通代码是否执行，是另外一件事，见下方两个事件。
	t.add("L1.after-service")
	return ChainOutcome{Events: t.events, Err: err}
}

// service 是第二层恢复边界：业务输入来自 RunChain，结果 error 交回 RunChain。
// (err error) 是具名结果变量，进入函数时零值为 nil；defer 可给该变量赋值。
func service(t *trace, mode string) (err error) {
	t.add("L2.begin")
	// 先注册 cleanup，再注册 recover，再注册尾部观察器。
	// 退出时按 LIFO：observer -> recover -> cleanup；recover 成功不会跳过更早的 defer。
	defer t.add("L2.cleanup")
	defer func() {
		if value := recover(); value != nil {
			// value 只存在于这个 if 的作用域；recover 必须在此 deferred function 里直接调用。
			// err = 修改外层结果变量；不能写 err :=，那会声明一个被返回值看不见的局部变量。
			err = captureFailure("order-7001", "leaf.validate", value)
			t.add("L2.recovered")
		}
	}()
	defer t.add("L2.observer")
	err = leaf(t, mode)
	// error 是普通返回，所以可以继续执行此行；panic 进入展开后不会再来到这里。
	t.add("L2.after-leaf")
	return err
}

// leaf 是第三层，仅由 service 调用；模拟订单检查，不拥有外部资源。
// 两个 defer 都已注册，panic 前后顺序必须和注册顺序区分。
func leaf(t *trace, mode string) error {
	t.add("L3.begin")
	defer t.add("L3.cleanup.first")
	defer t.add("L3.cleanup.second")
	switch mode {
	case "success":
		t.add("L3.success")
		return nil
	case "error":
		t.add("L3.return-error")
		return ErrInvalidOrder
	case "panic":
		t.add("L3.panic")
		panic("订单转换器违反内部不变量")
		// t.add("L3.after-panic") 不可达；恢复会在 service 的 defer 发生，不会返回这一行。
	default:
		return fmt.Errorf("未知实验 mode: %q", mode)
	}
}

// DeferValues 由 demo/test 调用，给“注册时实参求值”和“退出时闭包读取”一个最小对照。
func DeferValues() (events []string) {
	stage := "registered"
	// defer func 参数 stage 是注册时复制的 string 值；此后给 stage 赋新值不会改这个参数。
	defer func(captured string) { events = append(events, "argument="+captured) }(stage)
	// 闭包没有 stage 参数，它访问的是当前函数那个 stage 变量，退出时会读到 finished。
	defer func() { events = append(events, "closure="+stage) }()
	stage = "finished"
	return events // 先确定具名结果，再运行两个 defer，最后交给 caller。
}
