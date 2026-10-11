package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"time"

	inv "go-demo/lessons/m31_grpc_protobuf/gen/inventoryv1"
	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var errDemo = errors.New("E05 deliberate error after SQL writes")
var errEarly = errors.New("E06 deliberate observer early exit")

// Run是课程总owner。入口给120s业务预算，退出defer独立关闭每个App/精确数据/池。
// 创建后立刻注册guard，后续任意断言error或panic也会先放所有自有门并完成收尾。
// 不调用os.Exit，避免跳过defer；命令进程退出只在Run已经返回之后。
func Run(ctx context.Context, out io.Writer) (err error) {
	cfg, e := b.LoadConfig()
	if e != nil {
		return e
	}
	ns, e := b.NewNamespace()
	if e != nil {
		return e
	}
	r, e := b.OpenResources(ctx, cfg, ns, filepath.Join(".local", ns, "resources.json"), out)
	if e != nil {
		return e
	}
	consumerGate, readGate, beforeGate, lostGate, earlyGate := NewGate(), NewGate(), NewGate(), NewGate(), NewGate()
	consumeCommitGate := NewGate()
	var apps []*b.App
	defer func() {
		// 先同步取消服务端根ctx，才放实验门。只cancel HTTP caller时，网络把取消
		// 传播到服务端需要时间；门先放开可能让未提交事务抢先Commit。
		for _, app := range apps {
			app.Stop()
		}
		consumerGate.Release()
		readGate.Release()
		beforeGate.Release()
		lostGate.Release()
		earlyGate.Release()
		consumeCommitGate.Release()
		safe := true
		for i := len(apps) - 1; i >= 0; i-- {
			err = errors.Join(err, apps[i].Close())
			safe = safe && apps[i].SafeToRelease()
		}
		if safe {
			err = errors.Join(err, r.CleanupOwned(), r.Close())
		} else {
			err = errors.Join(err, b.CleanupCause(errors.New("仍有使用者未join，拒绝关闭仓储")))
		}
		if p := recover(); p != nil {
			cause, ok := p.(error)
			if !ok {
				cause = fmt.Errorf("课程panic: %v", p)
			}
			err = errors.Join(err, cause)
		}
	}()
	if e = r.Provision(ctx); e != nil {
		return e
	}
	var holdRead atomic.Bool
	hooks := b.Hooks{
		BeforeConsume: consumerGate.Wait,
		AfterRead: func(ctx context.Context, v b.View) error {
			if v.ID == b.OrderID("alice", "order-main") && holdRead.Load() {
				return readGate.Wait(ctx)
			}
			return nil
		},
		AfterOrderWrites: func(ctx context.Context, o b.Order) error {
			switch o.Key {
			case "rollback":
				return errDemo
			case "panic":
				panic(errDemo)
			case "cancel-before":
				return beforeGate.Wait(ctx)
			case "early-exit":
				return earlyGate.Wait(ctx)
			}
			return nil
		},
		AfterOrderCommit: func(ctx context.Context, o b.Order) error {
			if o.Key == "lost-response" {
				return lostGate.Wait(ctx)
			}
			return nil
		},
		AfterConsumeCommit: func(ctx context.Context, event b.Event) error {
			if event.Order.ID == b.OrderID("alice", "order-main") {
				return consumeCommitGate.Wait(ctx)
			}
			return nil
		},
	}
	a, e := b.NewApp(r, hooks)
	if e != nil {
		return e
	}
	apps = append(apps, a)
	if e = a.Start(); e != nil {
		return e
	}
	if e = wait(ctx, consumerGate.Entered, "consumer before poll"); e != nil {
		return e
	}
	transport := &http.Transport{MaxConnsPerHost: 4, MaxIdleConnsPerHost: 4, IdleConnTimeout: 2 * time.Second}
	client := &http.Client{Transport: transport}
	defer transport.CloseIdleConnections()
	call := func(ctx context.Context, method, path, owner, key, body string) result {
		return Call(ctx, client, a.URL, method, path, owner, key, body)
	}

	// E01先验完整响应，再独立查DB。这时consumer固定在门上，查询应为PENDING。
	fmt.Fprintln(r.Out, "E01 CREATE + advisory RPC + authoritative Tx")
	first := call(ctx, "POST", "/orders", "alice", "order-main", `{"sku":"sku-m37","quantity":2}`)
	if e = b.Assert(first.Err == nil && first.Status == 201 && first.Body.Code == "OK" && first.Body.Outcome == "COMMITTED" && first.Body.Order != nil, "create full envelope: %+v", first); e != nil {
		return e
	}
	o := *first.Body.Order
	if e = b.Assert(o.ID == b.OrderID("alice", "order-main") && o.Owner == "alice" && o.SKU == b.SKU && o.Quantity == 2 && o.AmountMinor == 598 && o.Currency == "CNY" && o.Status == "ACCEPTED" && o.Version == 1 && o.RequestID == first.Body.RequestID, "order DTO content"); e != nil {
		return e
	}
	if e = Audit(ctx, r, 1, 2, 8, 1, 0, 0); e != nil {
		return e
	}
	q := call(ctx, "GET", "/orders/"+o.ID, "alice", "", "")
	if e = b.Assert(q.Err == nil && q.Status == 200 && q.Body.View != nil && q.Body.View.DeliveryStatus == "PENDING" && !q.Body.CacheHit, "first pending query"); e != nil {
		return e
	}
	q = call(ctx, "GET", "/orders/"+o.ID, "alice", "", "")
	if e = b.Assert(q.Err == nil && q.Body.CacheHit && q.Body.View.AmountMinor == 598, "cached pending DTO"); e != nil {
		return e
	}
	exists, gen, ttl, e := r.Cache.Inspect(ctx, "alice", o.ID)
	if e != nil {
		return e
	}
	if e = b.Assert(exists == 1 && gen == "0" && ttl > 0 && ttl <= 30000, "Redis pending key/TTL %d %s %d", exists, gen, ttl); e != nil {
		return e
	}
	next, e := r.Broker.Offset(ctx)
	if e != nil {
		return e
	}
	if e = b.Assert(next == -1, "consumer gate before poll must have no committed offset"); e != nil {
		return e
	}
	rpc, e := a.InventoryClient().Inspect(ctx, &inv.InspectRequest{Sku: b.SKU})
	if e != nil {
		return e
	}
	if e = b.Assert(rpc.Sku == b.SKU && rpc.Available == 8, "RPC real stock content"); e != nil {
		return e
	}
	_, e = a.InventoryClient().Reserve(ctx, &inv.ReserveRequest{Sku: b.SKU, Quantity: 1})
	if status.Code(e) != codes.Unimplemented {
		return fmt.Errorf("只读RPC Reserve code=%s", status.Code(e))
	}
	if e = Audit(ctx, r, 1, 2, 8, 1, 0, 0); e != nil {
		return e
	}

	// E02重复HTTP通过同一个真实服务。6个有限caller各有最终Done及创建即登记guard。
	fmt.Fprintln(r.Out, "E02 IDEMPOTENCY + payload conflict + ownership")
	if e = Repeats(ctx, client, a.URL, o); e != nil {
		return e
	}
	badCases := []struct {
		method, path, owner, key, body string
		code                           int
		word                           string
	}{
		{"POST", "/orders", "alice", "order-main", `{"sku":"sku-m37","quantity":3}`, 409, "CONFLICT"},
		{"POST", "/orders", "alice", "bad-zero", `{"sku":"sku-m37","quantity":0}`, 422, "ORDER_INVALID"},
		{"POST", "/orders", "alice", "bad-owner", `{"sku":"sku-m37","quantity":1,"owner":"bob"}`, 400, "INPUT"},
		{"POST", "/orders", "alice", "bad-duplicate", `{"sku":"sku-m37","quantity":1,"quantity":2}`, 400, "INPUT"},
		{"POST", "/orders", "", "no-auth", `{"sku":"sku-m37","quantity":1}`, 401, "AUTH"},
		{"GET", "/orders/" + o.ID, "bob", "", "", 404, "NOT_FOUND"},
		{"GET", "/requests/order-main", "bob", "", "", 404, "NOT_FOUND"},
	}
	for _, tc := range badCases {
		got := call(ctx, tc.method, tc.path, tc.owner, tc.key, tc.body)
		if e = b.Assert(got.Err == nil && got.Status == tc.code && got.Body.Code == tc.word, "negative status/body %d/%s want %d/%s", got.Status, got.Body.Code, tc.code, tc.word); e != nil {
			return e
		}
	}
	if e = Audit(ctx, r, 1, 2, 8, 1, 0, 0); e != nil {
		return e
	}

	// E03旧SQL视图在门上；consumer提交receipt并失效后，旧请求才能继续Lua回填。
	fmt.Fprintln(r.Out, "E03 STALE FILL FENCE + consumer effect before offset")
	if e = r.Cache.Invalidate(ctx, "alice", o.ID); e != nil {
		return e
	}
	holdRead.Store(true)
	stale := StartCaller(ctx, func(ctx context.Context) result { return call(ctx, "GET", "/orders/"+o.ID, "alice", "", "") }, nil)
	defer func() { _, clean := stale.Finish(nil); err = errors.Join(err, clean) }()
	if e = wait(ctx, readGate.Entered, "old SQL PENDING read"); e != nil {
		return e
	}
	// 另一个普通reader在消费之前填入PENDING，用同一Query实现且不带观察门。
	// 这样commit门上的比较确实是SQL已RECORDED、实际Redis仍PENDING，不仅画示意图。
	if v, _, e := r.Cache.Query(ctx, r.Store, "alice", o.ID, b.Hooks{}); e != nil || v.DeliveryStatus != "PENDING" {
		return errors.Join(e, errors.New("消费前PENDING回填失败"))
	}
	consumerGate.Release()
	if e = wait(ctx, consumeCommitGate.Entered, "consumer actual SQL Commit before cache invalidation"); e != nil {
		return e
	}
	canonical, e := r.Store.Read(ctx, "alice", o.ID)
	if e != nil {
		return e
	}
	cached, hit, e := r.Cache.Query(ctx, r.Store, "alice", o.ID, b.Hooks{})
	if e != nil {
		return e
	}
	if e = b.Assert(canonical.DeliveryStatus == "RECORDED" && hit && cached.DeliveryStatus == "PENDING", "真实SQLCommit→Redis失效窗口"); e != nil {
		return e
	}
	windowOffset, e := r.Broker.Offset(ctx)
	if e != nil {
		return e
	}
	if e = b.Assert(windowOffset == -1, "消费Commit门上offset尚未确认"); e != nil {
		return e
	}
	fmt.Fprintln(r.Out, "CACHE_COMMIT_WINDOW mysql=RECORDED redis=PENDING offset_not_confirmed=true")
	consumeCommitGate.Release()
	if e = WaitOffset(ctx, a, r, 1); e != nil {
		return e
	}
	exists, gen, _, e = r.Cache.Inspect(ctx, "alice", o.ID)
	if e != nil {
		return e
	}
	if e = b.Assert(exists == 0 && gen == "2", "consumer invalidated actual key gen2 exists0"); e != nil {
		return e
	}
	readGate.Release()
	old, e := stale.Completed(ctx)
	if e != nil {
		return e
	}
	holdRead.Store(false)
	if e = b.Assert(old.Err == nil && old.Body.View != nil && old.Body.View.DeliveryStatus == "PENDING" && !old.Body.CacheHit, "held request retains old snapshot"); e != nil {
		return e
	}
	exists, _, _, e = r.Cache.Inspect(ctx, "alice", o.ID)
	if e != nil {
		return e
	}
	if e = b.Assert(exists == 0, "fence rejects late PENDING fill"); e != nil {
		return e
	}
	// 用刚才同一旧View真的SET一次危险版本，观察业务错读，再失效修复。
	if e = r.Cache.UnsafeFillForLesson(ctx, *old.Body.View); e != nil {
		return e
	}
	bad := call(ctx, "GET", "/orders/"+o.ID, "alice", "", "")
	if e = b.Assert(bad.Err == nil && bad.Body.CacheHit && bad.Body.View != nil && bad.Body.View.DeliveryStatus == "PENDING", "无fence危险SET确实覆盖为旧状态"); e != nil {
		return e
	}
	if e = r.Cache.Invalidate(ctx, "alice", o.ID); e != nil {
		return e
	}
	fmt.Fprintln(r.Out, "BAD_UNFENCED_FILL mysql=RECORDED redis=PENDING; repair=INCR+DEL+fenced_query")
	q = call(ctx, "GET", "/orders/"+o.ID, "alice", "", "")
	if e = b.Assert(q.Err == nil && q.Body.View != nil && q.Body.View.DeliveryStatus == "RECORDED" && !q.Body.CacheHit, "fresh RECORDED query"); e != nil {
		return e
	}
	q = call(ctx, "GET", "/orders/"+o.ID, "alice", "", "")
	if e = b.Assert(q.Err == nil && q.Body.CacheHit && q.Body.View.DeliveryStatus == "RECORDED", "actual cache refill confirmed"); e != nil {
		return e
	}
	if e = Audit(ctx, r, 1, 2, 8, 1, 1, 1); e != nil {
		return e
	}

	// E04真实重复record与持久幂等。改payload只调用真实Apply，不毒化主topic；
	// 客户端重建以后第三次投递仍命中数据库inbox，证明不是进程map去重。
	fmt.Fprintln(r.Out, "E04 DUPLICATE EVENT + same-group client restart")
	stored, found, e := r.Store.FindByKey(ctx, "alice", "order-main")
	if e != nil || !found {
		return errors.Join(e, errors.New("original order missing"))
	}
	event := b.EventFor(stored)
	if _, e = r.Broker.Publish(ctx, event); e != nil {
		return e
	}
	if e = WaitOffset(ctx, a, r, 2); e != nil {
		return e
	}
	if e = Audit(ctx, r, 1, 2, 8, 1, 1, 1); e != nil {
		return e
	}
	corrupt := event
	corrupt.Order.Quantity = 3
	_, e = r.Store.Apply(ctx, corrupt, r.Broker.Topic, 0, 99)
	if e = b.Assert(errors.Is(e, b.ErrConflict), "same event id different payload rejected"); e != nil {
		return e
	}
	if e = a.Close(); e != nil {
		return e
	}
	if e = b.Assert(a.SafeToRelease(), "normal app finalDone/resource use ended"); e != nil {
		return e
	}
	hooks.BeforeConsume = nil
	a, e = b.NewApp(r, hooks)
	if e != nil {
		return e
	}
	apps = append(apps, a)
	if e = a.Start(); e != nil {
		return e
	}
	if _, e = r.Broker.Publish(ctx, event); e != nil {
		return e
	}
	if e = WaitOffset(ctx, a, r, 3); e != nil {
		return e
	}
	if e = Audit(ctx, r, 1, 2, 8, 1, 1, 1); e != nil {
		return e
	}
	var firstOffset, effects int64
	if e = r.SQL.QueryRowContext(ctx, "SELECT first_offset FROM `"+r.Store.Table("inbox")+"` WHERE event_id=?", event.EventID).Scan(&firstOffset); e != nil {
		return e
	}
	if e = r.SQL.QueryRowContext(ctx, "SELECT effect_count FROM `"+r.Store.Table("receipts")+"` WHERE order_id=?", o.ID).Scan(&effects); e != nil {
		return e
	}
	if e = b.Assert(firstOffset == 0 && effects == 1, "persistent first_offset0/effects1 after three broker deliveries"); e != nil {
		return e
	}
	bob := call(ctx, "POST", "/orders", "bob", "order-main", `{"sku":"sku-m37","quantity":1}`)
	if e = b.Assert(bob.Err == nil && bob.Status == 201 && bob.Body.Order != nil && bob.Body.Order.Owner == "bob" && bob.Body.Order.ID != o.ID, "same key isolated per user"); e != nil {
		return e
	}
	if e = WaitOffset(ctx, a, r, 4); e != nil {
		return e
	}
	bobRead := call(ctx, "GET", "/orders/"+bob.Body.Order.ID, "bob", "", "")
	if e = b.Assert(bobRead.Err == nil && bobRead.Body.View != nil && bobRead.Body.View.Owner == "bob" && bobRead.Body.View.AmountMinor == 299, "bob query ownership/body"); e != nil {
		return e
	}
	if e = Audit(ctx, r, 2, 3, 7, 2, 2, 2); e != nil {
		return e
	}

	// E05业务错误/panic在未提交真实SQL后产生，随后独立查账证明三项一起回滚。
	fmt.Fprintln(r.Out, "E05 ROLLBACK + PANIC + cancellation + committed response timeout")
	for _, key := range []string{"rollback", "panic"} {
		got := call(ctx, "POST", "/orders", "alice", key, `{"sku":"sku-m37","quantity":1}`)
		word := "DEPENDENCY"
		if key == "panic" {
			word = "UNKNOWN"
		}
		if e = b.Assert(got.Err == nil && got.Status == 503 && got.Body.Code == word, "after SQL failure response key=%s status=%d code=%s", key, got.Status, got.Body.Code); e != nil {
			return e
		}
		if e = Audit(ctx, r, 2, 3, 7, 2, 2, 2); e != nil {
			return e
		}
	}
	if e = TimedCall(ctx, client, a, beforeGate, "cancel-before"); e != nil {
		return e
	}
	if e = Audit(ctx, r, 2, 3, 7, 2, 2, 2); e != nil {
		return e
	}
	if e = TimedCall(ctx, client, a, lostGate, "lost-response"); e != nil {
		return e
	}
	resolved := call(ctx, "GET", "/requests/lost-response", "alice", "", "")
	if e = b.Assert(resolved.Err == nil && resolved.Status == 200 && resolved.Body.Outcome == "COMMITTED" && resolved.Body.Order != nil && resolved.Body.Order.Quantity == 1, "timed-out request authoritative reconciliation"); e != nil {
		return e
	}
	if e = WaitOffset(ctx, a, r, 5); e != nil {
		return e
	}
	if e = b.Observe(ctx, func() (bool, error) {
		var sent int64
		e := r.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+r.Store.Table("outbox")+"` WHERE sent=TRUE").Scan(&sent)
		return sent == 3, e
	}); e != nil {
		return e
	}
	if e = Audit(ctx, r, 3, 4, 6, 3, 3, 3); e != nil {
		return e
	}
	records, e := r.Broker.Records(ctx, cfg)
	if e != nil {
		return e
	}
	if e = b.Assert(len(records) == 5, "actual broker records5 including 2 duplicate"); e != nil {
		return e
	}
	for i := 0; i < 3; i++ {
		if e = b.Assert(bytes.Equal(records[i].Value, records[0].Value) && string(records[i].Key) == o.ID && records[i].Offset == int64(i), "duplicate raw record[%d] id/key/payload/offset", i); e != nil {
			return e
		}
	}
	fmt.Fprintf(r.Out, "BROKER_AUDIT records=%d duplicate_offsets=0,1,2 durable_first_offset=%d effects=%d\n", len(records), firstOffset, effects)

	// E06刻意让观察者提前返回，但defer早已登记。取消caller和App、放门、join
	// caller最终Done都完成后，外层才允许清理表/关闭SQL。原观察错误仍在Join树。
	fmt.Fprintln(r.Out, "E06 EARLY OBSERVER FAILURE + final close")
	if e = EarlyExit(ctx, client, a, earlyGate); !errors.Is(e, errEarly) || errors.Is(e, b.ErrCleanup) {
		return fmt.Errorf("early error/cleanup boundary: %w", e)
	}
	if e = b.Assert(a.SafeToRelease(), "early failure app joined"); e != nil {
		return e
	}
	if e = Audit(ctx, r, 3, 4, 6, 3, 3, 3); e != nil {
		return e
	}
	// 构造成功但未Start：两个listener/client亦需关闭，不误等Serve/未接纳handler。
	unstarted, e := b.NewApp(r, b.Hooks{})
	if e != nil {
		return e
	}
	apps = append(apps, unstarted)
	if e = unstarted.Close(); e != nil {
		return e
	}
	if e = b.Assert(unstarted.SafeToRelease(), "unstarted app close"); e != nil {
		return e
	}
	fmt.Fprintf(r.Out, "PASS namespace=%s orders=3 qty=4 stock=6 outbox=3 inbox=3 receipts=3 records=5 next=5\n", ns)
	return nil
}

