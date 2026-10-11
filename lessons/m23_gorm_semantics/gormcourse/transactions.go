package gormcourse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// reserve只借caller明确传来的db句柄；tx是*gorm.DB并不等于任意*gorm.DB都有事务。
// Statement.ConnPool的动态类型区分外层*sql.DB与事务*sql.Tx；Go没有Spring线程绑定。
// owner/sku/qty来自实验请求值，条件扣减使“检查库存+扣减”在服务器原子完成。
func (l *lab) reserve(db *gorm.DB, sku, owner string, qty int64) error {
	if qty <= 0 {
		return ErrConflict
	}
	result := db.Model(&Stock{}).Scopes(owned(owner)).Where("sku = ? AND quantity >= ?", sku, qty).Update("quantity", gorm.Expr("quantity - ?", qty))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConflict
	}
	fmt.Fprintf(l.out, "[reserve] sku=%s rows=1 caller_pool=%T（Tx内仍未提交）\n", sku, db.Statement.ConnPool)
	return nil
}

// insertOrder是业务第二步，只借同一句柄，不拥有Commit/Rollback权。
func (l *lab) insertOrder(db *gorm.DB, sku, id string) error {
	return db.Create(&Order{ID: id, SKU: sku, Owner: "tx", Quantity: 1}).Error
}

// manualTx把M21的withTx契约迁到GORM：开始后立刻登记清理，完整body成功才显式Commit。
// gorm.Transaction v1.31.2本身忽略Rollback返回错误；这里用底层Tx保留清理原因。
// 命名err是返回槽，defer读取退出时值并Join；panic分支先回滚再继续向外传播。
func manualTx(db *gorm.DB, body func(*gorm.DB) error) (err error) {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	raw, ok := tx.Statement.ConnPool.(*sql.Tx)
	if !ok {
		return errors.New("手动事务不是sql.Tx")
	}
	defer func() {
		if value := recover(); value != nil {
			cleanup := rollback(raw)
			if cause, ok := value.(error); ok {
				panic(errors.Join(cause, cleanup))
			}
			if cleanup != nil {
				panic(fmt.Errorf("panic=%v cleanup=%w", value, cleanup))
			}
			panic(value)
		}
		err = errors.Join(err, rollback(raw))
	}()
	if err = body(tx); err != nil {
		return err
	}
	return tx.Commit().Error // 提交调用本身失败也必须交给caller，不能根据已执行SQL猜成功。
}

