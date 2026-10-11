// Package txcourse 用真实 MySQL 表区分 Go 控制流与数据库持久事实。
package txcourse

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
)

var (
	ErrConflict = errors.New("库存不足或请求冲突")
	ErrUnknown  = errors.New("提交结果 UNKNOWN，必须按原 request_id 查账")
	ErrStepTwo  = errors.New("第二步订单插入失败")
	ErrJoin     = errors.New("worker 清理尚未确认完成")
)

// Store 不拥有 db：Run 拥有池生命周期。stock/orders 只能由 newStore 创建，
// 是本 run 独占的两个表名；业务函数借用 Store，不能 Close 共享 db。
type Store struct {
	db            *sql.DB
	stock, orders string
	out           io.Writer
}

// Request 是每次调用的业务值副本。RequestID 同时是唯一键和查账键，
// 未知提交后仍使用原值，换新 key 会失去幂等边界。
type Request struct {
	RequestID, SKU string
	Quantity       int64
}

// Snapshot 是独立连接 SELECT 得到的值，不是从 error 或内存日志猜出的状态。
type Snapshot struct {
	Stock, Orders, OrderQuantity int64
	OrderSKU                     string
}

var identifier = regexp.MustCompile(`^m21_[a-f0-9]{24}_(stock|orders)$`)

// newStore 由 Run 调用，用密码学随机后缀隔离同时执行的 demo/test/race。
// ? 不能绑定 SQL 标识符，所以表名必须先验证再加反引号，业务值仍全部参数绑定。
func newStore(db *sql.DB, out io.Writer) (*Store, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	prefix := "m21_" + hex.EncodeToString(b[:])
	return &Store{db, prefix + "_stock", prefix + "_orders", out}, nil
}

// quote 只接受本课生成标识符；panic 表示程序内部破坏了建表契约，不能接受外部输入。
func quote(name string) string {
	if !identifier.MatchString(name) {
		panic("非法 M21 内部表名")
	}
	return "`" + name + "`"
}

// create 由 Run 在登记 defer drop 之后调用；即使第二个 CREATE 失败，drop 也会
// 精确清理第一个表。DDL 自己会提交，绝不能把 CREATE/DROP 混进库存业务 Tx。
func (s *Store) create(ctx context.Context) error {
	for _, query := range []string{
		"CREATE TABLE " + quote(s.stock) + " (sku VARCHAR(80) PRIMARY KEY, quantity BIGINT NOT NULL CHECK (quantity >= 0)) ENGINE=InnoDB",
		"CREATE TABLE " + quote(s.orders) + " (request_id VARCHAR(80) PRIMARY KEY, sku VARCHAR(80) NOT NULL, quantity BIGINT NOT NULL CHECK (quantity > 0)) ENGINE=InnoDB",
	} {
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	fmt.Fprintf(s.out, "[namespace] %s / %s\n", s.stock, s.orders)
	return nil
}

// drop 是本 run 表的唯一清理 owner。用新预算执行精确 DROP，然后独立查询
// information_schema 确认两个目标已不存在；没有 broad DROP 或删别的 run。
func (s *Store) drop() error {
	ctx, cancel := shortContext()
	defer cancel()
	var err error
	for _, name := range []string{s.orders, s.stock} {
		_, e := s.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+quote(name))
		err = errors.Join(err, e)
	}
	var remaining int
	e := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME IN (?,?)", s.stock, s.orders).Scan(&remaining)
	err = errors.Join(err, e)
	if e == nil && remaining != 0 {
		err = errors.Join(err, fmt.Errorf("精确 DROP 后仍有 %d 个本 run 表", remaining))
	}
	fmt.Fprintf(s.out, "[cleanup.tables] remaining=%d\n", remaining)
	return err
}

// seed 为各 case 创建独立 SKU；不删除/覆盖之前 case 的数据，让错误和修复可同时查。
func (s *Store) seed(ctx context.Context, sku string, quantity int64) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO "+quote(s.stock)+" (sku,quantity) VALUES (?,?)", sku, quantity)
	return err
}

