package lockcourse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// snapshotCases 由Run调用，依次独立证明RC和RR。同一A Tx先普通读；B另一个
// 会话提交更新；A第二次普通读，再FOR UPDATE当前读，再普通读。没有并发sleep：
// B的Commit返回是A下一阶段的显式屏障，两Conn同时借出保证不同物理会话。
func (s *store) snapshotCases(ctx context.Context) (err error) {
	for i, iso := range []string{"READ-COMMITTED", "REPEATABLE-READ"} {
		if err = s.snapshotOne(ctx, 20+i, iso); err != nil {
			return err
		}
	}
	return nil
}

// snapshotOne 拥有A/B两会话和A Tx，caller给隔离级别（已白名单校验）。SET SESSION
// 后读取@@transaction_isolation确认实际值；不把BeginTx选项误读为会话变量改变。
// 所有defer LIFO保证先Rollback Tx、再恢复session、最后Conn归池。
func (s *store) snapshotOne(ctx context.Context, id int, iso string) (err error) {
	if err = s.seedStock(ctx, id, 10); err != nil {
		return err
	}
	if err = s.explain(ctx, "snapshot."+iso, "SELECT stock FROM `"+s.stock+"` WHERE id="+fmt.Sprint(id)+" FOR UPDATE", "PRIMARY", "const"); err != nil {
		return err
	}
	a, e := acquire(ctx, s.db, iso)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, a.release()) }()
	b, e := acquire(ctx, s.db, "READ-COMMITTED")
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, b.release()) }()
	if a.id == b.id {
		return errors.New("隔离实验A/B会话重复")
	}
	tx, e := a.conn.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, rollback(tx)) }()
	query := "SELECT stock FROM `" + s.stock + "` WHERE id=?"
	var first, second, current, third int
	if err = tx.QueryRowContext(ctx, query, id).Scan(&first); err != nil {
		return err
	}
	// B使用另一固定Conn，完整提交之后才允许A继续；普通一致性读没有阻塞B。
	if err = transact(ctx, b.conn, func(bt *sql.Tx) error {
		_, e := bt.ExecContext(ctx, "UPDATE `"+s.stock+"` SET stock=20 WHERE id=?", id)
		return e
	}); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, query, id).Scan(&second); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, query+" FOR UPDATE", id).Scan(&current); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, query, id).Scan(&third); err != nil {
		return err
	}
	want := 20
	if iso == "REPEATABLE-READ" {
		want = 10
	}
	fmt.Fprintf(s.out, "[snapshot.%s] connection=%d,%d plain=%d -> B COMMIT20 -> plain=%d locking=%d plain_after_lock=%d\n", iso, a.id, b.id, first, second, current, third)
	// RR的普通读仍用第一次read view；锁定读20没有把旧快照自动“升级成20”。
	if first != 10 || second != want || current != 20 || third != want {
		return errors.New("隔离读值不符合本课预期")
	}
	return nil
}

