package lesson

import (
	"context"
	"errors"
)

// runSend 由 Run 调用，展示 worker 观察取消后仍可能在结果发送处卡住。
// 两组都没有业务接收者；危险组靠 owner 后来 drain 恢复，修复组靠请求 Done 返回。
func runSend(scope context.Context, t *trace) error {
	if err := sendCase(scope, t, false); err != nil {
		return err
	}
	return sendCase(scope, t, true)
}

// sendCase 是发送任务 owner：创建 out，保留 cancel、observed 和 done。
// out 不关闭：任务发送期间关闭它会 panic，Context 不能保护向已关闭 channel 发送。
func sendCase(scope context.Context, t *trace, fixed bool) (resultErr error) {
	ctx, cancel := context.WithCancel(scope)
	out := make(chan int)
	observed, done := make(chan struct{}), make(chan struct{})
	go sendAfterCancellation(scope, ctx, out, observed, done, fixed, t)
	defer func() {
		cancel()
		if !fixed {
			// 若前面的断言失败，owner 仍有恢复动作。scope 是故障上限，done 可防止重取。
			select {
			case <-out:
			case <-done:
			case <-scope.Done():
			}
		}
		resultErr = errors.Join(resultErr, join(done, "结果发送 worker"))
	}()
	cancel()
	if err := wait(scope, observed, "发送前已观察取消"); err != nil {
		return err
	}
	if !fixed {
		if err := check(!closed(done), "已经观察 ctx 取消，但无接收者，危险发送任务应仍未退出"); err != nil {
			return err
		}
		t.event("send-owner", "危险版已观察取消，却卡在 out 发送；取消通知不是万能解阻塞")
		select {
		case value := <-out:
			t.event("send-owner", "恢复 owner drain value=%d，解除危险发送", value)
		case <-scope.Done():
			return scope.Err()
		}
	}
	if err := wait(scope, done, "发送 worker 最终退出"); err != nil {
		return err
	}
	t.event("send-owner", "fixed=%t，defer 已完成，done 已收到", fixed)
	return nil
}

// sendAfterCancellation 由 sendCase 创建。它先观察取消，再故意展示另一个阻塞点。
// out 是只发送参数 chan<- int；observed/done 也由这个 worker 独占关闭。
// fixed=false 的 scope 分支只是教学 watchdog，生产危险代码就是 out <- 42。
func sendAfterCancellation(scope, ctx context.Context, out chan<- int, observed, done chan<- struct{}, fixed bool, t *trace) {
	defer close(done)
	defer t.event("send-worker", "fixed=%t defer 清理完成", fixed)
	<-ctx.Done()
	close(observed)
	if !fixed {
		// 错误点：这个等待忽略 request ctx。即使上一行看见取消，也不能强制发送完成。
		select {
		case out <- 42:
			t.event("send-worker", "危险版：请求取消后，恢复接收者仍收到结果")
		case <-scope.Done():
			t.event("send-worker", "实验 watchdog 兜底退出；不是业务取消修复")
		}
		return
	}
	select {
	case out <- 42:
		// 若发送/取消同时就绪，可能发送成功；不能承诺 cancel 后绝无发送。
		// 本实验 out 无接收者，所以该分支不可选，Done 是唯一可就绪分支。
		t.event("send-worker", "结果被 channel 接受；不等于消费者已完成业务")
	case <-ctx.Done():
		t.event("send-worker", "修复版：发送等待也观察 request Done，返回 %v", ctx.Err())
	}
}
