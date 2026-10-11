package lesson

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// atomicBoundaries 由 Run 调用，用实际键副作用分别检验 pipeline、MULTI/EXEC 与 Lua。
// “不被其他客户端插入执行”与“错误时回滚已做写入”是两项不同契约；本函数不假装它们相同。
func (l *lab) atomicBoundaries(ctx context.Context) error {
	pipeBefore, pipeWrong, pipeAfter := l.key("pipeline-before"), l.key("pipeline-wrong"), l.key("pipeline-after")
	if err := l.client.HSet(ctx, pipeWrong, "state", "hash").Err(); err != nil {
		return err
	}
	if err := l.client.Expire(ctx, pipeWrong, 90*time.Second).Err(); err != nil {
		return err
	}
	pipe := l.client.Pipeline() // 新建的是命令缓冲；不创建应用 goroutine，也不独占长期连接。
	before := pipe.Set(ctx, pipeBefore, "survives", 90*time.Second)
	wrong := pipe.Incr(ctx, pipeWrong) // INCR hash 是合法语法，但 Redis 执行时 WRONGTYPE。
	after := pipe.Set(ctx, pipeAfter, "also-survives", 90*time.Second)
	// Pipeline.Get/Set 只排队，不等同普通 client 的同步完成；此时 Cmd.Err()==nil 不是服务端认可。
	_, err := l.client.Get(ctx, pipeBefore).Result()
	if err := l.check("pipeline_queued_not_executed", errors.Is(err, redis.Nil), "observer GET before Exec = redis.Nil"); err != nil {
		return err
	}
	commands, execErr := pipe.Exec(ctx) // 一批写入并逐条读回复；先前已执行的命令不会被回滚。
	if err := l.check("pipeline_individual_errors", len(commands) == 3 && redis.HasErrorPrefix(execErr, "WRONGTYPE") && before.Err() == nil && redis.HasErrorPrefix(wrong.Err(), "WRONGTYPE") && after.Err() == nil,
		fmt.Sprintf("commands=%d before=%v wrong=%v after=%v", len(commands), before.Err(), wrong.Err(), after.Err())); err != nil {
		return err
	}
	if err := l.expectStrings(ctx, "pipeline_writes_survive", map[string]string{pipeBefore: "survives", pipeAfter: "also-survives"}); err != nil {
		return err
	}
	txBefore, txWrong, txAfter := l.key("tx-before"), l.key("tx-wrong"), l.key("tx-after")
	if err := l.client.HSet(ctx, txWrong, "state", "hash").Err(); err != nil {
		return err
	}
	if err := l.client.Expire(ctx, txWrong, 90*time.Second).Err(); err != nil {
		return err
	}
	tx := l.client.TxPipeline() // go-redis 给这批命令加 MULTI/EXEC；不是 database/sql.Tx。
	txFirst := tx.Set(ctx, txBefore, "committed-before-error", 90*time.Second)
	txBad := tx.Incr(ctx, txWrong)
	txLast := tx.Set(ctx, txAfter, "committed-after-error", 90*time.Second)
	_, execErr = tx.Exec(ctx)
	if err := l.check("multi_exec_no_sql_rollback", redis.HasErrorPrefix(execErr, "WRONGTYPE") && txFirst.Err() == nil && redis.HasErrorPrefix(txBad.Err(), "WRONGTYPE") && txLast.Err() == nil,
		fmt.Sprintf("before=%v bad=%v after=%v", txFirst.Err(), txBad.Err(), txLast.Err())); err != nil {
		return err
	}
	if err := l.expectStrings(ctx, "transaction_writes_survive", map[string]string{txBefore: "committed-before-error", txAfter: "committed-after-error"}); err != nil {
		return err
	}
	// 对照排队期语法错误：这里 INCR 参数数量不合法，会污染 MULTI 队列，EXECABORT 时不执行。
	queueKey := l.key("tx-queue-abort")
	queue := l.client.TxPipeline()
	queue.Set(ctx, queueKey, "must-not-execute", 90*time.Second)
	queue.Do(ctx, "INCR", queueKey, "extra-argument")
	_, execErr = queue.Exec(ctx)
	_, missingErr := l.client.Get(ctx, queueKey).Result()
	if err := l.check("multi_queue_error_aborts", redis.HasErrorPrefix(execErr, "EXECABORT") && errors.Is(missingErr, redis.Nil), fmt.Sprintf("EXEC=%v GET=redis.Nil", execErr)); err != nil {
		return err
	}
	luaBefore, luaAfter := l.key("lua-before"), l.key("lua-after")
	// KEYS 必须显式声明被访问的键；ARGV 是普通参数，不从脚本拼不受控键名。
	// Lua 在一段执行内不让其他命令穿插，但 redis.call 错误会终止脚本，并保留此前 SET。
	script := `redis.call('SET', KEYS[1], 'lua-before-survives', 'PX', 90000)
redis.call('INCR', KEYS[2])
redis.call('SET', KEYS[3], 'must-not-execute', 'PX', 90000)
return 1`
	luaErr := l.client.Eval(ctx, script, []string{luaBefore, txWrong, luaAfter}).Err()
	_, missingErr = l.client.Get(ctx, luaAfter).Result()
	if err := l.check("lua_error_keeps_prior_writes", redis.HasErrorPrefix(luaErr, "WRONGTYPE") && errors.Is(missingErr, redis.Nil), fmt.Sprintf("EVAL=%v after=redis.Nil", luaErr)); err != nil {
		return err
	}
	return l.expectStrings(ctx, "lua_prior_write_survives", map[string]string{luaBefore: "lua-before-survives"})
}

