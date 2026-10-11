package txcourse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// cancelCase 由 Run 调用：holder 在主池占一连接，workerDB 最多一连接，观察查询
// 另占主池一连接，总计最多3。只锁本 run 的 cancel SKU，不修改全局配置。
// 真正屏障是 PROCESSLIST 中 worker 的带标记 UPDATE，不能靠 sleep 猜“正在等锁”。
func (s *Store) cancelCase(ctx context.Context, c Config) (err error) {
	fmt.Fprintln(s.out, "=== E03 BeginTx ctx 取消：实际锁等待、独立 join、服务端查账 ===")
	r := Request{"cancel-key", "cancel", 1}
	if err := s.seed(ctx, r.SKU, 5); err != nil {
		return err
	}
	// 已取消的 ctx 在 BeginTx 前就被拒绝，没有创建一个可供回滚的 tx。
	canceledCtx, cancelBefore := context.WithCancel(ctx)
	cancelBefore()
	if tx, e := s.db.BeginTx(canceledCtx, nil); !errors.Is(e, context.Canceled) || tx != nil {
		if tx != nil {
			err = errors.Join(err, rollback(tx))
		}
		return errors.Join(err, errors.New("已取消 BeginTx 应返回 Canceled 和 nil Tx"))
	}
	if err := s.cancelIdle(ctx); err != nil {
		return err
	}
	holder, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rollback(holder)) }()
	var original int64
	if err := holder.QueryRowContext(ctx, "SELECT quantity FROM "+quote(s.stock)+" WHERE sku=? FOR UPDATE", r.SKU).Scan(&original); err != nil {
		return err
	}
	workerDB, err := openPool(c, "", 1)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, workerDB.Close()) }()
	idCh := make(chan int64, 1) // ID 消息不转移 Conn/Tx 所有权，也不代表任务完成。
	worker := startOwned(ctx, func(workCtx context.Context) (result taskResult) {
		conn, e := workerDB.Conn(workCtx)
		if e != nil {
			return taskResult{Err: e}
		}
		defer func() { result.CleanupErr = errors.Join(result.CleanupErr, conn.Close()) }()
		var id int64
		if e := conn.QueryRowContext(workCtx, "SELECT CONNECTION_ID()").Scan(&id); e != nil {
			return taskResult{Err: e}
		}
		tx, e := conn.BeginTx(workCtx, nil)
		if e != nil {
			return taskResult{Err: e}
		}
		defer func() { result.CleanupErr = errors.Join(result.CleanupErr, rollback(tx)) }()
		// 先在 Tx 内插入订单，制造已经发生但尚未提交的副作用；然后真正等行锁。
		if e := s.insertOrder(workCtx, tx, r); e != nil {
			return taskResult{Err: e}
		}
		idCh <- id
		_, e = tx.ExecContext(workCtx, "/*m21_cancel_lock*/ UPDATE "+quote(s.stock)+" SET quantity=quantity-1 WHERE sku=? AND quantity>=1", r.SKU)
		if e == nil {
			return taskResult{Err: errors.New("取消前 holder 应持锁，worker 不应成功完成 UPDATE")}
		}
		return taskResult{Err: fmt.Errorf("worker 真正 UPDATE 等待取消: %w", e)}
	}, nil)
	defer worker.stopAndJoin(&err) // 先于 workerDB.Close/holder.Rollback 执行。
	var id int64
	select {
	case id = <-idCh:
	case <-worker.finished:
		joinCtx, cancel := shortContext()
		defer cancel()
		return worker.join(joinCtx, errors.New("worker 未到达 SQL 前已失败"))
	case <-ctx.Done():
		return ctx.Err()
	}
	var state string
	if err := observe(ctx, "worker 真实 SQL 锁等待", func() (bool, error) {
		var info sql.NullString
		var currentState sql.NullString
		e := s.db.QueryRowContext(ctx, "SELECT INFO,STATE FROM information_schema.PROCESSLIST WHERE ID=?", id).Scan(&info, &currentState)
		if errors.Is(e, sql.ErrNoRows) {
			return false, nil
		}
		state = currentState.String
		return info.Valid && strings.Contains(info.String, "m21_cancel_lock") && strings.Contains(info.String, s.stock), e
	}); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "[cancel.barrier] server_connection=%d marked_UPDATE=true state=%s\n", id, state)
	worker.cancel() // 只通知；不能在这里宣称服务器已回滚或 wrapper 已退出。
	joinCtx, cancelJoin := shortContext()
	joinedErr := worker.join(joinCtx, nil)
	cancelJoin()
	if !worker.joined || !errors.Is(joinedErr, context.Canceled) || errors.Is(joinedErr, ErrJoin) || worker.joinedCleanup != nil {
		return errors.Join(errors.New("取消结果/Finished join 不符合契约"), joinedErr)
	}
	// database/sql awaitDone 可能先回滚，故此处 Commit 可以返回 Canceled 或 ErrTxDone。
	// 本实验没有再提交取消事务；用源码契约解释竞态，不断言哪种 Go 返回值必然胜出。
	if err := rollback(holder); err != nil {
		return err
	}
	cleanupCtx, cancelCleanup := shortContext()
	defer cancelCleanup()
	if err := observe(cleanupCtx, "被取消的服务端连接消失", func() (bool, error) {
		var count int
		e := s.db.QueryRowContext(cleanupCtx, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE ID=?", id).Scan(&count)
		return count == 0, e
	}); err != nil {
		return err
	}
	// 不以 Cancel/ErrTxDone 为回滚证据：连接消失后独立 SELECT 没有订单、库存仍5。
	if err := s.verify(cleanupCtx, "cancel.server-rollback", r, original, 0); err != nil {
		return err
	}
	if workerDB.Stats().InUse != 0 {
		return errors.New("Finished 后 workerDB 仍持连接")
	}
	fmt.Fprintln(s.out, "[cancel.finished] Canceled retained; cleanup_error=nil; worker Finished=true; server connection gone; worker inuse=0")
	return nil
}

