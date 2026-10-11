package lesson

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// staleUnsafe/staleFenced使用相同暂停/恢复时序，只改变下游接受写入的规则。
// 调用stale由Run串行调度，两个实验分别使用SQL id=1/2，不相互覆盖证据。
func (l *lab) staleUnsafe(ctx context.Context) error { return l.stale(ctx, false) }
func (l *lab) staleFenced(ctx context.Context) error { return l.stale(ctx, true) }

// stale的owner是当前实验函数。A的GET已成功、ctx尚有效，但之后在resume等待；
// Redis实际过期和B的SQL写入发生后才解除等待，复现“检查后失效”的副作用窗口。
func (l *lab) stale(ctx context.Context, fenced bool) (err error) {
	row, suffix := 1, "stale-unsafe"
	if fenced {
		row, suffix = 2, "stale-fenced"
	}
	key := l.key(suffix)
	getLease := func(clientIsA bool) (lease, error) {
		client := l.b
		if clientIsA {
			client = l.a
		}
		ttl := 5 * time.Second
		if clientIsA {
			ttl = 250 * time.Millisecond
		}
		if fenced {
			result, err := l.acquireFenced(ctx, client, key, ttl)
			if err != nil {
				return lease{}, err
			}
			return requireLease(result)
		}
		return requireLease(acquire(ctx, client, key, ttl))
	}
	a, err := getLease(true)
	if err != nil {
		return err
	}
	g := newGroup(ctx, l.report)
	resume, checked := make(chan struct{}), make(chan struct{})
	open := onceRelease(resume)
	g.releases = []func(){open}
	defer g.finish(&err)
	worker := g.start("A old worker", func(workCtx context.Context) outcome {
		owner, readErr := l.a.Get(workCtx, key).Result()
		if readErr != nil || owner != a.Owner {
			return outcome{Err: errors.Join(readErr, errors.New("A初始GET未确认自己的owner"))}
		}
		close(checked) // 这里只证明过去这一刻owner匹配，不能跨越下一行等待继续授权。
		if err := await(workCtx, resume, "A恢复工作门"); err != nil {
			return outcome{Err: err}
		}
		// 此处ctx仍未取消。即便再检查ctx.Err，Redis过期也不会主动关闭这个ctx。
		rows, writeErr := l.writeValue(workCtx, row, "A-old", a.Fence, fenced)
		return outcome{Value: rows, Err: writeErr}
	}, nil)
	select {
	case <-checked:
	case <-worker.finished:
		result, joinErr := g.join(ctx, worker)
		return errors.Join(errors.New("A在初始检查前退出"), result.Err, joinErr)
	case <-ctx.Done():
		return ctx.Err()
	}
	if err = l.expired(ctx, key); err != nil {
		return err
	}
	b, err := getLease(false)
	if err != nil {
		return err
	}
	if fenced && b.Fence <= a.Fence {
		return errors.New("权威授予序号未严格递增")
	}
	rowsB, err := l.writeValue(ctx, row, "B-new", b.Fence, fenced)
	if err != nil || rowsB != 1 {
		return errors.Join(err, errors.New("B真实写入应接受一行"))
	}
	fmt.Fprintf(l.out, "TIMELINE A_checked -> actual_TTL_expired -> B_granted(token=%d) -> B_SQL_accepted -> resume_A(token=%d)\n", b.Fence, a.Fence)
	open()
	result, joinErr := g.join(ctx, worker)
	if err = errors.Join(result.Err, joinErr); err != nil {
		return err
	}
	value, effects, accepted, err := l.readValue(ctx, row)
	if err != nil {
		return err
	}
	if fenced {
		if err = l.check("stale_SQL_rejected", result.Value == 0 && value == "B-new" && effects == 1 && accepted == b.Fence,
			fmt.Sprintf("A token=%d rows=%d; B token=%d; SQL value=%s effects=%d last_token=%d", a.Fence, result.Value, b.Fence, value, effects, accepted)); err != nil {
			return err
		}
		var issued int64
		if err = l.db.QueryRowContext(ctx, "SELECT issued_token FROM "+l.state+" WHERE id=0").Scan(&issued); err != nil {
			return err
		}
		if err = l.check("authority_durable_issued_token", issued == b.Fence, fmt.Sprintf("separate SELECT issued=%d accepted=%d", issued, accepted)); err != nil {
			return err
		}
	} else {
		if err = l.check("GET_ctx_do_not_fence_SQL", result.Value == 1 && value == "A-old" && effects == 2,
			fmt.Sprintf("A rows=%d; SQL value=%s effects=%d; ctx_error=%v", result.Value, value, effects, g.ctx.Err())); err != nil {
			return err
		}
	}
	deletedA, err := release(ctx, l.a, a)
	if err != nil {
		return err
	}
	ownerB, err := l.b.Get(ctx, key).Result()
	if err != nil {
		return err
	}
	if err = l.check(suffix+"_safe_release", deletedA == 0 && ownerB == b.Owner, "A compare-delete=0; B owner preserved"); err != nil {
		return err
	}
	_, err = release(ctx, l.b, b)
	return err
}

