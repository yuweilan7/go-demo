package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// Proxies 按 MySQL/Redis 两组独立namespace执行真实通信故障。代理仅占用本课
// 动态loopback listener/socket，不动共享服务、配置、ACL或SSH隧道。
func Proxies(ctx context.Context, out io.Writer) error {
	if e := commitLoss(ctx, out); e != nil {
		return fmt.Errorf("commit wire loss: %w", e)
	}
	if e := redisLoss(ctx, out); e != nil {
		return e
	}
	return hookCancellation(ctx, out)
}

// hookCancellation补充Go错误传播与OS强杀的区别：真实ACK/消费DB提交后，本课
// 生命周期ctx才被cancel，hook实际读取Done/Err并返回。取消不能吞UNKNOWN，
// 后面同event重投/去重修复真实效果。这里不把hook取消称为网络回复丢失。
func hookCancellation(ctx context.Context, out io.Writer) error {
	return withSession(ctx, out, func(s *session) (err error) {
		key := "hook-cancel"
		if e := s.authorize(ctx, key); e != nil {
			return e
		}
		if _, e := s.original.Store.Create(ctx, command(key, "hook-original"), b.Hooks{}); e != nil {
			return e
		}
		life, cancel := context.WithCancel(ctx)
		_, cause := s.original.Broker.RelayOne(life, s.original.Store, b.Hooks{AfterRelayACK: func(ctx context.Context, _ b.Event) error { cancel(); <-ctx.Done(); return ctx.Err() }})
		cancel()
		if e := b.Assert(errors.Is(cause, b.ErrUnknown) && errors.Is(cause, context.Canceled) && !errors.Is(cause, b.ErrCleanup), "真实ACK后的ctx取消必须保留UNKNOWN/Canceled: %v", cause); e != nil {
			return e
		}
		if e := audit(ctx, s.original, 1, 0, 0, 1, -1); e != nil {
			return e
		}
		fmt.Fprintf(out, "F07_ACK_CANCEL actual_ack=true category=%s original_error=%v sent=0\n", classify(cause), cause)
		if _, e := s.original.Broker.RelayOne(ctx, s.original.Store, b.Hooks{}); e != nil {
			return e
		}
		if e := s.fresh(ctx, s.cfg, "hook-consumer"); e != nil {
			return e
		}
		c, e := b.NewKafkaClient(s.cfg, s.original.Namespace+"-hook-cancel", b.ConsumerOptions(s.original.Broker.Topic, s.original.Broker.Group)...)
		if e != nil {
			return e
		}
		s.clients = append(s.clients, c)
		defer func() { err = errors.Join(err, c.Close()) }()
		record, e := b.PollOne(ctx, c)
		if e != nil {
			return e
		}
		life, cancel = context.WithCancel(ctx)
		cause = s.business.Broker.ProcessRecord(life, c, s.business.Store, s.business.Cache, record, b.Hooks{AfterConsumeCommit: func(ctx context.Context, _ b.Event) error { cancel(); <-ctx.Done(); return ctx.Err() }})
		cancel()
		if e = b.Assert(errors.Is(cause, b.ErrUnknown) && errors.Is(cause, context.Canceled) && !errors.Is(cause, b.ErrCleanup), "真实消费Commit后的取消必须保留UNKNOWN/Canceled: %v", cause); e != nil {
			return e
		}
		if e = audit(ctx, s.original, 1, 1, 1, 2, -1); e != nil {
			return e
		}
		fmt.Fprintf(out, "F07_CONSUME_CANCEL actual_sql_commit=true category=%s original_error=%v next=-1\n", classify(cause), cause)
		if e = s.business.Broker.ProcessRecord(ctx, c, s.business.Store, s.business.Cache, record, b.Hooks{}); e != nil {
			return e
		}
		record, e = b.PollOne(ctx, c)
		if e != nil {
			return e
		}
		if e = s.business.Broker.ProcessRecord(ctx, c, s.business.Store, s.business.Cache, record, b.Hooks{}); e != nil {
			return e
		}
		if e = c.Close(); e != nil {
			return e
		}
		a, e := s.app(b.Hooks{})
		if e != nil {
			return e
		}
		client, t := httpClient()
		defer t.CloseIdleConnections()
		if e = verifyAPI(ctx, client, a, key, "RECORDED"); e != nil {
			return e
		}
		if e = audit(ctx, s.original, 1, 1, 1, 2, 2); e != nil {
			return e
		}
		fmt.Fprintln(out, "F07_RECOVERED root_cancellation_not_normalized=true same_event_effects=1 next=2")
		return a.Close()
	})
}

