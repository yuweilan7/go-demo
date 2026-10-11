package lockcourse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// deadlockCase 由Run调用，owner先确认两worker都完成首条UPDATE，A持30/B持31；
// 再只让A申请31，并用PROCESSLIST确认实际SQL已进入服务器；最后让B申请30。
// 这构成环，不靠sleep撞死锁。服务端选谁当victim不固定，只断言恰一条1213。
func (s *store) deadlockCase(ctx context.Context) (err error) {
	if err = s.seedStock(ctx, 30, 5); err != nil {
		return err
	}
	if err = s.seedStock(ctx, 31, 5); err != nil {
		return err
	}
	var own scope
	defer own.close(&err)
	gates := [2]*gate{own.gate(), own.gate()}
	ready := [2]chan int64{make(chan int64, 1), make(chan int64, 1)}
	for worker := 0; worker < 2; worker++ {
		own.start(ctx, func(ctx context.Context) (r taskResult) {
			defer captureTaskPanic(&r)
			c, e := acquire(ctx, s.db, "READ-COMMITTED")
			if e != nil {
				r.Err = e
				return
			}
			defer func() { r.Cleanup = errors.Join(r.Cleanup, c.release()) }()
			key := fmt.Sprintf("deadlock-%d", worker) // 重试始终保留原业务key，不能造新订单。
			for attempt := 0; attempt < 3; attempt++ {
				first, second := 30, 31
				if attempt == 0 && worker == 1 {
					first, second = 31, 30
				}
				e = transact(ctx, c.conn, func(tx *sql.Tx) error {
					if e := s.decrement(ctx, tx, first, ""); e != nil {
						return e
					}
					if attempt == 0 {
						ready[worker] <- c.id // 真实首UPDATE已经完成，Tx持有first记录锁。
						if e := gates[worker].wait(ctx); e != nil {
							return e
						}
					}
					marker := fmt.Sprintf("%s_deadlock_%d", s.prefix, worker)
					if e := s.decrement(ctx, tx, second, marker); e != nil {
						return e
					}
					// 一笔订单在本实验表示两SKU各1件，总qty2；订单与两次扣减
					// 同Tx。没有发货/RPC，重试不会重复外部副作用。
					_, e := tx.ExecContext(ctx, "INSERT INTO `"+s.orders+"` VALUES (?,30,2)", key)
					return e
				})
				if retryable(e, 1213) {
					// 1213对应服务端整个Tx回滚；transact也显式Rollback结束本地
					// 对象。重试不是只重放第二条UPDATE，而是Begin+首扣+次扣+订单。
					r.Retries++
					r.Evidence = append(r.Evidence, fmt.Sprintf("connection=%d request=%s attempt=%d actual_error=%v; rollback后新Tx按30->31", c.id, key, attempt+1, e))
					continue
				}
				if e != nil {
					r.Err = e
					return
				}
				r.Count = 1
				return
			}
			r.Err = errors.New("死锁整事务重试3次预算耗尽")
			return
		})
	}
	a, e := receive(ctx, ready[0])
	if e != nil {
		return e
	}
	b, e := receive(ctx, ready[1])
	if e != nil {
		return e
	}
	if a == b {
		return errors.New("死锁实验需要两个不同会话")
	}
	fmt.Fprintf(s.out, "[deadlock.first_locks] A=%d holds30 B=%d holds31\n", a, b)
	gates[0].open()
	if err = s.waitSQL(ctx, a, s.prefix+"_deadlock_0"); err != nil {
		return err
	}
	gates[1].open() // 现在B申请A持有的30，真正闭合等待环。
	commits, retries := 0, 0
	for _, t := range own.tasks {
		r, e := t.join(ctx)
		if e != nil {
			return e
		}
		for _, line := range r.Evidence {
			fmt.Fprintln(s.out, "[deadlock.retry]", line)
		}
		if e = errors.Join(r.Err, r.Cleanup); e != nil {
			return e
		}
		commits += r.Count
		retries += r.Retries
	}
	if commits != 2 || retries != 1 {
		return fmt.Errorf("应两笔最终提交/一次1213，实际commits=%d retries=%d", commits, retries)
	}
	if err = s.audit(ctx, "deadlock.recovered", 30, 3, 2, 4); err != nil {
		return err
	}
	if err = s.audit(ctx, "deadlock.second_sku", 31, 3, 0, 0); err != nil {
		return err
	}
	// 两行初始总库存10，最终6，订单qty4。若只重试失败statement，会少扣
	// victim的首行或漏订单；这条独立总量查询会把那种“恢复成功”判为失败。
	var total int
	if err = s.db.QueryRowContext(ctx, "SELECT SUM(stock) FROM `"+s.stock+"` WHERE id IN (30,31)").Scan(&total); err != nil {
		return err
	}
	if total != 6 {
		return errors.New("死锁恢复后两SKU总库存不守恒")
	}
	fmt.Fprintf(s.out, "[deadlock.conservation] initial=10 remaining=%d order_qty=4 commits=%d retries=%d\n", total, commits, retries)
	return nil
}