// expectStrings 被原子边界实验调用，独立 GET 真实值而非只信先前 Cmd.Err。
// map 的迭代顺序不保证，因此只断言每个键值，不把日志的键顺序写成业务保证。
func (l *lab) expectStrings(ctx context.Context, name string, expected map[string]string) error {
	for key, want := range expected {
		got, err := l.client.Get(ctx, key).Result()
		if err != nil || got != want {
			return fmt.Errorf("%s 键=%s got=%q want=%q error=%v", name, key, got, want, err)
		}
	}
	return l.check(name, true, fmt.Sprintf("independent GET verified=%d", len(expected)))
}

// incrementWithExpiry 是业务修复的完整 Redis 内边界。调用前校验 TTL 正整数毫秒，
// INCR 本身先检查键类型/整数；脚本把成功 INCR 和 PEXPIRE 放在同一个隔离执行中。
// 已有 orphan(-1) 也补 TTL；后续正常增量不刷新窗口，不会无限延长限流周期。
const incrementWithExpiry = `local ms = tonumber(ARGV[1])
if not ms or ms <= 0 or ms ~= math.floor(ms) then
  return redis.error_reply('ERR ttl must be positive integer milliseconds')
end
local n = redis.call('INCR', KEYS[1])
if n == 1 or redis.call('PTTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ms)
end
return n`

// expiryWindow 由 Run 调用，模拟限流计数第一次 INCR 后请求取消、尚未 EXPIRE 的窗口。
// 本函数的 owner 先观察 afterINCR，再发 cancel，最后等第一任务 Done；不会依赖调度 sleep。
func (l *lab) expiryWindow(ctx context.Context) (err error) {
	key := l.key("incr-expire-window")
	g := newTaskGroup(ctx, l.report)
	defer g.finish(&err)
	afterINCR, expireGate := make(chan struct{}), make(chan struct{})
	release := onceRelease(expireGate)
	g.releases = append(g.releases, release)
	first := g.start(func(workerCtx context.Context) (string, error) {
		n, err := l.client.Incr(workerCtx, key).Result()
		if err != nil {
			return "", err
		}
		close(afterINCR) // INCR 回复已经被解析：Redis 当前计数确实是1，但没有过期。
		if err := await(workerCtx, expireGate, "第一请求准备发送 EXPIRE"); err != nil {
			return fmt.Sprint(n), err
		}
		// 坏代码常见模式：只在 n==1 时 EXPIRE。若进程/请求在这里之前退出，就遗漏 TTL。
		return fmt.Sprint(n), l.client.Expire(workerCtx, key, 90*time.Second).Err()
	})
	if err := await(ctx, afterINCR, "第一请求 INCR 已完成"); err != nil {
		return err
	}
	n, err := l.client.Incr(ctx, key).Result() // 第二请求看到2，旧业务分支 n!=1 不设置TTL。
	if err != nil {
		return err
	}
	ttl, err := l.client.TTL(ctx, key).Result()
	if err != nil {
		return err
	}
	if err := l.check("incr_expire_competing_window", n == 2 && ttl == time.Duration(-1), fmt.Sprintf("second_count=%d TTL=%v before first EXPIRE", n, ttl)); err != nil {
		return err
	}
	g.cancel() // 不释放 expireGate；只有取消分支可进行，确定第一请求不能走到 EXPIRE。
	joinCtx, joinCancel := context.WithTimeout(context.Background(), 3*time.Second)
	joinErr := g.wait(joinCtx)
	joinCancel()
	if joinErr != nil {
		return joinErr
	}
	if err := l.check("cancel_does_not_undo_incr", first.value == "1" && errors.Is(first.err, context.Canceled), "first INCR=1; first Done=true; EXPIRE never sent"); err != nil {
		return err
	}
	ttl, err = l.client.TTL(ctx, key).Result()
	if err != nil || ttl != time.Duration(-1) {
		return fmt.Errorf("取消后的 TTL 证据=%v error=%v", ttl, err)
	}
	// 对已形成孤儿的键，脚本也修复；只是 pipeline 打包 INCR+EXPIRE 不具备该隔离边界。
	n, err = l.client.Eval(ctx, incrementWithExpiry, []string{key}, 90000).Int64()
	if err != nil {
		return err
	}
	pttl, err := l.client.PTTL(ctx, key).Result()
	if err != nil {
		return err
	}
	if err := l.check("lua_repairs_orphan_counter", n == 3 && pttl > 0 && pttl <= 90*time.Second, fmt.Sprintf("count=%d PTTL=%v", n, pttl)); err != nil {
		return err
	}
	newKey := l.key("lua-new-counter")
	n, err = l.client.Eval(ctx, incrementWithExpiry, []string{newKey}, 90000).Int64()
	if err != nil {
		return err
	}
	pttl, err = l.client.PTTL(ctx, newKey).Result()
	if err != nil {
		return err
	}
	if err := l.check("lua_initial_counter_has_ttl", n == 1 && pttl > 0 && pttl <= 90*time.Second, fmt.Sprintf("count=%d PTTL=%v", n, pttl)); err != nil {
		return err
	}
	// 无效 TTL 在写入前拒绝；之后独立 GET 证明未递增，不把 EVAL error 当全回滚承诺。
	invalidErr := l.client.Eval(ctx, incrementWithExpiry, []string{newKey}, 0).Err()
	count, err := l.client.Get(ctx, newKey).Int()
	if err != nil {
		return err
	}
	// 本版 HasErrorPrefix 会先剥去开头的 "ERR "，prefix 应传 "ttl"，不是整条协议文案。
	return l.check("lua_validate_before_side_effect", redis.HasErrorPrefix(invalidErr, "ttl") && count == 1, fmt.Sprintf("invalid_ttl_error=%v count=%d", invalidErr, count))
}
