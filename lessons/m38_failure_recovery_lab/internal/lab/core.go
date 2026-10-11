package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"time"

	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// command 只构造合成alice命令的值副本，金额/状态/ID仍由唯一Store.Create决定。
func command(key, request string) b.Command {
	return b.Command{Owner: "alice", Key: key, RequestID: request, Input: b.Input{SKU: b.SKU, Quantity: 1}}
}

// Core 是10～20分钟主阅读实验：Tx回滚、真实请求超时、停止时在途任务、pool
// 等待、HTTP响应丢失、重复投递、缓存晚回填和真实group rebalance。
// 所有故障复用同一套业务，不布置额外exercise；各step错误由withSession总guard收尾。
func Core(ctx context.Context, out io.Writer) error {
	return withSession(ctx, out, func(s *session) error {
		if e := s.authorize(ctx, "http-lost", "cache-bad", "cache-fixed"); e != nil {
			return e
		}
		if e := s.fresh(ctx, s.cfg, "core-client"); e != nil {
			return e
		}
		r := s.business
		consumer, before, stopGate := newGate(), newGate(), newGate()
		s.gates = append(s.gates, consumer, before, stopGate)
		a, e := s.app(b.Hooks{BeforeConsume: consumer.wait, AfterOrderWrites: func(ctx context.Context, o b.Order) error {
			switch o.Key {
			case "early":
				return errInjected
			case "panic":
				panic(errInjected)
			case "timeout":
				return before.wait(ctx)
			case "stop":
				return stopGate.wait(ctx)
			}
			return nil
		}})
		if e != nil {
			return e
		}
		if e = await(ctx, consumer.entered, "consumer未poll"); e != nil {
			return e
		}
		client, transport := httpClient()
		defer transport.CloseIdleConnections()
		for _, key := range []string{"early", "panic"} {
			response := call(ctx, client, a.URL, "POST", "/orders", key)
			if e = b.Assert(response.err == nil && response.status == 503, "Tx%s真实HTTP错误 status=%d err=%v", key, response.status, response.err); e != nil {
				return e
			}
			if e = audit(ctx, s.original, 0, 0, 0, 0, -1); e != nil {
				return e
			}
			fmt.Fprintf(out, "F02_ROLLBACK key=%s no_order=true stock=10\n", key)
		}
		// timeout的期限作用于真实HTTP请求。先等SQL写后门，再等待客户端到期、
		// 服务端hook确实读到取消和handler空闲，才查“最终无提交”的事实。
		deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var timed response
		j := launch(deadline, func(ctx context.Context) error {
			timed = call(ctx, client, a.URL, "POST", "/orders", "timeout")
			return timed.err // 若observer提前失败，session仍保留未知写/原期限原因。
		})
		s.jobs = append(s.jobs, j)
		if e = await(ctx, before.entered, "真实Tx writes后"); e != nil {
			return e
		}
		if e = await(ctx, j.done, "HTTP timeout caller.Done"); e != nil {
			return e
		}
		if e = await(ctx, before.observed, "服务端hook观察取消"); e != nil {
			return e
		}
		if e = b.Assert(errors.Is(timed.err, context.DeadlineExceeded) && errors.Is(timed.err, b.ErrUnknown), "client timeout保留原deadline/UNKNOWN: %v", timed.err); e != nil {
			return e
		}
		j.consumed = true
		if e = b.Observe(ctx, func() (bool, error) { n, _ := a.ActiveHandlers(); return n == 0, nil }); e != nil {
			return e
		}
		if e = audit(ctx, s.original, 0, 0, 0, 0, -1); e != nil {
			return e
		}
		fmt.Fprintln(out, "F01_TIMEOUT caller=DeadlineExceeded server_observed_cancel=true handler_done=true effects=0")
		var stopping response
		j = launch(ctx, func(ctx context.Context) error {
			stopping = call(ctx, client, a.URL, "POST", "/orders", "stop")
			if stopping.body.Outcome == "UNKNOWN" {
				return errors.Join(b.ErrUnknown, stopping.err)
			}
			return stopping.err
		})
		s.jobs = append(s.jobs, j)
		if e = await(ctx, stopGate.entered, "stop时在途事务"); e != nil {
			return e
		}
		a.Stop() // 同步取消服务ctx。不能只cancel客户端就放Tx门，网络传播存在延迟。
		if e = await(ctx, stopGate.observed, "服务端根ctx取消观察"); e != nil {
			return e
		}
		stopGate.open()
		if e = a.Close(); e != nil {
			return e
		}
		if e = await(ctx, j.done, "停止请求caller.Done"); e != nil {
			return e
		}
		if e = b.Assert(a.SafeToRelease() && stopping.body.Outcome == "UNKNOWN", "停止root后App/handler/task最终Done，caller保留unknown"); e != nil {
			return e
		}
		j.consumed = true
		if e = audit(ctx, s.original, 0, 0, 0, 0, -1); e != nil {
			return e
		}
		fmt.Fprintln(out, "F01_STOP app_final_done=true caller_done=true root_cancel_observed=true stock=10")
		if e = pool(ctx, s, r); e != nil {
			return e
		}
		// 旧App已经Done。新App仍复用同一个client/唯一Store，不留下第二个写者。
		hold := newGate()
		s.gates = append(s.gates, hold)
		a, e = s.app(b.Hooks{BeforeConsume: hold.wait})
		if e != nil {
			return e
		}
		proxy, e := newHTTPDrop(a.URL, out)
		if e != nil {
			return e
		}
		s.httpProxies = append(s.httpProxies, proxy)
		lost := call(ctx, client, proxy.url, "POST", "/orders", "http-lost")
		if e = b.Assert(errors.Is(lost.err, b.ErrUnknown), "真实201丢失应交caller UNKNOWN actual=%v", lost.err); e != nil {
			return e
		}
		if e = proxy.close(); e != nil {
			return e
		}
		if e = b.Observe(ctx, func() (bool, error) {
			var n int
			e := r.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+r.Store.Table("outbox")+"` WHERE sent=TRUE").Scan(&n)
			return n == 1, e
		}); e != nil {
			return e
		}
		if e = verifyAPI(ctx, client, a, "http-lost", "PENDING"); e != nil {
			return e
		}
		if e = audit(ctx, s.original, 1, 1, 0, 1, -1); e != nil {
			return e
		}
		fmt.Fprintln(out, "F04_HTTP_RESPONSE_LOST raw_201=true original_key_lookup=true no_blind_write_retry=true")
		hold.open()
		if e = waitState(ctx, a, r, 1); e != nil {
			return e
		}
		if e = verifyAPI(ctx, client, a, "http-lost", "RECORDED"); e != nil {
			return e
		}
		// 同字节重复不是新事件：broker真实offset1/2，但inbox/receipt效果保持1。
		o, found, e := r.Store.FindByKey(ctx, "alice", "http-lost")
		if e != nil || !found {
			return errors.Join(e, errors.New("original key missing"))
		}
		for range 2 {
			if _, e = r.Broker.Publish(ctx, b.EventFor(o)); e != nil {
				return e
			}
		}
		if e = waitState(ctx, a, r, 3); e != nil {
			return e
		}
		if e = audit(ctx, s.original, 1, 1, 1, 3, 3); e != nil {
			return e
		}
		if e = rebalance(ctx, s, a); e != nil {
			return e
		}
		if e = cacheRace(ctx, s, a, client); e != nil {
			return e
		}
		return audit(ctx, s.original, 3, 3, 3, 5, 5)
	})
}

