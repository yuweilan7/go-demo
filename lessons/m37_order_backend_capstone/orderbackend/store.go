package orderbackend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store 借用同一个GORM根句柄和sql池，永不Close。表名前缀先验证后才能拼标识符；
// 参数?只绑定值，不能拿它绑定表名。所有用例Tx都从这个根派生。
type Store struct {
	DB     *gorm.DB
	SQL    *sql.DB
	Prefix string
	Out    io.Writer
}

var namespacePattern = regexp.MustCompile(`^m37_[a-f0-9]{16}$`)

// TableSuffixes每次返回独立slice，不把可写全局数组暴露给后续课程。
func TableSuffixes() []string { return []string{"stock", "orders", "outbox", "inbox", "receipts"} }

// NewStore 由Resources构造，接受具体非nil指针，避免接口typed nil接线陷阱。
func NewStore(db *gorm.DB, pool *sql.DB, prefix string, out io.Writer) (*Store, error) {
	if db == nil || pool == nil || out == nil || !namespacePattern.MatchString(prefix) {
		return nil, ErrInvalid
	}
	return &Store{DB: db, SQL: pool, Prefix: prefix, Out: out}, nil
}

// Table只允许固定五个业务表名。SQL标识符不能参数绑定，调用者不能拿外部输入拼表。
func (s *Store) Table(suffix string) string {
	for _, v := range TableSuffixes() {
		if suffix == v {
			return s.Prefix + "_" + suffix
		}
	}
	panic(ErrInvalid)
}
func (s *Store) table(suffix string) string { return "`" + s.Table(suffix) + "`" }

// rollback由withTx唯一owner调用。ErrTxDone只表示对象已结束，绝不推断已回滚/已提交。
func rollback(tx *sql.Tx) error {
	err := tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return CleanupCause(err)
}

// withTx 由Create/Apply调用，callback同步借用tx。GORM的默认单条写事务不足以覆盖
// 整个用例，且Transaction库helper忽略Rollback错误；这里显式用底层sql.Tx收尾。
// 具名err让defer读取最终错误并Join。panic先回滚，再携带清理原因继续展开，不能
// recover后返回nil让半成品提交。只有body全部成功才会走Commit。
func (s *Store) withTx(ctx context.Context, body func(*gorm.DB) error) (err error) {
	tx := s.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	raw, ok := tx.Statement.ConnPool.(*sql.Tx)
	if !ok {
		return errors.New("GORM Begin未绑定sql.Tx")
	}
	defer func() {
		if p := recover(); p != nil {
			clean := rollback(raw)
			if clean != nil {
				cause, ok := p.(error)
				if !ok {
					cause = fmt.Errorf("业务panic: %v", p)
				}
				panic(errors.Join(cause, clean))
			}
			panic(p)
		}
		err = errors.Join(err, rollback(raw))
	}()
	if err = body(tx); err != nil {
		return err
	}
	// hook即便错误地在取消后返回nil，也不能直接Commit。检查的是Begin/body使用
	// 的同一ctx。检查以后仍可能取消或丢失Commit响应，所以Commit错误依然UNKNOWN。
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = tx.Commit().Error; err != nil {
		return errors.Join(ErrUnknown, err)
	}
	return nil
}

// FindByKey 是UNKNOWN后的权威查单接口。owner/key条件不可省；空结果仅说明查询
// 时尚未见记录，不能证明另一个仍运行的请求已经不会提交。调用者先join/对账。
func (s *Store) FindByKey(ctx context.Context, owner, key string) (o Order, found bool, err error) {
	q := s.DB.WithContext(ctx).Table(s.Table("orders")).Where("owner=? AND request_key=?", owner, key).Take(&o)
	if errors.Is(q.Error, gorm.ErrRecordNotFound) {
		return o, false, nil
	}
	return o, q.Error == nil, q.Error
}

// RecoverDuplicate是Create实际失败出口，辅助测试直接验证这同一函数。
// 必须先看整棵error树的严重状态；1062+cleanup或UNKNOWN不能靠原单查询被改成成功。
func RecoverDuplicate(c Command, cause error, query func() (Order, bool, error)) (Order, bool, error) {
	if errors.Is(cause, ErrCleanup) || errors.Is(cause, ErrUnknown) {
		return Order{}, false, cause
	}
	var duplicate *driver.MySQLError
	if !errors.As(cause, &duplicate) || duplicate.Number != 1062 {
		return Order{}, false, cause
	}
	o, found, e := query()
	if e != nil {
		return Order{}, false, errors.Join(cause, e)
	}
	if !found {
		return Order{}, false, cause
	}
	if !SamePayload(o, c) {
		return Order{}, false, errors.Join(cause, ErrConflict)
	}
	return o, true, nil
}

