package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// audit 是独立观察出口：直接 SQL、独立无group Kafka reader、broker持久offset；
// 不依赖被测函数自报成功。n是订单数，sent/effects是明确期望数量，messages/next
// 是broker原始数量和该业务group next位置。库存守恒从持久SUM独立计算。
func audit(ctx context.Context, r *b.Resources, n, sent, effects, messages int, next int64) error {
	var orders, qty, stock, ob, actualSent, inbox, receipts, effectCount int64
	queries := []struct {
		sql string
		to  []*int64
	}{
		{"SELECT COUNT(*),COALESCE(SUM(quantity),0) FROM `" + r.Store.Table("orders") + "`", []*int64{&orders, &qty}},
		{"SELECT quantity FROM `" + r.Store.Table("stock") + "` WHERE sku='sku-m37'", []*int64{&stock}},
		{"SELECT COUNT(*),COALESCE(SUM(sent),0) FROM `" + r.Store.Table("outbox") + "`", []*int64{&ob, &actualSent}},
		{"SELECT COUNT(*) FROM `" + r.Store.Table("inbox") + "`", []*int64{&inbox}},
		{"SELECT COUNT(*),COALESCE(SUM(effect_count),0) FROM `" + r.Store.Table("receipts") + "`", []*int64{&receipts, &effectCount}},
	}
	for _, q := range queries {
		args := make([]any, len(q.to))
		for i, p := range q.to {
			args[i] = p
		}
		if e := r.SQL.QueryRowContext(ctx, q.sql).Scan(args...); e != nil {
			return e
		}
	}
	if e := b.Assert(orders == int64(n) && qty == int64(n) && stock == 10-qty && stock >= 0 && ob == orders && actualSent == int64(sent) && inbox == int64(effects) && receipts == inbox && effectCount == inbox, "SQL不变量 n=%d sent=%d effects=%d 实际 orders=%d qty=%d stock=%d outbox=%d/%d inbox=%d receipts=%d effects=%d", n, sent, effects, orders, qty, stock, ob, actualSent, inbox, receipts, effectCount); e != nil {
		return e
	}
	// COUNT/SUM还不足以证明映射正确；逐字段SQL反断言检出inbox载荷或receipt
	// 与原事件/订单错配。这里的SQL是独立观察，不复用被测Apply的返回值。
	var invalid int
	check := "SELECT COUNT(*) FROM `" + r.Store.Table("inbox") + "` i LEFT JOIN `" + r.Store.Table("outbox") + "` o ON o.event_id=i.event_id WHERE o.event_id IS NULL OR i.payload<>o.payload OR i.topic<>? OR i.partition_id<>0 OR i.first_offset<0"
	if e := r.SQL.QueryRowContext(ctx, check, r.Broker.Topic).Scan(&invalid); e != nil {
		return e
	}
	if e := b.Assert(invalid == 0, "inbox真实payload/topic/partition必须对应原事件"); e != nil {
		return e
	}
	check = "SELECT COUNT(*) FROM `" + r.Store.Table("receipts") + "` x LEFT JOIN `" + r.Store.Table("orders") + "` o ON o.order_id=x.order_id LEFT JOIN `" + r.Store.Table("outbox") + "` e ON e.event_id=x.event_id AND e.order_id=x.order_id WHERE o.order_id IS NULL OR e.event_id IS NULL OR x.quantity<>o.quantity OR x.effect_count<>1"
	if e := r.SQL.QueryRowContext(ctx, check).Scan(&invalid); e != nil {
		return e
	}
	if e := b.Assert(invalid == 0, "receipt实际order/event/quantity/effect必须对应权威源单"); e != nil {
		return e
	}
	// 每个outbox payload还要同完整权威订单比较，COUNT=1不能掩盖错误owner/金额。
	rows, e := r.SQL.QueryContext(ctx, "SELECT event_id,payload FROM `"+r.Store.Table("outbox")+"`")
	if e != nil {
		return e
	}
	var events []b.Event
	for rows.Next() {
		var id, payload string
		if e = rows.Scan(&id, &payload); e != nil {
			break
		}
		var event b.Event
		if e = json.Unmarshal([]byte(payload), &event); e != nil {
			break
		}
		if id != event.EventID {
			e = errors.New("outbox event_id与payload不一致")
			break
		}
		events = append(events, event)
	}
	e = errors.Join(e, rows.Err(), rows.Close())
	if e != nil {
		return e
	} // 关闭Rows再下一查询；observer max1不能一边持Rows一边再借池。
	for _, event := range events {
		v, e := r.Store.Read(ctx, event.Order.Owner, event.Order.ID)
		if e != nil {
			return e
		}
		want, _ := json.Marshal(b.EventFor(v.Order))
		got, _ := json.Marshal(event)
		if e = b.Assert(string(want) == string(got) && v.Quantity == 1 && v.AmountMinor == 299 && v.Currency == "CNY" && v.Status == "ACCEPTED" && v.Version == 1, "权威order/outbox完整载荷与字段一致"); e != nil {
			return e
		}
	}
	records, e := r.Broker.Records(ctx, r.Config)
	if e != nil {
		return e
	}
	if e = b.Assert(len(records) == messages, "raw broker messages expected=%d actual=%d", messages, len(records)); e != nil {
		return e
	}
	for _, record := range records {
		var event b.Event
		if e = json.Unmarshal(record.Value, &event); e != nil {
			return e
		}
		valid := false
		for _, known := range events {
			raw, _ := json.Marshal(known)
			if string(raw) == string(record.Value) && string(record.Key) == known.Order.ID {
				valid = true
			}
		}
		if e = b.Assert(valid, "broker实际Key/Value必须属于权威冻结事件 offset=%d", record.Offset); e != nil {
			return e
		}
		fmt.Fprintf(r.Out, "RAW_RECORD topic=%s partition=%d offset=%d event=%s order=%s value=%s\n", record.Topic, record.Partition, record.Offset, event.EventID, event.Order.ID, record.Value)
	}
	// 独立核对inbox首次offset确实指向原event，且为本实验顺序处理的最低位置。
	// 消费者SQL去重不能偷偷把first_offset改成后来的重复投递位置。
	rows, e = r.SQL.QueryContext(ctx, "SELECT event_id,first_offset FROM `"+r.Store.Table("inbox")+"`")
	if e != nil {
		return e
	}
	for rows.Next() {
		var id string
		var first int64
		if e = rows.Scan(&id, &first); e != nil {
			break
		}
		minimum := int64(-1)
		for _, record := range records {
			var event b.Event
			_ = json.Unmarshal(record.Value, &event)
			if event.EventID == id {
				minimum = record.Offset
				break
			}
		}
		if minimum < 0 || first != minimum {
			e = fmt.Errorf("inbox实际首次位置不等原event最低offset id=%s first=%d raw_min=%d", id, first, minimum)
			break
		}
	}
	e = errors.Join(e, rows.Err(), rows.Close())
	if e != nil {
		return e
	}
	offset, e := r.Broker.Offset(ctx)
	if e != nil {
		return e
	}
	if e = b.Assert(offset == next, "broker持久next expected=%d actual=%d", next, offset); e != nil {
		return e
	}
	fmt.Fprintf(r.Out, "AUDIT namespace=%s orders=%d quantity=%d stock=%d outbox=%d sent=%d inbox=%d receipts=%d effects=%d raw=%d next=%d\n", r.Namespace, orders, qty, stock, ob, actualSent, inbox, receipts, effectCount, len(records), offset)
	return nil
}

// waitState 定期查事实，ticker仅控制查询频率，绝不根据经过时间猜消费完成；还
// 会立即传播App后台失败，避免错误任务已退出仍观察到总预算结束。
func waitState(ctx context.Context, a *b.App, r *b.Resources, next int64) error {
	return b.Observe(ctx, func() (bool, error) {
		if e := a.BackgroundError(); e != nil {
			return false, e
		}
		n, e := r.Broker.Offset(ctx)
		return n == next, e
	})
}
