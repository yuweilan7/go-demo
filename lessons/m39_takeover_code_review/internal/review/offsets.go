package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/twmb/franz-go/pkg/kgo"
	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// consumer取得驱动client即登记session账本。group/topic来自已预检且登记的Resources；
// 不临时增加别人的group，不reset offset。恢复client要先等旧使用者Done/Close。
func (s *session) consumer() (*b.KafkaClient, error) {
	c, e := b.NewKafkaClient(s.r.Config, s.r.Namespace+"-review", b.ConsumerOptions(s.r.Broker.Topic, s.r.Broker.Group)...)
	if e != nil {
		return nil, e
	}
	s.clients = append(s.clients, c)
	return c, nil
}

// poll取得真实有限记录，返回的Record指针由caller只读借用，绝不改Key/Value/Offset。
// fetch位置不等于持久group next，后者必须单独Broker.Offset核对。
func poll(ctx context.Context, c *b.KafkaClient, n int) ([]*kgo.Record, error) {
	var records []*kgo.Record
	for len(records) < n {
		f := c.K.PollRecords(ctx, n-len(records))
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		if es := f.Errors(); len(es) > 0 {
			return nil, es[0].Err
		}
		records = append(records, f.Records()...)
	}
	return records, nil
}

// applyOnly是修复worker的业务范围：真实Apply Tx→缓存失效，不允许worker独自commit。
// 协议校验重复公开入口的有限前置条件；真正SQL副作用仍调用M37 Store.Apply。
func applyOnly(ctx context.Context, s *session, r *kgo.Record) error {
	var e b.Event
	if err := json.Unmarshal(r.Value, &e); err != nil {
		return err
	}
	if r.Topic != s.r.Broker.Topic || r.Partition != 0 || string(r.Key) != e.Order.ID {
		return b.ErrConflict
	}
	_, err := s.r.Store.Apply(ctx, e, r.Topic, r.Partition, r.Offset)
	if err != nil {
		return err
	}
	if err = s.r.Cache.Invalidate(ctx, e.Order.Owner, e.Order.ID); err != nil {
		return errors.Join(b.ErrUnknown, err)
	}
	return nil
}

// prefix由唯一collector持有，记录连续完成位置；它不是加锁共享map。
// next代表所有offset<next都已SQL+失效完成。高offset先完成只填map，不能越缺口推进。
type prefix struct {
	next      int64
	completed map[int64]bool
}

// mark由唯一collector在worker真实done后调用。删除已跨过的事实，返回安全next值；
// 它只做有限batch计算，不连接broker，也没有证明旧generation在途写被fence。
func (p *prefix) mark(offset int64) int64 {
	if p.completed == nil {
		p.completed = map[int64]bool{}
	}
	p.completed[offset] = true
	for p.completed[p.next] {
		delete(p.completed, p.next)
		p.next++
	}
	return p.next
}

// Offsets真实制造“低offset停在业务前、高offset完成且commit”的漏洞。
// 两个session分别展示坏例/修复，名字与数据独立，不靠reset已有group抹掉坏事实。
func Offsets(ctx context.Context, out io.Writer) error {
	for _, bad := range []bool{true, false} {
		if e := withSession(ctx, out, func(s *session) error { return offsetCase(ctx, s, bad) }); e != nil {
			return e
		}
	}
	return nil
}