// unlocking故意raw DEL只操作自己随机run键，演示它会误删新owner；修复组换另一个键。
// 竞争是在A租约仍有效时发生，过期以真实EXISTS屏障判定，重复释放以Lua返回值判定。
func (l *lab) unlocking(ctx context.Context) error {
	badKey := l.key("unlock-bad")
	a, err := requireLease(acquire(ctx, l.a, badKey, 250*time.Millisecond))
	if err != nil {
		return err
	}
	competition := acquire(ctx, l.b, badKey, 5*time.Second)
	if err = l.check("competition_is_not_error", competition.State == contended && competition.Err == nil, string(competition.State)); err != nil {
		return err
	}
	if err = l.expired(ctx, badKey); err != nil {
		return err
	}
	b, err := requireLease(acquire(ctx, l.b, badKey, 5*time.Second))
	if err != nil {
		return err
	}
	// 危险点：没有带a.Owner，DEL无法判断当前是谁。此时删的是B，不是过期的A。
	removed, err := l.a.Del(ctx, a.Key).Result()
	if err != nil {
		return err
	}
	exists, err := l.b.Exists(ctx, b.Key).Result()
	if err != nil {
		return err
	}
	if err = l.check("raw_DEL_deleted_new_owner", removed == 1 && exists == 0, "A raw DEL=1; B lock EXISTS=0"); err != nil {
		return err
	}
	key := l.key("unlock-fixed")
	a, err = requireLease(acquire(ctx, l.a, key, 250*time.Millisecond))
	if err != nil {
		return err
	}
	if err = l.expired(ctx, key); err != nil {
		return err
	}
	b, err = requireLease(acquire(ctx, l.b, key, 5*time.Second))
	if err != nil {
		return err
	}
	deleted, err := release(ctx, l.a, a)
	if err != nil {
		return err
	}
	renewed, err := renew(ctx, l.a, a, 5*time.Second)
	if err != nil {
		return err
	}
	value, err := l.b.Get(ctx, key).Result()
	if err != nil {
		return err
	}
	if err = l.check("token_delete_and_renew_guard", deleted == 0 && renewed == 0 && value == b.Owner, "stale release=0 renew=0; B still owns"); err != nil {
		return err
	}
	first, err := release(ctx, l.b, b)
	if err != nil {
		return err
	}
	second, err := release(ctx, l.b, b)
	if err != nil {
		return err
	}
	return l.check("duplicate_release_idempotent", first == 1 && second == 0, fmt.Sprintf("first=%d second=%d", first, second))
}