// pool 用本课business池实际取走全部3条连接，再从另一goroutine请求第4条。
// WaitCount来自database/sql，pprof栈定位DB.conn等待；持有Conn的owner是本函数，
// defer在任何提前返回时归还连接。ctx到期只结束等待者，不能归还别人的Conn。
func pool(ctx context.Context, s *session, r *b.Resources) (err error) {
	var held []io.Closer
	defer func() {
		for _, c := range held {
			err = errors.Join(err, b.CleanupCause(c.Close()))
		}
	}()
	for range 3 {
		c, e := r.SQL.Conn(ctx)
		if e != nil {
			return e
		}
		held = append(held, c)
	}
	baseline := r.SQL.Stats().WaitCount
	deadline, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	j := launch(deadline, func(ctx context.Context) error { _, e := r.Store.Stock(ctx); return e })
	s.jobs = append(s.jobs, j)
	if e := b.Observe(ctx, func() (bool, error) { st := r.SQL.Stats(); return st.InUse == 3 && st.WaitCount > baseline, nil }); e != nil {
		return e
	}
	var trace strings.Builder
	if e := pprof.Lookup("goroutine").WriteTo(&trace, 2); e != nil {
		return e
	}
	if e := os.WriteFile(filepath.Join(s.dir, "pool-wait-stack.txt"), []byte(trace.String()), 0600); e != nil {
		return e
	}
	if e := b.Assert(strings.Contains(trace.String(), "database/sql.(*DB).conn"), "栈应定位真实SQL连接等待"); e != nil {
		return e
	}
	if e := await(ctx, j.done, "pool waiter.Done"); e != nil {
		return e
	}
	if e := b.Assert(errors.Is(j.err, context.DeadlineExceeded), "pool真正deadline应保留原原因 actual=%v", j.err); e != nil {
		return e
	}
	j.consumed = true // 已由独立断言确认的预期故障；观察提前失败时不会执行此句。
	fmt.Fprintf(s.out, "F03_POOL saturated=3 observer_max=1 wait_count_delta=%d waiter_deadline=true held_owner_unchanged=true\n", r.SQL.Stats().WaitCount-baseline)
	return nil
}

