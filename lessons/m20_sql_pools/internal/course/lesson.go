package course

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"time"
)

// lesson 由 Run 创建；db 是整个课程共享的池指针，table 是本 run 唯一的合成表。
// out 只由 owner goroutine 写，worker 通过 outcome 交回结果，避免日志竞争。
type lesson struct {
	db    *sql.DB
	table string
	out   io.Writer
}

// Run 由命令行入口或实库测试调用，拥有 DB 和专属表；它是唯一关闭 DB 的层。
// repository/实验函数借用 db，不能每次请求都 Close 共享池。
func Run(ctx context.Context, config Config, out io.Writer) (err error) {
	fmt.Fprintln(out, "M20 真实 MySQL：值映射、Rows、Conn、Tx、池等待与 SQL 取消")
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dependency := range info.Deps {
			if dependency.Path == "github.com/go-sql-driver/mysql" {
				fmt.Fprintf(out, "[版本] Go=%s driver=%s\n", info.GoVersion, dependency.Version)
			}
		}
	}
	db, err := openPool(config)
	if err != nil {
		return err
	}
	// 这条 defer 最早注册，因 LIFO 最后执行。任务/Rows/Tx 先清理，表再 DROP，
	// DB 最后关闭。具名 err 是闭包退出时读取的同一个返回槽，Close 错误不会被吞掉。
	defer func() {
		err = errors.Join(err, db.Close())
		fmt.Fprintf(out, "[DB owner] Close 后 open=%d inuse=%d idle=%d\n",
			db.Stats().OpenConnections, db.Stats().InUse, db.Stats().Idle)
	}()
	fmt.Fprintf(out, "[sql.Open] open=%d；只构造池，尚未验证网络\n", db.Stats().OpenConnections)
	if err = db.PingContext(ctx); err != nil {
		return fmt.Errorf("PingContext（仅鉴权/连通）: %w", err)
	}
	var version, database, zone string
	var maxConnections int
	if err = db.QueryRowContext(ctx, "SELECT VERSION(), DATABASE(), @@session.time_zone, @@max_connections").
		Scan(&version, &database, &zone, &maxConnections); err != nil {
		return fmt.Errorf("读取真实会话元信息: %w", err)
	}
	if version != config.ServerVersion || database != config.Database || zone != "+00:00" {
		return fmt.Errorf("实际会话与安装索引不一致：version=%s database=%s zone=%s", version, database, zone)
	}
	fmt.Fprintf(out, "[真实会话] server=%s database=%s time_zone=%s max_connections=%d\n", version, database, zone, maxConnections)
	var random [12]byte
	if _, err = rand.Read(random[:]); err != nil {
		return fmt.Errorf("生成独占表后缀: %w", err)
	}
	name := "m20_" + hex.EncodeToString(random[:])
	table, err := quotedTable(name)
	if err != nil {
		return err
	}
	current := &lesson{db: db, table: table, out: out}
	var existed int
	if err = db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", config.Database, name).
		Scan(&existed); err != nil {
		return err
	}
	if existed != 0 {
		return errors.New("随机表名已存在，拒绝认领/清理该对象")
	}
	owned := false
	defer func() {
		if !owned {
			return
		}
		// 主 ctx 可能取消，独立 cleanupCtx 仍允许 DROP 和证明对象不存在。
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		err = errors.Join(err, current.drop(cleanupCtx, config.Database, name))
	}()
	if err = current.create(ctx); err != nil {
		// CREATE返回网络错误也可能已经在服务器完成，不能断言“没创建”。
		// 预先证明随机名字不存在后，本run才尝试创建；用独立预算读回对象事实。
		checkCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		var count int
		checkErr := db.QueryRowContext(checkCtx,
			"SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", config.Database, name).
			Scan(&count)
		if checkErr == nil {
			owned = count == 1
			fmt.Fprintf(out, "[CREATE错误后核验] table=%s remaining=%d；按读回事实决定本run清理\n", name, count)
		} else {
			fmt.Fprintf(out, "[CREATE结果未知] table=%s；未能读回对象事实，保留精确名字供owner恢复\n", name)
		}
		return errors.Join(err, checkErr)
	}
	owned = true
	fmt.Fprintf(out, "[命名空间] 本 run 自建 table=%s\n", name)
	steps := []struct {
		name string
		run  func(context.Context) error
	}{
		{"1 真实参数化写读/NULL/DECIMAL/时间/Scan", current.values},
		{"2 Conn 的特定连接作用域", current.connScope},
		{"3 Rows 未关闭导致池排队/timeout/释放修复", current.rowsPool},
		{"4 Tx 持池后再次从 DB 借池/修复", current.txPool},
		{"5 SQL 已到服务端：行锁等待/取消/独立清理", func(ctx context.Context) error { return current.sqlWait(ctx, config) }},
		{"6 Rows 迭代中的取消与 Rows.Err", current.rowsCancellation},
	}
	for _, step := range steps {
		fmt.Fprintf(out, "\n[%s]\n", step.name)
		if err = step.run(ctx); err != nil {
			return fmt.Errorf("实验 %s: %w", step.name, err)
		}
	}
	if err = require(db.Stats().InUse == 0, "所有实验结束后池 InUse 必须为0"); err != nil {
		return err
	}
	fmt.Fprintln(out, "[PASS] SELECT 副作用、参数边界、空行/NULL、池等待、SQL等待、取消和恢复均通过")
	return nil
}

