package course

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// schemaAndValues 是 Run 的第一段。先建立用户/订单真实约束，再用故障表比较错误类型。
// 它借用池，不关闭 DB；每个 SQL 的参数只是值，任何文本都不拼成 SQL 语法。
func (l *lesson) schemaAndValues(ctx context.Context) (err error) {
	if err := l.create(ctx, "users", `(id BIGINT NOT NULL PRIMARY KEY,name VARCHAR(80) NOT NULL)
 ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
		return err
	}
	if err := l.create(ctx, "orders", `(
 id BIGINT NOT NULL PRIMARY KEY,
 tenant_id BIGINT NOT NULL,
 user_id BIGINT NOT NULL,
 request_key VARCHAR(64) COLLATE utf8mb4_0900_bin NOT NULL,
 amount DECIMAL(12,2) NOT NULL,
 status VARCHAR(16) NOT NULL DEFAULT 'NEW',
 note VARCHAR(100) NULL DEFAULT '未填写',
 created_at DATETIME(6) NOT NULL,
 shipped_at DATETIME(6) NULL,
 payload VARCHAR(256) NOT NULL DEFAULT '',
 UNIQUE KEY uq_request (tenant_id,request_key),
 CHECK (amount>=0),
 FOREIGN KEY (user_id) REFERENCES `+l.table("users")+`(id)
 ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
		return err
	}
	// BIGINT→int64 是明确宽度的契约；不能用架构相关的 int 或经过 float64 传递 ID。
	const largeID int64 = 9007199254740993
	_, err = l.db.ExecContext(ctx, "INSERT INTO "+l.table("users")+" (id,name) VALUES (?,?),(?,?)", 1, "小明🛍️", largeID, "大ID用户")
	if err != nil {
		return err
	}
	when := time.Date(2024, 2, 29, 23, 59, 59, 123456000, time.UTC)
	// 缺列、显式 NULL、空串分别是三种写入：DEFAULT 只作用在缺列/DEFAULT 表达式上。
	_, err = l.db.ExecContext(ctx, "INSERT INTO "+l.table("orders")+" (id,tenant_id,user_id,request_key,amount,created_at) VALUES (1,1,?,?,?,?)", largeID, "Req-A", "16777217.03", when)
	if err != nil {
		return err
	}
	_, err = l.db.ExecContext(ctx, "INSERT INTO "+l.table("orders")+" (id,tenant_id,user_id,request_key,amount,note,created_at) VALUES (2,1,1,'req-a','0.10',NULL,?),(3,1,1,'empty','0.00','',?)", when, when)
	if err != nil {
		return err
	}
	rows, err := l.db.QueryContext(ctx, "SELECT id,user_id,amount,status,note,created_at,shipped_at FROM "+l.table("orders")+" ORDER BY id")
	if err != nil {
		return err
	}
	// Rows 是当前函数拥有的流，defer 覆盖 Scan/断言提前返回；正常结束也显式检查 Err。
	defer func() { err = errors.Join(err, rows.Close()) }()
	notes := make([]sql.NullString, 0, 3)
	for rows.Next() {
		var id, user int64
		var amount, status string
		var note sql.NullString
		var created time.Time
		var shipped sql.NullTime
		if err = rows.Scan(&id, &user, &amount, &status, &note, &created, &shipped); err != nil {
			return err
		}
		notes = append(notes, note)
		if err = require(created.Equal(when) && !shipped.Valid && status == "NEW", "UTC微秒、NULL发货时间和default状态必须准确读回"); err != nil {
			return err
		}
		if id == 1 {
			if err = require(user == largeID && amount == "16777217.03", "BIGINT/DECIMAL必须无损"); err != nil {
				return err
			}
		}
		fmt.Fprintf(l.out, "[value] id=%d user=%d amount=%s note.Valid=%t note=%q created=%s shipped.Valid=%t\n", id, user, amount, note.Valid, note.String, created.Format(time.RFC3339Nano), shipped.Valid)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if err = require(len(notes) == 3 && notes[0].Valid && notes[0].String == "未填写" && !notes[1].Valid && notes[2].Valid && notes[2].String == "", "缺值/NULL/空串必须区分"); err != nil {
		return err
	}
	// 同租户完全相同的键被挡住，大小写不同的 Req-A/req-a 已真实共存。
	_, dup := l.db.ExecContext(ctx, "INSERT INTO "+l.table("orders")+" (id,tenant_id,user_id,request_key,amount,created_at) VALUES (4,1,1,'Req-A','9.99',?)", when)
	if err = expectCode(dup, 1062); err != nil {
		return err
	}
	count, err := l.scalar(ctx, "SELECT COUNT(*) FROM "+l.table("orders"))
	if err != nil {
		return err
	}
	var original string
	if err = l.db.QueryRowContext(ctx, "SELECT amount FROM "+l.table("orders")+" WHERE tenant_id=1 AND request_key=?", "Req-A").Scan(&original); err != nil {
		return err
	}
	if err = require(count == 3 && original == "16777217.03", "唯一键拒绝后不能产生第4笔订单或覆盖原金额"); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[unique] code=1062 rows=%d original=%s；不同payload重用键是冲突，不能自动当成功\n", count, original)
	_, fk := l.db.ExecContext(ctx, "INSERT INTO "+l.table("orders")+" (id,tenant_id,user_id,request_key,amount,created_at) VALUES (4,1,999,'missing-user','1.00',?)", when)
	if err = expectCode(fk, 1452); err != nil {
		return err
	}
	_, negative := l.db.ExecContext(ctx, "INSERT INTO "+l.table("orders")+" (id,tenant_id,user_id,request_key,amount,created_at) VALUES (4,1,1,'negative','-0.01',?)", when)
	if err = expectCode(negative, 3819); err != nil {
		return err
	}
	_, nullErr := l.db.ExecContext(ctx, "INSERT INTO "+l.table("orders")+" (id,tenant_id,user_id,request_key,amount,created_at) VALUES (4,1,1,NULL,'1.00',?)", when)
	if err = expectCode(nullErr, 1048); err != nil {
		return err
	}
	count, err = l.scalar(ctx, "SELECT COUNT(*) FROM "+l.table("orders"))
	if err != nil {
		return err
	}
	if err = require(count == 3, "FK/CHECK/NOT NULL失败后行数必须仍为3"); err != nil {
		return err
	}
	var ci, binary int
	var name string
	if err = l.db.QueryRowContext(ctx, "SELECT _utf8mb4'A' COLLATE utf8mb4_0900_ai_ci = _utf8mb4'a', _utf8mb4'A' COLLATE utf8mb4_0900_bin = _utf8mb4'a'").Scan(&ci, &binary); err != nil {
		return err
	}
	if err = l.db.QueryRowContext(ctx, "SELECT name FROM "+l.table("users")+" WHERE id=1").Scan(&name); err != nil {
		return err
	}
	if err = require(ci == 1 && binary == 0 && name == "小明🛍️", "字符必须无损，比较规则必须与键的业务含义一致"); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[constraints] FK=1452 CHECK=3819 NOT_NULL=1048 rows=3; utf8mb4=%q ai_ci(A=a)=%d bin(A=a)=%d\n", name, ci, binary)
	return l.lossyTypes(ctx)
}

// lossyTypes 使用独占故障表，实际写同一个业务值到 FLOAT/DOUBLE 与正确类型。
// 这不是 Go 算术展示：读取的是 MySQL 持久化后的值，随后还核对失败写没有增加行。
func (l *lesson) lossyTypes(ctx context.Context) error {
	if err := l.create(ctx, "types", `(id BIGINT PRIMARY KEY,amount_bad FLOAT,amount_ok DECIMAL(12,2),user_bad DOUBLE,user_ok BIGINT,user_narrow INT,text_id VARCHAR(32),KEY idx_text(text_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
		return err
	}
	_, err := l.db.ExecContext(ctx, "INSERT INTO "+l.table("types")+" (id,amount_bad,amount_ok,user_bad,user_ok,user_narrow,text_id) VALUES (1,?,?,?,?,?,?)", "16777217.03", "16777217.03", "9007199254740993", int64(9007199254740993), 1, "1")
	if err != nil {
		return err
	}
	var badAmount, badUser float64
	var exact, storedFloat string
	var exactUser int64
	if err = l.db.QueryRowContext(ctx, "SELECT amount_bad,amount_ok,user_bad,user_ok,CAST(amount_bad AS DECIMAL(20,2)) FROM "+l.table("types")+" WHERE id=1").Scan(&badAmount, &exact, &badUser, &exactUser, &storedFloat); err != nil {
		return err
	}
	if err = require(badAmount != 16777217.03 && exact == "16777217.03" && int64(badUser) != exactUser, "真实浮点列必须暴露金额和ID精度损失"); err != nil {
		return err
	}
	_, narrow := l.db.ExecContext(ctx, "INSERT INTO "+l.table("types")+" (id,user_narrow) VALUES (2,?)", int64(2147483648))
	if err = expectCode(narrow, 1264); err != nil {
		return err
	}
	n, err := l.scalar(ctx, "SELECT COUNT(*) FROM "+l.table("types"))
	if err != nil {
		return err
	}
	if err = require(n == 1, "INT越界失败后不能留下半行"); err != nil {
		return err
	}
	cents, err := decimalCents(exact)
	if err != nil {
		return err
	}
	if err = require(cents == 1677721703, "十进制文本转整数分应无损"); err != nil {
		return err
	}
	// text协议的FLOAT格式化也会影响返回的小数展示；CAST读回单独说明已存浮点值。
	// 不能把驱动返回的16777200误写成底层浮点存储恰好等于这个十进制数字。
	fmt.Fprintf(l.out, "[bad types] FLOAT-returned=%.2f FLOAT-as-DECIMAL=%s DECIMAL=%s cents=%d DOUBLE-ID=%.0f BIGINT-ID=%d INT-overflow=1264 rows=%d\n", badAmount, storedFloat, exact, cents, badUser, exactUser, n)
	_, err = l.db.ExecContext(ctx, "INSERT INTO "+l.table("types")+" (id,text_id) VALUES (2,'01'),(3,'001'),(4,'abc')")
	if err != nil {
		return err
	}
	return l.scaleAndLength(ctx)
}

// scaleAndLength 由故障类型段调用，证明strict下VARCHAR超长拒绝，而DECIMAL小数位仍可能转换。
// SHOW WARNINGS是会话状态，必须与INSERT使用同一个Conn；DB两次调用可能借到不同连接。
func (l *lesson) scaleAndLength(ctx context.Context) (err error) {
	_, tooLong := l.db.ExecContext(ctx, "INSERT INTO "+l.table("types")+" (id,text_id) VALUES (5,?)", strings.Repeat("x", 33))
	if err = expectCode(tooLong, 1406); err != nil {
		return err
	}
	n, err := l.scalar(ctx, "SELECT COUNT(*) FROM "+l.table("types"))
	if err != nil {
		return err
	}
	if err = require(n == 4, "超长失败后不能产生第5行"); err != nil {
		return err
	}
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	if _, err = conn.ExecContext(ctx, "INSERT INTO "+l.table("types")+" (id,amount_ok) VALUES (5,'1.005')"); err != nil {
		return err
	}
	rows, err := conn.QueryContext(ctx, "SHOW WARNINGS")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	warning := 0
	for rows.Next() {
		var level, message string
		var code int
		if err = rows.Scan(&level, &code, &message); err != nil {
			return err
		}
		if code == 1265 {
			warning++
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	var rounded string
	if err = conn.QueryRowContext(ctx, "SELECT amount_ok FROM "+l.table("types")+" WHERE id=5").Scan(&rounded); err != nil {
		return err
	}
	if err = require(warning == 1 && rounded == "1.01", "本次DECIMAL(12,2)应读回1.01与1265警告；nil错误不代表无损"); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[scale/length] VARCHAR33-to32 code=1406 unchanged-rows=4 DECIMAL-input=1.005 stored=%s warning=1265 Exec-error=nil\n", rounded)
	return nil
}

// decimalCents 由值实验/单测调用，只接受本课非负 DECIMAL(12,2) 文本。
// int64 是值副本；不通过 float64，避免把精确十进制含义变成二进制近似。
func decimalCents(text string) (int64, error) {
	parts := strings.Split(text, ".")
	if len(parts) != 2 || len(parts[0]) < 1 || len(parts[0]) > 10 || len(parts[1]) != 2 {
		return 0, errors.New("需要非负 DECIMAL(12,2) 文本")
	}
	for _, part := range parts {
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return 0, errors.New("金额包含非数字")
			}
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	fraction, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return whole*100 + fraction, nil
}
