// demo 是 M10 的正常入口；所有实验有界，坏提交只用于对照，错误测试另由 build tag 显式启用。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"runtime/pprof"
	"strings"
	"time"

	"go-demo/lessons/m10_testing_debugging/internal/lifecycle"
	"go-demo/lessons/m10_testing_debugging/internal/order"
	"go-demo/lessons/m10_testing_debugging/internal/protocol"
)

// main 由 Go runtime 调用，解析本进程的实验选择并把错误带到进程退出码。
// run 返回前已执行自己的 defer；这里的 os.Exit 不会执行 main 尚未执行的 defer，因此 main 不持有资源。
func main() {
	experiment := flag.String("experiment", "all", "all、logic 或 stack")
	flag.Parse()
	if err := run(*experiment); err != nil {
		fmt.Fprintln(os.Stderr, "实验失败:", err)
		os.Exit(1)
	}
}

// run 由 main 调用，按固定阅读顺序串行执行，各实验返回错误后停止，不用日志掩盖失败。
func run(name string) error {
	switch name {
	case "logic":
		return runLogic()
	case "stack":
		return runStack()
	case "all":
	default:
		return fmt.Errorf("未知 experiment=%q", name)
	}
	fmt.Println("E01 金额规则: 12.99 元必须是 1299 分，1e2.00 必须被拒绝")
	cents, err := order.ParseCents("12.99")
	if err != nil || cents != 1299 {
		return fmt.Errorf("金额业务事实未成立: cents=%d err=%v", cents, err)
	}
	_, err = order.ParseCents("1e2.00")
	if !errors.Is(err, order.ErrInvalidAmount) {
		return fmt.Errorf("指数金额未拒绝: %v", err)
	}
	fmt.Printf("accepted cents=%d; rejected exponent err=%v\n", cents, err)
	for _, experiment := range []func() error{runLogic, runWorker, runHTTP, runStack} {
		if err := experiment(); err != nil {
			return err
		}
	}
	return nil
}

// runLogic 由 demo 调用，两次创建相同合成订单，把错误提交和修复提交放在独立 Store 上比较。
// 本函数中的断言是“观察到坏版确实破坏业务”，而故障测试直接表达业务契约，因而会 FAIL。
func runLogic() error {
	fmt.Println("E02 错误提交与修复: 订单应收 1299 分，本次支付 1300 分")
	wrong, err := order.NewStore("order-42", 1299)
	if err != nil {
		return err
	}
	_, wrongErr := wrong.ApplyPaymentWrong(order.Payment{ID: "order-42", Cents: 1300})
	wrongValue, err := wrong.View("order-42")
	if err != nil {
		return err
	}
	fmt.Printf("wrong err=%v; state=%s credited=%d count=%d\n", wrongErr, wrongValue.State, wrongValue.CreditedCents, wrongValue.CreditCount)
	if !errors.Is(wrongErr, order.ErrAmountMismatch) || wrongValue.State != "paid" {
		return fmt.Errorf("坏版未复现预计的错误状态")
	}
	fixed, err := order.NewStore("order-42", 1299)
	if err != nil {
		return err
	}
	_, fixedErr := fixed.ApplyPayment(order.Payment{ID: "order-42", Cents: 1300})
	fixedValue, err := fixed.View("order-42")
	if err != nil {
		return err
	}
	fmt.Printf("fixed err=%v; state=%s credited=%d count=%d\n", fixedErr, fixedValue.State, fixedValue.CreditedCents, fixedValue.CreditCount)
	if !errors.Is(fixedErr, order.ErrAmountMismatch) || fixedValue.State != "pending" || fixedValue.CreditedCents != 0 || fixedValue.CreditCount != 0 {
		return fmt.Errorf("修复版拒绝后仍改变业务: %+v", fixedValue)
	}
	return nil
}

