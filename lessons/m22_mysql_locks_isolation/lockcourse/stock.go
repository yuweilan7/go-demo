package lockcourse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// stockCases 由Run调用，是15分钟核心阅读实验：同一库存业务，先证明check-then
// update不安全，再对比原子条件更新、FOR UPDATE、version CAS。所有并发都只
// 有两个worker；每种修复处理12个不同key请求，stock5最多成功5笔。
func (s *store) stockCases(ctx context.Context) error {
	if err := s.badStock(ctx); err != nil {
		return err
	}
	for i, mode := range []string{"atomic", "locking", "cas"} {
		if err := s.safeStock(ctx, 10+i, mode); err != nil {
			return err
		}
	}
	return s.quantityCase(ctx)
}

// badStock 的owner启动两个独立RC Tx。ready通知只在各自SELECT读到1之后发送，
// owner收两条再开门，因此不是偶然撞到超卖。stock用有符号INT故意允许反例；
// 应用修复才是本实验主题，不用CHECK约束把危险算法藏成另一个SQL错误。
func (s *store) badStock(ctx context.Context) (err error) {
	if err = s.seedStock(ctx, 1, 1); err != nil {
		return err
	}
	var own scope
	defer own.close(&err)
	goUpdate := own.gate()
	ready := make(chan int64, 2) // 两worker各发一次；即使owner早退也不会卡在发送。
	for worker := 0; worker < 2; worker++ {
		own.start(ctx, func(ctx context.Context) (r taskResult) {
			defer captureTaskPanic(&r)
			c, e := acquire(ctx, s.db, "READ-COMMITTED")
			if e != nil {
				r.Err = e
				return
			}
			defer func() { r.Cleanup = errors.Join(r.Cleanup, c.release()) }()
			r.Err = transact(ctx, c.conn, func(tx *sql.Tx) error {
				var available int
				if e := tx.QueryRowContext(ctx, "SELECT stock FROM `"+s.stock+"` WHERE id=1").Scan(&available); e != nil {
					return e
				}
				if available != 1 {
					return fmt.Errorf("屏障前应读取stock1，实际%d", available)
				}
				ready <- c.id // 读值是局部int副本；它并没有锁住数据库这一行。
				if e := goUpdate.wait(ctx); e != nil {
					return e
				}
				// 错误点：校验和扣减是两条SQL，两个读值都通过；UPDATE的行锁只
				// 串行化两次扣减，不会重新执行之前的available>=1判断。
				if _, e := tx.ExecContext(ctx, "UPDATE `"+s.stock+"` SET stock=stock-1 WHERE id=1"); e != nil {
					return e
				}
				_, e := tx.ExecContext(ctx, "INSERT INTO `"+s.orders+"` VALUES (?,1,1)", fmt.Sprintf("bad-%d", worker))
				return e
			})
			if r.Err == nil {
				r.Count = 1
			}
			return
		})
	}
	first, e := receive(ctx, ready)
	if e != nil {
		return e
	}
	second, e := receive(ctx, ready)
	if e != nil {
		return e
	}
	if first == second {
		return errors.New("双会话必须是不同CONNECTION_ID")
	}
	fmt.Fprintf(s.out, "[bad.barrier] connection=%d,%d 都普通读stock1；现在允许扣减\n", first, second)
	goUpdate.open()
	for _, t := range own.tasks {
		r, e := t.join(ctx)
		if e != nil {
			return e
		}
		if e = errors.Join(r.Err, r.Cleanup); e != nil {
			return e
		}
	}
	// -1与两条订单是独立反证：不是只听worker说“成功”，库存不变量确实破坏。
	return s.audit(ctx, "bad.oversell", 1, -1, 2, 2)
}

