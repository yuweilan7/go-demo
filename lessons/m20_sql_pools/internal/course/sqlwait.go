package course

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// sqlWait 由 Run 调用，建立最多3个连接：current.db持行锁，workerDB执行UPDATE，
// observerDB读同账号自己的processlist。三个池互不混用Stats，不重启/改服务。
// 这一实验明确区分“已借到连接并在真实SQL I/O等待”与前面的“尚未借到连接”。
func (current *lesson) sqlWait(ctx context.Context, config Config) (err error) {
	workerDB, err := openPool(config)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, workerDB.Close()) }()
	observerDB, err := openPool(config)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, observerDB.Close()) }()
	holder, err := current.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rollback(holder)) }()
	var original int64
	// InnoDB行锁的owner是holder事务。Scan结束不会释放事务锁；Rollback/Commit
	// 才结束该事务。FOR UPDATE锁住精确id=1，实验不会锁其他run的表。
	if err = holder.QueryRowContext(ctx, "SELECT quantity FROM "+current.table+" WHERE id=? FOR UPDATE", 1).Scan(&original); err != nil {
		return err
	}
	connectionID := make(chan int64, 1)
	worker := startTask(ctx, 3*time.Second, func(workCtx context.Context) (result outcome) {
		// worker自己拥有Conn/Tx；连接ID交给owner只作诊断，不转移资源所有权。
		conn, openErr := workerDB.Conn(workCtx)
		if openErr != nil {
			return outcome{Err: openErr}
		}
		defer func() { result.CleanupErr = errors.Join(result.CleanupErr, conn.Close()) }()
		var id int64
		if openErr = conn.QueryRowContext(workCtx, "SELECT CONNECTION_ID()").Scan(&id); openErr != nil {
			return outcome{Err: openErr}
		}
		tx, beginErr := conn.BeginTx(workCtx, nil)
		if beginErr != nil {
			return outcome{Err: beginErr}
		}
		defer func() { result.CleanupErr = errors.Join(result.CleanupErr, rollback(tx)) }()
		connectionID <- id // buffer1确保owner提前离开时worker不被诊断消息卡住。
		_, result.Err = tx.ExecContext(workCtx, "/*m20_io_lock*/ UPDATE "+current.table+" SET quantity=? WHERE id=?", 404, 1)
		// 故意从不Commit。即使SQL与取消竞争导致执行了UPDATE，事务连接关闭/回滚
		// 后也必须没有持久变化。最终SELECT证明这一点，而非凭error猜测“没执行”。
		return result
	})
	defer func() {
		// 所有提前返回也先取消worker并解除holder行锁，再等待worker真正退出。
		// holder.Rollback不接收ctx，驱动write/read timeout仍为它提供I/O上限。
		worker.cancel()
		err = errors.Join(err, rollback(holder))
		worker.stopAndJoin(&err)
	}()
	var id int64
	select {
	case id = <-connectionID:
	case result := <-worker.result:
		// 这里仅提前得到业务结果，不是wrapper已完成。保存pending供defer中的
		// stopAndJoin使用；它仍必须新预算等finished，再合并wrapper清理错误。
		worker.pending = &result
		return errors.Join(errors.New("SQL worker未进入已建立事务阶段"), result.Err, result.CleanupErr)
	case <-ctx.Done():
		return ctx.Err()
	}
	var observedState string
	if err = observe(ctx, "服务端确实收到锁等待UPDATE", func() (bool, error) {
		var info sql.Null[string]
		lookupErr := observerDB.QueryRowContext(ctx,
			"SELECT STATE, INFO FROM information_schema.PROCESSLIST WHERE ID=?", id).Scan(&observedState, &info)
		if errors.Is(lookupErr, sql.ErrNoRows) {
			return false, errors.New("SQL worker在服务端屏障前已消失")
		}
		if lookupErr != nil {
			return false, lookupErr
		}
		return info.Valid && strings.Contains(info.V, "m20_io_lock") && strings.Contains(strings.ToUpper(info.V), "UPDATE"), nil
	}); err != nil {
		return err
	}
	stats := workerDB.Stats()
	if err = require(stats.InUse == 1 && stats.WaitCount == 0,
		"服务端UPDATE可见时worker池已借连接，InUse=1/WaitCount=0，阻塞不在借池"); err != nil {
		return err
	}
	fmt.Fprintf(current.out, "[SQL屏障] connection_id=%d processlist.State=%q UPDATE已到MySQL；workerDB InUse=%d WaitCount=%d\n",
		id, observedState, stats.InUse, stats.WaitCount)
	result, joinErr := joinWithin(worker)
	if joinErr != nil {
		return joinErr
	}
	if err = require(errors.Is(result.Err, context.DeadlineExceeded), "真实SQL行锁等待必须触发工作ctx deadline"); err != nil {
		return errors.Join(err, result.Err)
	}
	if result.CleanupErr != nil {
		return errors.Join(errors.New("SQL worker资源清理失败"), result.CleanupErr)
	}
	// 客户端已退出不自动证明服务端线程停止。先释放自己的holder，再用独立清理
	// ctx观察该连接ID在processlist消失；owner最后SELECT持久事实与资源恢复。
	if err = rollback(holder); err != nil {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err = observe(cleanupCtx, "取消的SQL连接在服务端结束", func() (bool, error) {
		var count int
		lookupErr := observerDB.QueryRowContext(cleanupCtx, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE ID=?", id).Scan(&count)
		return count == 0, lookupErr
	}); err != nil {
		return err
	}
	actual, err := current.quantity(cleanupCtx, 1)
	if err != nil {
		return err
	}
	if err = require(actual == original && actual == 101 && workerDB.Stats().InUse == 0,
		"SQL超时Tx未Commit，服务端连接消失后SELECT仍101且worker连接释放"); err != nil {
		return err
	}
	var recovered int
	if err = workerDB.QueryRowContext(cleanupCtx, "SELECT 1").Scan(&recovered); err != nil {
		return err
	}
	if err = require(recovered == 1 && workerDB.Stats().InUse == 0, "取消销毁的连接能被新连接替换，SQL池恢复"); err != nil {
		return err
	}
	fmt.Fprintf(current.out, "[SQL取消/副作用SELECT] error=DeadlineExceeded；worker Rollback/Conn.Close完成；processlist连接消失；quantity=%d；新连接SELECT 1=%d\n", actual, recovered)
	return nil
}
