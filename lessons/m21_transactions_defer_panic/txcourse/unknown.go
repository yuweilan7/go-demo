package txcourse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
)

// unknownCase 对比两次真实通信失败：一个 COMMIT 未到服务器，一个服务器已
// 提交但 OK 包被丢弃。客户端两者都只能 UNKNOWN，不能从同样 error 推断未写。
// 代理证据是实验仪器，生产调用者没有它，只能按原 request_id 查业务权威记录。
func (s *Store) unknownCase(ctx context.Context, c Config) error {
	fmt.Fprintln(s.out, "=== E04 Commit 通信失败：未转发 / 已提交但响应丢失 ===")
	for _, mode := range []faultMode{dropBeforeCommit, dropCommitResponse} {
		if err := s.oneUnknown(ctx, c, mode); err != nil {
			return err
		}
	}
	return nil
}

// oneUnknown 拥有本 case 的代理和单连接 proxyDB；所有 return 都先关 DB 再
// stopAndJoin 代理。正常路径显式等 Finished 再查账，defer 仍保护任何提前失败。
func (s *Store) oneUnknown(ctx context.Context, c Config, mode faultMode) (err error) {
	name := "commit-not-forwarded"
	stock, orders := int64(5), int64(0)
	if mode == dropCommitResponse {
		name, stock, orders = "commit-response-lost", 4, 1
	}
	r := Request{name + "-key", name, 1}
	if err := s.seed(ctx, r.SKU, 5); err != nil {
		return err
	}
	p, err := startProxy(net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), mode)
	if err != nil {
		return err
	}
	defer func() {
		_, cleanupErr := p.stopAndJoin()
		err = errors.Join(err, cleanupErr)
	}()
	db, err := openPool(c, p.addr(), 1)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	// 事务固定在这一个代理连接；Conn 保证诊断ID和Tx属于同一会话，不凭池复用猜测。
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	var id int64
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rollback(tx)) }()
	if err := s.reserve(ctx, tx, r); err != nil {
		return err
	}
	if err := s.insertOrder(ctx, tx, r); err != nil {
		return err
	}
	commitErr := tx.Commit()
	if commitErr == nil {
		return errors.New("主动截断 COMMIT 通信却没有得到真实驱动 error")
	}
	// 包装保留 request_id 和底层错误；Unknown 是业务状态，不把它当可自动重试。
	unknown := errors.Join(ErrUnknown, fmt.Errorf("request_id=%s Commit 原因: %w", r.RequestID, commitErr))
	if !errors.Is(unknown, ErrUnknown) || !errors.Is(unknown, commitErr) {
		return errors.New("UNKNOWN 丢失原提交失败")
	}
	// Commit 已把本机 Tx 标 done；Rollback=ErrTxDone 不能推翻已提交的可能性。
	if e := tx.Rollback(); !errors.Is(e, sql.ErrTxDone) {
		return fmt.Errorf("Commit 失败后 Tx 应已结束: %w", e)
	}
	evidence, proxyErr := p.stopAndJoin()
	if proxyErr != nil {
		return proxyErr
	}
	if !evidence.CommitSeen || (mode == dropBeforeCommit && (evidence.Forwarded || evidence.ServerOK)) ||
		(mode == dropCommitResponse && (!evidence.Forwarded || !evidence.ServerOK || !evidence.Dropped)) {
		return fmt.Errorf("真实协议故障没有命中目标边界: %+v", evidence)
	}
	cleanupCtx, cancel := shortContext()
	defer cancel()
	// 先确认原 server connection 退出，避免未转发 case 的断连回滚还在进行。
	if err := observe(cleanupCtx, "故障连接服务端退出", func() (bool, error) {
		var n int
		e := s.db.QueryRowContext(cleanupCtx, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE ID=?", id).Scan(&n)
		return n == 0, e
	}); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "[%s.protocol] COMMIT_seen=%t forwarded=%t server_OK=%t response_dropped=%t Finished=true\n", name, evidence.CommitSeen, evidence.Forwarded, evidence.ServerOK, evidence.Dropped)
	fmt.Fprintf(s.out, "[%s.client] %v\n", name, unknown)
	if err := s.verify(cleanupCtx, name+".reconcile-original-key", r, stock, orders); err != nil {
		return err
	}
	if orders == 1 {
		// 不重放库存扣减：同 key 查到账完整记录就恢复 COMMITTED 并返回原订单。
		fmt.Fprintf(s.out, "[recovery] request_id=%s -> COMMITTED；返回原订单，无第二次下单\n", r.RequestID)
	} else {
		// 这里只在本实验已独立确认原会话结束+不存在记录后证明未提交。
		// 生产中的暂时 NoRows 可能遇副本延迟/仍执行，应保留 UNKNOWN 继续查权威库。
		fmt.Fprintf(s.out, "[recovery] request_id=%s -> 本实验会话已结束且权威库无记录，确认未提交\n", r.RequestID)
	}
	return nil
}
