package lesson

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/redis/go-redis/v9"
)

// authentication 由 all 入口调用。另建短寿命且独占的错误凭据 client，
// 不修改真实账号/ACL、不打印认证参数；错误 client 和正常长寿命 client 的 owner 不同。
func (l *lab) authentication(ctx context.Context) (err error) {
	invalid := l.cfg // Config 值副本；改变 password 字符串字段不修改正常 client 配置。
	invalid.Password += "-m25-deliberately-invalid"
	client := redis.NewClient(options(invalid, l.stats))
	defer func() { err = errors.Join(err, client.Close()) }()
	authErr := client.Ping(ctx).Err()
	// HasErrorPrefix 识别 Redis 的服务端错误链；它不等同网络 error，也不能按 redis.Nil 处理。
	if err := l.check("auth_error_distinct", redis.HasErrorPrefix(authErr, "WRONGPASS") && !errors.Is(authErr, redis.Nil), "wrong credential rejected with WRONGPASS; credential omitted"); err != nil {
		return err
	}
	if err := l.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("正常 client 认证修复核对：%w", err)
	}
	return l.check("auth_repair_valid_client", true, "existing valid client still authenticated")
}

// poolWait 固定一个独占 client 的 PoolSize=1，held 任务在 Read gate 中占住唯一借池额度。
// 第二请求没有可用连接，PendingRequests>0 才取消；第三请求实测 PoolTimeout。
// 所有任务由 g 拥有；函数最后才 Close client，禁止每个 GET 请求随手 Close 共享池。
func (l *lab) poolWait(ctx context.Context) (err error) {
	client, gate, err := l.faultClient(ctx, true, 3*time.Second, 200*time.Millisecond)
	if err != nil {
		return err
	}
	defer func() {
		if client != nil {
			err = errors.Join(err, client.Close())
		}
	}()
	g := newTaskGroup(ctx, l.report)
	g.releases, g.forceClose = []func(){gate.open}, client.Close
	defer g.finish(&err) // LIFO：先释放/join g，再执行上面的 client.Close。
	key := l.key("pool-value")
	if err := l.client.Set(ctx, key, "pool-real-value", 90*time.Second).Err(); err != nil {
		return err
	}
	gate.arm()
	held := g.start(func(workerCtx context.Context) (string, error) { return client.Get(workerCtx, key).Result() })
	if err := awaitCaptured(ctx, gate, held); err != nil {
		return err
	}
	requestCtx, requestCancel := context.WithCancel(g.ctx)
	defer requestCancel()
	waiter := g.start(func(context.Context) (string, error) { return client.Get(requestCtx, key).Result() })
	if err := waitCondition(ctx, func() bool { return client.PoolStats().PendingRequests >= 1 }, "第二请求真实借池等待"); err != nil {
		return err
	}
	stats := client.PoolStats()
	if err := l.check("pool_has_real_waiter", stats.TotalConns == 1 && stats.IdleConns == 0 && stats.PendingRequests >= 1,
		fmt.Sprintf("total=%d idle=%d pending=%d", stats.TotalConns, stats.IdleConns, stats.PendingRequests)); err != nil {
		return err
	}
	requestCancel() // 通知等待者；下一句才等它真正退出。本请求还没有把 GET 发到 Redis。
	if err := await(ctx, waiter.done, "取消借池请求 Done"); err != nil {
		return err
	}
	if err := l.check("pool_wait_cancelled", errors.Is(waiter.err, context.Canceled), fmt.Sprintf("waiter=%v held_still_waiting=true", waiter.err)); err != nil {
		return err
	}
	timed := g.start(func(workerCtx context.Context) (string, error) { return client.Get(workerCtx, key).Result() })
	if err := waitCondition(ctx, func() bool { return client.PoolStats().PendingRequests >= 1 }, "第三请求真实借池等待"); err != nil {
		return err
	}
	if err := await(ctx, timed.done, "PoolTimeout 请求 Done"); err != nil {
		return err
	}
	if err := l.check("pool_timeout_distinct", errors.Is(timed.err, redis.ErrPoolTimeout) && !errors.Is(timed.err, redis.Nil), fmt.Sprintf("error=%v no command dispatched", timed.err)); err != nil {
		return err
	}
	gate.open()
	if err := g.wait(ctx); err != nil {
		return err
	}
	if err := l.check("held_request_joined", held.err == nil && held.value == "pool-real-value", fmt.Sprintf("GET=%q error=%v Done=true", held.value, held.err)); err != nil {
		return err
	}
	stats = client.PoolStats()
	if err := l.check("pool_reusable_after_waiters", stats.PendingRequests == 0 && stats.IdleConns == 1, fmt.Sprintf("pending=%d idle=%d", stats.PendingRequests, stats.IdleConns)); err != nil {
		return err
	}
	if err := client.Close(); err != nil {
		return err
	}
	// Close 后驱动不能服务新请求，这证明 owner 结束的后果；不能用此句推断任务已经退出。
	closedErr := client.Get(ctx, key).Err()
	// 已显式 Close 的正常路径把局部指针设 nil，让退出 defer 不再对同一池重复 Close。
	client = nil
	return l.check("owner_close_ends_client", errors.Is(closedErr, redis.ErrClosed), fmt.Sprintf("after Close=%v", closedErr))
}