// Call执行真实HTTP并严格解码完整响应；response.Body和deadline来自caller，
// Body.Close错误通过Join保留，不只检查HTTP状态。Transport由Run拥有且最后关闭闲连接。
func Call(ctx context.Context, client *http.Client, url, method, path, owner, key, body string) (r result) {
	req, e := http.NewRequestWithContext(ctx, method, url+path, bytes.NewBufferString(body))
	if e != nil {
		r.Err = e
		return
	}
	if owner != "" {
		req.Header.Set("Authorization", "Bearer m37-"+owner)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := client.Do(req)
	if e != nil {
		r.Err = e
		return
	}
	defer func() { r.Err = errors.Join(r.Err, resp.Body.Close()) }()
	r.Status = resp.StatusCode
	d := json.NewDecoder(io.LimitReader(resp.Body, 8193))
	d.DisallowUnknownFields()
	r.Err = d.Decode(&r.Body)
	var extra any
	if e = d.Decode(&extra); e != io.EOF {
		r.Err = errors.Join(r.Err, errors.New("HTTP响应含尾随数据"))
	}
	return
}

// Audit用database/sql独立读取GORM写出的实际持久事实。固定预期同时比较逐订单
// 金额/币种/qty、库存守恒与receipt/inbox，不让两个错误互相抵消成“总数正确”。
func Audit(ctx context.Context, r *b.Resources, orders, qty, stock, outbox, inbox, receipts int64) error {
	var gotOrders, gotQty, bad, gotOutbox, gotInbox, gotReceipts, totalEffects int64
	s := r.Store
	if e := r.SQL.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(quantity),0),COALESCE(SUM(amount_minor<>quantity*299 OR currency<>'CNY' OR status<>'ACCEPTED' OR aggregate_version<>1 OR request_key NOT IN ('order-main','lost-response') OR (owner='alice' AND request_key='order-main' AND quantity<>2) OR (owner='bob' AND quantity<>1) OR (request_key='lost-response' AND (owner<>'alice' OR quantity<>1))),0) FROM `"+s.Table("orders")+"`").Scan(&gotOrders, &gotQty, &bad); e != nil {
		return e
	}
	for _, pair := range []struct {
		suffix string
		target *int64
	}{{"outbox", &gotOutbox}, {"inbox", &gotInbox}, {"receipts", &gotReceipts}} {
		if e := r.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+s.Table(pair.suffix)+"`").Scan(pair.target); e != nil {
			return e
		}
	}
	if e := r.SQL.QueryRowContext(ctx, "SELECT COALESCE(SUM(effect_count),0) FROM `"+s.Table("receipts")+"`").Scan(&totalEffects); e != nil {
		return e
	}
	var badReceipts int64
	if e := r.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+s.Table("receipts")+"` r LEFT JOIN `"+s.Table("orders")+"` o ON o.order_id=r.order_id LEFT JOIN `"+s.Table("inbox")+"` i ON i.event_id=r.event_id WHERE o.order_id IS NULL OR i.event_id IS NULL OR r.quantity<>o.quantity").Scan(&badReceipts); e != nil {
		return e
	}
	if e := b.Assert(badReceipts == 0, "receipt必须对应源订单和持久inbox且quantity相同"); e != nil {
		return e
	}
	gotStock, e := s.Stock(ctx)
	if e != nil {
		return e
	}
	if e = b.Assert(gotOrders == orders && gotQty == qty && gotStock == stock && stock+qty == 10 && bad == 0 && gotOutbox == outbox && gotInbox == inbox && gotReceipts == receipts && totalEffects == receipts, "SQL orders=%d qty=%d stock=%d outbox=%d inbox=%d receipts=%d effects=%d bad=%d", gotOrders, gotQty, gotStock, gotOutbox, gotInbox, gotReceipts, totalEffects, bad); e != nil {
		return e
	}
	fmt.Fprintf(r.Out, "SQL_AUDIT orders=%d qty=%d stock=%d sum=10 outbox=%d inbox=%d receipts=%d effects=%d\n", gotOrders, gotQty, gotStock, gotOutbox, gotInbox, gotReceipts, totalEffects)
	return nil
}

