package lab

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/go-sql-driver/mysql"
)

var ErrUnknown = errors.New("UNKNOWN: 按原key查权威库，不能制造新key重试")
var ErrVersion = errors.New("聚合版本乱序，停止本分区进度")
var ErrEffect = errors.New("inbox之后业务效果故障")
var ErrKeyConflict = errors.New("原请求key绑定的业务参数不一致")

// Event 是稳定业务事实，不包含可变的Kafka offset。relay每次重投保留同一个ID；
// SKU作为聚合/key，Version作为业务顺序，Qty由原订单确定，不能让重投重新扣源库存。
type Event struct {
	ID, Request, SKU string
	Qty, Version     int64
}
type Request struct {
	Key, SKU string
	Qty      int64
}

// Store 只借用DB，不关闭它。Prefix来自parent随机生成并登记的名字；
// 子进程也校验同一格式，避免把任意命令行值拼进表名。Outbox/Inbox是SQL持久事实。
type Store struct {
	DB     *sql.DB
	Prefix string
	Out    io.Writer
	Owned  []string
}

var prefixPattern = regexp.MustCompile(`^m30_[a-f0-9]{16}$`)

// table 的caller只传源码中的固定后缀。SQL参数?不能绑定标识符，因此先严格校验前缀。
func (s *Store) table(suffix string) string { return "`" + s.Prefix + "_" + suffix + "`" }

// require 是实验独立断言出口；失败保留上下文，不把日志打印当成业务正确性证明。
func require(ok bool, format string, args ...any) error {
	if !ok {
		return fmt.Errorf("断言失败: "+format, args...)
	}
	return nil
}

// eventID由Place/对账调用。相同key跨进程仍得到相同ID，不能用每次请求的新随机数。
func eventID(key string) string { h := sha256.Sum256([]byte(key)); return hex.EncodeToString(h[:]) }

// rollback 被Tx owner的defer调用。ErrTxDone只是对象已经结束；它不证明服务器回滚。
func rollback(tx *sql.Tx) error {
	e := tx.Rollback()
	if errors.Is(e, sql.ErrTxDone) {
		return nil
	}
	return cleanupError(e)
}

// withTx 的caller传同步callback；callback只能借Tx，不能通过外层DB绕开事务。
// 显式Commit只在完整成功路径发生。具名err让退出闭包把清理失败加入原错误；
// panic仍在同一goroutine展开并传播，defer会先Rollback，不根据err==nil误提交。
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		// panic没有给named err赋值，直接deferred recover只用于保留清理原因，
		// 回滚后原panic继续传播，不恢复成成功、不走显式Commit。
		if value := recover(); value != nil {
			cleanup := rollback(tx)
			if cleanup != nil {
				cause, ok := value.(error)
				if !ok {
					cause = fmt.Errorf("业务panic: %v", value)
				}
				panic(errors.Join(cause, cleanup))
			}
			panic(value)
		}
		err = errors.Join(err, rollback(tx))
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return errors.Join(ErrUnknown, err)
	}
	return nil
}

// Create 在任何CREATE前查询全部名字不存在；Run已先写无秘密资源登记。
// names以精确数组保存，失败也由外层Cleanup逐个删除已获独占授权的对象。
func (s *Store) Create(ctx context.Context) error {
	if !prefixPattern.MatchString(s.Prefix) {
		return errors.New("非法M30前缀")
	}
	for _, suffix := range tableSuffixes {
		var n int
		if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", s.Prefix+"_"+suffix).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return errors.New("独占表已存在，拒绝创建或清理未知owner对象")
		}
	}
	// 全部不存在之后才取得独占清理授权，CREATE响应丢失也能按精确名单收尾。
	for _, suffix := range tableSuffixes {
		s.Owned = append(s.Owned, s.Prefix+"_"+suffix)
	}
	definitions := []string{
		"(sku VARCHAR(64) PRIMARY KEY, qty BIGINT NOT NULL, version BIGINT NOT NULL) ENGINE=InnoDB",
		"(request_id VARCHAR(80) PRIMARY KEY,event_id CHAR(64) NOT NULL UNIQUE,sku VARCHAR(64) NOT NULL,qty BIGINT NOT NULL,version BIGINT NOT NULL) ENGINE=InnoDB",
		"(event_id CHAR(64) PRIMARY KEY,request_id VARCHAR(80) NOT NULL,sku VARCHAR(64) NOT NULL,qty BIGINT NOT NULL,version BIGINT NOT NULL,sent BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB",
		"(event_id CHAR(64) PRIMARY KEY,request_id VARCHAR(80) NOT NULL,sku VARCHAR(64) NOT NULL,qty BIGINT NOT NULL,version BIGINT NOT NULL) ENGINE=InnoDB",
		"(sku VARCHAR(64) PRIMARY KEY,qty BIGINT NOT NULL,version BIGINT NOT NULL,effects BIGINT NOT NULL) ENGINE=InnoDB",
	}
	for i, suffix := range tableSuffixes {
		if _, err := s.DB.ExecContext(ctx, "CREATE TABLE "+s.table(suffix)+definitions[i]); err != nil {
			return err
		}
	}
	return nil
}

