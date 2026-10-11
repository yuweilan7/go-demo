package course

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// product 是 Scan 的目标值，Nullable.Valid 才能区分数据库 NULL 与 Go 的零值。
// AmountText 保存 MySQL DECIMAL 文本；AmountCents 是本课无浮点的业务金额。
type product struct {
	ID          int64
	Code        string
	Note        sql.Null[string]
	AmountText  string
	AmountCents int64
	CreatedAt   time.Time
	ShippedAt   sql.Null[time.Time]
	Quantity    int
}

// values 由 Run 顺序调用，写入仅含合成数据的两行并用 SELECT 验证每个副作用。
// 这里同步等待真实 MySQL，不创建 goroutine；QueryRow 的错误延迟到 Scan 返回。
func (current *lesson) values(ctx context.Context) error {
	created := time.Date(2024, time.February, 29, 23, 59, 59, 123456000, time.UTC)
	boundaryCode := "o' OR 1=1 --;中文🙂"
	result, err := current.db.ExecContext(ctx, "INSERT INTO "+current.table+
		" (id,code,note,amount,created_at,shipped_at,quantity) VALUES (?,?,?,?,?,?,?),(?,?,?,?,?,?,?)",
		int64(1), boundaryCode, nil, "99999999.99", created, nil, 100,
		int64(2), "ordinary", "", "0.10", created, created, 200)
	if err != nil {
		return fmt.Errorf("参数化 INSERT: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if err = require(affected == 2, "INSERT RowsAffected=2"); err != nil {
		return err
	}
	first, err := current.readProduct(ctx, 1)
	if err != nil {
		return err
	}
	second, err := current.readProduct(ctx, 2)
	if err != nil {
		return err
	}
	if err = require(first.Code == boundaryCode && first.AmountCents == 9999999999 &&
		!first.Note.Valid && !first.ShippedAt.Valid && first.CreatedAt.Equal(created) && first.Quantity == 100,
		"id1 SELECT必须逐项保留参数边界、最大金额、NULL、微秒时间和quantity"); err != nil {
		return err
	}
	if err = require(second.Note.Valid && second.Note.V == "" && second.AmountCents == 10 &&
		second.ShippedAt.Valid && second.ShippedAt.V.Equal(created), "id2空字符串与NULL、0.10与发货时间必须正确"); err != nil {
		return err
	}
	var total, matching int
	if err = current.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+current.table).Scan(&total); err != nil {
		return err
	}
	if err = current.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+current.table+" WHERE code=?", boundaryCode).Scan(&matching); err != nil {
		return err
	}
	if err = require(total == 2 && matching == 1, "SQL形状保持两行，仅一个code精确匹配注入形状的值"); err != nil {
		return err
	}
	fmt.Fprintf(current.out, "[SELECT] id1 code=%q note.Valid=%t amount=%s cents=%d created=%s shipped.Valid=%t quantity=%d\n",
		first.Code, first.Note.Valid, first.AmountText, first.AmountCents, first.CreatedAt.Format(time.RFC3339Nano), first.ShippedAt.Valid, first.Quantity)
	fmt.Fprintf(current.out, "[SELECT] id2 note.Valid=%t note=%q amount=%s cents=%d shipped=%s；total=%d exact-match=%d\n",
		second.Note.Valid, second.Note.V, second.AmountText, second.AmountCents, second.ShippedAt.V.Format(time.RFC3339Nano), total, matching)
	// ClientFoundRows=false 时 UPDATE 返回实际改变行数。同值更新返回0，
	// 不表示 WHERE 没有匹配行；下面用 SELECT 证明行仍存在且值确实为101。
	for _, expected := range []int64{1, 0} {
		result, err = current.db.ExecContext(ctx, "UPDATE "+current.table+" SET quantity=? WHERE id=?", 101, 1)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		if err != nil {
			return err
		}
		actual, readErr := current.quantity(ctx, 1)
		if readErr != nil {
			return readErr
		}
		if err = require(affected == expected && actual == 101, "RowsAffected与SELECT值必须同时满足本次更新预期"); err != nil {
			return err
		}
		fmt.Fprintf(current.out, "[UPDATE] RowsAffected=%d SELECT quantity=%d\n", affected, actual)
	}
	_, err = current.readProduct(ctx, 999)
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("无行必须是可识别 ErrNoRows，实际=%w", err)
	}
	fmt.Fprintln(current.out, "[QueryRow] id999 Scan=ErrNoRows；Row本身不是(error)返回")
	if err = current.scanFailure(ctx, "note", "NULL不能Scan进string"); err != nil {
		return err
	}
	if err = current.scanFailure(ctx, "code", "非数字文本不能Scan进int64"); err != nil {
		return err
	}
	return current.listProducts(ctx)
}

