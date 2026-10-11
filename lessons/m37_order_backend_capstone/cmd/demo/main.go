package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"go-demo/lessons/m37_order_backend_capstone/internal/lab"
	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// main只拥有总预算/信号订阅。run函数返回前已执行全部业务资源defer；只有那以后
// 才允许os.Exit。直接在拥有SQL/后台任务的函数里Exit会跳过defer，留下未知状态。
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() (err error) {
	verify := flag.String("verify-cleanup", "", "只读核验本课精确资源账本")
	recovery := flag.Bool("verify-recovery", false, "真实关闭旧client后验证精确缓存借用与原owner清理")
	flag.Parse()
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(root, 120*time.Second)
	defer cancel()
	if *recovery && *verify != "" {
		return b.ErrInvalid
	}
	if *verify != "" {
		d, e := b.ReadManifest(*verify)
		if e != nil {
			return e
		}
		cfg, e := b.LoadConfig()
		if e != nil {
			return e
		}
		r, e := b.OpenResources(ctx, cfg, d.Namespace, ".local/verify-unused/resources.json", os.Stdout)
		if e != nil {
			return e
		}
		defer func() { err = errors.Join(err, r.Close()) }()
		return r.VerifyGone(ctx, d)
	}
	if *recovery {
		return lab.RunRecovery(ctx, os.Stdout)
	}
	return lab.Run(ctx, os.Stdout)
}
