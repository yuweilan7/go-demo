package lesson

import (
	"context"
	"database/sql"
	"fmt"
)

// ExecSQL 是只用于阅读/编译核对的 SQL adapter，不在本课默认实验执行。
// caller 明确传 ctx、已有 Tx 和 SQL/参数；Tx 由创建它的 usecase 拥有，本函数不 Commit。
// 本课无数据库/driver，因此不能把编译通过宣称为 SQL 取消或回滚已经实测。
func ExecSQL(ctx context.Context, tx *sql.Tx, statement string, args ...any) (int64, error) {
	// ExecContext 把当前调用的预算交给 database/sql/driver。若改成 Exec 或新 Background，
	// caller 的等待预算便丢失；statement/lock timeout 是服务端机制，需要额外配置和实证。
	result, err := tx.ExecContext(ctx, statement, args...)
	if err != nil {
		return 0, fmt.Errorf("事务内执行 SQL: %w", err)
	}
	// RowsAffected 仍可能返回 error；即便受影响行为 1，也只是本 Tx 内执行事实，
	// 并不证明 Commit 成功、客户端读到提交响应或其他连接可见。
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取 SQL 受影响行: %w", err)
	}
	return rows, nil
}
