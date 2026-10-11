package lesson

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

// rollback在Tx owner的defer中调用；sql.ErrTxDone表示已经Commit/Rollback终结，
// 不把正常终结包装成失败。其他回滚错误与原错误Join，不能直接覆盖业务错误。
func rollback(tx *sql.Tx, retErr *error) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		*retErr = errors.Join(*retErr, fmt.Errorf("Tx回滚清理: %w", err))
	}
}

// acquireFenced是本课的权威“串行授予”协议，caller为staleFenced中的A/B。
// MySQL id=0行锁从发号直到SET回复与Commit，防止“先拿锁、停顿、后来才领更大号”。
// 这是一条刻意容易读清的协议：只有SET确定成功且持久序号Commit返回nil才交给worker。
// Tx占用一个真实连接；Redis请求也有deadline，因此不无限持有发号行锁。
func (l *lab) acquireFenced(ctx context.Context, client *redis.Client, key string, ttl time.Duration) (result acquisition, err error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return acquisition{State: rejected, Err: err}, err
	}
	defer rollback(tx, &err)
	// UPDATE是当前读并取得InnoDB排他行锁。各申请必须经过同一权威行，
	// 不能绕开这里从另一条Redis INCR或本机atomic拿“版本”。
	if _, err = tx.ExecContext(ctx, "UPDATE "+l.state+" SET issued_token=issued_token+1 WHERE id=0"); err != nil {
		return acquisition{State: rejected, Err: err}, err
	}
	var token int64
	if err = tx.QueryRowContext(ctx, "SELECT issued_token FROM "+l.state+" WHERE id=0").Scan(&token); err != nil {
		return acquisition{State: rejected, Err: err}, err
	}
	result = acquire(ctx, client, key, ttl)
	result.Lease.Fence = token
	if result.State != granted {
		// UNKNOWN可能留下自己的Redis租约，但不允许任何业务写入。独立预算比较释放，
		// 不用失败ctx做收尾、不删其他owner。发号事务随后回滚，失败尝试可不消耗号。
		var releaseErr error
		if result.Lease.Key != "" && result.Lease.Owner != "" {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, releaseErr = release(cleanup, client, result.Lease)
			cancel()
		}
		err = errors.Join(result.Err, releaseErr)
		return result, err
	}
	if err = tx.Commit(); err != nil {
		// Commit error不证明回滚。这个申请永远不把token交给业务，即便服务器已提交。
		// 后续新的确定授予可有更大号；独立读取/恢复属于生产协议，不盲重试旧写。
		result.State, result.Err = unknown, fmt.Errorf("权威授予Commit结果未知: %w", err)
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, releaseErr := release(cleanup, client, result.Lease)
		cancel()
		err = errors.Join(result.Err, releaseErr)
		return result, err
	}
	if !time.Now().Before(result.Lease.Until) {
		// 权威号已持久保存，但Commit耗时也会吃租约窗口。过期后不能把这个申请
		// 作为确定的有效租约交给worker；保留已消耗的号，安全释放并报告UNKNOWN。
		result.State, result.Err = unknown, errors.New("授予Commit结束时保守租约期限已过")
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, releaseErr := release(cleanup, client, result.Lease)
		cancel()
		err = errors.Join(result.Err, releaseErr)
		return result, err
	}
	return result, nil
}

// writeValue是A/B向下游MySQL提交的真实副作用，row由实验选择，value是业务值。
// guarded=false故意没有拒旧谓词；guarded=true把比较和修改放在同一条UPDATE里。
// SQL参数?绑定值，表名来自严格验证过的run标识符；数据库不读取Redis owner。
func (l *lab) writeValue(ctx context.Context, row int, value string, token int64, guarded bool) (int64, error) {
	statement := "UPDATE " + l.state + " SET value_text=?, effects=effects+1 WHERE id=?"
	// []any把字符串、int、int64放在同一参数slice里；any是interface{}的别名。
	// args...在ExecContext调用处展开这些值，驱动按?位置绑定，不是拼接SQL文本。
	args := []any{value, row}
	if guarded {
		statement = "UPDATE " + l.state + " SET value_text=?, effects=effects+1, last_token=? WHERE id=? AND last_token<?"
		args = []any{value, token, row, token}
	}
	result, err := l.db.ExecContext(ctx, statement, args...)
	if err != nil {
		return 0, err
	}
	// RowsAffected=0是拒旧/不存在，不能直接称成功。本课随后SELECT已存在行的值、
	// effects、last_token，独立证明旧写被拒且B的业务状态仍然保存。
	return result.RowsAffected()
}

