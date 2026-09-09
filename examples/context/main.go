// context 示例通过可观察的输出说明：截止时间、取消广播、父子传播和请求级值。
package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// context 的 key 应使用包内自定义类型，避免和其他包碰巧使用的 key 冲突。
// 不要直接使用 "request-id" 这样的 string 作为公共 key。
type requestIDKey struct{}
type userIDKey struct{}

type workerResult struct {
	name string
	err  error
}

func main() {
	demonstrateRootContexts()
	demonstrateValues()
	demonstrateCancellationBroadcast()
	demonstrateCancellationTree()
	demonstrateDeadlineAndTimeout()
	demonstrateCancellationCause()
}

func demonstrateRootContexts() {
	fmt.Println("=== 1. Background 与 TODO：两个不可取消的根 context ===")

	background := context.Background()
	todo := context.TODO()

	_, backgroundHasDeadline := background.Deadline()
	_, todoHasDeadline := todo.Deadline()

	// 根 context 的 Done 返回 nil。接收 nil channel 会永久阻塞，所以实际代码
	// 通常把 ctx.Done() 放进 select；nil case 会自动处于禁用状态。
	fmt.Printf("Background: deadline=%t, done=nil=%t, err=%v\n",
		backgroundHasDeadline, background.Done() == nil, background.Err())
	fmt.Printf("TODO:       deadline=%t, done=nil=%t, err=%v\n",
		todoHasDeadline, todo.Done() == nil, todo.Err())
	fmt.Println("Background 用作已知的顶层起点；TODO 表示调用方暂时还没决定该传哪个 context")
}

func demonstrateValues() {
	fmt.Println("\n=== 2. WithValue：沿父链查找、子节点可覆盖 ===")

	root := context.Background()
	request := context.WithValue(root, requestIDKey{}, "req-001")
	request = context.WithValue(request, userIDKey{}, int64(42))
	retry := context.WithValue(request, requestIDKey{}, "req-001-retry")

	// Value 从当前节点向父节点查找：最近的同 key 值优先。
	requestID, requestIDOK := request.Value(requestIDKey{}).(string)
	userID, userIDOK := request.Value(userIDKey{}).(int64)
	retryID, retryIDOK := retry.Value(requestIDKey{}).(string)
	_, rootCanSeeChildValue := root.Value(requestIDKey{}).(string)

	fmt.Printf("父请求: requestID=%q(ok=%t), userID=%d(ok=%t)\n",
		requestID, requestIDOK, userID, userIDOK)
	fmt.Printf("重试子节点覆盖 requestID=%q(ok=%t)，根节点能反向看到子值=%t\n",
		retryID, retryIDOK, rootCanSeeChildValue)

	// 不要写 ctx.Value(key).(string)：缺少值或类型不符时会 panic。
	// comma-ok 形式把“没有值”变成显式分支。
	_, missing := request.Value(struct{ name string }{"missing"}).(string)
	fmt.Println("安全读取不存在的值，ok =", missing)
	fmt.Println("Value 只适合 request ID、trace ID 等请求级元数据，不适合必选参数或配置")
}

func demonstrateCancellationBroadcast() {
	fmt.Println("\n=== 3. WithCancel：关闭 Done，向所有观察者广播 ===")

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan workerResult, 2)
	started := make(chan struct{}, 2)

	go waitForCancellation(ctx, "worker-A", started, results)
	go waitForCancellation(ctx, "worker-B", started, results)

	// 确认两个 goroutine 都已开始等待，再发出取消，避免用 Sleep 猜调度时机。
	<-started
	<-started
	fmt.Println("多次调用 Done 得到同一个 channel:", ctx.Done() == ctx.Done())
	cancel()

	// cancel 不会发送一份只能被一个接收者取走的值，而是关闭 Done channel。
	// channel 关闭后，所有当前和未来的接收者都能立刻继续。
	observed := []workerResult{<-results, <-results}
	sort.Slice(observed, func(i, j int) bool { return observed[i].name < observed[j].name })
	for _, result := range observed {
		fmt.Printf("%s 收到广播，Err=%v\n", result.name, result.err)
	}

	// Done 已关闭，所以再次接收也会立即完成；取消函数也允许重复调用。
	<-ctx.Done()
	cancel()
	fmt.Println("取消后再次接收 Done 仍立即完成；重复调用 cancel 也安全")
}