// offsetCase的caller给本次专属session。两个worker都创建即登记，低worker有明确
// gate等待点；collector用done同步观察高worker结果，而不是用sleep猜完成顺序。
func offsetCase(ctx context.Context, s *session, bad bool) error {
	var events []b.Event
	for _, key := range []string{"low", "high"} {
		o, e := s.r.Store.Create(ctx, command("alice", key), b.Hooks{})
		if e != nil {
			return e
		}
		event := b.EventFor(o)
		events = append(events, event)
		if _, e = s.r.Broker.Publish(ctx, event); e != nil {
			return e
		}
		if e = s.r.Store.MarkSent(ctx, event); e != nil {
			return e
		}
	}
	c, e := s.consumer()
	if e != nil {
		return e
	}
	batch, e := poll(ctx, c, 2)
	if e != nil {
		return e
	}
	if e = b.Assert(batch[0].Offset == 0 && batch[1].Offset == 1, "真实batch offset0/1"); e != nil {
		return e
	}
	lowGate := s.makeGate()
	lowCtx, lowCancel := context.WithCancel(s.ctx)
	defer lowCancel()
	low := s.launch("offset0 worker", func() error {
		if err := lowGate.wait(lowCtx); err != nil {
			return err
		}
		return applyOnly(lowCtx, s, batch[0])
	})
	if e = await(ctx, lowGate.entered, "低offset业务前屏障"); e != nil {
		return e
	}
	high := s.launch("offset1 worker", func() error {
		if bad {
			// BAD：goroutine自己commit高offset。broker next=2会包括尚未完成的offset0。
			return s.r.Broker.ProcessRecord(s.ctx, c, s.r.Store, s.r.Cache, batch[1], b.Hooks{})
		}
		return applyOnly(s.ctx, s, batch[1]) // 修复worker没有进度确认权。
	})
	if e = high.join(); e != nil {
		return e
	}
	if e = counts(ctx, s, 8, 2, 1); e != nil {
		return e
	}
	high.consumed = true
	before, e := s.r.Broker.Offset(ctx)
	if e != nil {
		return e
	}
	if bad {
		if e = b.Assert(before == 2, "坏例高offsetcommit使next2 got=%d", before); e != nil {
			return e
		}
		lowCancel()
		e = low.join()
		if e = b.Assert(errors.Is(e, context.Canceled), "低worker取消而尚未执行业务"); e != nil {
			return e
		}
		// 独立权威读取低订单没有receipt，与高订单有receipt同时成立。
		v, e := s.r.Store.Read(ctx, "alice", events[0].Order.ID)
		if e != nil {
			return e
		}
		if e = b.Assert(v.DeliveryStatus == "PENDING", "低offset业务仍未完成"); e != nil {
			return e
		}
		low.consumed = true
		fmt.Fprintln(s.out, "R04_BAD low_done_effect=false high_effect=true broker_next=2")
	} else {
		p := &prefix{}
		if e = b.Assert(p.mark(1) == 0 && before == -1, "高先完成不能越缺口确认 next=%d", before); e != nil {
			return e
		}
		fmt.Fprintln(s.out, "R04_FIXED high_finished=true contiguous_next=0 broker_next=-1")
		lowGate.open()
		if e = low.join(); e != nil {
			return e
		}
		if e = counts(ctx, s, 8, 2, 2); e != nil {
			return e
		}
		low.consumed = true
		if e = b.Assert(p.mark(0) == 2, "补齐0后连续前缀为2"); e != nil {
			return e
		}
		if e = c.K.CommitRecords(ctx, batch[1]); e != nil {
			return errors.Join(b.ErrUnknown, e)
		}
		c.K.AllowRebalance()
	}
	// 先两worker最终Done/旧client.Close，再fresh，杜绝旧writer与恢复consumer同时写。
	if e = c.Close(); e != nil {
		return e
	}
	// 发布同一个高event作为offset2探针；它不是新业务事件。实测fresh第一条为2，
	// 比“等一会没看到0”更强地证明重启从持久next跳过低offset。
	probe, e := s.r.Broker.Publish(ctx, events[1])
	if e != nil {
		return e
	}
	if e = b.Assert(probe.Offset == 2, "真实探针offset2"); e != nil {
		return e
	}
	fresh, e := s.consumer()
	if e != nil {
		return e
	}
	got, e := poll(ctx, fresh, 1)
	if e != nil {
		return e
	}
	if e = b.Assert(got[0].Offset == 2, "fresh真实首条应从next2开始 got=%d", got[0].Offset); e != nil {
		return e
	}
	next, e := s.r.Broker.Offset(ctx)
	if e != nil {
		return e
	}
	if e = b.Assert(next == 2, "fetch探针不自动commit next=%d", next); e != nil {
		return e
	}
	raw, e := s.r.Broker.Records(ctx, s.r.Config)
	if e != nil {
		return e
	}
	if e = b.Assert(len(raw) == 3 && bytes.Equal(raw[1].Value, raw[2].Value) && bytes.Equal(raw[1].Key, raw[2].Key), "raw真实3条/探针同event"); e != nil {
		return e
	}
	wantEffects := int64(2)
	if bad {
		wantEffects = 1
	}
	if e = counts(ctx, s, 8, 2, wantEffects); e != nil {
		return e
	}
	fmt.Fprintf(s.out, "R04_RESTART bad=%t first_offset=%d broker_next=%d raw=%d effects=%d\n", bad, got[0].Offset, next, len(raw), wantEffects)
	return nil
}

