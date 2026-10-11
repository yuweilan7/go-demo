package lesson

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// TestScenarios 由 go test 执行，复跑所有有界课程路径，检查必要状态断言确实到达。
// 这是端到端课程验收；下面两个测试另从真实 HTTP 外部状态验证未知结果/并发幂等。
func TestScenarios(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := Run(ctx, &output, "all"); err != nil {
		t.Fatalf("课程实验失败: %v\n%s", err, output.String())
	}
	for _, name := range []string{"tree", "http", "budgets", "traps", "send", "lifecycle"} {
		if !strings.Contains(output.String(), "PASS "+name) {
			t.Fatalf("未到达 %s 的最终收尾断言", name)
		}
	}
}

// TestTimeoutThenSideEffect 独立控制真实 handler 的 gate，不调用 runHTTP 的用例函数。
// 它验证客户端 error 与服务端写入不是同一个事实，不依赖精确耗时/偶然日志排序。
func TestTimeoutThenSideEffect(t *testing.T) {
	scope, cancelScope := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancelScope()
	lab := newHTTPLab(scope, &trace{start: time.Now(), out: io.Discard})
	t.Cleanup(func() {
		if err := lab.close(); err != nil {
			t.Errorf("HTTP 收尾失败: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(scope, 250*time.Millisecond)
	defer cancel()
	op := lab.register("independent-late", false, false)
	task := lab.startCharge(ctx, op, chargeCommand{Order: "independent-order"})
	t.Cleanup(func() {
		cancel()
		if err := join(task.done, "独立超时 client"); err != nil {
			t.Errorf("client 未完成: %v", err)
		}
	})
	if err := wait(scope, op.started, "真实 handler started"); err != nil {
		t.Fatal(err)
	}
	result, err := awaitClient(scope, task)
	if err != nil || !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatalf("应是客户端 deadline: result=%+v collect=%v", result, err)
	}
	if closed(op.exited) || lab.state.count("independent-order") != 0 {
		t.Fatal("gate 未放行之前，handler 仍活跃，副作用应为零")
	}
	op.release()
	if err := wait(scope, op.exited, "真实 handler 最终退出"); err != nil {
		t.Fatal(err)
	}
	if actual := lab.state.count("independent-order"); actual != 1 {
		t.Fatalf("timeout 后仍应产生一次真实效果，实际 %d", actual)
	}
}

// TestConcurrentHTTPIdempotency 同时给六个真实 HTTP handler 放行同一订单/幂等键。
// 独立断言最终效果只有一次、所有回执都读完、五次回执 Duplicate=true。
// 这覆盖串行 retry demo 没有覆盖的并发检查/写入原子边界；仍只证明本进程内账本。
func TestConcurrentHTTPIdempotency(t *testing.T) {
	scope, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	lab := newHTTPLab(scope, &trace{start: time.Now(), out: io.Discard})
	t.Cleanup(func() {
		if err := lab.close(); err != nil {
			t.Errorf("HTTP 收尾失败: %v", err)
		}
	})
	ops := make([]*operation, 0, 6)
	tasks := make([]*clientTask, 0, 6)
	for index := 0; index < 6; index++ {
		op := lab.register("parallel-"+string(rune('A'+index)), true, false)
		ops = append(ops, op)
		tasks = append(tasks, lab.startCharge(scope, op, chargeCommand{Order: "parallel-order", IdempotencyKey: "same-key"}))
	}
	t.Cleanup(func() {
		cancel()
		for _, task := range tasks {
			if err := join(task.done, "并发 client"); err != nil {
				t.Errorf("client 未完成: %v", err)
			}
		}
	})
	for _, op := range ops {
		if err := wait(scope, op.started, "六个 handler 都 ready"); err != nil {
			t.Fatal(err)
		}
	}
	for _, op := range ops {
		op.release()
	}
	duplicates := 0
	for _, task := range tasks {
		result, err := awaitClient(scope, task)
		if err != nil || result.err != nil || result.receipt.Total != 1 {
			t.Fatalf("并发协议/回执失败: %+v collect=%v", result, err)
		}
		if result.receipt.Duplicate {
			duplicates++
		}
	}
	if actual := lab.state.count("parallel-order"); actual != 1 || duplicates != 5 {
		t.Fatalf("唯一效果与防重回执应为 1/5，实际 %d/%d", actual, duplicates)
	}
}

// TestCallerAlreadyCanceled 验证 Run 不会在 caller 早已取消后继续创建 HTTP 服务。
// 断言错误链保留 Canceled，而不是把它转成“成功退出”。
func TestCallerAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, io.Discard, "http"); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消 caller 应直接返回 Canceled，实际 %v", err)
	}
}
