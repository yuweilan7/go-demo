package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// RunRecovery只验证导出借用接口，不实施M38强杀矩阵。它调用同一Service/Store/Cache，
// 原owner保持账本；原App与client实际关闭后才开fresh，整个时刻仅一个MaxOpen4池。
// ctx给观察预算，所有取得对象立即登记最终guard；出错/panic也精确清理原owner数据。
func RunRecovery(ctx context.Context, out io.Writer) (err error) {
	cfg, e := b.LoadConfig()
	if e != nil {
		return e
	}
	ns, e := b.NewNamespace()
	if e != nil {
		return e
	}
	ownerPath := filepath.Join(".local", ns, "resources.json")
	owner, e := b.OpenResources(ctx, cfg, ns, ownerPath, out)
	if e != nil {
		return e
	}
	var fresh *b.Resources
	var apps []*b.App
	oldGate, newGate := NewGate(), NewGate()
	originalClosed := false
	defer func() {
		for _, a := range apps {
			a.Stop()
		}
		oldGate.Release()
		newGate.Release()
		safe := true
		for i := len(apps) - 1; i >= 0; i-- {
			err = errors.Join(err, apps[i].Close())
			safe = safe && apps[i].SafeToRelease()
		}
		// 原client已关而fresh构造失败时，用一次独立预算重新取得清理连接。没有
		// Provision，没有接管名单；原owner仍调用自己的CleanupOwnedVia。
		if safe && originalClosed && fresh == nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			var acquire error
			fresh, acquire = b.OpenResources(cleanup, cfg, ns, filepath.Join(".local", ns, "cleanup-client.json"), out)
			cancel()
			err = errors.Join(err, b.CleanupCause(acquire))
		}
		if safe {
			if fresh != nil {
				err = errors.Join(err, fresh.CleanupOwned(), owner.CleanupOwnedVia(fresh))
				cleanup, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				err = errors.Join(err, b.CleanupCause(fresh.VerifyGone(cleanup, owner.Manifest())))
				cancel()
			} else {
				err = errors.Join(err, owner.CleanupOwned())
			}
		} else {
			err = errors.Join(err, b.CleanupCause(errors.New("恢复用例尚有借用者未Done")))
		}
		if fresh != nil && safe {
			err = errors.Join(err, fresh.Close())
		}
		if safe {
			err = errors.Join(err, owner.Close())
		}
		if p := recover(); p != nil {
			cause, ok := p.(error)
			if !ok {
				cause = fmt.Errorf("恢复实验panic: %v", p)
			}
			err = errors.Join(err, cause)
		}
	}()
	if e = owner.Provision(ctx); e != nil {
		return e
	}
	old, e := b.NewApp(owner, b.Hooks{BeforeConsume: oldGate.Wait})
	if e != nil {
		return e
	}
	apps = append(apps, old)
	if e = old.Start(); e != nil {
		return e
	}
	if e = wait(ctx, oldGate.Entered, "old consumer before poll"); e != nil {
		return e
	}
	transport := &http.Transport{MaxConnsPerHost: 2, MaxIdleConnsPerHost: 2}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	first := Call(ctx, client, old.URL, "POST", "/orders", "alice", "order-main", `{"sku":"sku-m37","quantity":2}`)
	if e = b.Assert(first.Err == nil && first.Status == 201 && first.Body.Order != nil && first.Body.Outcome == "COMMITTED", "restore original order"); e != nil {
		return e
	}
	order := *first.Body.Order
	initial := Call(ctx, client, old.URL, "GET", "/orders/"+order.ID, "alice", "", "")
	if e = b.Assert(initial.Err == nil && initial.Status == 200 && initial.Body.View != nil && initial.Body.View.DeliveryStatus == "PENDING", "restore original pending view"); e != nil {
		return e
	}
	if e = b.Observe(ctx, func() (bool, error) {
		var n int
		e := owner.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+owner.Store.Table("outbox")+"` WHERE sent=TRUE").Scan(&n)
		return n == 1, e
	}); e != nil {
		return e
	}
	if e = Audit(ctx, owner, 1, 2, 8, 1, 0, 0); e != nil {
		return e
	}
	// 读真实登记文件，冻结一次只授予原订单两键的能力。之后另造自己登记的精确
	// Redis fixture，故意不列入这次借用能力，用于证明未知已有键不会被收编。
	grant, e := b.ReadManifest(ownerPath)
	if e != nil {
		return e
	}
	if e = b.Assert(len(grant.Keys) == 2, "original cache grant keys=2"); e != nil {
		return e
	}
	unknownID := b.OrderID("alice", "outside-grant")
	if e = owner.Cache.Invalidate(ctx, "alice", unknownID); e != nil {
		return e
	}
	if e = owner.Cache.UnsafeFillForLesson(ctx, b.View{Order: b.Order{ID: unknownID, Owner: "alice"}, DeliveryStatus: "PENDING"}); e != nil {
		return e
	}
	if e = old.Close(); e != nil {
		return e
	}
	if e = b.Assert(old.SafeToRelease(), "old App finalDone"); e != nil {
		return e
	}
	originalClosed = true // Close即使返回清理错误，后续也须用fresh连接尝试清理原账本。
	if e = owner.Close(); e != nil {
		return e
	}
	fmt.Fprintf(owner.Out, "RESTORE_OLD_CLOSED namespace=%s sql_open=%d\n", ns, owner.SQL.Stats().OpenConnections)
	fresh, e = b.OpenResources(ctx, cfg, ns, filepath.Join(".local", ns, "borrower-resources.json"), out)
	if e != nil {
		return e
	}
	wrong := grant
	wrong.Namespace = "m37_0000000000000000"
	if e = b.Assert(errors.Is(fresh.BorrowCacheKeys(wrong), b.ErrInvalid), "cross namespace grant refused"); e != nil {
		return e
	}
	malformed := grant
	malformed.Keys = append(append([]string(nil), grant.Keys...), "golearn:m37:invalid:owner=alice")
	if e = b.Assert(errors.Is(fresh.BorrowCacheKeys(malformed), b.ErrInvalid), "malformed grant refused"); e != nil {
		return e
	}
	if e = fresh.BorrowCacheKeys(grant); e != nil {
		return e
	}
	if _, _, e = fresh.Cache.Query(ctx, fresh.Store, "alice", unknownID, b.Hooks{}); !errors.Is(e, b.ErrOwnership) {
		return fmt.Errorf("unknown existing key must refuse: %w", e)
	}
	if e = b.Assert(len(fresh.Manifest().Keys) == 0, "borrower must not register unknown existing key"); e != nil {
		return e
	}
	// 原owner这次明确追加授权自己的完整四键，检查先前拒绝没有删除fixture。
	// 这也是caller授权与名字格式校验的区别：能力来自可信创建者，不来自HTTP输入。
	if e = fresh.BorrowCacheKeys(owner.Manifest()); e != nil {
		return e
	}
	exists, _, ttl, e := fresh.Cache.Inspect(ctx, "alice", unknownID)
	if e != nil {
		return e
	}
	if e = b.Assert(exists == 1 && ttl > 0, "refusal preserved existing owner fixture"); e != nil {
		return e
	}
	restored, e := b.NewApp(fresh, b.Hooks{BeforeConsume: newGate.Wait})
	if e != nil {
		return e
	}
	apps = append(apps, restored)
	if e = restored.Start(); e != nil {
		return e
	}
	if e = wait(ctx, newGate.Entered, "fresh consumer before poll"); e != nil {
		return e
	}
	retry := Call(ctx, client, restored.URL, "POST", "/orders", "alice", "order-main", `{"sku":"sku-m37","quantity":2}`)
	if e = b.Assert(retry.Err == nil && retry.Status == 201 && retry.Body.Order != nil && *retry.Body.Order == order && retry.Body.RequestID != first.Body.RequestID, "fresh original key must return same order with new requestID"); e != nil {
		return e
	}
	cached := Call(ctx, client, restored.URL, "GET", "/orders/"+order.ID, "alice", "", "")
	if e = b.Assert(cached.Err == nil && cached.Status == 200 && cached.Body.CacheHit && cached.Body.View != nil && cached.Body.View.DeliveryStatus == "PENDING", "fresh API borrowed existing cache"); e != nil {
		return e
	}
	isolated := Call(ctx, client, restored.URL, "GET", "/requests/order-main", "bob", "", "")
	if e = b.Assert(isolated.Err == nil && isolated.Status == 404 && isolated.Body.Code == "NOT_FOUND", "fresh authority query owner isolation"); e != nil {
		return e
	}
	newGate.Release()
	if e = WaitOffset(ctx, restored, fresh, 1); e != nil {
		return e
	}
	confirmed := Call(ctx, client, restored.URL, "GET", "/orders/"+order.ID, "alice", "", "")
	if e = b.Assert(confirmed.Err == nil && confirmed.Status == 200 && !confirmed.Body.CacheHit && confirmed.Body.View != nil && confirmed.Body.View.DeliveryStatus == "RECORDED", "fresh consumer invalidates borrowed cache"); e != nil {
		return e
	}
	if e = Audit(ctx, fresh, 1, 2, 8, 1, 1, 1); e != nil {
		return e
	}
	if e = restored.Close(); e != nil {
		return e
	}
	if e = b.Assert(restored.SafeToRelease(), "fresh App finalDone"); e != nil {
		return e
	}
	d := fresh.Manifest()
	if e = b.Assert(len(d.Tables)+len(d.Keys)+len(d.Topics)+len(d.Groups) == 0, "borrower cleanup registry must stay empty"); e != nil {
		return e
	}
	// fresh仅清理自己的空账本，原对象必须还在；原owner最后才通过fresh连接清理。
	if e = fresh.CleanupOwned(); e != nil {
		return e
	}
	if e = Audit(ctx, fresh, 1, 2, 8, 1, 1, 1); e != nil {
		return e
	}
	fmt.Fprintf(fresh.Out, "RESTORE_PROOF namespace=%s original_key_same_order=true cache_borrowed=true unknown_refused=true consumer_next=1 owner_retained=true\n", ns)
	return nil
}