// readProduct 是本课 repository 风格的借用者：caller 传 id，DB池负责借连接。
// QueryRow 不返回 error；Scan 完成结果转换、报告 ErrNoRows/SQL/类型错误并关闭 Rows。
func (current *lesson) readProduct(ctx context.Context, id int64) (product, error) {
	var row product
	err := current.db.QueryRowContext(ctx, "SELECT id,code,note,amount,created_at,shipped_at,quantity FROM "+current.table+" WHERE id=?", id).
		Scan(&row.ID, &row.Code, &row.Note, &row.AmountText, &row.CreatedAt, &row.ShippedAt, &row.Quantity)
	if err != nil {
		return product{}, fmt.Errorf("readProduct id=%d Scan: %w", id, err)
	}
	row.AmountCents, err = decimalCents(row.AmountText)
	return row, err
}

// quantity 只读一个业务事实，caller 用它验证 UPDATE、事务与取消后的实际表值。
func (current *lesson) quantity(ctx context.Context, id int64) (int64, error) {
	var value int64
	err := current.db.QueryRowContext(ctx, "SELECT quantity FROM "+current.table+" WHERE id=?", id).Scan(&value)
	return value, err
}

// scanFailure 展示真实驱动结果转换反例。列名只选两个本地常量，不接收任意标识符。
// Scan错误属于这一行的转换，未必出现在 Rows.Err；owner提前返回仍须关闭 Rows。
func (current *lesson) scanFailure(ctx context.Context, column, reason string) (err error) {
	if column != "note" && column != "code" {
		return errors.New("内部列选择越界")
	}
	rows, err := current.db.QueryContext(ctx, "SELECT "+column+" FROM "+current.table+" WHERE id=?", 1)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close(), rows.Err()) }()
	if !rows.Next() {
		return errors.Join(errors.New("反例缺少合成行"), rows.Err())
	}
	var scanErr error
	if column == "note" {
		var invalid string
		scanErr = rows.Scan(&invalid)
	} else {
		var invalid int64
		scanErr = rows.Scan(&invalid)
	}
	if scanErr == nil {
		return fmt.Errorf("预期Scan反例未发生：%s", reason)
	}
	fmt.Fprintf(current.out, "[Scan反例→修复] %s；error=%v；Nullable/string映射已在readProduct验证\n", reason, scanErr)
	return nil
}

// listProducts 是 Rows 正常 owner。QueryContext 成功后立刻注册 defer，
// Next拉取下一行，Scan按列序写入指针；循环结束必须再问 Rows.Err 才能区分EOF和失败。
func (current *lesson) listProducts(ctx context.Context) (err error) {
	rows, err := current.db.QueryContext(ctx, "SELECT id,quantity FROM "+current.table+" ORDER BY id")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close(), rows.Err()) }()
	count, sum := 0, int64(0)
	for rows.Next() {
		var id, quantity int64
		if err = rows.Scan(&id, &quantity); err != nil {
			return fmt.Errorf("遍历Scan: %w", err)
		}
		count++
		sum += quantity
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("遍历Rows.Err: %w", err)
	}
	if err = require(count == 2 && sum == 301, "真实Rows遍历必须完整取得两行且quantity和为301"); err != nil {
		return err
	}
	fmt.Fprintf(current.out, "[Rows正常修复] count=%d quantity-sum=%d Rows.Err=nil；EOF/Close释放连接\n", count, sum)
	return nil
}
