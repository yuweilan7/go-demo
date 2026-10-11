package txcourse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"runtime"

	"github.com/go-sql-driver/mysql"
)

// Run 是 cmd/demo 和完整 integration test 的共同入口。它拥有一个最多3连接的池、
// 两张表；defer 注册顺序让实验任务先收尾、表再 DROP、池最后 Close。
// 真正业务证据来自 SELECT，不把 Ping/返回 error 作为持久状态的替代品。
func Run(ctx context.Context, out io.Writer) (err error) {
	c, err := LoadConfig()
	if err != nil {
		return err
	}
	db, err := openPool(c, "", 3)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, db.Close())
		stats := db.Stats()
		fmt.Fprintf(out, "[cleanup.DB] open=%d inuse=%d idle=%d\n", stats.OpenConnections, stats.InUse, stats.Idle)
		if stats.OpenConnections != 0 || stats.InUse != 0 {
			err = errors.Join(err, errors.New("DB Close 后仍有连接"))
		}
	}()
	var version, database string
	if err := db.QueryRowContext(ctx, "SELECT VERSION(),DATABASE()").Scan(&version, &database); err != nil {
		return err
	}
	if version != c.Version || database != c.Database {
		return fmt.Errorf("服务与 READY 索引不符: version=%s database=%s", version, database)
	}
	fmt.Fprintf(out, "[environment] %s / driver1.10.1 / MySQL%s / %s\n", runtime.Version(), version, database)
	s, err := newStore(db, out)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.drop()) }()
	if err := s.create(ctx); err != nil {
		return err
	}
	for _, experiment := range []func(context.Context) error{
		s.correctCases,
		s.dangerousCases,
		func(ctx context.Context) error { return s.cancelCase(ctx, c) },
		func(ctx context.Context) error { return s.unknownCase(ctx, c) },
	} {
		if err := experiment(ctx); err != nil {
			return err
		}
	}
	return nil
}

// correctCases 由 Run 串行调用。每个 SKU 独立，正常/失败各用同样库存和订单契约。
// Assert 用独立固定预期；MySQLError 1062 证实第二步确实在真实服务上失败。
func (s *Store) correctCases(ctx context.Context) error {
	fmt.Fprintln(s.out, "=== E01 正确模式：成功 / 第二步失败 / 业务冲突 / panic ===")
	r := Request{"success-key", "success", 1}
	if err := s.seed(ctx, r.SKU, 5); err != nil {
		return err
	}
	if err := s.Place(ctx, r); err != nil {
		return err
	}
	if err := s.verify(ctx, "safe.success", r, 4, 1); err != nil {
		return err
	}
	// 同一个 key 重入会先扣再遇重复主键；必须回滚第二次扣减，仍 stock4/order1。
	err := s.Place(ctx, r)
	if !isDuplicate(err) || !errors.Is(err, ErrStepTwo) {
		return fmt.Errorf("真实第二步失败原因缺失: %w", err)
	}
	if err := s.verify(ctx, "safe.step2.rollback", r, 4, 1); err != nil {
		return err
	}
	r = Request{"conflict-key", "conflict", 1}
	if err := s.seed(ctx, r.SKU, 0); err != nil {
		return err
	}
	if err := s.Place(ctx, r); !errors.Is(err, ErrConflict) {
		return fmt.Errorf("库存0应业务冲突: %w", err)
	}
	if err := s.verify(ctx, "safe.conflict", r, 0, 0); err != nil {
		return err
	}
	r = Request{"panic-safe-key", "panic-safe", 1}
	if err := s.seed(ctx, r.SKU, 5); err != nil {
		return err
	}
	panicErr := catchPanic(func() error {
		return withTx(ctx, s.db, func(tx *sql.Tx) error {
			if err := s.reserve(ctx, tx, r); err != nil {
				return err
			}
			panic("订单插入前故障")
		})
	})
	if failure, ok := errors.AsType[*PanicFailure](panicErr); !ok || len(failure.Stack) == 0 {
		return errors.New("panic 必须以带现场栈的失败交给边界")
	}
	return s.verify(ctx, "safe.panic.rollback", r, 5, 0)
}

// isDuplicate 只看真实驱动类型/1062，不匹配错误文字；Join/包装不会丢掉底层原因。
func isDuplicate(err error) bool {
	e, ok := errors.AsType[*mysql.MySQLError](err)
	return ok && e != nil && e.Number == 1062
}

// seedDuplicate 建立已存在的订单，供危险/修复相同主键路径使用。它是实验准备，
// 不伪装正常业务；数量1且不预扣库存，使错误提交的额外扣减清楚可见。
func (s *Store) seedDuplicate(ctx context.Context, r Request) error {
	if err := s.seed(ctx, r.SKU, 5); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO "+quote(s.orders)+" (request_id,sku,quantity) VALUES (?,?,?)", r.RequestID, r.SKU, r.Quantity)
	return err
}

