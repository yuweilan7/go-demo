package course

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/go-sql-driver/mysql"
)

// lesson 是 Run 的内部状态。db 指针共享同一个池，out 仅由 owner goroutine 写。
// names 保存精确目标；created 只收录预先确认不存在且本 run 尝试创建的对象。
type lesson struct {
	db       *sql.DB
	out      io.Writer
	database string
	names    map[string]string
	created  []string
}

// Run 是整课资源 owner，由入口/真实测试调用。它先核对会话，再执行四组实验。
// 任何阶段失败都沿同一返回路径：worker join→Rows/Tx/Conn 释放→独占表 DROP→DB.Close。
func Run(ctx context.Context, config Config, out io.Writer) (err error) {
	db, err := sql.Open("mysql", config.driverConfig().FormatDSN())
	if err != nil {
		return fmt.Errorf("构造池: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(3 * time.Minute)
	db.SetConnMaxIdleTime(30 * time.Second)
	defer func() {
		err = errors.Join(err, db.Close())
		s := db.Stats()
		fmt.Fprintf(out, "[DB finished] open=%d inuse=%d idle=%d\n", s.OpenConnections, s.InUse, s.Idle)
	}()
	var version, database, zone, mode string
	var max int
	if err = db.QueryRowContext(ctx, "SELECT VERSION(),DATABASE(),@@session.time_zone,@@session.sql_mode,@@max_connections").Scan(&version, &database, &zone, &mode, &max); err != nil {
		return err
	}
	if err = require(version == config.version && database == config.database && zone == "+00:00", "服务端版本/库/时区必须与只读索引一致"); err != nil {
		return err
	}
	fmt.Fprintf(out, "[versions] Go=%s driver=v1.10.1 mysql=%s database=%s zone=%s max_connections=%d pool_max=4\n[sql_mode] %s\n", runtime.Version(), version, database, zone, max, mode)
	var entropy [12]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return err
	}
	prefix := "m24_" + hex.EncodeToString(entropy[:])
	l := &lesson{db: db, out: out, database: database, names: make(map[string]string)}
	for _, suffix := range []string{"users", "orders", "types", "pages", "evolve", "versions", "audit"} {
		l.names[suffix] = prefix + "_" + suffix
	}
	fmt.Fprintf(out, "[namespace] %s；最多1200条查询种子/6条分页初始数据；只清理本run七张表\n", prefix)
	// 先注册表清理，后开始 CREATE。主 ctx 失效时也用新预算核验/清理，保留主错误。
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err = errors.Join(err, l.cleanup(cleanup))
	}()
	steps := []struct {
		name string
		run  func(context.Context) error
	}{
		{"1 schema与真实类型损失", l.schemaAndValues},
		{"2 联合索引/计划/查询形状", l.plans},
		{"3 offset/keyset/页间插入和更新", l.pagination},
		{"4 expand/contract/隐式提交/DDL锁", l.migrations},
	}
	for _, step := range steps {
		fmt.Fprintf(out, "\n=== %s ===\n", step.name)
		if err = step.run(ctx); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	if err = require(db.Stats().InUse == 0, "实验全部退出后不能仍持有池连接"); err != nil {
		return err
	}
	fmt.Fprintln(out, "[PASS] 类型/约束/计划/分页/兼容迁移/DDL事实全部通过")
	return nil
}

// table 返回已经由 quote 验证的内部表名；初始化只使用固定 suffix，失败是源码错误。
func (l *lesson) table(suffix string) string {
	q, err := quote(l.names[suffix])
	if err != nil {
		panic(err)
	}
	return q
}

// create 由 schema/migration 调用：先证明目标不存在，后创建，禁止 IF NOT EXISTS 认领他人表。
// CREATE 响应错误可能发生在服务端已完成之后；此时读回结构并决定所有权，不能猜“没创建”。
func (l *lesson) create(ctx context.Context, suffix, definition string) error {
	name := l.names[suffix]
	var before int
	if err := l.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", l.database, name).Scan(&before); err != nil {
		return err
	}
	if before != 0 {
		return errors.New("随机目标已存在，拒绝认领或删除")
	}
	_, createErr := l.db.ExecContext(ctx, "CREATE TABLE "+l.table(suffix)+" "+definition)
	if createErr == nil {
		l.created = append(l.created, suffix)
		return nil
	}
	// 即便前置SELECT之后另一个创建者碰巧占用了同名，1050也不能让本run认领它。
	// 其他响应/网络错误才按自己这次CREATE的结果未知路径读回；只清理可核对的目标。
	var serverErr *mysql.MySQLError
	if errors.As(createErr, &serverErr) && serverErr.Number == 1050 {
		return createErr
	}
	check, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var after int
	checkErr := l.db.QueryRowContext(check, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", l.database, name).Scan(&after)
	if checkErr == nil && after == 1 {
		l.created = append(l.created, suffix)
	}
	fmt.Fprintf(l.out, "[CREATE error readback] target=%s known=%t exists=%d\n", name, checkErr == nil, after)
	return errors.Join(createErr, checkErr)
}

// cleanup 由 Run 的 defer 调用，逆序删除以满足 orders→users 的外键依赖。
// DROP 成功仍 SELECT information_schema 核对剩余数，不能把 Exec 的返回当最终结构事实。
func (l *lesson) cleanup(ctx context.Context) (err error) {
	for i := len(l.created) - 1; i >= 0; i-- {
		suffix := l.created[i]
		_, dropErr := l.db.ExecContext(ctx, "DROP TABLE "+l.table(suffix))
		var remaining int
		checkErr := l.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", l.database, l.names[suffix]).Scan(&remaining)
		err = errors.Join(err, dropErr, checkErr)
		if checkErr == nil {
			err = errors.Join(err, require(remaining == 0, "DROP 后目标必须不存在"))
		}
		fmt.Fprintf(l.out, "[cleanup] %s remaining=%d check_ok=%t\n", l.names[suffix], remaining, checkErr == nil)
	}
	return err
}

// require 把业务断言失败作为普通 error 返回，给上层走完整清理；不以 panic 提前结束进程。
func require(ok bool, message string) error {
	if !ok {
		return errors.New("断言失败: " + message)
	}
	return nil
}

// expectCode 只识别驱动错误类型/错误码。调用者随后读数据库事实；错误码不能证明回滚/未写。
func expectCode(err error, code uint16) error {
	if err == nil {
		return fmt.Errorf("预期MySQL code=%d，实际没有错误", code)
	}
	var server *mysql.MySQLError
	if !errors.As(err, &server) || server.Number != code {
		return fmt.Errorf("预期 MySQL code=%d，实际 %w", code, err)
	}
	return nil
}

// rollback 由 Tx owner 的 defer 调用。ErrTxDone 表示 database/sql 已结束该 Tx；
// 不说明服务端 DDL/DML 是否撤销，那必须由独立 SELECT 证明。
func rollback(tx *sql.Tx) error {
	err := tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

// scalar 是只读小结果查询，调用者传入精确 SQL 和绑定值；Scan 同步取一行并释放资源。
func (l *lesson) scalar(ctx context.Context, query string, args ...any) (int, error) {
	var n int
	err := l.db.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}