// runWorker 由 demo 调用，入口拥有 ctx、worker 和清理屏障，返回前必须加入 Done。
func runWorker() (resultErr error) {
	fmt.Println("E06 取消通知、观察通知、清理完成、owner 等待退出")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	w := lifecycle.Start(ctx)
	defer func() {
		w.Stop()
		w.ReleaseCleanup()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cleanupCancel()
		// 清理失败也属于调用方需要处理的结果；不能只打印后丢掉原始原因。
		// 具名结果槽让 defer 在 runWorker 返回 main 之前补入错误链。
		resultErr = errors.Join(resultErr, lifecycle.Await(cleanupCtx, w.Done, "deferred worker cleanup"))
	}()
	if err := lifecycle.Await(ctx, w.Started, "started"); err != nil {
		return err
	}
	w.Stop()
	if err := lifecycle.Await(ctx, w.Observed, "cancellation observed"); err != nil {
		return err
	}
	select {
	case <-w.Done:
		return fmt.Errorf("清理未放行 worker 却已经退出")
	default:
		fmt.Printf("stop returned; observed closed; done open; cleaned=%t\n", w.Cleaned())
	}
	w.ReleaseCleanup()
	if err := lifecycle.Await(ctx, w.Done, "done"); err != nil {
		return err
	}
	if !w.Cleaned() {
		return fmt.Errorf("退出后清理事实缺失")
	}
	fmt.Println("release closed; done closed; owner joined; cleaned=true")
	return nil
}

// runHTTP 由 demo 调用，拥有真实动态端口 server 和 client；所有响应 body 在本次读取后关闭。
func runHTTP() error {
	fmt.Println("E07 真实 loopback HTTP: 拒绝、支付、重放，并核对实际内存副作用")
	store, err := order.NewStore("order-42", 1299)
	if err != nil {
		return err
	}
	server := httptest.NewServer(protocol.NewHandler(store))
	defer server.Close()
	client := server.Client()
	client.Timeout = 2 * time.Second
	defer client.CloseIdleConnections()
	fmt.Printf("actual dynamic endpoint=%s\n", server.URL)
	for i, amount := range []string{"13.00", "12.99", "12.99"} {
		body := fmt.Sprintf(`{"id":"order-42","amount":%q}`, amount)
		response, err := client.Post(server.URL+"/pay", "application/json", strings.NewReader(body))
		if err != nil {
			return fmt.Errorf("HTTP attempt %d: %w", i+1, err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
		closeErr := response.Body.Close() // 不把 defer 放在 loop 中堆积至函数退出，每个响应在本轮释放。
		if err := errors.Join(readErr, closeErr); err != nil {
			return fmt.Errorf("read/close HTTP response: %w", err)
		}
		var decoded protocol.Response
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return err
		}
		value, err := store.View("order-42")
		if err != nil {
			return err
		}
		fmt.Printf("attempt=%d HTTP=%d code=%s actual state=%s credited=%d count=%d\n", i+1, response.StatusCode, decoded.Code, value.State, value.CreditedCents, value.CreditCount)
		if i == 0 {
			if response.StatusCode != 409 || decoded.Code != "AMOUNT_MISMATCH" || value.State != "pending" || value.CreditedCents != 0 || value.CreditCount != 0 {
				return fmt.Errorf("HTTP 拒绝路径改变了业务: %+v", value)
			}
		} else if response.StatusCode != 200 || decoded.Code != "PAID" || decoded.State != "paid" || decoded.Count != 1 || value.CreditedCents != 1299 || value.CreditCount != 1 {
			return fmt.Errorf("HTTP 支付/重放未恰好入账一次: %+v", value)
		}
	}
	return nil
}

// waitForApproval 是 E08 的明确栈定位符号。owner 创建并最终关闭 gate；当前 goroutine 不拥有关闭它的权限。
func waitForApproval(gate <-chan struct{}, entered chan<- struct{}, done chan<- struct{}) {
	defer close(done)
	close(entered) // 表示已经进入此函数；不假称它证明下一条 receive 已经执行。
	<-gate
}

// runStack 由 demo 调用，主动制造一个有 owner 的等待，再采样真实 goroutine 栈并放行清理。
// 栈是采样时的运行时诊断；若恰好采到 runnable，不能伪称已看到阻塞。本课实际记录采到的状态。
func runStack() error {
	fmt.Println("E08 标准 goroutine 栈定位: waitForApproval 等待 owner 的 gate")
	gate, entered, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go waitForApproval(gate, entered, done)
	defer func() { close(gate); <-done }() // owner 放行并加入有限 worker；不能用关闭进程替代清理。
	if err := lifecycle.Await(ctx, entered, "stack worker entered"); err != nil {
		return err
	}
	var dump bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&dump, 2); err != nil {
		return err
	}
	if !strings.Contains(dump.String(), "main.waitForApproval") {
		return fmt.Errorf("真实栈中缺少待定位符号")
	}
	fmt.Print(dump.String())
	fmt.Println("stack captured; owner will close gate and wait for done before returning")
	return nil
}