// WaitOffset观察持久进度和后台任务最终错误；以真正Offset谓词等待，不靠Sleep。
func WaitOffset(ctx context.Context, a *b.App, r *b.Resources, want int64) error {
	return b.Observe(ctx, func() (bool, error) {
		if e := a.BackgroundError(); e != nil {
			return false, e
		}
		n, e := r.Broker.Offset(ctx)
		return n == want, e
	})
}

// Repeats启动六个有限caller，每创建一个就加入owner名单；defer在任何中途断言失败
// 也取消并join所有已启动对象。每个响应的requestID不同，但orderID/创建ID保持原值。
func Repeats(ctx context.Context, client *http.Client, url string, o b.Order) (err error) {
	var callers []*Caller
	defer func() {
		for _, c := range callers {
			_, e := c.Finish(nil)
			err = errors.Join(err, e)
		}
	}()
	for range 6 {
		c := StartCaller(ctx, func(ctx context.Context) result {
			return Call(ctx, client, url, "POST", "/orders", "alice", "order-main", `{"sku":"sku-m37","quantity":2}`)
		}, nil)
		callers = append(callers, c)
	}
	ids := map[string]bool{}
	for _, c := range callers {
		r, e := c.Completed(ctx)
		if e != nil {
			return e
		}
		if e = b.Assert(r.Err == nil && r.Status == 201 && r.Body.Order != nil && r.Body.Order.ID == o.ID && r.Body.Order.RequestID == o.RequestID && !ids[r.Body.RequestID], "concurrent idempotent response"); e != nil {
			return e
		}
		ids[r.Body.RequestID] = true
	}
	return nil
}