// openPool 由 Run/SQL观察池创建者调用。默认 MaxOpen=1 是可控教学放大镜：
// 它让一个未释放 owner 就足以耗尽池。3分钟 lifetime、30秒 idle 回收控制闲连接，
// 它们不会中断正在使用的连接，也不能替代每个请求的 context 等待预算。
func openPool(config Config) (*sql.DB, error) {
	db, err := sql.Open("mysql", config.driverConfig().FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("构造 MySQL 池: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(3 * time.Minute)
	db.SetConnMaxIdleTime(30 * time.Second)
	return db, nil
}

// create 只创建 Run 验证并生成的表；不带 IF NOT EXISTS，发生随机碰撞时直接失败，
// 不把已存在的对象当成自己所有。标识符经 quotedTable 校验，业务值仍用 ?。
func (current *lesson) create(ctx context.Context) error {
	_, err := current.db.ExecContext(ctx, "CREATE TABLE "+current.table+` (
		id BIGINT NOT NULL PRIMARY KEY,
		code VARCHAR(100) NOT NULL,
		note VARCHAR(100) NULL,
		amount DECIMAL(10,2) NOT NULL,
		created_at DATETIME(6) NOT NULL,
		shipped_at DATETIME(6) NULL,
		quantity INT NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
	return err
}

// drop 只在 CREATE 成功后注册。先 DROP 精确对象，再 SELECT information_schema
// 证明它已不存在；不会按前缀批量扫描/删除，也不删除安装者提供的课程数据库。
func (current *lesson) drop(ctx context.Context, database, name string) error {
	if _, err := current.db.ExecContext(ctx, "DROP TABLE "+current.table); err != nil {
		return fmt.Errorf("清理本 run 的表: %w", err)
	}
	var remaining int
	if err := current.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", database, name).
		Scan(&remaining); err != nil {
		return fmt.Errorf("SELECT 核验 DROP: %w", err)
	}
	if err := require(remaining == 0, "DROP后独占表必须不存在"); err != nil {
		return err
	}
	fmt.Fprintln(current.out, "[清理] DROP 本 run 表；information_schema SELECT remaining=0")
	return nil
}

// require 给正常实验/测试提供独立不变量断言；它不把“没有 API 错误”替代业务事实。
func require(condition bool, fact string) error {
	if !condition {
		return fmt.Errorf("断言失败：%s", fact)
	}
	return nil
}

// rollback 是 Tx owner 的最后一道收尾，允许已经 Commit/Rollback 的 ErrTxDone。
// ErrTxDone 仅表示这个 Tx 已结束，不单独证明业务已提交；持久事实仍通过 SELECT。
func rollback(tx *sql.Tx) error {
	err := tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}