// commitLoss 在真实COM_QUERY COMMIT发出后，实际收到服务端OK但不交给driver。
// 调用者因此得到UNKNOWN+原invalid connection，父观察池看到order/outbox已经
// 提交；恢复只开fresh并继续同原key的relay/consumer，不能新key重扣库存。
func commitLoss(ctx context.Context, out io.Writer) error {
	return withSession(ctx, out, func(s *session) error {
		if e := s.authorize(ctx, "wire-commit"); e != nil {
			return e
		}
		p, e := newWire("mysql", s.cfg.MySQLAddress, out)
		if e != nil {
			return e
		}
		s.proxies = append(s.proxies, p)
		proxyCfg := s.cfg
		proxyCfg.MySQLAddress = p.address // Config值副本，只改变这次client端点。
		if e = s.fresh(ctx, proxyCfg, "mysql-proxy-client"); e != nil {
			return e
		}
		p.arm("COMMIT")
		j := launch(ctx, func(ctx context.Context) error {
			_, e := s.business.Store.Create(ctx, command("wire-commit", "wire-original"), b.Hooks{})
			return e
		})
		s.jobs = append(s.jobs, j)
		if e = await(ctx, p.hit, "实际COMMIT OK回复捕获"); e != nil {
			return e
		}
		if e = audit(ctx, s.original, 1, 0, 0, 0, -1); e != nil {
			return e
		}
		if e = p.close(); e != nil {
			return e
		}
		if e = await(ctx, j.done, "COMMIT错误caller.Done"); e != nil {
			return e
		}
		if e = b.Assert(errors.Is(j.err, b.ErrUnknown) && !errors.Is(j.err, b.ErrCleanup), "COMMIT wire丢失保留UNKNOWN和原driver原因 actual=%v", j.err); e != nil {
			return e
		}
		j.consumed = true
		fmt.Fprintf(out, "F05_COMMIT_UNKNOWN category=%s original_error=%v authority_order=1 stock=9\n", classify(j.err), j.err)
		if e = s.closeBusiness(); e != nil {
			return e
		}
		if e = s.fresh(ctx, s.cfg, "mysql-restored"); e != nil {
			return e
		}
		a, e := s.app(b.Hooks{})
		if e != nil {
			return e
		}
		if e = waitState(ctx, a, s.business, 1); e != nil {
			return e
		}
		client, t := httpClient()
		defer t.CloseIdleConnections()
		if e = verifyAPI(ctx, client, a, "wire-commit", "RECORDED"); e != nil {
			return e
		}
		if e = audit(ctx, s.original, 1, 1, 1, 1, 1); e != nil {
			return e
		}
		fmt.Fprintln(out, "F05_RECOVERED original_key=true order=1 effects=1 next=1 fresh_client=true")
		return a.Close()
	})
}