// reserve 由业务层调用，参数 tx 是显式同一事务的连接归属。
// 条件 UPDATE 把“检查库存”和“扣减”合成一个原子 SQL；RowsAffected=0 是业务
// 冲突，不等于通信失败。所有 SQL 使用 tx；改用 s.db 会绕开本次事务。
func (s *Store) reserve(ctx context.Context, tx *sql.Tx, r Request) error {
	result, err := tx.ExecContext(ctx, "UPDATE "+quote(s.stock)+" SET quantity=quantity-? WHERE sku=? AND quantity>=?", r.Quantity, r.SKU, r.Quantity)
	if err != nil {
		return fmt.Errorf("Tx 库存 UPDATE: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取库存 affected rows: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("sku=%s affected=%d: %w", r.SKU, n, ErrConflict)
	}
	// Tx 内再次读 quantity：这里看到自己的未提交写入，不能当作持久成功。
	var remaining int64
	if err := tx.QueryRowContext(ctx, "SELECT quantity FROM "+quote(s.stock)+" WHERE sku=?", r.SKU).Scan(&remaining); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "[%s.reserve] affected=1 tx_quantity=%d（未提交）\n", r.SKU, remaining)
	return nil
}

// insertOrder 由 Place/危险实验调用，仅接受 *sql.Tx，阻止下层误用 *sql.DB。
// duplicate request_id 会由真实 MySQL 主键拒绝；error 包装保留 MySQLError 1062。
func (s *Store) insertOrder(ctx context.Context, tx *sql.Tx, r Request) error {
	result, err := tx.ExecContext(ctx, "INSERT INTO "+quote(s.orders)+" (request_id,sku,quantity) VALUES (?,?,?)", r.RequestID, r.SKU, r.Quantity)
	if err != nil {
		return errors.Join(ErrStepTwo, fmt.Errorf("Tx 订单 INSERT: %w", err))
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("订单 affected=%d，期望1", n)
	}
	fmt.Fprintf(s.out, "[%s.insert] affected=1 request_id=%s（未提交）\n", r.SKU, r.RequestID)
	return nil
}

// Snapshot 由验收/查账调用，通过池借另一条连接读取已经提交的业务事实。
// QueryRow.Scan 自动处理一行 Rows 的关闭；没有把裸 *Rows 交给 caller。
func (s *Store) Snapshot(ctx context.Context, r Request) (Snapshot, error) {
	var v Snapshot
	if err := s.db.QueryRowContext(ctx, "SELECT quantity FROM "+quote(s.stock)+" WHERE sku=?", r.SKU).Scan(&v.Stock); err != nil {
		return v, err
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(quantity),0),COALESCE(MAX(sku),'') FROM "+quote(s.orders)+" WHERE request_id=?", r.RequestID).Scan(&v.Orders, &v.OrderQuantity, &v.OrderSKU); err != nil {
		return v, err
	}
	return v, nil
}

// verify 的参数是 case 独立预期，不从待测函数的返回值推导预期。
// 一个库存和一个订单 SELECT 联合证明正常守恒/故障部分效果，不只断言 err!=nil。
func (s *Store) verify(ctx context.Context, label string, r Request, stock, orders int64) error {
	v, err := s.Snapshot(ctx, r)
	if err != nil {
		return err
	}
	fmt.Fprintf(s.out, "[%s.SELECT] stock=%d orders=%d order_quantity=%d order_sku=%s\n", label, v.Stock, v.Orders, v.OrderQuantity, v.OrderSKU)
	if v.Stock != stock || v.Orders != orders || (orders == 1 && (v.OrderQuantity != r.Quantity || v.OrderSKU != r.SKU)) {
		return fmt.Errorf("%s 业务表不符合独立预期: %+v", label, v)
	}
	return nil
}
