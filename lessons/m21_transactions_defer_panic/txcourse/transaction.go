package txcourse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime/debug"
)

// PanicFailure 是边界恢复后的失败结果；保留原 panic 值、现场栈与清理错误。
// 它不把 panic 改成成功。errors.Is 可继续发现原 panic 中的 error 原因。
type PanicFailure struct {
	Value      any
	Stack      []byte
	CleanupErr error
}

func (p *PanicFailure) Error() string { return fmt.Sprintf("业务 panic: %v", p.Value) }

// Unwrap 由 errors.Is/As 调用，把原 error 型 panic 和 rollback 失败共同保留。
func (p *PanicFailure) Unwrap() []error {
	if cause, ok := p.Value.(error); ok {
		return []error{cause, p.CleanupErr}
	}
	return []error{p.CleanupErr}
}

// rollback 由所有正确资源 owner 使用。已结束的 Tx 返回 ErrTxDone 是契约结果，
// 尤其 Commit 成功之后 defer Rollback 不能撤销提交，也不能制造“提交失败”。
// 其他清理错误必须向 caller 传播；ErrTxDone 自身不证明服务器已回滚。
func rollback(tx *sql.Tx) error {
	err := tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("Rollback 清理: %w", err)
	}
	return nil
}

// withTx 是 Place 的唯一 Tx owner，fn 借用 tx，不可自行 Commit/Close。
// 这里只抽必要契约：Begin 成功即登记清理；普通 error 直接返回；正常末尾显式
// Commit；panic 先 Rollback 再继续传播。fn 不是另一个 goroutine，panic 在同一栈展开。
func withTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("BeginTx: %w", err)
	}
	defer func() {
		// recover 必须在直接执行的 deferred function 中调用；放普通 helper 无效。
		if value := recover(); value != nil {
			cleanupErr := rollback(tx)
			if cleanupErr != nil {
				panic(&PanicFailure{Value: value, Stack: debug.Stack(), CleanupErr: cleanupErr})
			}
			panic(value) // 原值继续向业务边界传播，不恢复成 nil、不进入 Commit。
		}
		err = errors.Join(err, rollback(tx))
	}()
	if err = fn(tx); err != nil {
		return err // return 先赋结果 err，再执行上面的 defer。
	}
	// 不把 Commit 藏进 defer，也不依据 err==nil 猜 body 是否完整完成。
	// 请求可能到达服务器再丢响应，非 nil 返回只能给 UNKNOWN，不能无条件重放。
	if err = tx.Commit(); err != nil {
		return errors.Join(ErrUnknown, fmt.Errorf("显式 Commit: %w", err))
	}
	return nil
}

// Place 是正常业务入口，Run/后续课程可以沿这里审查真实事务。
// 所有请求值从 caller 来；qty>0 是业务输入契约；整个库存扣减+订单插入在同一 Tx。
func (s *Store) Place(ctx context.Context, r Request) error {
	if r.Quantity <= 0 || r.RequestID == "" || r.SKU == "" {
		return ErrConflict
	}
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := s.reserve(ctx, tx, r); err != nil {
			return err
		}
		return s.insertOrder(ctx, tx, r)
	})
	if err != nil {
		return fmt.Errorf("request_id=%s: %w", r.RequestID, err)
	}
	return nil
}

// catchPanic 是实验边界：caller 需要继续比较下一 case，所以把传播到这里的
// panic 转成明确失败。它不拥有 Tx；正确版的 Tx 已在内层展开时先清理。
func catchPanic(fn func() error) (err error) {
	defer func() {
		if value := recover(); value != nil {
			if failure, ok := value.(*PanicFailure); ok {
				err = failure
			} else {
				err = &PanicFailure{Value: value, Stack: debug.Stack()}
			}
		}
	}()
	return fn()
}

// badNamedPanic 故意违反契约：err 是具名结果，初始 nil；panic 不自动给它赋值。
// 延迟闭包读到 nil 就 Commit，外层虽然观察到 panic，数据库却留下半笔业务。
// Run 仅在显式标记的危险 case 调用，tx 最后总会 Commit/Rollback，不泄漏连接。
func (s *Store) badNamedPanic(ctx context.Context, r Request) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			fmt.Fprintln(s.out, "[bad.panic.defer] named err=nil -> 错误 Commit")
			err = tx.Commit()
		} else {
			err = errors.Join(err, rollback(tx))
		}
	}()
	if err = s.reserve(ctx, tx, r); err != nil {
		return err
	}
	panic("扣减后订单代码 panic") // 没有走 return，因此 err 仍 nil。
}

// badShadow 故意在 if 初始化的内层作用域创建另一个 err，并只打印后吞掉它。
// := 不是“总是复用外层 err”；这里 if 的 err 与具名结果 err 是两个变量。
func (s *Store) badShadow(ctx context.Context, r Request) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			fmt.Fprintln(s.out, "[bad.shadow.defer] outer err=nil -> 错误 Commit")
			err = tx.Commit()
		} else {
			err = errors.Join(err, rollback(tx))
		}
	}()
	if err = s.reserve(ctx, tx, r); err != nil {
		return err
	}
	if err := s.insertOrder(ctx, tx, r); err != nil {
		fmt.Fprintln(s.out, "[bad.shadow.body] inner err=真实重复主键，离开 if 时被丢弃")
		// 正确语句是 return err：表达式会先把内层错误赋给外层具名结果。
	}
	return // 外层 err 仍 nil。此处裸 return 合法；if 内裸 return 会被编译器拒绝。
}

// badCaptured 在 defer 注册时保存 err 的 nil 接口值副本，退出时不重新读 err。
// 第二步真实失败交给 caller，清理却用“旧 nil”错误提交库存，所以非 nil 不等于回滚。
func (s *Store) badCaptured(ctx context.Context, r Request) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func(oldErr error) {
		if oldErr == nil {
			fmt.Fprintln(s.out, "[bad.capture.defer] 注册时 oldErr=nil -> 错误 Commit")
			err = errors.Join(err, tx.Commit())
		} else {
			err = errors.Join(err, rollback(tx))
		}
	}(err) // 实参现在求值，不是退出时求值。
	if err = s.reserve(ctx, tx, r); err != nil {
		return err
	}
	err = s.insertOrder(ctx, tx, r)
	return err
}

// badForgot 的错误版内部只 return，没有登记 Rollback。为了有界观察，实验 harness
// 预先拥有 tx 并保留兜底清理，函数返回时可以实测 InUse=1 和事务内未提交库存。
// 这不“修复”内部反例；它是在观察后防止课堂真的遗留锁/池资源的外层保护。
func (s *Store) badForgot(ctx context.Context, tx *sql.Tx, r Request) error {
	if err := s.reserve(ctx, tx, r); err != nil {
		return err
	}
	return s.insertOrder(ctx, tx, r)
}

// badMix 先 Tx 扣库存，却用外层 DB 写订单。DB 会借另一连接，默认 autocommit。
// 后面 return error 只回滚 tx 连接，订单所在连接已经提交，出现“有订单没扣库存”。
func (s *Store) badMix(ctx context.Context, r Request) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rollback(tx)) }()
	if err := s.reserve(ctx, tx, r); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, "INSERT INTO "+quote(s.orders)+" (request_id,sku,quantity) VALUES (?,?,?)", r.RequestID, r.SKU, r.Quantity)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return errors.Join(err, errors.New("混用 DB 插入没有影响1行"))
	}
	return ErrStepTwo // 模拟后续业务失败；订单已在 tx 外自动提交。
}