func waitForCancellation(
	ctx context.Context,
	name string,
	started chan<- struct{},
	results chan<- workerResult,
) {
	started <- struct{}{}

	// 真实工作循环通常还会有 I/O、ticker 或任务 channel 等其他 case。
	// ctx 本身不会强行杀死 goroutine；goroutine 必须主动观察 Done 并返回。
	<-ctx.Done()
	results <- workerResult{name: name, err: ctx.Err()}
}

func demonstrateCancellationTree() {
	fmt.Println("\n=== 4. 取消树：向下传播，不向上或横向传播 ===")

	parent, cancelParent := context.WithCancel(context.Background())
	childA, cancelChildA := context.WithCancel(parent)
	childB, cancelChildB := context.WithCancel(parent)
	defer cancelChildA()
	defer cancelChildB()

	cancelChildA()
	<-childA.Done()
	fmt.Printf("只取消 child-A: parent=%v, child-A=%v, child-B仍活跃=%t\n",
		parent.Err(), childA.Err(), isActive(childB))

	// 取消父节点会继续取消所有尚未取消的后代。
	cancelParent()
	<-childB.Done()
	fmt.Printf("再取消 parent: parent=%v, child-B=%v\n", parent.Err(), childB.Err())
	fmt.Println("某个 RPC 返回错误不会自动取消兄弟 RPC；协调者要据此调用父级 cancel")
}

func isActive(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	default:
		return true
	}
}

func demonstrateDeadlineAndTimeout() {
	fmt.Println("\n=== 5. WithTimeout / WithDeadline：自动取消与手动取消 ===")

	timeoutCtx, stopTimeoutTimer := context.WithTimeout(context.Background(), 30*time.Millisecond)
	// 即使预期一定会超时，也应调用返回的函数，及时释放关联资源。
	defer stopTimeoutTimer()

	deadline, hasDeadline := timeoutCtx.Deadline()
	fmt.Printf("WithTimeout 已设置截止时间=%t，当前尚未过期=%t\n",
		hasDeadline, time.Until(deadline) > 0)
	<-timeoutCtx.Done()
	fmt.Printf("计时结束: Err=%v, 是 DeadlineExceeded=%t\n",
		timeoutCtx.Err(), errors.Is(timeoutCtx.Err(), context.DeadlineExceeded))

	// WithDeadline 接收绝对时间。这里给出很远的截止时间，却先手动取消，
	// 因此结束原因是 Canceled，而不是 DeadlineExceeded。
	deadlineCtx, cancelBeforeDeadline := context.WithDeadline(
		context.Background(),
		time.Now().Add(time.Hour),
	)
	cancelBeforeDeadline()
	<-deadlineCtx.Done()
	fmt.Printf("截止前手动取消: Err=%v, 是 Canceled=%t\n",
		deadlineCtx.Err(), errors.Is(deadlineCtx.Err(), context.Canceled))

	// 子节点不能把父节点的截止时间延后；父节点先到期时，整条子树都应停止。
	parent, cancelParent := context.WithTimeout(context.Background(), time.Hour)
	defer cancelParent()
	child, cancelChild := context.WithDeadline(parent, time.Now().Add(2*time.Hour))
	defer cancelChild()
	parentDeadline, _ := parent.Deadline()
	childDeadline, _ := child.Deadline()
	fmt.Println("子节点请求更晚截止时间，实际仍沿用父级更早期限:", childDeadline.Equal(parentDeadline))
}

func demonstrateCancellationCause() {
	fmt.Println("\n=== 6. WithCancelCause：保留业务取消原因 ===")

	// Go 1.20 起可以附带业务原因。为兼容既有代码，Err 仍只返回
	// context.Canceled；需要具体原因时使用 context.Cause。
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("rpc1 failed: inventory unavailable"))
	<-ctx.Done()

	fmt.Println("ctx.Err():", ctx.Err())
	fmt.Println("context.Cause(ctx):", context.Cause(ctx))
}
