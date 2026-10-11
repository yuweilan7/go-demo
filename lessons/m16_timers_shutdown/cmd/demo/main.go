package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	lesson "go-demo/lessons/m16_timers_shutdown"
)

// main 是课程实验总 owner，45 秒只限制整轮运行，不拿它代替各个 goroutine 的 join。
// 每个实验拥有自己的停止/清理协议；失败返回后这里报告实际 error，不能输出伪 PASS。
func main() {
	os.Exit(runMain()) // runMain 已执行所有 defer 后才退出；失败也不会跳过它的 budget cancel。
}

// runMain 保存退出码；把 os.Exit 放在 caller 外层，避免它跳过本函数已经登记的 defer。
func runMain() int {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "M16 FAILED:", err)
		return 1
	}
	return 0
}

// run 按阅读路径展开：当前 timer 契约 → 错误/修复 → 调度策略 → 真实订单 drain → 停机边界。
// 打印值来自实验报告，时间跨度只作观察；终态金额/资源状态由独立测试再核验。
func run(ctx context.Context) error {
	fmt.Println("M16 Go", runtime.Version(), "：Timer / task Result / 服务 Done 分别归属")
	r, err := lesson.TimerProbe(ctx)
	if err != nil {
		return err
	}
	fmt.Println("E01", r.String())
	stuck, err := lesson.StopOnlyProbe(ctx)
	if err != nil {
		return err
	}
	if err := lesson.CancelTickerProbe(ctx); err != nil {
		return err
	}
	fmt.Printf("E02 Stop-only worker 未退出=%t；fixture Reset 救援后已 join；ctx 修复已 join\n", stuck)
	for _, policy := range []lesson.Policy{lesson.Overlap, lesson.Serial, lesson.Skip} {
		r, err := lesson.PolicyProbe(ctx, policy)
		if err != nil {
			return err
		}
		fmt.Printf("E03 %s received=%d started=%d skipped=%d errors=%d\n", policy, r.Received, r.Started, r.Skipped, len(r.Errors))
	}
	core, err := lesson.CoreDrain(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("E04 real HTTP A/B total=%d分 ledger=%v fullRejected=%t closedRejected=%t Result等待取消=%t 提前关client拒绝=%t\n", core.Amount, core.Backend.Committed, core.FullRejected, core.ClosedRejected, core.ResultWaitCanceled, core.EarlyCloseRejected)
	for i, event := range core.Shutdown.Events {
		fmt.Printf("  %d → %s\n", i+1, event)
	}
	fmt.Printf("  HTTP资源 accepted/closed=%d/%d activeHandlers=%d serviceDone=%t\n", core.Backend.Accepted, core.Backend.Closed, core.Backend.ActiveHandlers, core.Shutdown.ClientClosed)
	for _, name := range []string{"idle", "running", "full", "timeout", "canceled-budget", "panic"} {
		r, err := lesson.ServiceProbe(ctx, name)
		if err != nil {
			return err
		}
		fmt.Printf("E05 %s rows=%d unknownAtDeadline=%v active/queued=%d/%d joined=%t/%t clientClosed=%t repeatSame=%t drainErr=%v\n", name, len(r.Shutdown.FinalRows), r.Shutdown.UnknownAtDeadline, r.Shutdown.ActiveAtDeadline, r.Shutdown.QueueAtDeadline, r.Shutdown.ProducerJoined, r.Shutdown.WorkersJoined, r.Shutdown.ClientClosed, r.RepeatSame, r.Shutdown.Err)
		if name == "panic" {
			for _, row := range r.Shutdown.FinalRows {
				fmt.Printf("  任务终态 order=%s err=%v\n", row.ID, row.Err)
			}
		}
	}
	if err := lesson.ParentCancelProbe(ctx); err != nil {
		return err
	}
	fmt.Println("E06 父ctx取消 → Ticket已终态 → pool仍未joined → owner.Close+Wait 才结束")
	abort, err := lesson.HTTPAbortProbe(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("E07 real HTTP deadline unknownAtDeadline=%v finalTicket成功合计=%d分 最终查询ledger=%v accepted/closed=%d/%d handlers=%d\n", abort.Shutdown.UnknownAtDeadline, abort.Amount, abort.Backend.Committed, abort.Backend.Accepted, abort.Backend.Closed, abort.Backend.ActiveHandlers)
	for _, row := range abort.Shutdown.FinalRows {
		fmt.Printf("  order=%s amount=%d err=%v\n", row.ID, row.Price, row.Err)
	}
	fmt.Println("M16 SELF VERIFIED：实验与资源收尾完成；主 agent 独立验收另行记录")
	return nil
}