// waitCondition 只负责有界观测一个真实状态，间隔不是屏障本身。
// PendingRequests 是当前实际 driver 借池计数；不拿 goroutine 已启动或固定耗时猜它在池里。
func waitCondition(parent context.Context, predicate func() bool, label string) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if predicate() {
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("观察 %s：%w", label, ctx.Err())
		}
	}
}

// socketBudgets 由 Run(all) 调用，四个独占 client 依次展示不同预算/取消边界。
// 每一步会独立 Close/join，不向共享 Redis 注入 SLEEP、CLIENT PAUSE 或耗时脚本。
func (l *lab) socketBudgets(ctx context.Context) error {
	for _, mode := range []string{"context-disabled", "context-deadline", "manual-cancel", "response-lost"} {
		if err := l.responseExperiment(ctx, mode); err != nil {
			return err
		}
	}
	return nil
}

// responseExperiment 的 caller 是 socketBudgets；mode 选择注入阶段而不是随机延迟。
// 新 client/gate 属于当前函数，worker 属于 g；返回前必定停止、释放、join、关闭。
func (l *lab) responseExperiment(ctx context.Context, mode string) (err error) {
	contextEnabled, readTimeout := true, 2*time.Second
	if mode == "context-disabled" {
		contextEnabled = false
	}
	if mode == "response-lost" {
		readTimeout = 250 * time.Millisecond
	}
	client, gate, err := l.faultClient(ctx, contextEnabled, readTimeout, time.Second)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, client.Close()) }()
	g := newTaskGroup(ctx, l.report)
	g.releases, g.forceClose = []func(){gate.open}, client.Close
	defer g.finish(&err)
	key := l.key(mode)
	value := "real-response"
	if mode == "response-lost" {
		value = "0"
	}
	if err := l.client.Set(ctx, key, value, 90*time.Second).Err(); err != nil {
		return err
	}
	requestCtx := g.ctx
	var cancel context.CancelFunc
	if mode == "context-disabled" || mode == "context-deadline" {
		requestCtx, cancel = context.WithTimeout(g.ctx, 250*time.Millisecond)
	} else {
		requestCtx, cancel = context.WithCancel(g.ctx)
	}
	defer cancel()
	gate.arm()
	t := g.start(func(context.Context) (string, error) {
		if mode == "response-lost" {
			n, err := client.Incr(requestCtx, key).Result()
			return fmt.Sprint(n), err
		}
		return client.Get(requestCtx, key).Result()
	})
	if err := awaitCaptured(ctx, gate, t); err != nil {
		return fmt.Errorf("%s：%w", mode, err)
	}
	switch mode {
	case "context-disabled":
		if err := await(ctx, requestCtx.Done(), "请求250ms deadline"); err != nil {
			return err
		}
		select {
		case <-t.done:
			return fmt.Errorf("禁用 context timeout 的读应仍停在 gate，实际=%v", t.err)
		default:
		}
		if err := l.check("ctx_deadline_not_socket_default", errors.Is(requestCtx.Err(), context.DeadlineExceeded), "ctx expired; worker still at gated Read; ContextTimeoutEnabled=false"); err != nil {
			return err
		}
		gate.open()
	case "manual-cancel":
		cancel() // 当前 socket 已设2s deadline；无Deadline的 cancel 不自动修改那个时间。
		select {
		case <-t.done:
			return fmt.Errorf("manual cancel 不应自行释放响应 gate，实际=%v", t.err)
		default:
		}
		if err := l.check("manual_cancel_not_socket_interrupt", errors.Is(requestCtx.Err(), context.Canceled), "cancel notified; worker still at gated Read; owner must release/join"); err != nil {
			return err
		}
		gate.open()
	case "response-lost":
		// caller 尚未读到 INCR reply，另一条正常连接已读到1：副作用真实完成，返回超时仍不能回滚。
		count, err := l.client.Get(ctx, key).Int()
		if err != nil {
			return err
		}
		if err := l.check("incr_effect_before_reply", count == 1, fmt.Sprintf("independent GET=%d while caller response held", count)); err != nil {
			return err
		}
	}
	// context-deadline/response-lost 不开 gate，等待实际 driver 给出的 read deadline 超时。
	if err := g.wait(ctx); err != nil {
		return err
	}
	if mode == "context-disabled" || mode == "manual-cancel" {
		return l.check(mode+"_released_joined", t.err == nil && t.value == "real-response", fmt.Sprintf("GET=%q error=%v Done=true", t.value, t.err))
	}
	var timeout net.Error
	if err := l.check(mode+"_read_timeout", errors.As(t.err, &timeout) && timeout.Timeout(), fmt.Sprintf("error=%v ctx_error=%v Done=true", t.err, requestCtx.Err())); err != nil {
		return err
	}
	if mode == "response-lost" {
		count, err := l.client.Get(ctx, key).Int()
		if err != nil {
			return err
		}
		return l.check("no_automatic_write_replay", count == 1, fmt.Sprintf("after timeout GET=%d MaxRetries=-1", count))
	}
	return nil
}