var tableSuffixes = []string{"stock", "orders", "outbox", "inbox", "projection"}

// Seed 每个case只初始化自己的SKU，源库存与消费投影都从10开始；两处是不同系统状态。
func (s *Store) Seed(ctx context.Context, sku string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "INSERT INTO "+s.table("stock")+" VALUES (?,10,0)", sku); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO "+s.table("projection")+" VALUES (?,10,0,0)", sku)
		return e
	})
}

// QueryOrder 是UNKNOWN请求查询接口。caller必须使用原key和权威主库；found=false
// 仅表示查询时未见记录，生产中不能由一次NoRows猜请求早已结束。Event返回值是副本。
func (s *Store) QueryOrder(ctx context.Context, key string) (e Event, found bool, err error) {
	err = s.DB.QueryRowContext(ctx, "SELECT event_id,request_id,sku,qty,version FROM "+s.table("orders")+" WHERE request_id=?", key).Scan(&e.ID, &e.Request, &e.SKU, &e.Qty, &e.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return e, false, nil
	}
	return e, err == nil, err
}

// recoverOrderDuplicate由Place的失败出口调用，E03也用真实1062核验同一边界。
// r是原请求副本；cause是withTx已经完成回滚后的整棵错误树，query同步借用caller
// 的ctx/DB查询原key，不新建任务、不拥有连接池。返回recovered=true才可交成成功。
func recoverOrderDuplicate(r Request, cause error, query func() (Event, bool, error)) (Event, bool, error) {
	// Is检查整棵Join树：即使某分支能As出1062，清理失败/提交未知也不能被查询
	// 到旧订单掩盖。直接保留原cause，且不调用query，不冒称第二次扣减已安全回滚。
	if errors.Is(cause, ErrCleanup) || errors.Is(cause, ErrUnknown) {
		return Event{}, false, cause
	}
	var duplicate *mysql.MySQLError
	if !errors.As(cause, &duplicate) || duplicate.Number != 1062 {
		return Event{}, false, cause
	}
	// As沿普通包装/Join提取*MySQLError；&duplicate让它写入这个指针变量。
	// 只有安全回滚后的1062才查权威原key；1062自身不表示Commit成功。
	old, found, err := query()
	if err != nil {
		return Event{}, false, errors.Join(cause, err)
	}
	if !found {
		return Event{}, false, cause
	}
	if old.SKU != r.SKU || old.Qty != r.Qty {
		return Event{}, false, errors.Join(cause, ErrKeyConflict)
	}
	return old, true, nil
}

