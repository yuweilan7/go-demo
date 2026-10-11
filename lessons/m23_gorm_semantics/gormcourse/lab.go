package gormcourse

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sync"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

var ErrBusiness = errors.New("合成订单第二步失败")
var ErrConflict = errors.New("归属不存在或库存不足")
var identifier = regexp.MustCompile(`^m23_[a-f0-9]{24}_(stocks|orders|lines|hook_audits|outboxes)$`)

// lab 是当前run的资源登记表。db和pool指向同一个池；broker只由Run关闭。
// 表名由随机prefix与锁定NamingStrategy得到，不接受业务请求提供的表名。
type lab struct {
	db     *gorm.DB
	pool   *sql.DB
	trace  *sqlTrace
	prefix string
	tables []string
	owned  bool
	broker *broker
	out    io.Writer
}

// safeOutput把owner与worker的输出统一串行化，caller传bytes.Buffer时也不会数据竞争。
// 只锁一次Write，不把锁跨SQL/AMQP等待持有，所以日志不会反过来阻塞业务资源清理。
type safeOutput struct {
	mu     sync.Mutex
	target io.Writer
}

func (s *safeOutput) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.target.Write(p)
}

// Run 由demo/integration test调用；按教学序列执行、任一步失败也走已登记的defer。
// 返回到caller时，自己的worker/Tx/queue/table/pool均已完成收尾，错误保留清理原因。
func Run(ctx context.Context, out io.Writer) (err error) {
	out = &safeOutput{target: out}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	pool, err := openPool(c)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, pool.Close())
		s := pool.Stats()
		fmt.Fprintf(out, "[cleanup.pool] open=%d inuse=%d idle=%d\n", s.OpenConnections, s.InUse, s.Idle)
		if s.OpenConnections != 0 || s.InUse != 0 || s.Idle != 0 {
			err = errors.Join(err, errors.New("池未完全关闭"))
		}
	}()
	var random [12]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	l := &lab{pool: pool, prefix: "m23_" + hex.EncodeToString(random[:]), out: out, trace: &sqlTrace{out: out}}
	for _, suffix := range []string{"stocks", "orders", "lines", "hook_audits", "outboxes"} {
		l.tables = append(l.tables, l.prefix+"_"+suffix)
	}
	// mysql dialector借已有pool，不新建第二池；关闭权仍属于本函数。
	l.db, err = gorm.Open(mysql.New(mysql.Config{Conn: pool}), &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: l.prefix + "_"}, Logger: l.trace, DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		return err
	}
	var version, database string
	if err = pool.QueryRowContext(ctx, "SELECT VERSION(),DATABASE()").Scan(&version, &database); err != nil {
		return err
	}
	if version != c.mysql.Version || database != "golearn_m20" {
		return errors.New("实库版本/归属与READY索引不同")
	}
	fmt.Fprintf(out, "[version] mysql=%s database=%s gorm=1.31.2 dialector=1.6.0 driver=1.10.1 rabbit=%s\n", version, database, c.rabbit.Version)
	if n, e := l.remaining(ctx); e != nil || n != 0 {
		return errors.Join(e, errors.New("随机教学表已存在，拒绝迁移/清理别人对象"))
	}
	l.owned = true
	defer func() { err = errors.Join(err, l.drop()) }() // 即使AutoMigrate只创建一部分，也精确收尾。
	fmt.Fprintf(out, "[namespace] tables=%v\n", l.tables)
	if err = l.db.WithContext(ctx).AutoMigrate(&Stock{}, &Order{}, &Line{}, &HookAudit{}, &Outbox{}); err != nil {
		return err
	}
	l.broker, err = openBroker(ctx, c, l.prefix+"_events", out)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, l.broker.close()) }()
	for _, step := range []struct {
		name string
		fn   func(context.Context) error
	}{
		{"E01.SQL/零值/全表/归属", l.crudCases},
		{"E02.链条件/Session/Scopes/generics", l.chainCases},
		{"E03.事务迁移/失败/panic", l.transactionCases},
		{"E04.软删除/关联/分页", l.hiddenCases},
		{"E05.未提交hook真实AMQP/修复", l.hookCases},
		{"E06.context真实行锁等待取消", l.cancelCase},
	} {
		fmt.Fprintf(out, "\n=== %s ===\n", step.name)
		if err = step.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return nil
}

// quote用于无法以?参数绑定的SQL标识符，只接受本run规则生成的名字。
func quote(name string) string {
	if !identifier.MatchString(name) {
		panic("非法M23内部表名")
	}
	return "`" + name + "`"
}

// remaining通过information_schema核对精确五个名字，前置保护和清理证明共用它。
func (l *lab) remaining(ctx context.Context) (int, error) {
	var n int
	err := l.pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME IN (?,?,?,?,?)", l.tables[0], l.tables[1], l.tables[2], l.tables[3], l.tables[4]).Scan(&n)
	return n, err
}

// drop只清理owned本run对象，用新Background预算，业务取消不影响此处清理能力。
// AutoMigrate的DDL会独立提交，所以不能期待业务Rollback替我们删除教学表。
func (l *lab) drop() (err error) {
	if !l.owned {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := len(l.tables) - 1; i >= 0; i-- {
		_, e := l.pool.ExecContext(ctx, "DROP TABLE IF EXISTS "+quote(l.tables[i]))
		err = errors.Join(err, e)
	}
	n, e := l.remaining(ctx)
	err = errors.Join(err, e)
	if e == nil && n != 0 {
		err = errors.Join(err, fmt.Errorf("本run残留%d张表", n))
	}
	fmt.Fprintf(l.out, "[cleanup.tables] exact=%v remaining=%d\n", l.tables, n)
	return err
}

// seed给每个错误/修复case独立SKU，避免前一case的写入污染后一case预期。
func (l *lab) seed(ctx context.Context, sku, owner string, quantity int64) error {
	return l.db.WithContext(ctx).Create(&Stock{SKU: sku, Owner: owner, Quantity: quantity, Active: true}).Error
}

// verify以database/sql独立SELECT查账，不拿待测GORM的Error/内存model作为持久证据。
// 预期qty/orderCount由case固定给出，联合证明库存与订单两步是否一起持久。
func (l *lab) verify(ctx context.Context, label, sku, id string, wantQty, wantOrders int64) error {
	var qty, count int64
	if err := l.pool.QueryRowContext(ctx, "SELECT quantity FROM "+quote(l.tables[0])+" WHERE sku=?", sku).Scan(&qty); err != nil {
		return err
	}
	if err := l.pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quote(l.tables[1])+" WHERE id=?", id).Scan(&count); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[%s.SELECT] quantity=%d orders=%d\n", label, qty, count)
	if qty != wantQty || count != wantOrders {
		return fmt.Errorf("%s库存/订单预期%d/%d实得%d/%d", label, wantQty, wantOrders, qty, count)
	}
	return nil
}

// require把固定业务不变量转换为error，让Run先清理再向入口返回失败。
func require(ok bool, text string) error {
	if !ok {
		return errors.New(text)
	}
	return nil
}