// TimedCall用2500ms真实客户端deadline，门证明停在提交前或提交后。caller error
// 不能推断事务结果，先观察handler真正退出，再由后续权威SQL/原key查询区分。
func TimedCall(ctx context.Context, client *http.Client, a *b.App, gate *Gate, key string) (err error) {
	deadline, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	c := StartCaller(deadline, func(ctx context.Context) result {
		return Call(ctx, client, a.URL, "POST", "/orders", "alice", key, `{"sku":"sku-m37","quantity":1}`)
	}, nil)
	defer func() { gate.Release(); _, e := c.Finish(nil); err = errors.Join(err, e) }()
	// 如果连接/SQL在截止前根本没到目标门，马上报告真实范围失败；不能等整个课程
	// 120s后把“任意位置超时”写成“已提交/未提交窗口实验完成”。
	select {
	case <-gate.Entered:
	case <-c.Done:
		return fmt.Errorf("%s未进入目标SQL边界，caller已经结束: %w", key, c.final.Err)
	case <-ctx.Done():
		return ctx.Err()
	}
	r, e := c.Completed(ctx)
	if e != nil {
		return e
	}
	if e = b.Assert(errors.Is(r.Err, context.DeadlineExceeded), "real client timeout key=%s err=%v", key, r.Err); e != nil {
		return e
	}
	if e = b.Observe(ctx, func() (bool, error) { n, _ := a.ActiveHandlers(); return n == 0, nil }); e != nil {
		return e
	}
	fmt.Fprintf(a.Resources.Out, "CLIENT_TIMEOUT key=%s outcome=UNKNOWN handler_final_return=true\n", key)
	return nil
}

