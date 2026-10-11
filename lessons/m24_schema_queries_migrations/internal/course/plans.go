package course

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// plans 是 Run 的查询段，seed 1200条订单，不做容量压测。
// EXPLAIN 是本次统计/数据的计划证据；成本/估计行数不是跨版本语言保证。
func (l *lesson) plans(ctx context.Context) (err error) {
	// 一个短事务把合成种子成组写入；Tx owner 始终用 tx.Exec，而不是绕到外层 DB。
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rollback(tx)) }()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// 1200次跨SSH往返会把准备数据变成主耗时。按100行一批、总计12次SQL，
	// 只拼固定占位符，业务值仍进入args；有限批次不等于无界批量读写。
	for start := 0; start < 1200; start += 100 {
		placeholders := make([]string, 0, 100)
		args := make([]any, 0, 600)
		for i := start; i < start+100; i++ {
			tenant := i%20 + 10
			status := "NEW"
			if i%3 == 0 {
				status = "PAID"
			}
			placeholders = append(placeholders, "(?,?,1,?,'12.34',?,?,?)")
			args = append(args, 100+i, tenant, fmt.Sprintf("seed-%04d", i), status, base.Add(time.Duration(i)*time.Second), strings.Repeat("x", 200))
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO "+l.table("orders")+" (id,tenant_id,user_id,request_key,amount,status,created_at,payload) VALUES "+strings.Join(placeholders, ","), args...)
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_, err = l.db.ExecContext(ctx, "ALTER TABLE "+l.table("orders")+" ADD INDEX idx_feed (tenant_id,status,created_at,id)")
	if err != nil {
		return err
	}
	// ANALYZE TABLE 返回 Rows；必须消费/关闭，且该管理语句也有隐式提交语义。
	if _, err = l.rawRows(ctx, "ANALYZE TABLE "+l.table("orders")); err != nil {
		return err
	}
	goodSQL := "SELECT id,created_at FROM " + l.table("orders") + " WHERE tenant_id=10 AND status='PAID' ORDER BY created_at,id LIMIT 10"
	good, err := l.explain(ctx, "covering-feed", goodSQL)
	if err != nil {
		return err
	}
	if err = require(len(good) == 1 && good[0]["key"] == "idx_feed" && strings.Contains(good[0]["Extra"], "Using index") && !strings.Contains(good[0]["Extra"], "filesort"), "投影/等值前缀/稳定排序应该由联合索引覆盖"); err != nil {
		return err
	}
	queries := []struct{ label, sql string }{
		{"missing-left-prefix", "SELECT id,created_at FROM " + l.table("orders") + " WHERE status='PAID' ORDER BY created_at,id LIMIT 10"},
		{"wrong-sort", "SELECT id,created_at FROM " + l.table("orders") + " WHERE tenant_id=10 AND status='PAID' ORDER BY amount,id LIMIT 10"},
		{"function-on-column", "SELECT id FROM " + l.table("orders") + " WHERE CAST(tenant_id AS CHAR)='10' AND status='PAID' ORDER BY created_at,id LIMIT 10"},
		{"select-star", "SELECT * FROM " + l.table("orders") + " WHERE tenant_id=10 AND status='PAID' ORDER BY created_at,id LIMIT 10"},
		{"text-equality", "SELECT text_id FROM " + l.table("types") + " WHERE text_id='1'"},
		{"numeric-conversion", "SELECT text_id FROM " + l.table("types") + " WHERE text_id=1"},
	}
	for _, q := range queries {
		plan, planErr := l.explain(ctx, q.label, q.sql)
		if planErr != nil {
			return planErr
		}
		// 下列断言只针对本课固定版本/合成数据的对照，不升级为所有SQL的永久规律。
		if len(plan) != 1 {
			return errors.New("本课单表EXPLAIN应只有一行")
		}
		extra, access := plan[0]["Extra"], plan[0]["type"]
		switch q.label {
		case "missing-left-prefix", "function-on-column":
			err = require(access == "index" && strings.Contains(extra, "filesort"), "本次缺前缀/函数对照应扫描索引并排序")
		case "wrong-sort":
			err = require(strings.Contains(extra, "filesort"), "amount不在索引排序后缀，应观察filesort")
		case "select-star":
			err = require(!strings.Contains(extra, "Using index"), "读取payload等非索引列应失去覆盖")
		case "text-equality":
			err = require(access == "ref", "相同文本类型应精确索引查找")
		case "numeric-conversion":
			err = require(access == "index", "本次字符串转数字应观察索引扫描")
		}
		if err != nil {
			return err
		}
	}
	stringN, err := l.scalar(ctx, "SELECT COUNT(*) FROM "+l.table("types")+" WHERE text_id='1'")
	if err != nil {
		return err
	}
	numericN, err := l.scalar(ctx, "SELECT COUNT(*) FROM "+l.table("types")+" WHERE text_id=1")
	if err != nil {
		return err
	}
	if err = require(stringN == 1 && numericN == 3, "字符串ID与数字比较不仅改计划，也把1/01/001当成相同值"); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[conversion results] text='1' rows=%d text=1 rows=%d\n", stringN, numericN)
	// EXPLAIN ANALYZE 会执行 SELECT。这里只允许独占合成表的有界只读 SQL，绝不包装写语句。
	analyzed, err := l.rawRows(ctx, "EXPLAIN ANALYZE FORMAT=TREE "+goodSQL)
	if err != nil {
		return err
	}
	for _, row := range analyzed {
		fmt.Fprintf(l.out, "[ANALYZE executed SELECT] %s\n", row["EXPLAIN"])
	}
	lookup, err := l.rawRows(ctx, "EXPLAIN ANALYZE FORMAT=TREE SELECT payload FROM "+l.table("orders")+" WHERE tenant_id=10 AND status='PAID' ORDER BY created_at,id LIMIT 20")
	if err != nil {
		return err
	}
	for _, row := range lookup {
		fmt.Fprintf(l.out, "[ANALYZE noncovering SELECT] %s\n", row["EXPLAIN"])
	}
	return l.queryShape(ctx)
}

// rawRows 由 Explain/ANALYZE 调用，动态读取列名以适应真实服务的输出。
// RawBytes 借用驱动缓冲，Next 后可能变化，因此在当前行立刻复制成 string 放入 map。
func (l *lesson) rawRows(ctx context.Context, query string) (result []map[string]string, err error) {
	rows, err := l.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		values := make([]sql.RawBytes, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := make(map[string]string, len(columns))
		for i, column := range columns {
			row[column] = string(values[i])
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// explain 输出原 SQL 和影响阅读的六个字段，返回完整行供独立业务断言。
// FORMAT=TRADITIONAL 防止会话 explain_format 的默认值变化改变输出形状。
func (l *lesson) explain(ctx context.Context, label, query string) ([]map[string]string, error) {
	fmt.Fprintf(l.out, "[SQL %s] %s\n", label, query)
	rows, err := l.rawRows(ctx, "EXPLAIN FORMAT=TRADITIONAL "+query)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		fmt.Fprintf(l.out, "[PLAN %s] type=%s key=%s key_len=%s rows=%s filtered=%s Extra=%s\n", label, row["type"], row["key"], row["key_len"], row["rows"], row["filtered"], row["Extra"])
	}
	return rows, nil
}

// queryShape 同时观察 SELECT* 列数、无界读行数和 N+1 次数，结果不靠“看起来更快”推断。
// 两种取姓名的方法输出相同；差别是数据库往返次数和读取的列/行预算。
func (l *lesson) queryShape(ctx context.Context) (err error) {
	rows, err := l.db.QueryContext(ctx, "SELECT * FROM "+l.table("orders")+" LIMIT 10")
	if err != nil {
		return err
	}
	cols, err := rows.Columns()
	if err != nil {
		return errors.Join(err, rows.Close())
	}
	if err = rows.Close(); err != nil {
		return err
	}
	all, err := l.scalar(ctx, "SELECT COUNT(*) FROM "+l.table("orders"))
	if err != nil {
		return err
	}
	page, err := l.rawRows(ctx, "SELECT id,user_id FROM "+l.table("orders")+" ORDER BY id LIMIT 10")
	if err != nil {
		return err
	}
	queries := 1
	names := make([]string, 0, len(page))
	for _, row := range page {
		var name string
		if err = l.db.QueryRowContext(ctx, "SELECT name FROM "+l.table("users")+" WHERE id=?", row["user_id"]).Scan(&name); err != nil {
			return err
		}
		queries++
		names = append(names, name)
	}
	joined, err := l.rawRows(ctx, "SELECT o.id,u.name FROM "+l.table("orders")+" AS o JOIN "+l.table("users")+" AS u ON u.id=o.user_id ORDER BY o.id LIMIT 10")
	if err != nil {
		return err
	}
	fixed := make([]string, 0, len(joined))
	for _, row := range joined {
		fixed = append(fixed, row["name"])
	}
	if err = require(all == 1203 && len(page) == 10 && queries == 11 && reflect.DeepEqual(names, fixed) && len(cols) == 10, "无界行数/N+1查询数/Join结果必须按实际结果核对"); err != nil {
		return err
	}
	// COUNT 是本实验给出的结果预算证据；同时实际遍历全部行，不能把COUNT当作已发生批量读取。
	unbounded, err := l.rawRows(ctx, "SELECT id FROM "+l.table("orders")+" ORDER BY id")
	if err != nil {
		return err
	}
	if err = require(len(unbounded) == 1203, "实际无LIMIT读取应返回全部1203行"); err != nil {
		return err
	}
	wide, err := l.rawRows(ctx, "SELECT * FROM "+l.table("orders")+" WHERE tenant_id=10 AND status='PAID' ORDER BY created_at,id LIMIT 20")
	if err != nil {
		return err
	}
	payloadBytes := 0
	for _, row := range wide {
		payloadBytes += len(row["payload"])
	}
	if err = require(len(wide) == 20 && payloadBytes == 4000, "非覆盖读取20行应真实搬运不需要的4000字节payload"); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[shape] SELECT* columns=%d projection-columns=2 unbounded-actual-rows=%d bounded-rows=%d N+1=%d JOIN=1 same-names=true\n", len(cols), len(unbounded), len(page), queries)
	fmt.Fprintf(l.out, "[wide-result] noncovering-rows=%d unused-payload-bytes=%d；改投影id/created_at可覆盖，减少聚簇记录读取和网络数据\n", len(wide), payloadBytes)
	return nil
}
