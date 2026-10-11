package review

import (
	"context"
	"fmt"
	"io"

	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// Run是CLI与Live测试共用完整入口。scenario来自显式CLI范围选择；默认all全部
// 实际运行。共享SafeWriter保护bytes.Buffer/日志，不规定goroutine偶然日志顺序。
func Run(ctx context.Context, scenario string, candidateBad bool, out io.Writer) error {
	w := &b.SafeWriter{Out: out}
	found := false
	for _, trial := range []struct {
		name string
		run  func() error
	}{
		{"helpers", func() error { return Helpers(w) }},
		{"isolation", func() error { return Isolation(ctx, w, candidateBad) }},
		{"transactions", func() error { return Transactions(ctx, w) }},
		{"contexts", func() error { return Contexts(ctx, w) }},
		{"offsets", func() error { return Offsets(ctx, w) }},
		{"pools", func() error { return Pools(ctx, w) }},
		{"unknown", func() error { return Unknown(ctx, w) }},
		{"owners", func() error { return EarlyOwners(ctx, w) }},
	} {
		if scenario != "all" && scenario != trial.name {
			continue
		}
		found = true
		fmt.Fprintf(w, "REVIEW_START case=%s\n", trial.name)
		if e := trial.run(); e != nil {
			return fmt.Errorf("%s: %w", trial.name, e)
		}
		fmt.Fprintf(w, "REVIEW_PASS case=%s facts_and_owner_cleanup=true\n", trial.name)
	}
	if !found {
		return b.ErrInvalid
	}
	fmt.Fprintf(w, "M39 PASS scenario=%s candidate_bad=%t scope=bounded_real_review\n", scenario, candidateBad)
	return nil
}