// Create 是唯一写订单用例，HTTP Service与M38复用它。先权威查幂等，再在Tx锁SKU
// 当前行并二次查原key，解决“首单扣完库存后重试被误判缺货”。同一SKU串行是本课
// 限定，不把这把行锁扩展成多SKU/跨服务锁；唯一(owner,key)仍是持久兜底。
func (s *Store) Create(ctx context.Context, c Command, h Hooks) (o Order, err error) {
	if err = c.Validate(); err != nil {
		return o, err
	}
	old, found, err := s.FindByKey(ctx, c.Owner, c.Key)
	if err != nil {
		return o, err
	}
	if found {
		if !SamePayload(old, c) {
			return o, ErrConflict
		}
		return old, nil
	}
	o = Order{ID: OrderID(c.Owner, c.Key), Owner: c.Owner, Key: c.Key, RequestID: c.RequestID, SKU: c.Input.SKU, Quantity: c.Input.Quantity, AmountMinor: UnitMinor * c.Input.Quantity, Currency: "CNY", Status: "ACCEPTED", Version: 1}
	err = s.withTx(ctx, func(tx *gorm.DB) error {
		var stock struct{ Quantity int64 }
		if e := tx.Table(s.Table("stock")).Clauses(clause.Locking{Strength: "UPDATE"}).Where("sku=?", c.Input.SKU).Take(&stock).Error; e != nil {
			return e
		}
		// 闭包捕获o这个返回槽，但同步执行，无第二个goroutine并发改它。
		var existing Order
		q := tx.Table(s.Table("orders")).Clauses(clause.Locking{Strength: "UPDATE"}).Where("owner=? AND request_key=?", c.Owner, c.Key).Take(&existing)
		if q.Error == nil {
			if !SamePayload(existing, c) {
				return ErrConflict
			}
			o = existing
			return nil
		}
		if !errors.Is(q.Error, gorm.ErrRecordNotFound) {
			return q.Error
		}
		if stock.Quantity < c.Input.Quantity {
			return ErrStock
		}
		r := tx.Table(s.Table("stock")).Where("sku=? AND quantity>=?", c.Input.SKU, c.Input.Quantity).Update("quantity", gorm.Expr("quantity - ?", c.Input.Quantity))
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrStock
		}
		if e := tx.Table(s.Table("orders")).Create(&o).Error; e != nil {
			return e
		}
		event := EventFor(o)
		payload, e := json.Marshal(event)
		if e != nil {
			return e
		}
		// tx指针明确传给每项SQL，绝不换成s.DB。outbox和订单/扣库存同提交。
		if e = tx.Table(s.Table("outbox")).Create(map[string]any{"event_id": event.EventID, "order_id": o.ID, "payload": string(payload), "sent": false}).Error; e != nil {
			return e
		}
		fmt.Fprintf(s.Out, "TX_WRITES request=%s order=%s event=%s version=1 pool=%T stock_uncommitted=-%d\n", c.RequestID, o.ID, event.EventID, tx.Statement.ConnPool, c.Input.Quantity)
		if h.AfterOrderWrites != nil {
			return h.AfterOrderWrites(ctx, o)
		}
		return nil
	})
	if err != nil {
		old, recovered, cause := RecoverDuplicate(c, err, func() (Order, bool, error) { return s.FindByKey(ctx, c.Owner, c.Key) })
		if recovered {
			return old, nil
		}
		return o, cause
	}
	fmt.Fprintf(s.Out, "ORDER_COMMIT request=%s order=%s amount_minor=%d currency=%s\n", c.RequestID, o.ID, o.AmountMinor, o.Currency)
	if h.AfterOrderCommit != nil {
		if err = h.AfterOrderCommit(ctx, o); err != nil {
			return o, errors.Join(ErrUnknown, err)
		}
	}
	return o, nil
}