// rollback只忽略Tx已结束这一契约结果；它不能说明服务器是提交还是回滚。
func rollback(tx *sql.Tx) error {
	err := tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

// catchPanic是故障实验边界。只有它可以把继续传播的panic转成非nil结果供下一case继续。
// 它位于Transaction外层，GORM的defer先回滚，再执行本函数的recover。
func catchPanic(fn func() error) (err error) {
	defer func() {
		if value := recover(); value != nil {
			if cause, ok := value.(error); ok {
				err = fmt.Errorf("边界panic失败: %w", cause)
			} else {
				err = fmt.Errorf("边界panic失败: %v", value)
			}
		}
	}()
	return fn()
}

// recoverAsSuccess只用于标记的危险case：recover截断栈展开，但没有给err返回槽赋值。
// 被打断的body不会从panic后继续；此函数直接返回初始nil，外层Transaction会误提交。
func recoverAsSuccess(fn func() error) (err error) {
	defer func() { _ = recover() }()
	return fn()
}

// transactionCases沿M21相同“库存5/扣1/订单1”的不变量迁移到GORM。
// 每个危险case都是真副作用，随后用独立SQL查账；不是只观察callback是否返回error。
func (l *lab) transactionCases(ctx context.Context) error {
	root := l.db.WithContext(ctx)
	for _, sku := range []string{"manual", "manual-error", "manual-panic", "gorm", "mix", "mix-fixed", "swallow", "swallow-fixed", "panic-swallowed", "panic-fixed", "default-write"} {
		if err := l.seed(ctx, sku, "tx", 5); err != nil {
			return err
		}
	}
	if err := manualTx(root, func(tx *gorm.DB) error {
		if err := l.reserve(tx, "manual", "tx", 1); err != nil {
			return err
		}
		return l.insertOrder(tx, "manual", "manual")
	}); err != nil {
		return err
	}
	if err := l.verify(ctx, "manual.commit", "manual", "manual", 4, 1); err != nil {
		return err
	}
	manualError := manualTx(root, func(tx *gorm.DB) error {
		if err := l.reserve(tx, "manual-error", "tx", 1); err != nil {
			return err
		}
		if err := l.insertOrder(tx, "manual-error", "manual-error"); err != nil {
			return err
		}
		return ErrBusiness
	})
	if err := require(errors.Is(manualError, ErrBusiness), "手动事务业务错误必须保留"); err != nil {
		return err
	}
	if err := l.verify(ctx, "manual.error-rollback", "manual-error", "manual-error", 5, 0); err != nil {
		return err
	}
	manualPanic := catchPanic(func() error {
		return manualTx(root, func(tx *gorm.DB) error {
			if err := l.reserve(tx, "manual-panic", "tx", 1); err != nil {
				return err
			}
			panic(ErrBusiness)
		})
	})
	if err := require(errors.Is(manualPanic, ErrBusiness), "手动事务panic必须回滚并传播"); err != nil {
		return err
	}
	if err := l.verify(ctx, "manual.panic-rollback", "manual-panic", "manual-panic", 5, 0); err != nil {
		return err
	}
	if err := root.Transaction(func(tx *gorm.DB) error {
		if err := l.reserve(tx, "gorm", "tx", 1); err != nil {
			return err
		}
		return gorm.G[Order](tx).Create(ctx, &Order{ID: "gorm", SKU: "gorm", Owner: "tx", Quantity: 1})
	}); err != nil {
		return err
	}
	if err := l.verify(ctx, "gorm.commit", "gorm", "gorm", 4, 1); err != nil {
		return err
	}
	// 回调虽拿到tx，却把第二步传给root：另一连接的默认单条写事务已经提交。
	err := root.Transaction(func(tx *gorm.DB) error {
		if err := l.reserve(tx, "mix", "tx", 1); err != nil {
			return err
		}
		if err := l.insertOrder(root, "mix", "mix"); err != nil {
			return err
		}
		return ErrBusiness
	})
	if err := require(errors.Is(err, ErrBusiness), "混外层DB应返回业务失败"); err != nil {
		return err
	}
	if err := l.verify(ctx, "bad.outer-db", "mix", "mix", 5, 1); err != nil {
		return err
	}
	err = root.Transaction(func(tx *gorm.DB) error {
		if err := l.reserve(tx, "mix-fixed", "tx", 1); err != nil {
			return err
		}
		if err := l.insertOrder(tx, "mix-fixed", "mix-fixed"); err != nil {
			return err
		}
		return ErrBusiness
	})
	if err := require(errors.Is(err, ErrBusiness), "同tx修复应返回业务失败"); err != nil {
		return err
	}
	if err := l.verify(ctx, "fixed.outer-db", "mix-fixed", "mix-fixed", 5, 0); err != nil {
		return err
	}
	for _, sku := range []string{"swallow", "swallow-fixed", "default-write"} {
		if err := l.insertOrder(root, sku, sku); err != nil {
			return err
		}
	}
	err = root.Transaction(func(tx *gorm.DB) error {
		if err := l.reserve(tx, "swallow", "tx", 1); err != nil {
			return err
		}
		bad := l.insertOrder(tx, "swallow", "swallow")
		var duplicate *mysqldriver.MySQLError
		if !errors.As(bad, &duplicate) || duplicate.Number != 1062 {
			return errors.New("没有复现真实1062")
		}
		fmt.Fprintln(l.out, "[bad.swallow] SQL1062被吞，callback返回nil")
		return nil // tx本身Error没有被Create结果自动改写，callback返回nil，第一步真提交。
	})
	if err != nil {
		return err
	}
	if err := l.verify(ctx, "bad.swallow", "swallow", "swallow", 4, 1); err != nil {
		return err
	}
	err = root.Transaction(func(tx *gorm.DB) error {
		if err := l.reserve(tx, "swallow-fixed", "tx", 1); err != nil {
			return err
		}
		return l.insertOrder(tx, "swallow-fixed", "swallow-fixed")
	})
	var duplicate *mysqldriver.MySQLError
	if err := require(errors.As(err, &duplicate) && duplicate.Number == 1062, "修复必须传播真实1062"); err != nil {
		return err
	}
	if err := l.verify(ctx, "fixed.swallow", "swallow-fixed", "swallow-fixed", 5, 1); err != nil {
		return err
	}
	err = root.Transaction(func(tx *gorm.DB) error {
		return recoverAsSuccess(func() error {
			if err := l.reserve(tx, "panic-swallowed", "tx", 1); err != nil {
				return err
			}
			panic(ErrBusiness)
		})
	})
	if err != nil {
		return err
	}
	if err := l.verify(ctx, "bad.inner-recover", "panic-swallowed", "panic-swallowed", 4, 0); err != nil {
		return err
	}
	err = catchPanic(func() error {
		return root.Transaction(func(tx *gorm.DB) error {
			if err := l.reserve(tx, "panic-fixed", "tx", 1); err != nil {
				return err
			}
			panic(ErrBusiness)
		})
	})
	if err := require(errors.Is(err, ErrBusiness), "外层panic边界必须返回失败"); err != nil {
		return err
	}
	if err := l.verify(ctx, "fixed.outer-recover", "panic-fixed", "panic-fixed", 5, 0); err != nil {
		return err
	}
	if err := l.reserve(root, "default-write", "tx", 1); err != nil {
		return err
	}
	err = l.insertOrder(root, "default-write", "default-write")
	if err := require(errors.As(err, &duplicate) && duplicate.Number == 1062, "默认单条写应第二步1062"); err != nil {
		return err
	}
	return l.verify(ctx, "bad.default-two-writes", "default-write", "default-write", 4, 1)
}