// safeStock 对一种修复启动两个worker，每个负责交错的六个请求。初始CAS另有
// 明确屏障使两Tx读同version，至少一次affected=0冲突；后续冲突次数不固定。
// owner读取工作报告只用于与独立持久订单数对照，不以Count代替业务SELECT。
func (s *store) safeStock(ctx context.Context, id int, mode string) (err error) {
	if err = s.seedStock(ctx, id, 5); err != nil {
		return err
	}
	var own scope
	defer own.close(&err)
	startGate := own.gate()
	casGate := own.gate()
	casRead := make(chan int64, 2)
	for worker := 0; worker < 2; worker++ {
		own.start(ctx, func(ctx context.Context) (r taskResult) {
			defer captureTaskPanic(&r)
			c, e := acquire(ctx, s.db, "READ-COMMITTED")
			if e != nil {
				r.Err = e
				return
			}
			defer func() { r.Cleanup = errors.Join(r.Cleanup, c.release()) }()
			if e = startGate.wait(ctx); e != nil {
				r.Err = e
				return
			}
			for request := worker; request < 12; request += 2 {
				key := fmt.Sprintf("%s-%d", mode, request)
				var hook func() error
				if mode == "cas" && request == worker {
					hook = func() error { casRead <- c.id; return casGate.wait(ctx) }
				}
				// 只允许确认未提交的CAS冲突重试整Tx；通信Commit错误是UNKNOWN。
				// 首轮hook在Place中已消耗，重试不再等两方屏障，否则会等不存在的第二轮。
				for attempt := 0; attempt < 16; attempt++ {
					e = s.place(ctx, c.conn, id, key, mode, 1, hook)
					hook = nil
					if retryable(e, 0) {
						r.Retries++
						continue
					}
					var cleanup *txCleanupFailure
					if errors.Is(e, ErrNoStock) && !errors.As(e, &cleanup) {
						break
					}
					if e != nil {
						r.Err = e
						return
					}
					r.Count++
					break
				}
				if errors.Is(e, ErrCAS) {
					r.Err = errors.New("CAS重试预算耗尽")
					return
				}
			}
			return
		})
	}
	startGate.open()
	if mode == "cas" {
		a, e := receive(ctx, casRead)
		if e != nil {
			return e
		}
		b, e := receive(ctx, casRead)
		if e != nil {
			return e
		}
		if a == b {
			return errors.New("CAS需要独立会话")
		}
		fmt.Fprintln(s.out, "[cas.barrier] 两Tx都读stock5/version0；放行后只有一个CAS能影响1行")
		casGate.open()
	}
	success, retries := 0, 0
	for _, t := range own.tasks {
		r, e := t.join(ctx)
		if e != nil {
			return e
		}
		if e = errors.Join(r.Err, r.Cleanup); e != nil {
			return e
		}
		success += r.Count
		retries += r.Retries
	}
	if success != 5 {
		return fmt.Errorf("%s成功数应5实际%d", mode, success)
	}
	if mode == "cas" && retries == 0 {
		return errors.New("屏障CAS没有观察到冲突")
	}
	fmt.Fprintf(s.out, "[%s.report] attempts=12 committed_success=%d whole_tx_retries=%d\n", mode, success, retries)
	if err = s.audit(ctx, mode, id, 0, 5, 5); err != nil {
		return err
	}
	// 在已有订单key上再次请求，库存已0可能先报NoStock；为独立验证数据库唯一
	// 约束和事务回滚，另造库存1的sku重复插同key：INSERT1062后该sku不能被扣减。
	dupID := id + 100
	if err = s.seedStock(ctx, dupID, 1); err != nil {
		return err
	}
	c, e := acquire(ctx, s.db, "READ-COMMITTED")
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, c.release()) }()
	var existingKey string
	if err = s.db.QueryRowContext(ctx, "SELECT request_id FROM `"+s.orders+"` WHERE sku=? ORDER BY request_id LIMIT 1", id).Scan(&existingKey); err != nil {
		return err
	}
	e = s.place(ctx, c.conn, dupID, existingKey, "atomic", 1, nil)
	var failedCleanup *txCleanupFailure
	if code(e) != 1062 || errors.As(e, &failedCleanup) || errors.Is(e, ErrUnknown) {
		return fmt.Errorf("应真实唯一键1062，实际%w", e)
	}
	return s.audit(ctx, mode+".duplicate.rollback", dupID, 1, 0, 0)
}