// Place 是下单业务入口；Run/崩溃子进程传入合成Request。afterWrites只用于实验门，
// 同步调用时Tx仍拥有连接，强杀会跳过defer；afterCommit门则在真实Commit已确认之后。
// 同key重试返回原Event，不再次扣库存。真实1062竞争也先完整回滚再权威查原key。
func (s *Store) Place(ctx context.Context, r Request, outbox bool, afterWrites, afterCommit func() error) (event Event, err error) {
	if r.Key == "" || len(r.Key) > 80 || r.SKU == "" || r.Qty <= 0 {
		return event, errors.New("非法订单输入")
	}
	old, found, err := s.QueryOrder(ctx, r.Key)
	if err != nil {
		return event, err
	}
	if found {
		if old.SKU != r.SKU || old.Qty != r.Qty {
			return event, ErrKeyConflict
		}
		return old, nil
	}
	event = Event{ID: eventID(r.Key), Request: r.Key, SKU: r.SKU, Qty: r.Qty}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		// FOR UPDATE把同SKU版本/库存改动串行化；查询得到的是当前锁定行而非旧快照。
		var stock, version int64
		if e := tx.QueryRowContext(ctx, "SELECT qty,version FROM "+s.table("stock")+" WHERE sku=? FOR UPDATE", r.SKU).Scan(&stock, &version); e != nil {
			return e
		}
		// 初次QueryOrder无记录之后，另一个请求可能先提交同key。持锁后再做current
		// read，避免库存最后一个刚被原请求扣完时，把合法重试误报成“库存不足”。
		var existing Event
		q := tx.QueryRowContext(ctx, "SELECT event_id,request_id,sku,qty,version FROM "+s.table("orders")+" WHERE request_id=? FOR UPDATE", r.Key).Scan(&existing.ID, &existing.Request, &existing.SKU, &existing.Qty, &existing.Version)
		if q == nil {
			if existing.SKU != r.SKU || existing.Qty != r.Qty {
				return ErrKeyConflict
			}
			event = existing
			return nil
		}
		if !errors.Is(q, sql.ErrNoRows) {
			return q
		}
		if stock < r.Qty {
			return errors.New("源库存不足")
		}
		event.Version = version + 1 // event由闭包捕获；callback同步执行，没有并发共享写。
		if _, e := tx.ExecContext(ctx, "UPDATE "+s.table("stock")+" SET qty=qty-?,version=version+1 WHERE sku=?", r.Qty, r.SKU); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO "+s.table("orders")+" VALUES (?,?,?,?,?)", event.Request, event.ID, event.SKU, event.Qty, event.Version); e != nil {
			return e
		}
		if outbox {
			if _, e := tx.ExecContext(ctx, "INSERT INTO "+s.table("outbox")+" (event_id,request_id,sku,qty,version) VALUES (?,?,?,?,?)", event.ID, event.Request, event.SKU, event.Qty, event.Version); e != nil {
				return e
			}
		}
		if afterWrites != nil {
			return afterWrites()
		}
		return nil
	})
	if err != nil {
		old, recovered, cause := recoverOrderDuplicate(r, err, func() (Event, bool, error) {
			return s.QueryOrder(ctx, r.Key)
		})
		if recovered {
			return old, nil
		}
		return event, cause
	}
	if afterCommit != nil {
		err = afterCommit()
	}
	return event, err
}

// Pending 供单relay读取已提交的一条outbox。QueryRow.Scan内部关闭Rows；
// 没有并行relay领取协议，这条SELECT并不能当成跨进程互斥保证。
func (s *Store) Pending(ctx context.Context, id string) (e Event, err error) {
	err = s.DB.QueryRowContext(ctx, "SELECT event_id,request_id,sku,qty,version FROM "+s.table("outbox")+" WHERE event_id=? AND sent=FALSE", id).Scan(&e.ID, &e.Request, &e.SKU, &e.Qty, &e.Version)
	return
}

