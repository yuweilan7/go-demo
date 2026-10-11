package lockcourse

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// store 借用 Run 的 DB/输出，不拥有 Close。names 是精确preflight确认不存在后、
// 本run准备CREATE的尝试清单；先登记再发SQL，响应未知也不能逃掉DROP恢复。
type store struct {
	db                                  *sql.DB
	out                                 io.Writer
	prefix, stock, orders, ranges, scan string
	names                               []string
}

// Run 是模块公开入口，由cmd/demo或显式integration test调用；它拥有DB、随机表
// 和各实验。返回值只有在实验、任务join、DROP确认、DB.Close之后才交给caller。
func Run(ctx context.Context, out io.Writer, part string) (err error) {
	if part != "all" && part != "stock" {
		return errors.New("part只能为all或stock")
	}
	db, wantVersion, err := openPool()
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, db.Close())
		st := db.Stats()
		fmt.Fprintf(out, "[cleanup.DB] open=%d inuse=%d idle=%d\n", st.OpenConnections, st.InUse, st.Idle)
		if st.OpenConnections != 0 || st.InUse != 0 {
			err = errors.Join(err, errors.New("DB仍有连接"))
		}
	}()
	var version, database string
	var timeoutRollback, deadlockDetect int
	if err = db.QueryRowContext(ctx, "SELECT VERSION(),DATABASE(),@@innodb_rollback_on_timeout,@@innodb_deadlock_detect").Scan(&version, &database, &timeoutRollback, &deadlockDetect); err != nil {
		return err
	}
	if version != wantVersion || database != "golearn_m20" || deadlockDetect != 1 {
		return fmt.Errorf("实库版本/库/死锁检测不符合索引: version=%s database=%s detect=%d", version, database, deadlockDetect)
	}
	fmt.Fprintf(out, "[environment] MySQL=%s database=%s rollback_on_timeout=%d deadlock_detect=%d max_open=4\n", version, database, timeoutRollback, deadlockDetect)
	s, err := newStore(db, out)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.drop()) }() // 在CREATE之前登记，覆盖中途失败。
	if err = s.create(ctx); err != nil {
		return err
	}
	if err = s.stockCases(ctx); err != nil {
		return fmt.Errorf("E01库存: %w", err)
	}
	if part == "stock" {
		return nil
	}
	if err = s.snapshotCases(ctx); err != nil {
		return fmt.Errorf("E02隔离: %w", err)
	}
	if err = s.indexCases(ctx); err != nil {
		return fmt.Errorf("E03索引/gap: %w", err)
	}
	if err = s.deadlockCase(ctx); err != nil {
		return fmt.Errorf("E04死锁: %w", err)
	}
	if err = s.timeoutCase(ctx, timeoutRollback); err != nil {
		return fmt.Errorf("E05锁超时: %w", err)
	}
	if err = s.externalCase(ctx); err != nil {
		return fmt.Errorf("E06慢外部请求: %w", err)
	}
	return nil
}

// newStore由Run/真实DDL断言调用，仅生成本run随机名字，不执行SQL。DB/out是
// caller借给store的指针/接口；调用方登记drop之后才能调用create，不能认领旧表。
func newStore(db *sql.DB, out io.Writer) (*store, error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	p := "m22_" + hex.EncodeToString(nonce[:])
	return &store{db: db, out: out, prefix: p, stock: p + "_stock", orders: p + "_orders", ranges: p + "_ranges", scan: p + "_scan"}, nil
}

// create 仅创建随机run自有对象。先查精确名字已不存在，再CREATE（不用IF NOT
// EXISTS冒认旧表）；标识符来自程序生成的固定ASCII，SQL值仍由?传入。
func (s *store) create(ctx context.Context) error {
	return s.createWithBoundary(ctx, nil)
}

// createWithBoundary由create/实库归属断言调用。execCreate是应用执行边界：测试
// 实际Exec成功后让这层收到人为确认错误，模拟“上层未确认”而非wire响应丢失。
// nil使用真实db.Exec。preflight旧对象绝不登记，确认不存在后先登记本次尝试。
func (s *store) createWithBoundary(ctx context.Context, execCreate func(context.Context, string) error) error {
	if execCreate == nil {
		execCreate = func(ctx context.Context, query string) error { _, err := s.db.ExecContext(ctx, query); return err }
	}
	defs := []struct{ name, columns string }{
		{s.stock, "id INT PRIMARY KEY, stock INT NOT NULL, version INT NOT NULL DEFAULT 0"},
		{s.orders, "request_id VARCHAR(80) PRIMARY KEY, sku INT NOT NULL, qty INT NOT NULL"},
		{s.ranges, "id INT PRIMARY KEY, value INT NOT NULL DEFAULT 0"},
		{s.scan, "id INT PRIMARY KEY, bucket INT NOT NULL, value INT NOT NULL DEFAULT 0"},
	}
	for _, d := range defs {
		var n int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", d.name).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return fmt.Errorf("拒绝认领已有对象%s", d.name)
		}
		// Exec报错不能证明CREATE没发生：服务器可能已完成DDL、响应却丢失。
		// 精确不存在的随机名字从这一行起归本run恢复清理；禁止等成功后才登记。
		s.names = append(s.names, d.name)
		if err := execCreate(ctx, "CREATE TABLE `"+d.name+"` ("+d.columns+") ENGINE=InnoDB"); err != nil {
			return err
		}
	}
	fmt.Fprintf(s.out, "[namespace] tables=%s\n", strings.Join(s.names, ","))
	return nil
}

