package lab

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// CleanupProof 保存E14的实际通道/计数快照与事件顺序，供demo打印和测试独立断言。
// Before字段读取时worker已观察取消并进入cleanup gate，gate尚未释放，owner正在join。
type CleanupProof struct {
	RequestErr          error
	WorkErr             error
	BeforeFinished      bool
	BeforeOwnerReturned bool
	BeforeOwned         int64
	AfterOwned          int64
	Events              []string
}

// closedNow只读取关闭事实，不用sleep或全进程goroutine数猜调度。调用前必须有屏障
// 保证目标已进入受控阶段；它表示当前快照，后续的先后关系由gate与finished保证。
func closedNow(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

// cleanupBarrierExperiment扩展原有真实HTTP课程，直接演示result与finished的差别。
// owner只有在cleanup-finished之后才能返回，正常demo通过后还检查fixture最终资源清零。
func cleanupBarrierExperiment(ctx context.Context) (Observation, error) {
	return withFixture("E14 已取消请求的独立收尾屏障", 0, time.Second, 3*time.Second, 2, func(f *Fixture, c *Client) ([]string, error) {
		proof, err := cleanupBarrierProof(ctx, f, c)
		if err != nil {
			return nil, err
		}
		return []string{
			fmt.Sprintf("真实HTTP已进入handler -> work ctx=%v -> request result=%v（结果已消费）", proof.WorkErr, proof.RequestErr),
			fmt.Sprintf("cleanup gate未释放：request_finished=%t owner_returned=%t owned_requests=%d；owner在独立预算join等待", proof.BeforeFinished, proof.BeforeOwnerReturned, proof.BeforeOwned),
			fmt.Sprintf("release cleanup -> events=%v -> owned_requests=%d；再次等待finished可立即完成，不二次接收result", proof.Events, proof.AfterOwned),
		}, nil
	})
}

// cleanupBarrierProof拥有真实请求和额外owner goroutine。故障清理在独立gate上等待，
// 不响应已取消的工作ctx；安全timer给故障上限。每一条错误返回先解开两个gate，再用
// 独立预算join请求与owner，清理失败用errors.Join保留，不以ctx.Done当完成事实。
func cleanupBarrierProof(ctx context.Context, f *Fixture, c *Client) (proof CleanupProof, err error) {
	handlerGate := f.Gate("cleanup-proof")
	cleanupGate := newGate()
	workCtx, cancelWork := context.WithCancel(ctx)
	events := make(chan string, 2)
	ownerFinished := make(chan struct{})
	ownerStarted := false
	var ownerErr error
	request := startRequest(workCtx, c, "GET", f.URL+"/headers?gate=cleanup-proof", "", func() error {
		// RequestJSON真实返回后才到这里：它已观察网络取消并关闭所拥有body。
		// result虽然可读，本段清理仍未结束，所以finished必须保持未关闭。
		close(cleanupGate.Entered)
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-cleanupGate.Release:
			events <- "cleanup-finished"
			close(cleanupGate.Done)
			return nil
		case <-timer.C:
			return errors.New("E14 cleanup gate安全上限到期")
		}
	})
	defer func() {
		// 先解除清理等待再join，不能把解除gate放在join之后的另一个defer里。
		cancelWork()
		handlerGate.Open()
		cleanupGate.Open()
		err = errors.Join(err, request.StopAndJoin(nil))
		if ownerStarted {
			cleanupCtx, stopCleanup := context.WithTimeout(context.Background(), 3*time.Second)
			defer stopCleanup()
			joinErr := waitSignal(cleanupCtx, ownerFinished, "E14 owner finished")
			err = errors.Join(err, joinErr)
			if joinErr == nil {
				err = errors.Join(err, ownerErr)
			}
		}
	}()
	if err = waitSignal(ctx, handlerGate.Entered, "真实HTTP handler entered"); err != nil {
		return proof, err
	}
	cancelWork()
	proof.WorkErr = workCtx.Err()
	// 观察结果使用仍有效的实验ctx；已经取消的workCtx只负责工作，不负责清理等待。
	requestErr, waitErr := request.Result(ctx)
	if waitErr != nil {
		return proof, waitErr
	}
	proof.RequestErr = requestErr
	if err = waitSignal(ctx, cleanupGate.Entered, "owned request cleanup entered"); err != nil {
		return proof, err
	}
	if err = waitSignal(ctx, handlerGate.Canceled, "server observed cancellation"); err != nil {
		return proof, err
	}
	if err = require(errors.Is(requestErr, context.Canceled) && errors.Is(proof.WorkErr, context.Canceled), "必须由真实请求取消进入清理"); err != nil {
		return proof, err
	}
	ownerStarted = true
	go func() {
		// 这才是待验证的owner返回路径：它不打开cleanupGate，所以必须等测试控制者。
		ownerErr = request.StopAndJoin(handlerGate.Open)
		events <- "owner-return"
		close(ownerFinished)
	}()
	if err = waitSignal(ctx, request.joinWaiting, "owner已确认finished未关闭并开始join"); err != nil {
		return proof, err
	}
	proof.BeforeFinished = closedNow(request.Finished())
	proof.BeforeOwnerReturned = closedNow(ownerFinished)
	proof.BeforeOwned = c.OwnedRequests.Load()
	if err = require(!proof.BeforeFinished && !proof.BeforeOwnerReturned && proof.BeforeOwned == 1, "cleanup gate未释放时owner不得提前返回"); err != nil {
		return proof, err
	}
	cleanupGate.Open()
	if err = waitSignal(ctx, ownerFinished, "cleanup释放后的owner完成"); err != nil {
		return proof, err
	}
	if ownerErr != nil {
		return proof, ownerErr
	}
	for i := 0; i < 2; i++ {
		select {
		case event := <-events:
			proof.Events = append(proof.Events, event)
		case <-ctx.Done():
			return proof, fmt.Errorf("缺少cleanup/owner事件: %w", ctx.Err())
		}
	}
	proof.AfterOwned = c.OwnedRequests.Load()
	if err = require(proof.Events[0] == "cleanup-finished" && proof.Events[1] == "owner-return" && closedNow(request.Finished()) && proof.AfterOwned == 0, "收尾/owner返回顺序或资源计数错误"); err != nil {
		return proof, err
	}
	// result已消费，第二次join等待关闭广播，不会因结果通道耗尽而卡住。
	if err = request.StopAndJoin(nil); err != nil {
		return proof, err
	}
	return proof, nil
}
