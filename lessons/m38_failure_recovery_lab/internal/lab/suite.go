package lab

import (
	"context"
	"fmt"
	"io"

	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// Run 是正常入口默认完整矩阵；scenario是CLI/测试选中的范围。SafeWriter是共享
// 指针，保护多goroutine/子进程同时写日志，不承诺它们的偶然调度顺序。
func Run(ctx context.Context, scenario string, out io.Writer) error {
	w := &b.SafeWriter{Out: out}
	selected := false
	for _, trial := range []struct {
		name string
		run  func(context.Context, io.Writer) error
	}{{"core", Core}, {"proxies", Proxies}, {"kills", Kills}} {
		if scenario != "all" && scenario != trial.name {
			continue
		}
		selected = true
		fmt.Fprintf(w, "TRIAL_START scenario=%s\n", trial.name)
		if e := trial.run(ctx, w); e != nil {
			return fmt.Errorf("%s: %w", trial.name, e)
		}
		fmt.Fprintf(w, "TRIAL_PASS scenario=%s real_effects_and_cleanup=true\n", trial.name)
	}
	if !selected {
		return b.ErrInvalid
	}
	fmt.Fprintf(w, "M38 PASS scenario=%s proof=bounded_real_services_processes_protocols\n", scenario)
	return nil
}