// readValue由owner在worker Finished之后调用，读取持久业务状态而非任务返回文案。
// QueryRow.Scan同步消费唯一一行并归还内部Rows，本函数不持有可逃逸的Rows/Conn。
func (l *lab) readValue(ctx context.Context, row int) (value string, effects int, token int64, err error) {
	err = l.db.QueryRowContext(ctx, "SELECT value_text,effects,last_token FROM "+l.state+" WHERE id=?", row).Scan(&value, &effects, &token)
	// 返回类型中的名字是函数局部结果槽。这里省略return表达式，返回的就是Scan
	// 填好的四个槽；不是只返回err，也没有启动另一个SQL goroutine。
	return
}

// placeOrder是修复版下单usecase：Tx拥有INSERT唯一订单与库存条件扣减。
// Redis锁可以减少并发冲突，但唯一业务键与库存守恒由这一数据库事务维护。
func (l *lab) placeOrder(ctx context.Context, orderID string) (duplicate bool, err error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer rollback(tx, &err)
	_, err = tx.ExecContext(ctx, "INSERT INTO "+l.orders+" (order_id,units) VALUES (?,?)", orderID, 1)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			// 本课同一orderID代表同一固定载荷。真实API还应核对载荷/存储回执，
			// 不能把任意唯一索引冲突都认成“这份请求已成功”。
			err = nil
			return true, nil // defer先回滚这个尚未扣库存的Tx，再把Duplicate交给owner。
		}
		return false, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE "+l.state+" SET stock=stock-1,effects=effects+1 WHERE id=4 AND stock>=1")
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows != 1 {
		return false, errors.New("库存不足，事务必须回滚订单记录")
	}
	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("下单Commit结果未知，按orderID核验: %w", err)
	}
	return false, nil
}

// ordersExperiment由all入口调用。两个顺序请求都能取得锁，第一组仍重复扣库存；
// 第二组使用相同业务orderID，在真实Tx/唯一索引下只发生一次业务效果。
func (l *lab) ordersExperiment(ctx context.Context) error {
	key := l.key("order-lock")
	for attempt := 0; attempt < 2; attempt++ {
		current, err := requireLease(acquire(ctx, l.a, key, 5*time.Second))
		if err != nil {
			return err
		}
		_, writeErr := l.db.ExecContext(ctx, "UPDATE "+l.state+" SET stock=stock-1,effects=effects+1 WHERE id=3")
		_, releaseErr := release(ctx, l.a, current)
		if err = errors.Join(writeErr, releaseErr); err != nil {
			return err
		}
	}
	var stock, effects int
	if err := l.db.QueryRowContext(ctx, "SELECT stock,effects FROM "+l.state+" WHERE id=3").Scan(&stock, &effects); err != nil {
		return err
	}
	if err := l.check("lock_not_business_idempotency", stock == 0 && effects == 2, fmt.Sprintf("same logical order retried; stock=%d effects=%d; both leases valid", stock, effects)); err != nil {
		return err
	}
	duplicates := 0
	for attempt := 0; attempt < 2; attempt++ {
		current, err := requireLease(acquire(ctx, l.b, key, 5*time.Second))
		if err != nil {
			return err
		}
		duplicate, orderErr := l.placeOrder(ctx, "order-one")
		_, releaseErr := release(ctx, l.b, current)
		if err = errors.Join(orderErr, releaseErr); err != nil {
			return err
		}
		if duplicate {
			duplicates++
		}
	}
	var orders int
	if err := l.db.QueryRowContext(ctx, "SELECT stock,effects FROM "+l.state+" WHERE id=4").Scan(&stock, &effects); err != nil {
		return err
	}
	if err := l.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+l.orders+" WHERE order_id=?", "order-one").Scan(&orders); err != nil {
		return err
	}
	return l.check("sql_order_unique_and_stock", stock == 1 && effects == 1 && orders == 1 && duplicates == 1,
		fmt.Sprintf("stock=%d effects=%d orders=%d duplicate_receipts=%d", stock, effects, orders, duplicates))
}
