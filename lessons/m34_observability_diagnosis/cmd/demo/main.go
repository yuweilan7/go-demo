// demo 运行 M34 的有界真实 HTTP 与诊断对照。读取顺序：main -> Run ->
// contentionTrial -> checkout -> badCheckout/fixedCheckout -> callDependency -> downstream。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"go-demo/lessons/m34_observability_diagnosis/internal/lab"
)

// main 只选择入口；run 将资源 defer 完成之后才返回 error，os.Exit 不会截断其清理。
// leak-child 是正常 Run 的显式隔离故障入口，默认入口会完整运行并检查两种子进程。
func main() {
	out := flag.String("out", ".local/latest", "本次实验的独立样本目录")
	leak := flag.String("leak-child", "", "仅供有界子进程：unreachable 或 reachable")
	flag.Parse()
	if *leak != "" {
		if err := lab.LeakChild(*leak); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	_, err := lab.Run(ctx, *out)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "M34 failed:", err)
		os.Exit(1)
	}
}
