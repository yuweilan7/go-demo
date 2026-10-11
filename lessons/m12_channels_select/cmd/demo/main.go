// demo 是正常入口；内部危险实验都有屏障、次数上限和收尾，所以默认运行会成功结束。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m12_channels_select/internal/course"
)

// main 只负责选择课程段落、创建日志和总体上限。实际 owner/发送者/关闭者关系在 course 包中。
// 遇到非预期失败，所有该段落的受控清理已执行，再由进程入口以非零状态告知调用方。
func main() {
	// flag.String 返回 *string；flag.Parse 写入指针指向的值，下面 *experiment 才读取实际选项。
	experiment := flag.String("experiment", "all", "all|pipeline|states|select|failures|memory")
	flag.Parse()
	trace := course.NewTrace(os.Stdout)
	sections := []struct {
		name string
		run  func(context.Context, *course.Trace) error
	}{
		{"states", states},
		{"select", selects},
		{"failures", failures},
		{"pipeline", pipelines},
		{"memory", memory},
	}
	matched := false
	for _, section := range sections {
		if *experiment != "all" && *experiment != section.name {
			continue
		}
		matched = true
		fmt.Printf("\n=== %s ===\n", section.name)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := section.run(ctx, trace)
		cancel() // 段落结束即释放 timer；没有 defer 到 main 最后积累整个课程的超时资源。
		if err != nil {
			fmt.Fprintln(os.Stderr, "FAIL:", err)
			os.Exit(1)
		}
		fmt.Printf("PASS %s\n", section.name)
	}
	if !matched {
		fmt.Fprintln(os.Stderr, "未知 experiment:", *experiment)
		os.Exit(2)
	}
	fmt.Println("PASS M12：全部选中实验的协议断言通过，owner 已完成对应清理")
}

// states 沿状态表 -> close 排空 -> 无缓冲会合 -> range 的缺失结束信号顺序组织阅读。
func states(ctx context.Context, trace *course.Trace) error {
	course.ChannelStates(trace)
	course.DrainClosed(trace)
	if err := course.Rendezvous(ctx, trace); err != nil {
		return err
	}
	return course.RangeNeedsClose(ctx, trace)
}

// selects 把有界忙循环和修复放在同一调用中；不为随机 case 选择设定比例或先后断言。
func selects(ctx context.Context, trace *course.Trace) error {
	_, err := course.SelectCases(ctx, trace)
	return err
}

// failures 运行两个真实错误窗口，但 sender 内 recover/夹具救援使普通入口能够继续。
func failures(ctx context.Context, trace *course.Trace) error {
	blocked, err := course.BlockedConsumer(ctx, trace)
	if err != nil {
		return err
	}
	if !blocked.BlockedBeforeRescue || blocked.RescuedOrder != "O-02" || !blocked.ProducerExited {
		return fmt.Errorf("裸发送错误窗口或最后清理缺失: %+v", blocked)
	}
	_, err = course.ReceiverCloses(ctx, trace)
	return err
}

// pipelines 是 10～20 分钟核心课程：六个订单、一条非法金额，同一模型覆盖五种边界。
// expected 允许来源故障/提前停止成为预期实验结果，不能把“返回 error”统一当成程序没完成。
func pipelines(ctx context.Context, trace *course.Trace) error {
	normal := course.Options{Workers: 3, JobsBuffer: 2, ResultsBuffer: 1, FailAt: -1}
	cases := []struct {
		name     string
		orders   []course.Order
		opts     course.Options
		expected error
		consumed int
	}{
		{"normal", course.SampleOrders(), normal, nil, 6},
		{"empty", nil, normal, nil, 0},
		{"early-stop", course.SampleOrders(), course.Options{Workers: 3, JobsBuffer: 1, ResultsBuffer: 0, FailAt: -1, StopAfter: 2}, course.ErrEarlyStop, 2},
		{"source-error", course.SampleOrders(), course.Options{Workers: 3, JobsBuffer: 1, ResultsBuffer: 1, FailAt: 3}, course.ErrSource, 3},
	}
	for _, scenario := range cases {
		trace.Log("owner", "scenario_start", "%s", scenario.name)
		report := course.Run(ctx, scenario.orders, scenario.opts, trace)
		if len(report.Results) != scenario.consumed || report.Completion.WorkersExited != scenario.opts.Workers {
			return fmt.Errorf("%s 业务/退出计数错误: %+v", scenario.name, report)
		}
		if scenario.expected == nil && report.Err != nil || scenario.expected != nil && !errors.Is(report.Err, scenario.expected) {
			return fmt.Errorf("%s 批次结局错误: %v", scenario.name, report.Err)
		}
		trace.Log("owner", "scenario_result", "%s consumed=%d produced=%d validated=%d workersExited=%d err=%v", scenario.name, len(report.Results), report.Completion.Produced, report.Completion.Validated, report.Completion.WorkersExited, report.Err)
	}
	trace.Log("owner", "scenario_start", "saturation")
	saturated := course.Saturation(ctx, trace)
	if saturated.Err != nil || len(saturated.Results) != 6 || saturated.Completion.WorkersExited != 1 {
		return fmt.Errorf("饱和解除后未完成全部订单: %+v", saturated)
	}
	trace.Log("owner", "scenario_result", "saturation consumed=%d produced=%d validated=%d workersExited=%d err=%v", len(saturated.Results), saturated.Completion.Produced, saturated.Completion.Validated, saturated.Completion.WorkersExited, saturated.Err)
	return nil
}

// memory 与显式 cmd/fault 配对：普通入口仅执行可证明无竞争的值复制与指针所有权回传。
func memory(ctx context.Context, trace *course.Trace) error {
	report, err := course.MemoryOwnership(ctx, trace)
	if err != nil {
		return err
	}
	if report.CopiedAmount != 100 || report.OriginalAmount != 9 || report.ReturnedAmount != 200 || report.FinalCount != 6 {
		return fmt.Errorf("同步/值副本断言失败: %+v", report)
	}
	return nil
}
