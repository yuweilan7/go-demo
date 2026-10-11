package course

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// stats 只由 owner 调用。WaitCount/WaitDuration 是池累计计数/耗时，不是当前
// waiter 数/SQL运行耗时。独占本池且每次只启动一个借池者，才可以精确解释 delta。
func (current *lesson) stats(label string, before sql.DBStats) sql.DBStats {
	after := current.db.Stats()
	fmt.Fprintf(current.out, "[Stats %s] open=%d inuse=%d idle=%d WaitCount=%d delta=%d WaitDuration=%s delta=%s\n",
		label, after.OpenConnections, after.InUse, after.Idle, after.WaitCount,
		after.WaitCount-before.WaitCount, after.WaitDuration, after.WaitDuration-before.WaitDuration)
	return after
}

// connScope 由 Run 调用，展示 DB.Conn 获得“独占特定连接”的scope；Conn.Close
// 把它交还池，并不关闭整个DB。Java JDBC Connection类比适合这里，不适合*sql.DB。
func (current *lesson) connScope(ctx context.Context) (err error) {
	conn, err := current.db.Conn(ctx)
	if err != nil {
		return err
	}
	closed := false
	// Conn.Close重复调用会返回ErrConnDone；closed由同一owner goroutine读写，
	// 正常路径已关闭就不重复关闭，提前返回时defer仍负责归还连接。
	defer func() {
		if !closed {
			err = errors.Join(err, conn.Close())
		}
	}()
	var firstID, secondID int64
	if err = conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&firstID); err != nil {
		return err
	}
	if err = conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&secondID); err != nil {
		return err
	}
	if err = require(firstID == secondID && current.db.Stats().InUse == 1,
		"同一Conn两次SQL必须使用同一服务端连接，且池InUse=1"); err != nil {
		return err
	}
	fmt.Fprintf(current.out, "[Conn] connection_id=%d/%d InUse=1；owner关闭Conn后归还池\n", firstID, secondID)
	if err = conn.Close(); err != nil {
		return err
	}
	closed = true
	return require(current.db.Stats().InUse == 0, "Conn.Close后InUse必须为0")
}

// rowsPool 故意只读取第一行而不继续 Next/Close，使 Rows 持有唯一连接。
// Rows由本函数拥有。两个worker由本函数取消并join；每个提前返回也经过同样收尾。
func (current *lesson) rowsPool(ctx context.Context) (err error) {
	rows, err := current.db.QueryContext(ctx, "SELECT id,quantity FROM "+current.table+" ORDER BY id")
	if err != nil {
		return err
	}
	// 真实危险行为仅持续于下面有界实验；这个defer是异常路径的安全兜底。
	defer func() { err = errors.Join(err, rows.Close(), rows.Err()) }()
	if !rows.Next() {
		return errors.Join(errors.New("Rows反例缺少第一行"), rows.Err())
	}
	var id, quantity int64
	if err = rows.Scan(&id, &quantity); err != nil {
		return err
	}
	before := current.db.Stats()
	if err = require(before.InUse == 1 && before.MaxOpenConnections == 1, "未关闭Rows必须持有唯一连接"); err != nil {
		return err
	}
	fmt.Fprintf(current.out, "[危险Rows] 仅Scan id=%d quantity=%d；第二行未消费，Rows尚未Close\n", id, quantity)
	// 这个UPDATE若拿到连接会有副作用。Stats确认它确实排队，deadline让它退出；
	// 最后SELECT实际值，不能只根据DeadlineExceeded推测数据库没有执行它。
	blocked := startTask(ctx, 1500*time.Millisecond, func(workCtx context.Context) outcome {
		result, execErr := current.db.ExecContext(workCtx, "UPDATE "+current.table+" SET quantity=? WHERE id=?", 901, 1)
		if execErr != nil {
			return outcome{Err: execErr}
		}
		affected, resultErr := result.RowsAffected()
		return outcome{Value: affected, Err: resultErr}
	})
	defer blocked.stopAndJoin(&err)
	if err = observe(ctx, "Rows持池后的WaitCount+1", func() (bool, error) {
		return current.db.Stats().WaitCount == before.WaitCount+1, nil
	}); err != nil {
		return err
	}
	queued := current.stats("Rows排队屏障", before)
	if err = require(queued.InUse == 1 && queued.WaitDuration == before.WaitDuration,
		"正在排队时仍InUse=1，WaitDuration在退出等待前未累计这一段"); err != nil {
		return err
	}
	result, err := joinWithin(blocked)
	if err != nil {
		return err
	}
	if err = require(errors.Is(result.Err, context.DeadlineExceeded), "Rows借池worker必须在deadline退出"); err != nil {
		return errors.Join(err, result.Err)
	}
	afterTimeout := current.stats("Rows借池timeout", before)
	if err = require(afterTimeout.WaitCount-before.WaitCount == 1 && afterTimeout.WaitDuration > before.WaitDuration &&
		afterTimeout.InUse == 1, "一个timeout计一次池等待，Rows还未释放连接"); err != nil {
		return err
	}
	// 第二个worker同样先确认已排队，再主动Close Rows。先后由Stats屏障保证，
	// 不是“sleep一会儿所以大概开始了”。这次等待最终成功，而非timeout。
	recovered := startTask(ctx, 5*time.Second, func(workCtx context.Context) outcome {
		value, readErr := current.quantity(workCtx, 1)
		return outcome{Value: value, Err: readErr}
	})
	defer recovered.stopAndJoin(&err)
	if err = observe(ctx, "第二个Rows借池者排队", func() (bool, error) {
		return current.db.Stats().WaitCount == afterTimeout.WaitCount+1, nil
	}); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	result, err = joinWithin(recovered)
	if err != nil || result.Err != nil {
		return errors.Join(err, result.Err)
	}
	final := current.stats("Rows.Close恢复", before)
	if err = require(result.Value == 101 && final.InUse == 0 && final.WaitCount-before.WaitCount == 2 &&
		final.WaitDuration >= afterTimeout.WaitDuration, "Close后第二worker读到101、池归还，等待计数准确且累计耗时不回退"); err != nil {
		return err
	}
	// WaitCount在标准库建立连接请求时增加，WaitDuration在等待分支结束时累加。
	// 第二次看到计数后立即Close时，等待可能短到计时精度下为0；它不是保证正耗时的
	// 业务不变量。第一次deadline已证明正等待，第二次应核对计数/不回退/恢复事实。
	fmt.Fprintf(current.out, "[Rows修复/副作用SELECT] quantity=%d；timeout UPDATE未改变实际表值；Rows.Close使排队者恢复\n", result.Value)
	return nil
}

