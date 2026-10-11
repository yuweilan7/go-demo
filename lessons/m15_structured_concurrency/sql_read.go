package lesson

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SQLPriceFetcher是编译/阅读用adapter，本课没有实库driver、表或SQL执行验收。
// caller传入服务作用域的共享*sql.DB，adapter只借用连接池，不负责DB.Close。
// 返回函数可交给FanOut/WeightedBatch，但不要把本文件编译通过写成SQL取消已验证。
func SQLPriceFetcher(db *sql.DB) Fetch {
	return func(ctx context.Context, id string) (price int64, resultErr error) {
		// ctx包含同一整批deadline：Acquire/queue等待消耗的时间不会在这里重置。
		// QueryContext先可能等待DB池连接，再等待driver/server；两种等待不是pool.jobs队列。
		rows, err := db.QueryContext(ctx, "SELECT price_cents FROM sku_prices WHERE sku_id = ?", id)
		if err != nil {
			return 0, fmt.Errorf("SKU %s SQL查询: %w", id, err)
		}
		// Rows由本次读取拥有，注册时已捕获这一个rows指针。提前缺价、Scan失败均Close。
		// Close/Err释放本次池连接，但不关闭共享DB；实际归还时机须按driver/服务验证。
		defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return 0, fmt.Errorf("遍历价格: %w", err)
			}
			return 0, ErrUnavailable
		}
		if err := rows.Scan(&price); err != nil {
			return 0, fmt.Errorf("读取整数分: %w", err)
		}
		if rows.Next() {
			return 0, fmt.Errorf("SKU唯一键契约被破坏: %w", ErrBackend)
		}
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("价格遍历结束: %w", err)
		}
		if price < 0 {
			return 0, fmt.Errorf("价格不能为负: %w", ErrBackend)
		}
		return price, nil
	}
}