// dangerousCases 仅由明确故障章节调用，所有危险路径有外层资源保护。
// 先看错误副作用，再用同类独立 SKU 的 Place 对照修复，不把危险代码投入常规业务。
func (s *Store) dangerousCases(ctx context.Context) error {
	fmt.Fprintln(s.out, "=== E02 五个危险写法及实库修复 ===")
	r := Request{"bad-panic-key", "bad-panic", 1}
	if err := s.seed(ctx, r.SKU, 5); err != nil {
		return err
	}
	if err := catchPanic(func() error { return s.badNamedPanic(ctx, r) }); err == nil {
		return errors.New("危险 panic 必须被外层观察到")
	}
	if err := s.verify(ctx, "bad.panic.committed-partial", r, 4, 0); err != nil {
		return err
	}
	for _, tc := range []struct {
		name string
		fn   func(context.Context, Request) error
		nil  bool
	}{{"shadow", s.badShadow, true}, {"capture", s.badCaptured, false}} {
		r := Request{"bad-" + tc.name + "-key", "bad-" + tc.name, 1}
		if err := s.seedDuplicate(ctx, r); err != nil {
			return err
		}
		err := tc.fn(ctx, r)
		if (err == nil) != tc.nil || (!tc.nil && !isDuplicate(err)) {
			return fmt.Errorf("%s caller error 与预期不符: %w", tc.name, err)
		}
		if err := s.verify(ctx, "bad."+tc.name, r, 4, 1); err != nil {
			return err
		}
		fixed := Request{"fixed-" + tc.name + "-key", "fixed-" + tc.name, 1}
		if err := s.seedDuplicate(ctx, fixed); err != nil {
			return err
		}
		if err := s.Place(ctx, fixed); !isDuplicate(err) {
			return fmt.Errorf("修复版应保留重复主键: %w", err)
		}
		if err := s.verify(ctx, "fixed."+tc.name, fixed, 5, 1); err != nil {
			return err
		}
	}
	if err := s.forgotCase(ctx); err != nil {
		return err
	}
	r = Request{"bad-mix-key", "bad-mix", 1}
	if err := s.seed(ctx, r.SKU, 5); err != nil {
		return err
	}
	if err := s.badMix(ctx, r); !errors.Is(err, ErrStepTwo) {
		return fmt.Errorf("混用 DB 路径应失败: %w", err)
	}
	if err := s.verify(ctx, "bad.db-tx-mix", r, 5, 1); err != nil {
		return err
	}
	// 同样的“订单之后业务失败”全部留在 Tx 中，库存和订单一起回滚。
	r = Request{"fixed-mix-key", "fixed-mix", 1}
	if err := s.seed(ctx, r.SKU, 5); err != nil {
		return err
	}
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := s.reserve(ctx, tx, r); err != nil {
			return err
		}
		if err := s.insertOrder(ctx, tx, r); err != nil {
			return err
		}
		return ErrStepTwo
	})
	if !errors.Is(err, ErrStepTwo) {
		return errors.New("修复版丢失主失败")
	}
	return s.verify(ctx, "fixed.db-tx-mix", r, 5, 0)
}

// forgotCase 负责反例的真实 Tx。观察前不替坏函数补 Rollback，观察后显式修复；
// 最早登记的 defer 又保护任意提前失败，课堂不会把未结束事务交给下一 case。
func (s *Store) forgotCase(ctx context.Context) (err error) {
	r := Request{"forgot-key", "forgot", 1}
	if err := s.seedDuplicate(ctx, r); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rollback(tx)) }()
	if err := s.badForgot(ctx, tx, r); !isDuplicate(err) {
		return fmt.Errorf("忘回滚反例应在第二步失败: %w", err)
	}
	var inside int64
	if err := tx.QueryRowContext(ctx, "SELECT quantity FROM "+quote(s.stock)+" WHERE sku=?", r.SKU).Scan(&inside); err != nil {
		return err
	}
	if inside != 4 || s.db.Stats().InUse != 1 {
		return errors.New("错误返回后 Tx 应仍持连接且内部读到4")
	}
	fmt.Fprintf(s.out, "[bad.forgot] caller失败但 Tx 仍可读 quantity=%d pool.inuse=%d\n", inside, s.db.Stats().InUse)
	if err := s.verify(ctx, "bad.forgot.external-before-cleanup", r, 5, 1); err != nil {
		return err
	}
	if err := rollback(tx); err != nil {
		return err
	}
	if s.db.Stats().InUse != 0 {
		return errors.New("显式修复 Rollback 后连接仍被占用")
	}
	return s.verify(ctx, "fixed.forgot.rollback", r, 5, 1)
}
