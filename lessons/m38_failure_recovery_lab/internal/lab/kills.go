package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// Kills 按三个真实提交窗口顺序运行，每次新namespace和原owner账本。binary只
// 编译一次；每个session最终精确删除本run对象，不会把恢复前状态先删干净。
func Kills(ctx context.Context, out io.Writer) error {
	binary, e := childBinary(ctx)
	if e != nil {
		return e
	}
	for _, mode := range []string{"commit", "relay", "consume"} {
		if e = killTrial(ctx, out, binary, mode); e != nil {
			return fmt.Errorf("%s kill trial: %w", mode, e)
		}
	}
	return nil
}

// killTrial 父只读观察，child唯一写者。阶段报告经过identity校验后，父再查权威
// SQL+raw broker+持久offset确定窗口，然后真实Kill/Wait，最后才启动fresh App。
func killTrial(ctx context.Context, out io.Writer, binary, mode string) error {
	return withSession(ctx, out, func(s *session) error {
		key := "kill-" + mode
		if e := s.authorize(ctx, key); e != nil {
			return e
		}
		if mode != "commit" {
			// relay/consume的原单由同一个公开Store创建，父在启动child前完成写入。
			if _, e := s.original.Store.Create(ctx, command(key, "parent-seed"), b.Hooks{}); e != nil {
				return e
			}
			if mode == "consume" {
				if _, e := s.original.Broker.RelayOne(ctx, s.original.Store, b.Hooks{}); e != nil {
					return e
				}
			}
			if _, _, e := s.original.Cache.Query(ctx, s.original.Store, "alice", b.OrderID("alice", key), b.Hooks{}); e != nil {
				return e
			}
		}
		p, e := s.startProcess(binary, mode, key)
		if e != nil {
			return e
		}
		stage := map[string]string{"commit": "ORDER_COMMITTED_BEFORE_PUBLISH", "relay": "KAFKA_ACK_BEFORE_SENT", "consume": "CONSUME_DB_COMMIT_BEFORE_OFFSET"}[mode]
		if e = p.atStage(ctx, stage); e != nil {
			return e
		}
		sent, effects, messages := 0, 0, 0
		if mode == "relay" {
			messages = 1
		}
		if mode == "consume" {
			sent, effects, messages = 1, 1, 1
		}
		if e = audit(ctx, s.original, 1, sent, effects, messages, -1); e != nil {
			return e
		}
		// consume已提交receipt但缓存仍PENDING，因为hook在Invalidate之前。当前
		// 这个可观测暂态正是重投也须再次失效的理由，不能把缓存命中当权威事实。
		if mode == "consume" {
			v, hit, e := s.original.Cache.Query(ctx, s.original.Store, "alice", b.OrderID("alice", key), b.Hooks{})
			if e != nil {
				return e
			}
			if e = b.Assert(hit && v.DeliveryStatus == "PENDING", "consume提交后offset前应保留真实PENDING缓存"); e != nil {
				return e
			}
		}
		if e = p.kill(); e != nil {
			return e
		}
		// child资源账本可以为空；它借用原能力，绝不取得清理权。原owner/client仍
		// 保持自己的精确名单，未知对象不能因合法namespace而被收编。
		childManifestPath := filepath.Join(s.dir, mode+"-child.json")
		if _, e = os.Stat(childManifestPath); e == nil {
			d, e := b.ReadManifest(childManifestPath)
			if e != nil {
				return e
			}
			if e = b.Assert(len(d.Keys)+len(d.Tables)+len(d.Groups)+len(d.Topics) == 0, "child borrowed registry应为空"); e != nil {
				return e
			}
		}
		if e = s.fresh(ctx, s.cfg, "restore-"+mode); e != nil {
			return e
		}
		hold := newGate()
		s.gates = append(s.gates, hold)
		a, e := s.app(b.Hooks{BeforeConsume: hold.wait})
		if e != nil {
			return e
		}
		if e = await(ctx, hold.entered, "恢复consumer未poll"); e != nil {
			return e
		}
		client, t := httpClient()
		defer t.CloseIdleConnections()
		if e = verifyAPI(ctx, client, a, key, "PENDING"); e != nil {
			return e
		}
		// 原key的显式受控请求只用于验证幂等：整个原进程已退出且权威单已核验。
		// 不把新key重下单当恢复，也不吞UNKNOWN/cleanup。请求返回创建原单字段。
		reused := call(ctx, client, a.URL, "POST", "/orders", key)
		if e = b.Assert(reused.err == nil && reused.status == 201 && reused.body.Order != nil && reused.body.Order.ID == b.OrderID("alice", key), "恢复同原key原订单，actual=%v", reused.err); e != nil {
			return e
		}
		if mode == "commit" || mode == "relay" {
			if e = b.Observe(ctx, func() (bool, error) {
				var n int
				e := s.original.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+s.original.Store.Table("outbox")+"` WHERE sent=TRUE").Scan(&n)
				return n == 1, e
			}); e != nil {
				return e
			}
		}
		hold.open()
		want := int64(1)
		if mode == "relay" {
			want = 2
		}
		if e = waitState(ctx, a, s.business, want); e != nil {
			return e
		}
		if e = verifyAPI(ctx, client, a, key, "RECORDED"); e != nil {
			return e
		}
		if e = audit(ctx, s.original, 1, 1, 1, int(want), want); e != nil {
			return e
		}
		// inbox必须保留第一次成功处理的offset0，即使恢复收到offset1重复事件。
		var first int64
		if e = s.original.SQL.QueryRowContext(ctx, "SELECT first_offset FROM `"+s.original.Store.Table("inbox")+"`").Scan(&first); e != nil {
			return e
		}
		if e = b.Assert(first == 0, "持久首次offset应为0，actual=%d", first); e != nil {
			return e
		}
		if e = a.Close(); e != nil {
			return e
		}
		if e = b.Assert(a.SafeToRelease(), "恢复App最终Done"); e != nil {
			return e
		}
		fmt.Fprintf(out, "KILL_RECOVERED mode=%s original_key=%s orders=1 stock=9 effects=1 sent=1 next=%d old_pid_joined=true cache=RECORDED\n", mode, key, want)
		return nil
	})
}

// classify 保留多重原因的检查便于辅助测试。UNKNOWN和cleanup是业务严重状态，
// 即使错误树还含1062/ctx取消，也不能凭原单存在把整个失败归一化成成功。
func classify(err error) string {
	switch {
	case errors.Is(err, b.ErrCleanup):
		return "CLEANUP_FAILED"
	case errors.Is(err, b.ErrUnknown):
		return "UNKNOWN"
	default:
		return "KNOWN"
	}
}
