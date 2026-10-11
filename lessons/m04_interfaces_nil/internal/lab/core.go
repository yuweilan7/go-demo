package lab

import (
	"context"
	"errors"
	"fmt"
	"io"

	"go-demo/lessons/m04_interfaces_nil/internal/adapter"
	"go-demo/lessons/m04_interfaces_nil/internal/app"
	"go-demo/lessons/m04_interfaces_nil/internal/domain"
)

// RunCore 由 demo 的 core/all 场景调用，执行真正的“业务小接口 → 文件仓储”读取路径。
// repo 来自命令行文件配置，logger 属于本次实验；ctx 是有限同步请求，没有后台任务。
func RunCore(out io.Writer, repo *adapter.Repository) error {
	fmt.Fprintln(out, "[E01] 订单读路径：使用者接口 + 显式构造注入 + 真实文件")
	logger := adapter.NewLogger()
	service, err := app.Compose(repo, logger)
	if err != nil {
		return err
	}
	order, err := service.Read(context.Background(), "ORDER-1001")
	if err != nil {
		return err // 文件错误不能被显示成功的课程日志掩盖，向 demo 返回非零退出结果。
	}
	fmt.Fprintf(out, "success: id=%s status=%s amount_cents=%d\n", order.ID, order.Status, order.AmountCents)
	_, err = service.Read(context.Background(), "ORDER-404")
	var problem *domain.DomainError
	// errors.As 查找 %w 保留的具体业务错误。&problem 是结果变量的地址，供它写入找到的指针。
	if !errors.As(err, &problem) || problem == nil || problem.Code != "ORDER_NOT_FOUND" {
		return fmt.Errorf("unexpected missing-order result: %v", err)
	}
	fmt.Fprintf(out, "missing: code=%s wrapped_error=%v\n", problem.Code, err)

	ctx, cancel := context.WithCancel(context.Background())
	// 入口拥有取消函数；立即注册退出清理。cancel 可重复调用，不需要等待不存在的 goroutine。
	defer cancel()
	cancel() // 让仓储在 ReadFile 前的 ctx.Err 观察到取消；取消不是 typed nil，也不是 panic。
	_, err = service.Read(ctx, "ORDER-1001")
	if !errors.Is(err, context.Canceled) {
		return fmt.Errorf("expected context cancellation, got: %v", err)
	}
	fmt.Fprintf(out, "canceled: errors.Is(context.Canceled)=%t\n", errors.Is(err, context.Canceled))
	for index, event := range logger.Entries() {
		fmt.Fprintf(out, "business event[%d]=%s\n", index, event)
	}
	return nil
}