// drop 用独立预算精确删除本run拥有的CREATE尝试，然后再次查询精确名字。IF
// EXISTS覆盖“实际未创建/已创建但未确认/已清理”的恢复状态，不认领preflight旧表。
// 它不按前缀扫删，
// 不删数据库，不碰M20/M21或同层模块的数据；逐个错误保留，不因首个失败停清理。
func (s *store) drop() (err error) {
	ctx, cancel := cleanupContext()
	defer cancel()
	for i := len(s.names) - 1; i >= 0; i-- {
		name := s.names[i]
		_, e := s.db.ExecContext(ctx, "DROP TABLE IF EXISTS `"+name+"`")
		err = errors.Join(err, e)
		var n int
		e = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", name).Scan(&n)
		err = errors.Join(err, e)
		if e == nil && n != 0 {
			err = errors.Join(err, fmt.Errorf("清理后表%s仍存在", name))
		}
		fmt.Fprintf(s.out, "[cleanup.table] %s remaining=%d\n", name, n)
	}
	return err
}

// seedStock 给每个case的独占SKU准备库存，不重置其他case。复用本run表并不
// 等于复用业务行；参数化id/stock防止把业务值拼进SQL。
func (s *store) seedStock(ctx context.Context, id, stock int) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO `"+s.stock+"`(id,stock) VALUES (?,?)", id, stock)
	return err
}

// audit 在所有worker Finished后用池的另一条读连接查独立持久事实。调用方给
// expect，来自实验不变量而不是调用返回值；Count(DISTINCT)证明订单key唯一。
func (s *store) audit(ctx context.Context, label string, id, wantStock, wantOrders, wantQty int) error {
	var stock, orders, qty, unique int
	if err := s.db.QueryRowContext(ctx, "SELECT stock FROM `"+s.stock+"` WHERE id=?", id).Scan(&stock); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(qty),0),COUNT(DISTINCT request_id) FROM `"+s.orders+"` WHERE sku=?", id).Scan(&orders, &qty, &unique); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "[%s.SELECT] sku=%d stock=%d orders=%d qty=%d unique=%d\n", label, id, stock, orders, qty, unique)
	if stock != wantStock || orders != wantOrders || qty != wantQty || unique != orders {
		return fmt.Errorf("%s业务事实不符: stock=%d orders=%d qty=%d unique=%d", label, stock, orders, qty, unique)
	}
	return nil
}

// waitSQL 给锁实验提供观测屏障：只查本worker connection ID，Info必须包含本run
// 标记。PROCESSLIST state名是实现观察，不要求叫某个固定词；后续真实1205或
// 持锁期间完成结果分别证明阻塞/不阻塞。ticker不是sleep猜worker已启动。
func (s *store) waitSQL(ctx context.Context, id int64, marker string) error {
	return observe(ctx, func() (bool, error) {
		var state, info sql.NullString
		err := s.db.QueryRowContext(ctx, "SELECT STATE,INFO FROM information_schema.PROCESSLIST WHERE ID=?", id).Scan(&state, &info)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if strings.Contains(info.String, marker) {
			fmt.Fprintf(s.out, "[wait.PROCESSLIST] own_id=%d marker=%s state=%q\n", id, marker, state.String)
			return true, nil
		}
		return false, nil
	})
}

// explain 由隔离/索引实验调用，query来自本课固定SQL，wantKey/wantAccess是caller
// 给出的实验执行条件；记录实际FORMAT=JSON再断言，不猜优化器使用哪个索引。
// 本课查询只有单行结果，QueryRow.Scan管理Rows生命周期，不向外泄漏裸Rows。
func (s *store) explain(ctx context.Context, label, query, wantKey, wantAccess string) error {
	var plan string
	if err := s.db.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+query).Scan(&plan); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "[%s.EXPLAIN] %s\n", label, plan)
	var decoded struct {
		Block struct {
			Table struct {
				Key    string
				Access string `json:"access_type"`
			} `json:"table"`
		} `json:"query_block"`
	}
	if err := json.Unmarshal([]byte(plan), &decoded); err != nil {
		return err
	}
	if decoded.Block.Table.Key != wantKey || decoded.Block.Table.Access != wantAccess {
		return fmt.Errorf("%s实际执行计划key=%s access=%s，不符合本实验条件key=%s access=%s", label, decoded.Block.Table.Key, decoded.Block.Table.Access, wantKey, wantAccess)
	}
	return nil
}
