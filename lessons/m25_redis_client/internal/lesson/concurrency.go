package lesson

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// lostUpdate 由 Run 调用，两个 worker 共用线程安全 client，但各自 GET 的整数是独立值副本。
// Redis 每条命令原子并不能把 GET -> Go加1 -> SET 自动合成原子读改写。
func (l *lab) lostUpdate(ctx context.Context) (err error) {
	key := l.key("lost-update")
	if err := l.client.Set(ctx, key, 0, 90*time.Second).Err(); err != nil {
		return err
	}
	g := newTaskGroup(ctx, l.report)
	defer g.finish(&err)
	gate := make(chan struct{})
	release := onceRelease(gate)
	g.releases = append(g.releases, release)
	read := make(chan struct{}, 2)
	for range 2 {
		// 闭包捕获同一个 l 指针、key 和 gate，这是有意共享；value在每个调用内单独声明。
		// for range 2 不声明一个需要捕获的循环变量，因此不存在把worker编号读错的依赖。
		g.start(func(workerCtx context.Context) (string, error) {
			value, err := l.client.Get(workerCtx, key).Int()
			if err != nil {
				return "", err
			}
			read <- struct{}{} // 容量2：两次通知不会卡住；只证明各 worker 已取得旧值。
			// owner 必须收齐两次 GET 通知才 close gate。没有 sleep，也不要求谁先读。
			if err := await(workerCtx, gate, "两次 GET 完成后允许 SET"); err != nil {
				return "", err
			}
			err = l.client.Set(workerCtx, key, value+1, 90*time.Second).Err()
			return strconv.Itoa(value), err
		})
	}
	for range 2 {
		if err := await(ctx, read, "worker GET 旧值"); err != nil {
			return err
		}
	}
	release()
	if err := g.wait(ctx); err != nil {
		return err
	}
	for _, t := range g.tasks {
		if t.err != nil || t.value != "0" {
			return fmt.Errorf("读改写 worker old=%s error=%v", t.value, t.err)
		}
	}
	actual, err := l.client.Get(ctx, key).Int()
	if err != nil {
		return err
	}
	if err := l.check("read_modify_write_loses_one", actual == 1, fmt.Sprintf("two_read=0 two_write=1 final=%d expected_business=2", actual)); err != nil {
		return err
	}
	if err := l.client.Set(ctx, key, 0, 90*time.Second).Err(); err != nil {
		return err
	}
	// 修复仍使用同一个 client/键，但递增在 Redis 内执行。每个 INCR 的结果是独立 IntCmd。
	fixed := newTaskGroup(ctx, l.report)
	defer fixed.finish(&err)
	for range 8 {
		fixed.start(func(workerCtx context.Context) (string, error) {
			value, err := l.client.Incr(workerCtx, key).Result()
			return strconv.FormatInt(value, 10), err
		})
	}
	if err := fixed.wait(ctx); err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, t := range fixed.tasks {
		if t.err != nil {
			return t.err
		}
		seen[t.value] = true
	}
	actual, err = l.client.Get(ctx, key).Int()
	if err != nil {
		return err
	}
	return l.check("incr_repairs_lost_updates", actual == 8 && len(seen) == 8 && seen["1"] && seen["8"], fmt.Sprintf("workers=8 unique_replies=%d final=%d", len(seen), actual))
}
