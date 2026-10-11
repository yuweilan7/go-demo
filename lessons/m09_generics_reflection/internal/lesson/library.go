package lesson

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/cenkalti/backoff/v5"
)

// LibraryReport 把真实 backoff 库的尝试次数和最终错误交给 demo 与断言。
// CancellationCalls 观察“ctx 已取消但 operation 至少调用一次”的当前库契约边界。
type LibraryReport struct {
	SuccessCalls      int
	FailureCalls      int
	CancellationCalls int
	Value             Order
	Failure           error
	Cancellation      error
}

// RunLibrary 是 cmd/demo 的库签名阅读实验，所有 operation 只读合成数据，最多三次。
// Retry 为同步调用；BackOff 为状态接口，不能把同一个有状态策略无条件并发分享。
// 本实验失败返回不代表远端请求，不能作为真实 HTTP/数据库或幂等写入证明。
func RunLibrary(out io.Writer) LibraryReport {
	transient := errors.New("合成订单暂未可见")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel() // 此函数拥有 deadline timer，返回时释放；并不凭此中止一个任意阻塞 operation。
	report := LibraryReport{}

	// Operation[T] 的参数只有 ()，不会由 Retry 自动向 operation 传 ctx。
	// 真正的网络 operation 应在闭包里捕获 ctx 并交给支持取消的 I/O API。
	operation := func() (Order, error) {
		report.SuccessCalls++
		if report.SuccessCalls == 1 {
			return Order{}, transient
		}
		return SampleOrders()[0], nil
	}
	// ConstantBackOff 是实际库的普通接口实现；interval=0 保持真实 Retry 循环但不长时间等待。
	value, err := backoff.Retry(ctx, operation,
		backoff.WithBackOff(backoff.NewConstantBackOff(0)),
		backoff.WithMaxTries(3),
		backoff.WithMaxElapsedTime(time.Second),
	)
	if err != nil {
		panic(fmt.Sprintf("正常 Retry 未成功: %v", err))
	}
	report.Value = value
	fmt.Fprintf(out, "LIB success calls=%d value=%s\n", report.SuccessCalls, value.Summary())

	_, report.Failure = backoff.Retry(ctx, func() (Order, error) {
		report.FailureCalls++
		return Order{}, transient
	}, backoff.WithBackOff(backoff.NewConstantBackOff(0)), backoff.WithMaxTries(2))
	fmt.Fprintf(out, "LIB exhausted calls=%d original-error=%t\n", report.FailureCalls, errors.Is(report.Failure, transient))

	canceled, stop := context.WithCancel(context.Background())
	stop() // 先发取消通知，下面实际库仍会先调用 operation 一次。
	_, report.Cancellation = backoff.Retry(canceled, func() (Order, error) {
		report.CancellationCalls++
		return Order{}, transient
	}, backoff.WithMaxTries(3))
	fmt.Fprintf(out, "LIB already-canceled calls=%d canceled=%t\n", report.CancellationCalls, errors.Is(report.Cancellation, context.Canceled))
	// v5.0.3 Retry 在 loop 前注册 defer Timer.Stop；错误/成功/取消返回都走它。
	// 本代码不触及库内部私有 timer，因此清理结论来自锁定版本源码，不冒称直接计数了 timer。
	return report
}