// indexCases 实际记录PK/secondary索引及EXPLAIN，再对比同一RR/RC范围、RR唯一
// 等值查找和缺索引全扫描。探针都在holder未结束时完成或真实1205，最后独立
// SELECT查写入结果。不能仅依据“RR必有gap”或WHERE文案推断锁范围。
func (s *store) indexCases(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "INSERT INTO `"+s.ranges+"`(id) VALUES (10),(20),(30)"); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO `"+s.scan+"`(id,bucket) VALUES (10,1),(20,2),(30,3)"); err != nil {
		return err
	}
	rangeSQL := "SELECT id FROM `" + s.ranges + "` WHERE id>=10 AND id<20 FOR UPDATE"
	if err := s.explain(ctx, "range.pk", rangeSQL, "PRIMARY", "range"); err != nil {
		return err
	}
	for _, iso := range []string{"REPEATABLE-READ", "READ-COMMITTED"} {
		label := "gap." + iso
		if err := s.lockProbe(ctx, label, iso, rangeSQL, "INSERT INTO `"+s.ranges+"`(id) VALUES (15)", iso == "REPEATABLE-READ"); err != nil {
			return err
		}
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+s.ranges+"` WHERE id=15").Scan(&count); err != nil {
			return err
		}
		want := 1
		if iso == "REPEATABLE-READ" {
			want = 0
		}
		if count != want {
			return fmt.Errorf("%s INSERT事实count=%d", label, count)
		}
		fmt.Fprintf(s.out, "[%s.SELECT] id15_count=%d\n", label, count)
		if _, err := s.db.ExecContext(ctx, "DELETE FROM `"+s.ranges+"` WHERE id=15"); err != nil {
			return err
		}
	}
	uniqueSQL := "SELECT id FROM `" + s.ranges + "` WHERE id=10 FOR UPDATE"
	if err := s.explain(ctx, "unique.pk", uniqueSQL, "PRIMARY", "const"); err != nil {
		return err
	}
	if err := s.lockProbe(ctx, "unique.RR", "REPEATABLE-READ", uniqueSQL, "INSERT INTO `"+s.ranges+"`(id) VALUES (15)", false); err != nil {
		return err
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+s.ranges+"` WHERE id=15").Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("唯一等值锁旁的插入未留下")
	}
	fmt.Fprintln(s.out, "[unique.RR.SELECT] id15_count=1，holder仍在时插入已完成")
	scanSQL := "SELECT id FROM `" + s.scan + "` WHERE bucket=1 FOR UPDATE"
	if err := s.explain(ctx, "noindex", scanSQL, "", "ALL"); err != nil {
		return err
	}
	if err := s.lockProbe(ctx, "noindex.RR", "REPEATABLE-READ", scanSQL, "UPDATE `"+s.scan+"` SET value=1 WHERE id=30", true); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "CREATE INDEX ix_bucket ON `"+s.scan+"`(bucket)"); err != nil {
		return err
	}
	// 三行小表可能被优化器继续全扫。明确FORCE INDEX是本修复实验的执行条件，
	// 不是生产里一律加hint；真实EXPLAIN必须和声称的锁范围一致。
	indexedSQL := "SELECT id FROM `" + s.scan + "` FORCE INDEX(ix_bucket) WHERE bucket=1 FOR UPDATE"
	if err := s.explain(ctx, "indexed", indexedSQL, "ix_bucket", "ref"); err != nil {
		return err
	}
	if err := s.lockProbe(ctx, "indexed.RR", "REPEATABLE-READ", indexedSQL, "UPDATE `"+s.scan+"` SET value=1 WHERE id=30", false); err != nil {
		return err
	}
	var value int
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM `"+s.scan+"` WHERE id=30").Scan(&value); err != nil {
		return err
	}
	if value != 1 {
		return errors.New("索引修复后无关行UPDATE未生效")
	}
	var indexes string
	if err := s.db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(DISTINCT INDEX_NAME ORDER BY INDEX_NAME) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", s.scan).Scan(&indexes); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "[indexed.SELECT] id30_value=%d actual_indexes=%s\n", value, indexes)
	return nil
}

// lockProbe 的caller给holder查询和独占表探针SQL。holder owns Conn/Tx；worker
// owns另一Conn，autocommit探针只在自己的id上操作。owner在结果/Finished之前
// 不释放holder；因此1205确实来自相应锁范围，而成功是持锁期间实际成功。
func (s *store) lockProbe(ctx context.Context, label, iso, holderSQL, probeSQL string, wantTimeout bool) (err error) {
	h, e := acquire(ctx, s.db, iso)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, h.release()) }()
	tx, e := h.conn.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, rollback(tx)) }()
	rows, e := tx.QueryContext(ctx, holderSQL)
	if e != nil {
		return e
	}
	// 读完并Close Rows只释放协议结果，不释放Tx行锁；Tx仍由本函数持有。
	for rows.Next() {
		var id int
		if e = rows.Scan(&id); e != nil {
			break
		}
	}
	e = errors.Join(e, rows.Err(), rows.Close())
	if e != nil {
		return e
	}
	var own scope
	defer own.close(&err)
	ready := make(chan int64, 1)
	marker := s.prefix + "_" + strings.NewReplacer(".", "_", "-", "_").Replace(label)
	w := own.start(ctx, func(ctx context.Context) (r taskResult) {
		defer captureTaskPanic(&r)
		c, e := acquire(ctx, s.db, "READ-COMMITTED")
		if e != nil {
			r.Err = e
			return
		}
		defer func() { r.Cleanup = errors.Join(r.Cleanup, c.release()) }()
		ready <- c.id
		_, r.Err = c.conn.ExecContext(ctx, "/* "+marker+" */ "+probeSQL)
		return
	})
	id, e := receive(ctx, ready)
	if e != nil {
		return e
	}
	if id == h.id {
		return errors.New("holder/probe连接重复")
	}
	if wantTimeout {
		if err = s.waitSQL(ctx, id, marker); err != nil {
			return err
		}
	}
	r, e := w.join(ctx)
	if e != nil {
		return e
	}
	if r.Cleanup != nil {
		return r.Cleanup
	}
	if wantTimeout {
		if code(r.Err) != 1205 {
			return fmt.Errorf("%s应1205实际%w", label, r.Err)
		}
	} else if r.Err != nil {
		return r.Err
	}
	fmt.Fprintf(s.out, "[%s.probe] holder_id=%d worker_id=%d mysql_code=%d holder_Tx仍未释放 Finished=true\n", label, h.id, id, code(r.Err))
	return nil
}