// decrement 给死锁/外部调用实验扣一个确定SKU。marker由owner随机前缀生成，
// 只用于PROCESSLIST定位；真实业务id用?。affected=0保留为业务冲突而非SQL故障。
func (s *store) decrement(ctx context.Context, tx *sql.Tx, id int, marker string) error {
	query := "UPDATE `" + s.stock + "` SET stock=stock-1 WHERE id=? AND stock>=1"
	if marker != "" {
		query = "/* " + marker + " */ " + query
	}
	r, e := tx.ExecContext(ctx, query, id)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrNoStock
	}
	return nil
}

// timeoutCase 由Run调用，对比1205与1213。holder锁40，waiter先成功改41，再等40
// 触发真实1205；直接在同Tx查询首步写入41，按实库rollback_on_timeout
// 判断statement失败是否已撤销整个Tx。应用始终明确Rollback，不带残留首步提交。
func (s *store) timeoutCase(ctx context.Context, rollbackOnTimeout int) (err error) {
	if err = s.seedStock(ctx, 40, 5); err != nil {
		return err
	}
	if err = s.seedStock(ctx, 41, 5); err != nil {
		return err
	}
	h, e := acquire(ctx, s.db, "READ-COMMITTED")
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, h.release()) }()
	htx, e := h.conn.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, rollback(htx)) }()
	var held int
	if err = htx.QueryRowContext(ctx, "SELECT stock FROM `"+s.stock+"` WHERE id=40 FOR UPDATE").Scan(&held); err != nil {
		return err
	}
	var own scope
	defer own.close(&err)
	ready := make(chan int64, 1)
	marker := s.prefix + "_timeout"
	w := own.start(ctx, func(ctx context.Context) (r taskResult) {
		defer captureTaskPanic(&r)
		c, e := acquire(ctx, s.db, "READ-COMMITTED")
		if e != nil {
			r.Err = e
			return
		}
		defer func() { r.Cleanup = errors.Join(r.Cleanup, c.release()) }()
		tx, e := c.conn.BeginTx(ctx, nil)
		if e != nil {
			r.Err = e
			return
		}
		defer func() { r.Cleanup = errors.Join(r.Cleanup, rollback(tx)) }()
		if e = s.decrement(ctx, tx, 41, ""); e != nil {
			r.Err = e
			return
		}
		ready <- c.id
		e = s.decrement(ctx, tx, 40, marker)
		if code(e) != 1205 {
			r.Err = fmt.Errorf("预期1205，实际%w", e)
			return
		}
		var stock int
		if x := tx.QueryRowContext(ctx, "SELECT stock FROM `"+s.stock+"` WHERE id=41").Scan(&stock); x != nil {
			r.Err = errors.Join(e, x)
			return
		}
		want := 4
		if rollbackOnTimeout == 1 {
			want = 5
		}
		if stock != want {
			r.Err = fmt.Errorf("timeout配置与实际首步写入不符 stock=%d", stock)
			return
		}
		// 预期错误留在Evidence，不写成nil证明成功；随后defer必须真的Rollback，
		// 而不是让statement失败后的首步写入被自动提交。
		r.Evidence = append(r.Evidence, fmt.Sprintf("actual_error=%v rollback_on_timeout=%d same_Tx_stock41=%d; app显式Rollback", e, rollbackOnTimeout, stock))
		return
	})
	id, e := receive(ctx, ready)
	if e != nil {
		return e
	}
	if err = s.waitSQL(ctx, id, marker); err != nil {
		return err
	}
	r, e := w.join(ctx)
	if e != nil {
		return e
	}
	if e = errors.Join(r.Err, r.Cleanup); e != nil {
		return e
	}
	for _, line := range r.Evidence {
		fmt.Fprintln(s.out, "[timeout.actual]", line)
	}
	if err = rollback(htx); err != nil {
		return err
	}
	if err = s.audit(ctx, "timeout.rolled_back", 41, 5, 0, 0); err != nil {
		return err
	}
	// holder/worker结束之后用新Tx锁定两行，既验证业务回滚，也确认锁不再遗留。
	if err = transact(ctx, h.conn, func(tx *sql.Tx) error {
		var stock int
		for _, id := range []int{40, 41} {
			if e := tx.QueryRowContext(ctx, "SELECT stock FROM `"+s.stock+"` WHERE id=? FOR UPDATE", id).Scan(&stock); e != nil {
				return e
			}
			if stock != 5 {
				return errors.New("锁恢复后stock应5")
			}
		}
		return nil
	}); err != nil {
		return err
	}
	fmt.Fprintln(s.out, "[timeout.released] 新Tx按40->41取得两行锁，stock均5")
	return nil
}