// txPool 展示业务函数拿Tx后又错误调用外层DB。Tx已经预留唯一连接，
// db.ExecContext试图另借一个连接；它不会自动参加tx，MaxOpen=1时只会排队。
func (current *lesson) txPool(ctx context.Context) (err error) {
	tx, err := current.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rollback(tx)) }()
	if _, err = tx.ExecContext(ctx, "UPDATE "+current.table+" SET quantity=? WHERE id=?", 777, 1); err != nil {
		return err
	}
	before := current.db.Stats()
	blocked := startTask(ctx, 1200*time.Millisecond, func(workCtx context.Context) outcome {
		result, execErr := current.db.ExecContext(workCtx, "UPDATE "+current.table+" SET quantity=? WHERE id=?", 888, 1)
		if execErr != nil {
			return outcome{Err: execErr}
		}
		affected, resultErr := result.RowsAffected()
		return outcome{Value: affected, Err: resultErr}
	})
	defer blocked.stopAndJoin(&err)
	if err = observe(ctx, "Tx内错误使用DB后的排队", func() (bool, error) {
		return current.db.Stats().WaitCount == before.WaitCount+1, nil
	}); err != nil {
		return err
	}
	current.stats("Tx持连接，DB另借池", before)
	result, err := joinWithin(blocked)
	if err != nil {
		return err
	}
	if err = require(errors.Is(result.Err, context.DeadlineExceeded), "外层DB更新必须借池timeout"); err != nil {
		return errors.Join(err, result.Err)
	}
	// 修复不是盲目加池大小。事务内工作必须继续用tx，才保持同一连接和事务边界。
	var inTransaction int64
	if err = tx.QueryRowContext(ctx, "SELECT quantity FROM "+current.table+" WHERE id=?", 1).Scan(&inTransaction); err != nil {
		return err
	}
	after := current.stats("改用Tx查询", before)
	if err = require(inTransaction == 777 && after.WaitCount-before.WaitCount == 1 && after.InUse == 1 &&
		after.WaitDuration > before.WaitDuration, "Tx读到自己未提交的777且没有产生第二次借池等待"); err != nil {
		return err
	}
	if err = rollback(tx); err != nil {
		return err
	}
	actual, err := current.quantity(ctx, 1)
	if err != nil {
		return err
	}
	if err = require(actual == 101 && current.db.Stats().InUse == 0, "Rollback后的SELECT恢复101且DB可以再借连接"); err != nil {
		return err
	}
	fmt.Fprintf(current.out, "[Tx修复/副作用SELECT] Tx内=%d；Rollback后DB SELECT=%d；错误DB UPDATE未改变持久值\n", inTransaction, actual)
	return nil
}

// rowsCancellation 在一个正常的查询ctx中先读第一行，再取消并观察自动Close。
// 等待InUse=0后才调用Next，避免把“cancel已通知”误当成Rows watcher已经观察通知。
func (current *lesson) rowsCancellation(ctx context.Context) (err error) {
	queryCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	rows, err := current.db.QueryContext(queryCtx, "SELECT id FROM "+current.table+" ORDER BY id")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if !rows.Next() {
		return errors.Join(errors.New("取消实验缺少第一行"), rows.Err())
	}
	var id int64
	if err = rows.Scan(&id); err != nil {
		return err
	}
	cancel() // 关闭queryCtx.Done；Rows.awaitDone稍后观察通知并关闭Rows/释放连接。
	if err = observe(ctx, "Rows取消后连接释放", func() (bool, error) {
		return current.db.Stats().InUse == 0, nil
	}); err != nil {
		return err
	}
	if err = require(!rows.Next() && errors.Is(rows.Err(), context.Canceled), "取消后的Next必须false且Rows.Err=Canceled"); err != nil {
		return err
	}
	actual, err := current.quantity(ctx, 1)
	if err != nil {
		return err
	}
	if err = require(actual == 101, "取消只读Rows后的业务表值保持101，池恢复"); err != nil {
		return err
	}
	fmt.Fprintf(current.out, "[Rows取消] 已读取id=%d；cancel→Rows.awaitDone→InUse=0→Next=false/Err=Canceled；SELECT quantity=%d\n", id, actual)
	return nil
}