// renewCancellation有一个续租goroutine与一个工作goroutine，都归g所有。
// 第一次真实续租成功后，显式门暂停下一次续租；实际TTL过期/B取得后才放门。
// 续租0通知workCancel，工作观察取消后在wrapper清理门等待，展示cancel/Result/Finished。
func (l *lab) renewCancellation(ctx context.Context) (err error) {
	key := l.key("renew-cancel")
	a, err := requireLease(acquire(ctx, l.a, key, 400*time.Millisecond))
	if err != nil {
		return err
	}
	g := newGroup(ctx, l.report)
	workCtx, workCancel := context.WithCancel(g.ctx)
	defer workCancel()
	renewed, paused := make(chan struct{}), make(chan struct{})
	workAccepted := make(chan struct{})
	var acceptedErr error // worker写、关闭workAccepted发布，owner接收该屏障后才读。
	resumeRenew, cleanupGate, cleanupEntered := make(chan struct{}), make(chan struct{}), make(chan struct{})
	openRenew, openCleanup := onceRelease(resumeRenew), onceRelease(cleanupGate)
	g.releases = []func(){openRenew, openCleanup}
	defer g.finish(&err)
	work := g.start("cancellable worker", func(context.Context) outcome {
		// 先让真实MySQL接受一次业务更新，再进入取消等待；丢租后的cancel不会
		// 把这个已接受的autocommit更新撤回。workAccepted是副作用完成屏障。
		rows, writeErr := l.writeValue(workCtx, 5, "A-before-cancel", 0, false)
		if writeErr != nil || rows != 1 {
			acceptedErr = errors.Join(writeErr, errors.New("取消前SQL应接受一行"))
		}
		close(workAccepted) // 成功/失败都发布实际SQL结局，失败不能让owner只等一个不会出现的成功门。
		if acceptedErr != nil {
			return outcome{Err: acceptedErr}
		}
		<-workCtx.Done() // 当前工作真正等待的是这一条channel，不是Redis TTL自动杀任务。
		return outcome{Value: rows, Err: workCtx.Err()}
	}, func() error {
		close(cleanupEntered) // wrapper已发Result，但还没执行最早注册的close(Finished)。
		<-cleanupGate
		return nil
	})
	renewer := g.start("lease renewer", func(taskCtx context.Context) outcome {
		ticker := time.NewTicker(30 * time.Millisecond)
		defer ticker.Stop() // 续租任务拥有ticker；它的Result/Finished不能代替Stop。
		first, didPause := true, false
		for {
			select {
			case <-taskCtx.Done():
				return outcome{Err: taskCtx.Err()}
			case <-ticker.C:
			}
			if !first && !didPause {
				close(paused)
				didPause = true
				if err := await(taskCtx, resumeRenew, "暂停续租门"); err != nil {
					return outcome{Err: err}
				}
			}
			count, err := renew(taskCtx, l.a, a, 400*time.Millisecond)
			if err != nil || count == 0 {
				workCancel() // 通知不能撤销数据库已接受的效果，也不等于worker已清理。
				return outcome{Value: count, Err: err}
			}
			if first {
				close(renewed)
				first = false
			}
		}
	}, nil)
	select {
	case <-renewed:
	case <-renewer.finished:
		result, joinErr := g.join(ctx, renewer)
		return errors.Join(errors.New("首次续租未成功"), result.Err, joinErr)
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-workAccepted:
	case <-work.finished:
		result, joinErr := g.join(ctx, work)
		return errors.Join(errors.New("取消前SQL未到接受屏障"), result.Err, joinErr)
	case <-ctx.Done():
		return ctx.Err()
	}
	if acceptedErr != nil {
		return acceptedErr
	}
	select {
	case <-paused:
	case <-renewer.finished:
		result, joinErr := g.join(ctx, renewer)
		return errors.Join(errors.New("续租在暂停屏障前结束"), result.Err, joinErr)
	case <-ctx.Done():
		return ctx.Err()
	}
	if err = l.expired(ctx, key); err != nil {
		return err
	}
	b, err := requireLease(acquire(ctx, l.b, key, 5*time.Second))
	if err != nil {
		return err
	}
	openRenew()
	renewResult, joinErr := g.join(ctx, renewer)
	if err = errors.Join(renewResult.Err, joinErr); err != nil {
		return err
	}
	if err = await(ctx, cleanupEntered, "工作已发Result且进入wrapper清理"); err != nil {
		return err
	}
	finished := false
	select {
	case <-work.finished:
		finished = true
	default:
	}
	if err = l.check("cancel_and_Result_not_Finished", errors.Is(workCtx.Err(), context.Canceled) && len(work.result) == 1 && !finished && renewResult.Value == 0,
		"renew 1 -> pause -> TTL expire -> B acquire -> renew 0 -> cancel; Result ready; cleanup blocked; Finished=false"); err != nil {
		return err
	}
	openCleanup()
	workResult, joinErr := g.join(ctx, work)
	if joinErr != nil {
		return joinErr
	}
	if err = l.check("cancelled_worker_real_join", errors.Is(workResult.Err, context.Canceled), "cleanup released; Finished closed; owner joined work and renewer"); err != nil {
		return err
	}
	value, effects, _, readErr := l.readValue(ctx, 5)
	if readErr != nil {
		return readErr
	}
	if err = l.check("cancel_not_revoke_accepted_SQL", workResult.Value == 1 && value == "A-before-cancel" && effects == 1,
		fmt.Sprintf("after lease loss/cancel/Finished SQL value=%s effects=%d; accepted effect remains", value, effects)); err != nil {
		return err
	}
	owner, err := l.b.Get(ctx, key).Result()
	if err != nil {
		return err
	}
	if err = l.check("renew_failure_preserved_B", owner == b.Owner, "old renew=0 did not extend/overwrite B"); err != nil {
		return err
	}
	_, err = release(ctx, l.b, b)
	return err
}