// Unknown展示真正已提交后Go取消窗口。它不制造网络截断，也不声称client返回
// Canceled意味着未写入。原key/payload查账之后只续原outbox事件，不派新的下单写。
func Unknown(ctx context.Context, out io.Writer) error {
	return withSession(ctx, out, func(s *session) (err error) {
		requestCtx, cancel := context.WithCancel(s.ctx)
		defer cancel()
		input := command("alice", "unknown")
		o, cause := s.r.Store.Create(requestCtx, input, b.Hooks{AfterOrderCommit: func(actual context.Context, o b.Order) error {
			cancel()
			<-actual.Done() // 已commit后才发通知，再明确观察同ctx实际取消。
			return actual.Err()
		}})
		consumed := false
		defer func() {
			if !consumed {
				err = errors.Join(err, cause)
			}
		}() // 观察/恢复提前失败也保留原UNKNOWN。
		if e := b.Assert(errors.Is(cause, b.ErrUnknown) && errors.Is(cause, context.Canceled), "提交后保留UNKNOWN+Canceled"); e != nil {
			return e
		}
		if e := counts(ctx, s, 9, 1, 0); e != nil {
			return e
		}
		original, found, e := s.r.Store.FindByKey(ctx, "alice", "unknown")
		if e != nil {
			return e
		}
		if e = b.Assert(found && original.ID == o.ID && b.SamePayload(original, input), "原key完整payload相同"); e != nil {
			return e
		}
		if e = assertOrder(original, "alice", "unknown", 1); e != nil {
			return e
		}
		v, _, e := s.r.Cache.Query(ctx, s.r.Store, "alice", original.ID, b.Hooks{})
		if e != nil {
			return e
		}
		if e = b.Assert(v.DeliveryStatus == "PENDING", "提交后原单缓存PENDING"); e != nil {
			return e
		}
		fmt.Fprintln(s.out, "R07_UNKNOWN canceled=true order_committed=true original_key=unknown blind_create_retry=false")
		// 同步Create已返回，因而不存在旧handler；一个relay续原持久outbox，不重建event。
		did, e := s.r.Broker.RelayOne(ctx, s.r.Store, b.Hooks{})
		if e != nil {
			return e
		}
		if e = b.Assert(did, "原outbox得到真实ACK与sent"); e != nil {
			return e
		}
		c, e := s.consumer()
		if e != nil {
			return e
		}
		records, e := poll(ctx, c, 1)
		if e != nil {
			return e
		}
		if e = s.r.Broker.ProcessRecord(ctx, c, s.r.Store, s.r.Cache, records[0], b.Hooks{}); e != nil {
			return e
		}
		v, _, e = s.r.Cache.Query(ctx, s.r.Store, "alice", original.ID, b.Hooks{})
		if e != nil {
			return e
		}
		if e = b.Assert(v.DeliveryStatus == "RECORDED" && v.ID == original.ID, "原单RECORDED"); e != nil {
			return e
		}
		if e = assertOrder(v.Order, "alice", "unknown", 1); e != nil {
			return e
		}
		next, e := s.r.Broker.Offset(ctx)
		if e != nil {
			return e
		}
		if e = b.Assert(next == 1, "原group next1"); e != nil {
			return e
		}
		raw, e := s.r.Broker.Records(ctx, s.r.Config)
		if e != nil {
			return e
		}
		payload, e := json.Marshal(b.EventFor(original))
		if e != nil {
			return e
		}
		if e = b.Assert(len(raw) == 1 && bytes.Equal(raw[0].Value, payload) && string(raw[0].Key) == original.ID, "原event完整payload未改"); e != nil {
			return e
		}
		if e = counts(ctx, s, 9, 1, 1); e != nil {
			return e
		}
		consumed = true // 原cause只有整个独立恢复不变量通过后才算已处理。
		fmt.Fprintln(s.out, "R07_RECOVER original_order=true original_event=true raw=1 next=1 effects=1 delivery=RECORDED")
		return nil
	})
}