// cacheRace 用实际AfterRead屏障冻结SQL的旧PENDING值，再让同订单consumer提交和
// 失效。bad从这一旧值UnsafeFill；fixed则让原Query继续Lua比较旧generation。
// Query返回给旧caller的PENDING是允许快照，下一请求缓存被旧值污染才是错误。
func cacheRace(ctx context.Context, s *session, a *b.App, client *http.Client) error {
	r := s.business
	for i, key := range []string{"cache-bad", "cache-fixed"} {
		g := newGate()
		s.gates = append(s.gates, g)
		consume := newGate()
		s.gates = append(s.gates, consume)
		// 原App先退出，新的consumer在真实Poll之前挡住；只更换App不更换业务实现。
		if e := a.Close(); e != nil {
			return e
		}
		current, e := s.app(b.Hooks{BeforeConsume: consume.wait})
		if e != nil {
			return e
		}
		a = current
		created := call(ctx, client, a.URL, "POST", "/orders", key)
		if created.err != nil || created.status != 201 {
			return fmt.Errorf("cache创建: status=%d err=%v", created.status, created.err)
		}
		var stale b.View
		j := launch(ctx, func(ctx context.Context) error {
			v, _, e := r.Cache.Query(ctx, r.Store, "alice", b.OrderID("alice", key), b.Hooks{AfterRead: func(ctx context.Context, v b.View) error { stale = v; return g.wait(ctx) }})
			_ = v
			return e
		})
		s.jobs = append(s.jobs, j)
		if e = await(ctx, g.entered, "cache真实SELECT后"); e != nil {
			return e
		}
		consume.open()
		if e = waitState(ctx, a, r, int64(4+i)); e != nil {
			return e
		}
		g.open()
		if e = await(ctx, j.done, "旧Query caller.Done"); e != nil {
			return e
		}
		if j.err != nil {
			return j.err
		}
		if e = b.Assert(stale.DeliveryStatus == "PENDING", "门内真实旧SQL值应PENDING"); e != nil {
			return e
		}
		if i == 0 {
			if e = r.Cache.UnsafeFillForLesson(ctx, stale); e != nil {
				return e
			}
			bad := call(ctx, client, a.URL, "GET", "/orders/"+stale.ID, "")
			if e = b.Assert(bad.err == nil && bad.body.CacheHit && bad.body.View != nil && bad.body.View.DeliveryStatus == "PENDING", "bad真实旧缓存应复现PENDING"); e != nil {
				return e
			}
			if e = r.Cache.Invalidate(ctx, "alice", stale.ID); e != nil {
				return e
			}
			fmt.Fprintln(s.out, "CACHE_BAD old_fill=PENDING authority=RECORDED bad_hit=true repaired_by_owned_invalidation=true")
		} else {
			fmt.Fprintln(s.out, "CACHE_FIXED old_read=PENDING old_fill_rejected=true generation_check=Lua")
		}
		if e = verifyAPI(ctx, client, a, key, "RECORDED"); e != nil {
			return e
		}
	}
	return nil
}