// Read 是用户隔离的权威查询；用左连接把持久receipt变成PENDING/RECORDED视图。
// MySQL只有一个主库，缓存不是事实owner。Scan零行不自动ErrRecordNotFound，检查Rows。
func (s *Store) Read(ctx context.Context, owner, id string) (v View, err error) {
	q := s.DB.WithContext(ctx).Raw("SELECT o.*, CASE WHEN r.order_id IS NULL THEN 'PENDING' ELSE 'RECORDED' END AS delivery_status FROM "+s.table("orders")+" o LEFT JOIN "+s.table("receipts")+" r ON r.order_id=o.order_id WHERE o.owner=? AND o.order_id=?", owner, id).Scan(&v)
	if q.Error != nil {
		return v, q.Error
	}
	if q.RowsAffected != 1 {
		return View{}, ErrNotFound
	}
	return v, nil
}

// Stock 由只读gRPC与实验查账调用，同步读取真实MySQL；返回值是观察时的数量副本。
func (s *Store) Stock(ctx context.Context) (int64, error) {
	var n int64
	e := s.SQL.QueryRowContext(ctx, "SELECT quantity FROM "+s.table("stock")+" WHERE sku=?", SKU).Scan(&n)
	return n, e
}

// Pending只读已提交的outbox，单relay没有跨进程claim。没有行时返回found=false。
func (s *Store) Pending(ctx context.Context) (event Event, found bool, err error) {
	var payload string
	err = s.SQL.QueryRowContext(ctx, "SELECT payload FROM "+s.table("outbox")+" WHERE sent=FALSE ORDER BY event_id LIMIT 1").Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return event, false, nil
	}
	if err != nil {
		return event, false, err
	}
	err = json.Unmarshal([]byte(payload), &event)
	return event, err == nil, err
}

// MarkSent由relay在真实ACK后调用。失败/响应丢失保留UNKNOWN，不能声称消息没发布。
func (s *Store) MarkSent(ctx context.Context, event Event) error {
	r := s.DB.WithContext(ctx).Table(s.Table("outbox")).Where("event_id=? AND sent=FALSE", event.EventID).Update("sent", true)
	if r.Error != nil {
		return errors.Join(ErrUnknown, r.Error)
	}
	if r.RowsAffected != 1 {
		return errors.Join(ErrUnknown, Assert(false, "single relay sent rows=%d", r.RowsAffected))
	}
	return nil
}

// Apply由consumer调用，inbox+receipt属于同一MySQL Tx；它绝不再次扣源库存。
// firstOffset记录第一次成功处理的位置，重复投递只推进broker，不增加receipt效果。
// 参数event是值副本。JSON比较采用服务端规范编码，拒绝同ID不同业务载荷。
func (s *Store) Apply(ctx context.Context, event Event, topic string, partition int32, offset int64) (applied bool, err error) {
	if event.EventID != EventFor(event.Order).EventID || event.Order.Version != 1 {
		return false, ErrConflict
	}
	// Key不出现在JSON中，权威校验时也不比较它；以order_id/owner读取完整原单。
	view, e := s.Read(ctx, event.Order.Owner, event.Order.ID)
	if e != nil {
		return false, e
	}
	want := EventFor(view.Order)
	wb, _ := json.Marshal(want)
	payload, e := json.Marshal(event)
	if e != nil {
		return false, e
	}
	if string(wb) != string(payload) {
		return false, ErrConflict
	}
	err = s.withTx(ctx, func(tx *gorm.DB) error {
		if e := tx.Table(s.Table("inbox")).Create(map[string]any{"event_id": event.EventID, "payload": string(payload), "topic": topic, "partition_id": partition, "first_offset": offset}).Error; e != nil {
			return e
		}
		if e := tx.Table(s.Table("receipts")).Create(map[string]any{"order_id": event.Order.ID, "event_id": event.EventID, "quantity": event.Order.Quantity, "effect_count": 1}).Error; e != nil {
			return e
		}
		return nil
	})
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrUnknown) || errors.Is(err, ErrCleanup) {
		return false, err
	}
	var duplicate *driver.MySQLError
	if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
		return false, err
	}
	var existing string
	if e = s.SQL.QueryRowContext(ctx, "SELECT payload FROM "+s.table("inbox")+" WHERE event_id=?", event.EventID).Scan(&existing); e != nil {
		return false, errors.Join(err, e)
	}
	if existing != string(payload) {
		return false, errors.Join(err, ErrConflict)
	}
	return false, nil
}
