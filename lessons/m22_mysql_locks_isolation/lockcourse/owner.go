package lockcourse

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// gate 是 owner 创建的单向门，close 广播允许继续。sync.Once 使正常放行和
// 提前失败清理都可 Open；worker 只等待、不关闭门，避免双 close panic。
type gate struct {
	ch   chan struct{}
	once sync.Once
}

func newGate() *gate  { return &gate{ch: make(chan struct{})} }
func (g *gate) open() { g.once.Do(func() { close(g.ch) }) }

// wait 由 worker 等业务屏障。select 能观察取消；取消只使它返回 error，后续
// Tx/Conn 的 defer 仍要执行，owner 必须再等 Finished，不能把通知当退出。
func (g *gate) wait(ctx context.Context) error {
	select {
	case <-g.ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// taskResult 保存工作错误和清理错误两个事实。预期1213/1205不能吞掉 release
// 失败。Count 是工作报告；最终库存和订单仍由另一连接独立 SELECT 验证。
type taskResult struct {
	Err, Cleanup   error
	Count, Retries int
	Evidence       []string
}
type task struct {
	cancel         context.CancelFunc
	result         <-chan taskResult
	finished       <-chan struct{}
	wrapperCleanup error // 只在 Finished 后读，close建立 happens-before。
	joined         bool
	value          taskResult
}

// start 由 scope.start 调用，返回的 task 马上登记给唯一 owner。result buffer1
// 让 owner 早退时发送结果也能完成；Finished 是最后一项 defer 的广播。
// fn 内层 defer 已完成才能交 result，但 wrapper 的 after 清理仍可能未结束。
func start(ctx context.Context, fn func(context.Context) taskResult, after func() error) *task {
	// :=在这个函数层复用已有参数ctx、声明新cancel；ctx此后是派生取消范围。
	// worker读取的是这个Context接口值；通知由唯一task owner的cancel发出。
	ctx, cancel := context.WithCancel(ctx)
	results, done := make(chan taskResult, 1), make(chan struct{})
	t := &task{cancel: cancel, result: results, finished: done}
	go func() {
		defer close(done) // 最先注册、最后执行；所有其他 defer 都早于 Finished。
		defer cancel()
		if after != nil {
			defer func() { t.wrapperCleanup = after() }()
		}
		defer func() {
			if v := recover(); v != nil {
				results <- taskResult{Err: panicError(v)}
			}
		}()
		results <- fn(ctx)
	}()
	return t
}

// panicError 保留error型panic的原因；普通值也转换成明确失败，不能恢复成nil。
func panicError(v any) error {
	err := fmt.Errorf("worker panic: %v", v)
	// 类型断言检查any接口里实际保存的动态值是否实现error；ok=false不会panic。
	// cause取该error值，Join保留它供上层errors.Is追踪，而非仅保留打印文案。
	if cause, ok := v.(error); ok {
		return errors.Join(err, cause)
	}
	return err
}

// captureTaskPanic 是拥有资源的fn最先登记的defer，所以最后执行。Tx/Conn的
// 清理先把失败写入具名r，再在这里recover并保留panic原因；被恢复的fn直接
// 返回r，不会回到panic下一行执行。必须直接defer这个函数，不能用普通调用。
func captureTaskPanic(r *taskResult) {
	if v := recover(); v != nil {
		r.Err = errors.Join(r.Err, panicError(v))
	}
}

// join 只由单一 owner 调用，先等最终 Finished，再收结果和 wrapperCleanup。
// 超时没有设置 joined；caller 可放开自有门，再用新的预算真正 join。
func (t *task) join(ctx context.Context) (taskResult, error) {
	if t.joined {
		return t.value, nil
	}
	select {
	case <-t.finished:
		t.value = <-t.result
		t.value.Cleanup = errors.Join(t.value.Cleanup, t.wrapperCleanup)
		t.joined = true
		return t.value, nil
	case <-ctx.Done():
		return taskResult{}, errors.Join(ErrJoin, ctx.Err())
	}
}

// scope 是每组实验的唯一并发 owner：它拥有全部任务、门、取消通知和最终等待。
// 没有结果消费者关闭 result；即使实验中途出错，close 也先解除自有门再收尾。
type scope struct {
	tasks []*task
	gates []*gate
}

// gate由实验owner调用并立即登记，返回共享门指针给worker；close在早退时也会
// 打开这同一个门。slice保存指针副本，门的channel/Once对象仍只有一份。
func (s *scope) gate() *gate { g := newGate(); s.gates = append(s.gates, g); return g }

// start 在启动调用返回的同一步登记任务；caller 在创建 scope 后立即 defer close。
// fn 捕获的 store 是共享指针，但业务 SQL 只修改本run独占行，日志由owner打印。
func (s *scope) start(ctx context.Context, fn func(context.Context) taskResult) *task {
	t := start(ctx, fn, nil)
	s.tasks = append(s.tasks, t)
	return t
}

// close 在实验返回时运行。原错误先保留；尚未分类的worker失败、cleanup失败、
// join超时都 Join 到它。已join/已分类的预期故障不再次变成整课失败。
func (s *scope) close(err *error) {
	for _, g := range s.gates {
		g.open()
	}
	for _, t := range s.tasks {
		t.cancel()
	}
	ctx, cancel := cleanupContext()
	defer cancel()
	for _, t := range s.tasks {
		if t.joined {
			continue
		}
		r, e := t.join(ctx)
		*err = errors.Join(*err, e, r.Err, r.Cleanup)
	}
}

// receive 是 owner 等 worker 已到达第一阶段的明确通知；channel带方向只读。
// 这个通知表示读/首锁完成，仍不表示任务结束；结束要另外 join。
func receive(ctx context.Context, ch <-chan int64) (int64, error) {
	select {
	case id := <-ch:
		return id, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// observe 轮询真实状态，ticker只控制查询节奏，不根据“过了多久”猜调度。
// ctx 的deadline限制总等待；每次 predicate也必须使用有界SQL ctx。
func observe(ctx context.Context, predicate func() (bool, error)) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		ok, err := predicate()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