// cancelIdle 展示 BeginTx 的 ctx 控制“整个事务生命周期”，即使此刻没有 SQL I/O。
// caller 是 cancelCase；本函数拥有 Conn/Tx/child ctx，先制造两步未提交写入，再
// 取消并实际调用 Commit。database/sql 本地预检查与 awaitDone 竞争，允许两种错误。
func (s *Store) cancelIdle(ctx context.Context) (err error) {
	r := Request{"cancel-idle-key", "cancel-idle", 1}
	if err := s.seed(ctx, r.SKU, 5); err != nil {
		return err
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, err := s.db.Conn(workCtx)
	if err != nil {
		return err
	}
	connClosed := false
	defer func() {
		if !connClosed {
			err = errors.Join(err, conn.Close())
		}
	}()
	tx, err := conn.BeginTx(workCtx, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rollback(tx)) }()
	if err := s.reserve(workCtx, tx, r); err != nil {
		return err
	}
	if err := s.insertOrder(workCtx, tx, r); err != nil {
		return err
	}
	cancel() // 本时刻两次 SQL 已返回，但 Tx 尚未结束。
	commitErr := tx.Commit()
	if !errors.Is(commitErr, context.Canceled) && !errors.Is(commitErr, sql.ErrTxDone) {
		return fmt.Errorf("已取消事务 Commit 应被本地拒绝: %w", commitErr)
	}
	// 显式 Rollback 与 awaitDone 可能竞争；ErrTxDone 不当作清理失败。
	if err := rollback(tx); err != nil {
		return err
	}
	// Conn.Close 等被 Tx 持有的连接归还。只看 Commit error 不足以确认这个完成边界。
	if err := conn.Close(); err != nil {
		return err
	}
	connClosed = true // Conn.Close 重复调用返回 ErrConnDone，保护 defer 不能制造假失败。
	if s.db.Stats().InUse != 0 {
		return errors.New("idle 取消后事务连接尚未释放")
	}
	cleanupCtx, stop := shortContext()
	defer stop()
	if err := s.verify(cleanupCtx, "cancel.idle-commit-refused", r, 5, 0); err != nil {
		return err
	}
	// 用锁定读实际确认本 SKU 能重新取锁，不能让 MVCC 普通读隐藏一个仍活跃的 Tx。
	probe, err := s.db.BeginTx(cleanupCtx, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rollback(probe)) }()
	var quantity int64
	if err := probe.QueryRowContext(cleanupCtx, "SELECT quantity FROM "+quote(s.stock)+" WHERE sku=? FOR UPDATE", r.SKU).Scan(&quantity); err != nil {
		return err
	}
	if quantity != 5 {
		return errors.New("idle 取消后锁定读不符合原库存")
	}
	fmt.Fprintf(s.out, "[cancel.idle] Commit=%v；独立SELECT无副作用；新事务FOR UPDATE成功\n", commitErr)
	return nil
}