// Apply 由串行consumer调用，inbox和投影效果必须使用同一Tx。
// duplicate的1062分支只接受所有payload完全相同的原事件，否则拒绝ID复用。
// afterInbox/afterCommit是可选观测门；failEffect在inbox已插入后返回，验证整个Tx回滚。
func (s *Store) Apply(ctx context.Context, e Event, failEffect bool, afterInbox, afterCommit func() error) (applied bool, err error) {
	if e.ID == "" || e.ID != eventID(e.Request) || e.SKU == "" || e.Qty <= 0 || e.Version <= 0 {
		return false, errors.New("事件业务契约错误")
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		_, insertErr := tx.ExecContext(ctx, "INSERT INTO "+s.table("inbox")+" VALUES (?,?,?,?,?)", e.ID, e.Request, e.SKU, e.Qty, e.Version)
		if insertErr != nil {
			var d *mysql.MySQLError
			if !errors.As(insertErr, &d) || d.Number != 1062 {
				return insertErr
			}
			// 真实唯一键冲突会等已插入同ID的事务结束；不能吞掉其它SQL错误。
			// 后面的Scan读完整原payload，防止相同ID承载不同业务指令。
			var original Event
			if q := tx.QueryRowContext(ctx, "SELECT event_id,request_id,sku,qty,version FROM "+s.table("inbox")+" WHERE event_id=?", e.ID).Scan(&original.ID, &original.Request, &original.SKU, &original.Qty, &original.Version); q != nil {
				return q
			}
			if original != e {
				return ErrKeyConflict
			}
			return nil // 只读重复，安全跳过业务效果。
		}
		if afterInbox != nil {
			if e := afterInbox(); e != nil {
				return e
			}
		}
		if failEffect {
			return ErrEffect
		}
		var quantity, version int64
		if q := tx.QueryRowContext(ctx, "SELECT qty,version FROM "+s.table("projection")+" WHERE sku=? FOR UPDATE", e.SKU).Scan(&quantity, &version); q != nil {
			return q
		}
		if e.Version != version+1 {
			return fmt.Errorf("sku=%s expected=%d received=%d: %w", e.SKU, version+1, e.Version, ErrVersion)
		}
		if quantity < e.Qty {
			return errors.New("消费端库存不足")
		}
		if _, q := tx.ExecContext(ctx, "UPDATE "+s.table("projection")+" SET qty=qty-?,version=?,effects=effects+1 WHERE sku=?", e.Qty, e.Version, e.SKU); q != nil {
			return q
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, err
	} // Tx没提交，不能把闭包内的applied=true交成成功。
	if afterCommit != nil {
		err = afterCommit()
	}
	return applied, err
}

// Snapshot 由实验在崩溃Wait完成后调用；FOR UPDATE读让查询等断连事务释放锁，
// 不用一次普通快照SELECT假定server已经回滚。结果来自独立连接上的新事务。
func (s *Store) Snapshot(ctx context.Context, sku string) (v Snapshot, err error) {
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if e := tx.QueryRowContext(ctx, "SELECT qty,version FROM "+s.table("stock")+" WHERE sku=? FOR UPDATE", sku).Scan(&v.Stock, &v.SourceVersion); e != nil {
			return e
		}
		if e := tx.QueryRowContext(ctx, "SELECT qty,version,effects FROM "+s.table("projection")+" WHERE sku=? FOR UPDATE", sku).Scan(&v.Projected, &v.Version, &v.Effects); e != nil {
			return e
		}
		// consumer可能只插了inbox就崩溃，尚未锁投影行。这里锁定读inbox候选行，
		// 等其断连事务结束后再统计；单纯COUNT快照会暂时看不到未提交行而误判已清理。
		var barrier string
		if e := tx.QueryRowContext(ctx, "SELECT event_id FROM "+s.table("inbox")+" WHERE sku=? FOR UPDATE", sku).Scan(&barrier); e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		for _, q := range []struct {
			suffix string
			dst    *int64
		}{{"orders", &v.Orders}, {"outbox", &v.Outbox}, {"inbox", &v.Inbox}} {
			if e := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+s.table(q.suffix)+" WHERE sku=?", sku).Scan(q.dst); e != nil {
				return e
			}
		}
		return tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+s.table("outbox")+" WHERE sku=? AND sent=TRUE", sku).Scan(&v.Sent)
	})
	return
}

type Snapshot struct{ Stock, SourceVersion, Projected, Version, Effects, Orders, Outbox, Inbox, Sent int64 }

// Verify 与源码实现独立地比较预先写好的业务常量；输出帮助读者把状态连到case。
func (s *Store) Verify(ctx context.Context, sku string, want Snapshot) error {
	got, e := s.Snapshot(ctx, sku)
	if e != nil {
		return e
	}
	fmt.Fprintf(s.Out, "SQL sku=%s stock=%d orders=%d outbox=%d sent=%d inbox=%d projection=%d effects=%d version=%d\n", sku, got.Stock, got.Orders, got.Outbox, got.Sent, got.Inbox, got.Projected, got.Effects, got.Version)
	return require(got == want, "sku=%s got=%+v want=%+v", sku, got, want)
}
