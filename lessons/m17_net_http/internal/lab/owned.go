package lab

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// OwnedRequest 是 doAsync 调用方拥有的一次请求：Cancel 只通知，Result 只交付请求结果，
// Finished 才证明 RequestJSON、附加清理、取消函数和所有权计数都已走完。
// result 容量为1，只读取一次；finished 是关闭广播，正常消费过 result 后仍可反复等待。
// cleanupErr 在关闭 finished 之前写入，join 读取关闭信号后才读它，利用 channel 同步关系。
type OwnedRequest struct {
	cancel      context.CancelFunc
	result      chan error
	finished    chan struct{}
	joinWaiting chan struct{}
	waitOnce    sync.Once
	cleanupErr  error
}

// doAsync 由需要同时控制服务端 gate 的实验/测试调用。它创建独立 child ctx，把停止方式
// 和完成信号交给调用方；调用方必须立即 defer StopAndJoin，覆盖所有提前返回与 t.Fatal。
// 业务结果不是任务完成信号，不能在工作 ctx 到期后把 Result 的等待错误当成已退出。
func doAsync(ctx context.Context, c *Client, method, url, body string) *OwnedRequest {
	return startRequest(ctx, c, method, url, body, nil)
}

// startRequest 的 afterResult 只用于E14故障屏障：真实HTTP调用已返回后，让清理步骤
// 在独立gate等待，明确制造“result可读但finished未关闭”。正常调用传nil，不增加等待。
func startRequest(ctx context.Context, c *Client, method, url, body string, afterResult func() error) *OwnedRequest {
	requestCtx, cancel := context.WithCancel(ctx)
	r := &OwnedRequest{cancel: cancel, result: make(chan error, 1), finished: make(chan struct{}), joinWaiting: make(chan struct{})}
	c.OwnedRequests.Add(1)
	go func() {
		// defer按后注册先执行：先减 owned 计数，再cancel释放child关联，最后关闭finished。
		// 收到finished以后，owner才可声明这些清理都完成；不把send(result)当最后动作。
		defer close(r.finished)
		defer cancel()
		defer c.OwnedRequests.Add(-1)
		_, requestErr := RequestJSON(requestCtx, c, method, url, strings.NewReader(body), "m17-async", 2048)
		r.result <- requestErr
		if afterResult != nil {
			r.cleanupErr = afterResult()
		}
	}()
	return r
}

// Cancel 可重复调用，只关闭child ctx的Done；此时HTTP调用、body关闭和afterResult
// 可能仍在进行。caller必须继续StopAndJoin，不能直接返回。
func (r *OwnedRequest) Cancel() { r.cancel() }

// Result 同步等一次业务/网络结果。第一个error来自真实RequestJSON；第二个error表示
// owner仅停止了结果等待。等待ctx取消时，不消费result，也不证明worker已完成。
func (r *OwnedRequest) Result(ctx context.Context) (requestErr, waitErr error) {
	select {
	case requestErr = <-r.result:
		return requestErr, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("仅结束请求结果等待，尚未join: %w", ctx.Err())
	}
}

func (r *OwnedRequest) Finished() <-chan struct{} { return r.finished }

// waitFinished 只从finished确认清理事实；不再接收可能已经耗尽的result。
// JoinWaiting在确认finished尚未关闭时发出，供E14观察owner已经进入收尾等待；
// 关闭它不表示完成。ctx过期返回明确join失败，绝不返回nil伪装已退出。
func (r *OwnedRequest) waitFinished(ctx context.Context) error {
	select {
	case <-r.finished:
		return r.cleanupErr
	default:
		r.waitOnce.Do(func() { close(r.joinWaiting) })
	}
	select {
	case <-r.finished:
		return r.cleanupErr
	case <-ctx.Done():
		return fmt.Errorf("owned request未完成收尾: %w", ctx.Err())
	}
}

// StopAndJoin 是每个request owner的退出步骤：先通知取消，再解除它可能需要的gate，
// 然后创建独立3秒清理ctx等待finished。它不沿用已取消的工作ctx，不二次消费result。
// cleanup失败交给caller用errors.Join与主错误保留；已正常完成时重复等待仍立即结束。
func (r *OwnedRequest) StopAndJoin(unblock func()) error {
	r.Cancel()
	if unblock != nil {
		unblock()
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelCleanup()
	return r.waitFinished(cleanupCtx)
}

// combineOwnedCleanup 供具名返回的实验defer使用，保证工作错误和收尾错误都可errors.Is。
// 它不把预期的请求Canceled加入失败，因为业务result已经由caller按实验契约分类。
func combineOwnedCleanup(err *error, r *OwnedRequest, unblock func()) {
	*err = errors.Join(*err, r.StopAndJoin(unblock))
}