// redisLoss 先在真实GET回复处阻塞使读ctx超时；随后真实consumer的Apply已经提交，
// EVAL失效实际执行但回复被代理截断，offset仍未提交。两个故障都由协议reply门
// 定位，mock返回错误不能替代这些请求。fresh恢复继续同group重投、SQL去重和失效。
func redisLoss(ctx context.Context, out io.Writer) error {
	return withSession(ctx, out, func(s *session) (err error) {
		key := "redis-loss"
		if e := s.authorize(ctx, key); e != nil {
			return e
		}
		if _, e := s.original.Store.Create(ctx, command(key, "redis-original"), b.Hooks{}); e != nil {
			return e
		}
		if _, e := s.original.Broker.RelayOne(ctx, s.original.Store, b.Hooks{}); e != nil {
			return e
		}
		if _, _, e := s.original.Cache.Query(ctx, s.original.Store, "alice", b.OrderID("alice", key), b.Hooks{}); e != nil {
			return e
		}
		// GET慢回复先单独连接；OpenResources握手INFO成功后才arm，避免把构造失败
		// 冒称业务依赖慢。500ms期限由caller所有，release门由session所有。
		p, e := newWire("redis", s.cfg.RedisAddress, out)
		if e != nil {
			return e
		}
		s.proxies = append(s.proxies, p)
		cfg := s.cfg
		cfg.RedisAddress = p.address
		if e = s.fresh(ctx, cfg, "slow-redis"); e != nil {
			return e
		}
		p.arm("GET")
		deadline, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		j := launch(deadline, func(ctx context.Context) error {
			_, _, e := s.business.Cache.Query(ctx, s.business.Store, "alice", b.OrderID("alice", key), b.Hooks{})
			return e
		})
		s.jobs = append(s.jobs, j)
		if e = await(ctx, p.hit, "真实GET服务端reply已到代理"); e != nil {
			return e
		}
		if e = await(ctx, j.done, "慢Redis读取caller.Done"); e != nil {
			return e
		}
		if e = b.Assert(j.err != nil, "GET慢回复必须真实失败"); e != nil {
			return e
		}
		j.consumed = true
		fmt.Fprintf(out, "F06_REDIS_SLOW command=GET actual_server_reply=true client_error=%v effects=0\n", j.err)
		if e = p.close(); e != nil {
			return e
		}
		if e = s.closeBusiness(); e != nil {
			return e
		}
		// 新代理新连接，不复用已断socket。命令EVAL处截断失效回执，记录SQL效果
		// 与Kafka offset分开的真实事实。Kafka client立即有guard，任何失败仍join/Close。
		p, e = newWire("redis", s.cfg.RedisAddress, out)
		if e != nil {
			return e
		}
		s.proxies = append(s.proxies, p)
		cfg.RedisAddress = p.address
		if e = s.fresh(ctx, cfg, "unavailable-redis"); e != nil {
			return e
		}
		p.arm("EVAL")
		c, e := b.NewKafkaClient(s.cfg, s.original.Namespace+"-redis-fault", b.ConsumerOptions(s.original.Broker.Topic, s.original.Broker.Group)...)
		if e != nil {
			return e
		}
		s.clients = append(s.clients, c)
		// 该同步函数作用域独占consumer。work先join才Close，guard绝不会通过原
		// 工作ctx取消直接跳过最终等待。错误分类由ProcessRecord保留UNKNOWN。
		var worker *job
		defer func() {
			if worker != nil {
				worker.cancel()
				if e := worker.finish(); e != nil {
					err = errors.Join(err, e)
					return
				}
				if !worker.consumed {
					err = errors.Join(err, worker.err)
				}
			}
			err = errors.Join(err, c.Close())
		}()
		var record *kgo.Record
		record, e = b.PollOne(ctx, c)
		if e != nil {
			return e
		}
		worker = launch(ctx, func(ctx context.Context) error {
			return s.business.Broker.ProcessRecord(ctx, c, s.business.Store, s.business.Cache, record, b.Hooks{})
		})
		s.jobs = append(s.jobs, worker)
		if e = await(ctx, p.hit, "消费失效EVAL服务端reply已到代理"); e != nil {
			return e
		}
		if e = audit(ctx, s.original, 1, 1, 1, 1, -1); e != nil {
			return e
		}
		exists, generation, _, e := s.original.Cache.Inspect(ctx, "alice", b.OrderID("alice", key))
		if e != nil {
			return e
		}
		if e = b.Assert(exists == 0 && generation == "2", "EVAL实际失效已执行但回复丢失 exists=%d generation=%s", exists, generation); e != nil {
			return e
		}
		if e = p.close(); e != nil {
			return e
		}
		if e = await(ctx, worker.done, "Redis故障consumer worker.Done"); e != nil {
			return e
		}
		if e = b.Assert(errors.Is(worker.err, b.ErrUnknown), "消费SQL后失效错误保留UNKNOWN actual=%v", worker.err); e != nil {
			return e
		}
		worker.consumed = true
		fmt.Fprintf(out, "F06_REDIS_UNAVAILABLE category=%s original_error=%v sql_effects=1 next=-1 cache_deleted=true\n", classify(worker.err), worker.err)
		if e = c.Close(); e != nil {
			return e
		}
		if e = s.closeBusiness(); e != nil {
			return e
		}
		if e = s.fresh(ctx, s.cfg, "redis-restored"); e != nil {
			return e
		}
		a, e := s.app(b.Hooks{})
		if e != nil {
			return e
		}
		if e = waitState(ctx, a, s.business, 1); e != nil {
			return e
		}
		client, t := httpClient()
		defer t.CloseIdleConnections()
		if e = verifyAPI(ctx, client, a, key, "RECORDED"); e != nil {
			return e
		}
		if e = audit(ctx, s.original, 1, 1, 1, 1, 1); e != nil {
			return e
		}
		fmt.Fprintln(out, "F06_RECOVERED same_event_redelivered=true duplicate_sql_effects=0 invalidation_repeated=true next=1")
		return a.Close()
	})
}
