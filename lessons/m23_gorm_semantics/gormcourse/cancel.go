package gormcourse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// waitSQL由cancelCase的owner调用；PROCESSLIST是服务器的可观察屏障。
// ticker只控制轮询频率，是否继续取决于真实INFO，而非“睡够时间就猜worker在等锁”。
// gone=true时等待原会话真正消失，单次普通SELECT看不见未提交写入还不够。
func (l *lab) waitSQL(ctx context.Context, id int64, gone bool) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var info string
		err := l.pool.QueryRowContext(ctx, "SELECT COALESCE(INFO,'') FROM information_schema.PROCESSLIST WHERE ID=?", id).Scan(&info)
		if gone && errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(l.out, "[cancel.server-gone] id=%d\n", id)
			return nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !gone && strings.Contains(info, l.tables[0]) && strings.Contains(info, "m23_cancel_lock") {
			fmt.Fprintf(l.out, "[cancel.server-wait] id=%d INFO含自有表及m23_cancel_lock\n", id)
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// cancelCase创建holder与worker两个事务，owner在第三条连接观察真实行锁等待。
// worker明确把GORM.Statement.ConnPool换成专属*sql.Conn，然后Begin绑定该连接的Tx。
// 两个*gorm.DB包装的是不同连接所有权；WithContext/Session本身不创建事务或新池。
func (l *lab) cancelCase(ctx context.Context) (err error) {
	if err = l.seed(ctx, "cancel", "cancel", 5); err != nil {
		return err
	}
	holder := l.db.WithContext(ctx).Begin()
	if holder.Error != nil {
		return holder.Error
	}
	holderRaw, ok := holder.Statement.ConnPool.(*sql.Tx)
	if !ok {
		return errors.New("holder不是sql.Tx")
	}
	holderEnded := false // 此变量只由owner读写；提前失败和正常解除屏障都靠同一owner。
	defer func() {
		if !holderEnded {
			err = errors.Join(err, rollback(holderRaw))
		}
	}()
	var locked Stock
	if err = holder.Clauses(clause.Locking{Strength: "UPDATE"}).Where("sku = ?", "cancel").First(&locked).Error; err != nil {
		return err
	}
	// 真实超时预算从worker创建时计时，先有服务器屏障再观察超时；不sleep控制调度。
	workCtx, deadlineCancel := context.WithTimeout(ctx, 3*time.Second)
	defer deadlineCancel()
	idReady := make(chan int64, 1) // worker发出专属连接ID；容量1避免owner失败时卡在发送。
	task := startOwned(workCtx, func(workerCtx context.Context) (res workResult) {
		// 同一函数边界恢复panic：后注册的Conn.Close先执行并写CleanupErr，随后
		// 本defer写Err，两个事实都留在具名res；wrapper不可能替fn恢复丢失的返回槽。
		defer func() {
			if value := recover(); value != nil {
				if cause, ok := value.(error); ok {
					res.Err = fmt.Errorf("SQL worker panic: %w", cause)
				} else {
					res.Err = fmt.Errorf("SQL worker panic: %v", value)
				}
			}
		}()
		conn, e := l.pool.Conn(workerCtx)
		if e != nil {
			res.Err = e
			return res
		}
		defer func() { res.CleanupErr = errors.Join(res.CleanupErr, conn.Close()) }()
		var id int64
		if e = conn.QueryRowContext(workerCtx, "SELECT CONNECTION_ID()").Scan(&id); e != nil {
			res.Err = e
			return res
		}
		idReady <- id
		db := l.db.Session(&gorm.Session{NewDB: true, Context: workerCtx})
		db.Statement.ConnPool = conn // 同步创建的session私有Statement，不能改l.db的共享Statement。
		res.Err = manualTx(db, func(tx *gorm.DB) error {
			if err := l.insertOrder(tx, "cancel", "cancel"); err != nil {
				return err
			}
			// holder持有此行；这里实际阻塞在driver ExecContext等待服务端行锁结果。
			return tx.Exec("UPDATE "+quote(l.tables[0])+" SET quantity=quantity-1 WHERE sku=? /*m23_cancel_lock*/", "cancel").Error
		})
		return res
	}, nil)
	// 后注册先执行：提前失败先取消，再放holder，最后真join worker。
	// 主动解除自有服务端屏障，且Join原失败/放门失败，不依赖外层defer偶然先执行。
	defer func() {
		if !task.joined {
			task.cancel()
			if !holderEnded {
				e := rollback(holderRaw)
				holderEnded = true
				err = errors.Join(err, e)
			}
			err = task.stopAndJoin(err)
		}
	}()
	var id int64
	select {
	case id = <-idReady:
	case <-ctx.Done():
		return ctx.Err()
	case <-task.done:
		res, e := task.join(context.Background())
		return errors.Join(res.Err, res.CleanupErr, e, errors.New("worker在登记连接ID前退出"))
	}
	observeCtx, observeCancel := context.WithTimeout(ctx, 2*time.Second)
	err = l.waitSQL(observeCtx, id, false)
	observeCancel()
	if err != nil {
		return err
	}
	joinCtx, joinCancel := context.WithTimeout(context.Background(), 8*time.Second)
	res, joinErr := task.join(joinCtx) // 真等wrapper最终Done，不能因为结果到达先结束。
	joinCancel()
	if joinErr != nil {
		return joinErr
	}
	if !errors.Is(res.Err, context.DeadlineExceeded) || res.CleanupErr != nil {
		return errors.Join(res.Err, res.CleanupErr, errors.New("预期deadline与清理nil没有同时成立"))
	}
	fmt.Fprintln(l.out, "[cancel.client-done] work=DeadlineExceeded cleanup=nil Result与Done均已确认")
	err = rollback(holderRaw)
	holderEnded = true
	if err != nil {
		return err
	}
	// 客户端Done可能早于服务端检测断连。放holder后独立等原会话消失，再查账。
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer checkCancel()
	if err = l.waitSQL(checkCtx, id, true); err != nil {
		return err
	}
	if err = l.verify(checkCtx, "cancel.rollback", "cancel", "cancel", 5, 0); err != nil {
		return err
	}
	return require(l.pool.Stats().InUse == 0, "holder/worker退出后仍占池连接")
}