// EarlyExit是刻意失败的观察者，用同一个真实App/SQL用例。收尾guard在等待Entered
// 之前就注册，cancel caller并同步Stop应用根ctx以后再放门，保证未提交hook观察
// 实际服务端取消并回滚。客户端cancel本身只是通知，不能充当这条因果屏障。Close只join
// App拥有者，caller还必须另等自己的最终Done。原errEarly和任何cleanup error都保留。
func EarlyExit(ctx context.Context, client *http.Client, a *b.App, gate *Gate) (err error) {
	c := StartCaller(ctx, func(ctx context.Context) result {
		return Call(ctx, client, a.URL, "POST", "/orders", "alice", "early-exit", `{"sku":"sku-m37","quantity":1}`)
	}, nil)
	defer func() {
		c.cancel()
		a.Stop()
		// 不是推断Stop已传播：等真实SQL hook所使用的同一ctx被Wait读到取消。
		// 这只是取消观察，随后Close仍要等rollback和handler的最终退出。
		err = errors.Join(err, b.JoinDone(gate.Canceled, "early Tx hook observed cancellation", 2*time.Second))
		fmt.Fprintf(a.Resources.Out, "EARLY_CANCEL_OBSERVED before_release=%t\n", isClosed(gate.Canceled))
		gate.Release()
		err = errors.Join(err, b.CleanupCause(a.Close()))
		r, e := c.Finish(nil)
		err = errors.Join(err, e)
		if !errors.Is(r.Err, context.Canceled) {
			err = errors.Join(err, b.CleanupCause(errors.New("early caller未观察取消")))
		}
	}()
	if e := wait(ctx, gate.Entered, "early actual Tx writes"); e != nil {
		return e
	}
	return errEarly
}

// isClosed只用于已经受预算等待过的观察日志，不替代join或读取worker结果。
func isClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}
