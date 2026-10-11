// demo是本课唯一正常入口。它同步拥有每组实验，失败也要先完成该组defer再返回main。
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	lesson "go-demo/lessons/m15_structured_concurrency"
)

// main只处理已经完成清理的最终错误；os.Exit不会执行main的defer，所以ctx由run内部拥有。
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "M15 FAILED:", err)
		os.Exit(1)
	}
}

// run创建整课45秒预算，按“原Pool反例→HTTP核心→errgroup→semaphore→修复Pool”顺序调用。
// runtime.Caller定位已编译的当前源文件，而不假定shell当前目录正好是module目录。
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("无法定位module")
	}
	moduleDir := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	fmt.Println("E01 原Pool持读锁的实际反例（原文件不修改）")
	out, err := lesson.LegacyProbe(ctx, moduleDir)
	fmt.Print(out)
	if err != nil {
		return err
	}
	fmt.Println("E02 核心：同一Catalog顺序→有界真实HTTP→允许部分业务失败")
	httpReport, err := lesson.HTTPScenario(ctx)
	if err != nil {
		return err
	}
	printRows("sequential", httpReport.Sequential)
	printRows("parallel-stable-ID", httpReport.Parallel)
	fmt.Printf("HTTP completion=%v active/max/finished=%d/%d/%d\n", httpReport.Completed, httpReport.Counts.Active, httpReport.Counts.Max, httpReport.Counts.Finished)
	fmt.Println("E03 errgroup Go接纳取消与首错Wait")
	group, err := lesson.GroupScenario(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Go-admission-still-blocked=%t Wait-before-cleanup=%t first=%v counts=%+v\n", group.AdmissionBlocked, group.WaitBlocked, group.First, group.Counts)
	fmt.Println("E04 semaphore在go前Acquire，加权接纳可取消")
	counts, rows, weightedErr, err := lesson.SemaphoreScenario(ctx)
	if err != nil {
		return err
	}
	printRows("weighted", rows)
	fmt.Printf("weighted counts=%+v expected-batch-error=%v\n", counts, weightedErr)
	fmt.Println("E05 修复Pool：关闭/提交取消/panic部分失败/首错/消费者停止/递归修复")
	paths := []struct {
		name string
		run  func(context.Context) (lesson.PoolReport, error)
	}{
		{"close", lesson.PoolCloseScenario}, {"cancel-submit", lesson.PoolCancelSubmitScenario}, {"partial", lesson.PoolPartialScenario},
		{"first-error", lesson.PoolFirstErrorScenario}, {"consumer-exit", lesson.PoolConsumerExitScenario}, {"recursive-fix", lesson.PoolRecursiveFixScenario},
	}
	for _, path := range paths {
		report, err := path.run(ctx)
		if err != nil {
			return fmt.Errorf("%s: %w", path.name, err)
		}
		printRows(path.name, report.Rows)
		fmt.Printf("pool %s rejected=%v aggregate=%v blocked=%t counts=%+v\n", path.name, report.Rejected, report.Aggregate, report.Blocked, report.Counts)
	}
	fmt.Println("M15 PASS: accepted tickets terminal; actual workers joined; HTTP listener/Body/Transport closed")
	return nil
}

// printRows只在批次owner已经join后读稳定结果。日志按输入ID而非完成顺序打印。
func printRows(name string, rows []lesson.Row) {
	for _, row := range rows {
		fmt.Printf("%s id=%s price=%d err=%v\n", name, row.ID, row.Price, row.Err)
	}
}
