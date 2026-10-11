package course

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// SampleOrders 是全部 pipeline 对照实验共用的六个值。O-03 的 0 是实际订单数据，
// 不能用这个零值判断 channel 结束；它应该产生一条业务拒绝 Result。
func SampleOrders() []Order {
	return []Order{{"O-01", 100}, {"O-02", 200}, {"O-03", 0}, {"O-04", 400}, {"O-05", 500}, {"O-06", 600}}
}

// Saturation 由 demo/测试调用，故意暂不读 Results。只有一名 worker、两个容量为 1 的队列，
// 观察到 results 发送不能完成且 feeder 已成功发送第三单后，两个业务队列稳定为满，不用 Sleep 猜测。
// 然后同一个 owner 开始 Collect，使真实消费释放背压，并核验六单全部交付。
func Saturation(ctx context.Context, trace *Trace) (report Report) {
	blocked := make(chan string, 8) // 观察通知独立、有限；容量覆盖此实验到饱和屏障前的少量通知。
	produced := make(chan int, 8)   // 第三单已真实交给 jobs，才能证明它现在占据缓冲。
	pipeline, err := Start(ctx, SampleOrders(), Options{Workers: 1, JobsBuffer: 1, ResultsBuffer: 1, FailAt: -1, Blocked: blocked, Produced: produced}, trace)
	if err != nil {
		return Report{Err: err}
	}
	handedToCollector := false
	defer func() {
		if handedToCollector {
			return // Collect 已经 Cancel+Wait，不能再次读取单次 Completion。
		}
		pipeline.Cancel()
		cleanup, cancel := context.WithTimeout(context.Background(), CleanupLimit)
		defer cancel()
		completion, waitErr := pipeline.Wait(cleanup)
		report.Completion = completion
		report.Err = errors.Join(report.Err, waitErr)
	}()
	seenJobs, seenResults, accepted := false, false, 0
	for !seenJobs || !seenResults || accepted < 3 {
		select {
		case count := <-produced:
			accepted = count
		case stage := <-blocked:
			seenJobs = seenJobs || stage == "jobs"
			seenResults = seenResults || stage == "results"
		case <-ctx.Done():
			report.Err = fmt.Errorf("等待两个队列饱和: %w", ctx.Err())
			return report
		}
	}
	jobs, results := pipeline.Queues()
	if jobs != 1 || results != 1 {
		report.Err = fmt.Errorf("冻结消费者时应稳定为 jobs=1 results=1，实际 %d/%d", jobs, results)
		return report
	}
	trace.Log("owner", "queues_saturated", "jobs=%d/1 results=%d/1 produced=%d；worker 的 results 发送和 feeder 后续 jobs 发送均无法完成", jobs, results, accepted)
	handedToCollector = true
	return Collect(ctx, pipeline, 0, trace)
}

// BlockedReport 是错误实验的独立事实：发送未完成、后来实际接收了第二条、最后 producer 已退出。
type BlockedReport struct {
	BlockedBeforeRescue bool
	RescuedOrder        string
	ProducerExited      bool
}

// BlockedConsumer 由 demo/测试运行错误版本。消费者取第一单就停止，生产者的第二次裸发送
// 没有取消分支；owner 先证明不能完成，再以真实接收救援。defer 也保证任何断言失败时有界救援。
func BlockedConsumer(ctx context.Context, trace *Trace) (report BlockedReport, err error) {
	results := make(chan Order)
	secondAttempt, producerDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(producerDone)
		defer close(results) // 本实验只有此 producer，仍由它关闭发送侧。
		orders := SampleOrders()
		results <- orders[0]
		close(secondAttempt) // 仅说明到达第二次发送前；没有接收者，第二次发送不可能完成。
		results <- orders[1] // 危险点：消费者退出以后，这个裸 send 没有停止通道可等。
		trace.Log("bad-producer", "second_send_returned", "救援接收后才继续，扩大 buffer 只会推迟相同问题")
	}()
	defer func() {
		// 这是教学夹具的救援，不是危险版本的解决方案。真实修复见 pipeline.send 的取消 case。
		cleanup, cancel := context.WithTimeout(context.Background(), CleanupLimit)
		defer cancel()
		for {
			select {
			case order, ok := <-results:
				if !ok {
					joinErr := await(cleanup, producerDone, "错误 producer 最后退出")
					err = errors.Join(err, joinErr)
					report.ProducerExited = joinErr == nil
					trace.Log("owner", "bad_example_cleaned", "producerExited=%t", report.ProducerExited)
					return
				}
				report.RescuedOrder = order.ID
				trace.Log("rescuer", "rescue_receive", "补上接收 ID=%s，使裸 send 有机会结束", order.ID)
			case <-cleanup.Done():
				err = errors.Join(err, fmt.Errorf("错误实验救援超时: %w", cleanup.Err()))
				return
			}
		}
	}()
	select {
	case order := <-results:
		trace.Log("bad-consumer", "consumer_left", "只接收 ID=%s，业务上已离开", order.ID)
	case <-ctx.Done():
		return report, ctx.Err()
	}
	if err := await(ctx, secondAttempt, "错误发送前屏障"); err != nil {
		return report, err
	}
	select {
	case <-producerDone:
		return report, errors.New("没有第二次接收时 producer 不应结束")
	default:
		report.BlockedBeforeRescue = true
		trace.Log("owner", "send_cannot_finish", "消费者已退出，第二次 send 不能完成；随后由夹具救援")
	}
	return report, nil
}

// ReceiverCloses 复现接收者随意关闭仍有发送者的 channel。panic 在 sender goroutine 内
// recover，主 goroutine 的 recover 捕捉不到它。gate 保证先 close(out)，后执行第二次 send。
// recover 仅用于记录错误；正确修复是唯一关闭者协议，不能用吞 panic 代替生命周期管理。
func ReceiverCloses(ctx context.Context, trace *Trace) (message string, err error) {
	workerCtx, cancel := context.WithCancel(ctx)
	out := make(chan Order)
	gate := make(chan struct{})
	done := make(chan string, 1)
	consumedDone := false
	go func() {
		outcome := "sender returned without panic"
		defer func() {
			if recovered := recover(); recovered != nil {
				outcome = fmt.Sprint(recovered)
				trace.Log("bad-sender", "panic_recovered", "%s；recover 在发生 panic 的同一 goroutine", outcome)
			}
			done <- outcome
		}()
		select {
		case out <- SampleOrders()[0]:
		case <-workerCtx.Done():
			return
		}
		select {
		case <-gate:
			out <- SampleOrders()[1] // 主 goroutine 已经关闭 out，必定 send on closed channel。
		case <-workerCtx.Done():
			return
		}
	}()
	defer func() {
		cancel()
		if !consumedDone {
			cleanup, release := context.WithTimeout(context.Background(), CleanupLimit)
			defer release()
			select {
			case <-done:
			case <-cleanup.Done():
				err = errors.Join(err, cleanup.Err())
			}
		}
	}()
	select {
	case first := <-out:
		trace.Log("bad-consumer", "receiver_close", "收到 %s 后擅自 close(out)，发送者仍未完成", first.ID)
		close(out)
		close(gate)
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case message = <-done:
		consumedDone = true
		if !strings.Contains(message, "send on closed channel") {
			return message, fmt.Errorf("没有观测到发送已关闭 panic: %s", message)
		}
		return message, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
