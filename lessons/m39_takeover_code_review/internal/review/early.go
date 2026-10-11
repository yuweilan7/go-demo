package review

import (
	"context"
	"errors"
	"fmt"
	"io"

	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// EarlyOwners由默认入口与独立Live测试调用，故意在真实未提交Tx门处让实验owner
// 提前error/panic/取消。最终事实检查在所有Done以后、清表以前执行，避免DROP掩盖误提交。
// 原工作cause和收尾cause都必须保留；每轮在withSession返回以后检查errors.Is身份。
func EarlyOwners(ctx context.Context, out io.Writer) error {
	for _, mode := range []string{"error", "panic", "cancel"} {
		cause := errors.New("owner提前" + mode)
		if mode == "cancel" {
			cause = context.Canceled
		}
		result := withSession(ctx, out, func(s *session) error {
			g := s.makeGate()
			if e := s.newRoot(b.Hooks{AfterOrderWrites: func(actual context.Context, o b.Order) error { return g.wait(actual) }}); e != nil {
				return e
			}
			job := s.launch("提前退出时在途SQLcaller", func() error {
				_, e := s.root.service.Create(s.ctx, command("alice", "early"))
				return e
			})
			// 创建caller后立即装收尾断言；观察entered失败也必须保留尚未消费任务错误。
			s.beforeCleanup = func(fresh context.Context) error {
				if e := counts(fresh, s, 10, 0, 0); e != nil {
					return e
				}
				if e := b.Assert(errors.Is(job.err, context.Canceled), "真实Tx caller最终保留Canceled mode=%s cause=%v", mode, job.err); e != nil {
					return e
				}
				job.consumed = true
				fmt.Fprintf(s.out, "OWNER_EARLY mode=%s caller_done=true tx_rollback=true stock=10 orders=0\n", mode)
				return nil
			}
			if e := await(ctx, g.entered, "提前退出的真实Tx门"); e != nil {
				return e
			}
			if mode == "panic" {
				panic(cause)
			}
			if mode == "cancel" {
				s.cancel()
			}
			return cause
		})
		// job的预期取消直到beforeCleanup独立事实通过才消费，finish先前已经把未消费
		// job.err放进Join树，所以这里允许原cause与Canceled并存，不能改写成成功nil。
		if e := b.Assert(errors.Is(result, cause) && !errors.Is(result, b.ErrCleanup), "owner原cause保留且清理无失败 mode=%s result=%v", mode, result); e != nil {
			return e
		}
	}
	return nil
}