// place 由safeStock/quantityCase调用：conn属于caller，transact拥有每一次Tx；id/key/qty是值，
// SQL只能操作同Tx。三种方案的区别在“检查库存的依据如何与写入原子关联”。
// beforeCAS只用于第一轮确定性故障屏障，生产路径传nil；不在普通业务里sleep。
func (s *store) place(ctx context.Context, conn *sql.Conn, id int, key, mode string, qty int, beforeCAS func() error) error {
	// qty/id/key都是caller传入的值副本；数量不允许<=0，否则stock-负数会加库存。
	// 本课批量请求用qty1，quantityCase另以qty2和非法qty0核对这条业务输入边界。
	if qty <= 0 || id <= 0 || key == "" {
		return errors.New("订单id/key/qty必须合法且qty>0")
	}
	return transact(ctx, conn, func(tx *sql.Tx) error {
		var result sql.Result
		var err error
		switch mode {
		case "atomic":
			// 条件在持行锁后的当前值上判断，stock>=qty与stock-qty是一条UPDATE；
			// 第二个请求等锁后看到stock0，因此影响0行，不会扣到负数。
			result, err = tx.ExecContext(ctx, "UPDATE `"+s.stock+"` SET stock=stock-?,version=version+1 WHERE id=? AND stock>=?", qty, id, qty)
		case "locking":
			var available int
			// FOR UPDATE是锁定当前读；直到此Tx结束前，另一写者不能改此行。
			// 读后检查仍是Go分支，但现在这项事实受本事务行锁保护。
			if err = tx.QueryRowContext(ctx, "SELECT stock FROM `"+s.stock+"` WHERE id=? FOR UPDATE", id).Scan(&available); err != nil {
				return err
			}
			if available < qty {
				return ErrNoStock
			}
			result, err = tx.ExecContext(ctx, "UPDATE `"+s.stock+"` SET stock=stock-?,version=version+1 WHERE id=?", qty, id)
		case "cas":
			var available, version int
			if err = tx.QueryRowContext(ctx, "SELECT stock,version FROM `"+s.stock+"` WHERE id=?", id).Scan(&available, &version); err != nil {
				return err
			}
			if available < qty {
				return ErrNoStock
			}
			if beforeCAS != nil {
				if err = beforeCAS(); err != nil {
					return err
				}
			}
			// version是读到的int副本。若别人写过，version不再匹配，即使库存
			// 仍够也返回0；当前Tx必须退出回滚后重新读，不能反复用旧version。
			result, err = tx.ExecContext(ctx, "UPDATE `"+s.stock+"` SET stock=stock-?,version=version+1 WHERE id=? AND stock>=? AND version=?", qty, id, qty, version)
		default:
			return errors.New("未知库存方案")
		}
		if err != nil {
			return err
		}
		n, err := result.RowsAffected() // Result是驱动结果接口；影响行数不等于提交成功。
		if err != nil {
			return err
		}
		if n == 0 {
			if mode == "cas" {
				return ErrCAS
			}
			return ErrNoStock
		}
		if n != 1 {
			return fmt.Errorf("应精确扣1行，实际%d", n)
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO `"+s.orders+"`(request_id,sku,qty) VALUES (?,?,?)", key, id, qty)
		return err // INSERT失败传播到transact，退出Rollback撤销库存扣减。
	})
}

// quantityCase由stockCases调用，给qty参数一个真实数量边界而不是只讲通用公式。
// 初始3件先买2件；第二笔再买2件应0行且无订单；qty0在Begin之前拒绝，独立
// audit保持stock1/orders1/qty2，证明成功量守恒按数量而非仅按订单条数计。
func (s *store) quantityCase(ctx context.Context) (err error) {
	if err = s.seedStock(ctx, 60, 3); err != nil {
		return err
	}
	c, e := acquire(ctx, s.db, "READ-COMMITTED")
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, c.release()) }()
	if err = s.place(ctx, c.conn, 60, "qty-two", "atomic", 2, nil); err != nil {
		return err
	}
	e = s.place(ctx, c.conn, 60, "qty-two-insufficient", "atomic", 2, nil)
	var cleanup *txCleanupFailure
	if !errors.Is(e, ErrNoStock) || errors.As(e, &cleanup) {
		return fmt.Errorf("剩1件买2应NoStock且清理成功，实际%w", e)
	}
	if e = s.place(ctx, c.conn, 60, "qty-zero", "atomic", 0, nil); e == nil {
		return errors.New("qty0未被拒绝")
	}
	return s.audit(ctx, "quantity.guard", 60, 1, 1, 2)
}
