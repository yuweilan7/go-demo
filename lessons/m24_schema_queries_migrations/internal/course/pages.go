package course

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"
)

// cursor 是页面最后一个排序键的值副本；不持有 Rows 或连接。
// Created+ID 必须一起比较，只用时间会把同秒订单丢掉。ID 是唯一且不可变的 tie-breaker。
type cursor struct {
	Created time.Time
	ID      int64
}

// pagination 由 Run 调用，六条初始订单只用于分页，不混入计划种子。
// 每次“并发”写者是独立 goroutine/独立池连接，由屏障放在两页之间，不用 sleep 猜先后。
func (l *lesson) pagination(ctx context.Context) error {
	if err := l.create(ctx, "pages", `(id BIGINT PRIMARY KEY,created_at DATETIME(6) NOT NULL,KEY idx_page(created_at,id)) ENGINE=InnoDB`); err != nil {
		return err
	}
	if err := l.resetPages(ctx); err != nil {
		return err
	}
	first, c, err := l.readPage(ctx, nil, 0)
	if err != nil {
		return err
	}
	var tied int
	if err = l.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+l.table("pages")+" WHERE created_at=?", c.Created).Scan(&tied); err != nil {
		return err
	}
	// first=[6 5 4]，id4 与 id3 时间相同。时间-only 下一页严格小于会漏掉id3。
	wrong, err := l.pageIDs(ctx, "SELECT id,created_at FROM "+l.table("pages")+" WHERE created_at<? ORDER BY created_at DESC LIMIT 3", c.Created)
	if err != nil {
		return err
	}
	stable, _, err := l.readPage(ctx, &c, 0)
	if err != nil {
		return err
	}
	if err = require(reflect.DeepEqual(first, []int64{6, 5, 4}) && tied == 2 && reflect.DeepEqual(wrong, []int64{2, 1}) && reflect.DeepEqual(stable, []int64{3, 2, 1}), "相同排序值必须用唯一ID补全游标"); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[ties] first=%v timestamp-only=%v stable-tuple=%v tied=%d\n", first, wrong, stable, tied)
	// 无 tie-breaker 的同值顺序由SQL契约允许任意变化；不为了复现偶然调度硬断言某次顺序。
	if err = l.betweenPages(ctx, "INSERT id7 at newer time", "INSERT INTO "+l.table("pages")+" (id,created_at) VALUES (7,?)", c.Created.Add(10*time.Second)); err != nil {
		return err
	}
	offset, _, err := l.readPage(ctx, nil, 3)
	if err != nil {
		return err
	}
	keyset, _, err := l.readPage(ctx, &c, 0)
	if err != nil {
		return err
	}
	if err = require(reflect.DeepEqual(offset, []int64{4, 3, 2}) && reflect.DeepEqual(keyset, []int64{3, 2, 1}), "页间新头部插入会平移offset，keyset仍按上页最后键继续"); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[insert] first=%v offset-page2=%v duplicate=4 deferred-original=1 keyset-page2=%v new-head7-not-in-continuation=true\n", first, offset, keyset)
	if err = l.resetPages(ctx); err != nil {
		return err
	}
	first, c, err = l.readPage(ctx, nil, 0)
	if err != nil {
		return err
	}
	if err = l.betweenPages(ctx, "DELETE id6 at page head", "DELETE FROM "+l.table("pages")+" WHERE id=6"); err != nil {
		return err
	}
	offset, _, err = l.readPage(ctx, nil, 3)
	if err != nil {
		return err
	}
	keyset, _, err = l.readPage(ctx, &c, 0)
	if err != nil {
		return err
	}
	if err = require(reflect.DeepEqual(offset, []int64{2, 1}) && reflect.DeepEqual(keyset, []int64{3, 2, 1}), "页间头部删除会让offset漏id3，keyset不依赖行位置"); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[delete] first=%v offset-page2=%v missed=3 keyset-page2=%v\n", first, offset, keyset)
	if err = l.resetPages(ctx); err != nil {
		return err
	}
	first, c, err = l.readPage(ctx, nil, 0)
	if err != nil {
		return err
	}
	// keyset 不是跨请求快照：已读id5排序值被改到cursor之后，会重复；未读id3改到前面，会漏。
	if err = l.betweenPages(ctx, "UPDATE mutable sort keys", "UPDATE "+l.table("pages")+" SET created_at=CASE id WHEN 5 THEN ? WHEN 3 THEN ? END WHERE id IN (5,3)", c.Created.Add(-10*time.Second), c.Created.Add(10*time.Second)); err != nil {
		return err
	}
	keyset, _, err = l.readPage(ctx, &c, 0)
	if err != nil {
		return err
	}
	if err = require(reflect.DeepEqual(keyset, []int64{2, 1, 5}), "修改排序键会让keyset重复id5并漏id3，不能声称它总是无重漏"); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[mutable-sort] first=%v keyset-page2=%v duplicate=5 missed=3；修复选择不可变业务序号/受控快照\n", first, keyset)
	return l.snapshotRepair(ctx)
}

// resetPages 由各对照阶段调用，只删本run自己的pages数据；六条是固定小集合。
// 每两条共用时间，确保 tie 情形存在，而不是依赖机器时钟碰巧同秒。
func (l *lesson) resetPages(ctx context.Context) error {
	if _, err := l.db.ExecContext(ctx, "DELETE FROM "+l.table("pages")); err != nil {
		return err
	}
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	for id := 1; id <= 6; id++ {
		if _, err := l.db.ExecContext(ctx, "INSERT INTO "+l.table("pages")+" VALUES (?,?)", id, base.Add(time.Duration((id-1)/2)*time.Second)); err != nil {
			return err
		}
	}
	return nil
}

// readPage 是显式分页契约：降序(created_at,id)，固定size3，上次最后键的严格后续。
// after=nil走offset对照；输入cursor按值读，输出是新值，不共享可变数据库行。
func (l *lesson) readPage(ctx context.Context, after *cursor, offset int) ([]int64, cursor, error) {
	query := "SELECT id,created_at FROM " + l.table("pages")
	args := []any{}
	if after != nil {
		query += " WHERE created_at<? OR (created_at=? AND id<?)"
		args = append(args, after.Created, after.Created, after.ID)
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT 3"
	if after == nil {
		query += " OFFSET ?"
		args = append(args, offset)
	}
	return l.readPageQuery(ctx, l.db, query, args...)
}

// queryer 是 DB/Tx 都满足的最小读取接口。接口动态值为*sql.DB或*sql.Tx，
// 同一句 QueryContext 因接收对象不同，使用池新会话或已有事务绑定会话。
type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// readPageQuery 由DB分页/快照分页调用，拥有Rows并返回纯值，绝不把Rows交给下一页长期持有。
func (l *lesson) readPageQuery(ctx context.Context, q queryer, query string, args ...any) (ids []int64, last cursor, err error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, cursor{}, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		if err = rows.Scan(&last.ID, &last.Created); err != nil {
			return nil, cursor{}, err
		}
		ids = append(ids, last.ID)
	}
	return ids, last, rows.Err()
}

// pageIDs 用同一读取器运行显式坏SQL；返回纯值使当前Rows在后续写者启动前已关闭。
func (l *lesson) pageIDs(ctx context.Context, query string, args ...any) ([]int64, error) {
	ids, _, err := l.readPageQuery(ctx, l.db, query, args...)
	return ids, err
}

// betweenPages 的 owner 已读完第一页面，worker 的 ready/permit 把写入放在两页之间。
// 执行结果放入result之后还可能有wrapper defer；只有join(finished)返回才开始第二页。
func (l *lesson) betweenPages(ctx context.Context, label, query string, args ...any) (err error) {
	ready := make(chan struct{})
	permit := make(chan struct{})
	t := startTask(ctx, func(work context.Context) error {
		close(ready) // worker只宣布准备好；此时还没有发SQL。
		select {
		case <-permit:
		case <-work.Done():
			return work.Err()
		}
		_, err := l.db.ExecContext(work, query, args...)
		return err
	}, nil)
	defer func() { err = errors.Join(err, t.stopAndJoin()) }()
	select {
	case <-ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	close(permit) // 仅owner关闭，worker只接收；第一页面已完成才许可真实写者。
	if err = t.join(ctx); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[between-page writer finished] %s；SQL=%s\n", label, query)
	return nil
}

// snapshotRepair 演示一个有界导出在同一RR事务内读两页，期间外部写者真实提交更新。
// 快照来自同一个Tx的首次一致性读；跨HTTP请求重新BeginTx无法延续这个快照。
func (l *lesson) snapshotRepair(ctx context.Context) (err error) {
	if err = l.resetPages(ctx); err != nil {
		return err
	}
	tx, err := l.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rollback(tx)) }()
	first, c, err := l.readPageQuery(ctx, tx, "SELECT id,created_at FROM "+l.table("pages")+" ORDER BY created_at DESC,id DESC LIMIT 3")
	if err != nil {
		return err
	}
	if err = l.betweenPages(ctx, "UPDATE during RR snapshot", "UPDATE "+l.table("pages")+" SET created_at=CASE id WHEN 5 THEN ? WHEN 3 THEN ? END WHERE id IN (5,3)", c.Created.Add(-10*time.Second), c.Created.Add(10*time.Second)); err != nil {
		return err
	}
	second, _, err := l.readPageQuery(ctx, tx, "SELECT id,created_at FROM "+l.table("pages")+" WHERE created_at<? OR (created_at=? AND id<?) ORDER BY created_at DESC,id DESC LIMIT 3", c.Created, c.Created, c.ID)
	if err != nil {
		return err
	}
	if err = require(reflect.DeepEqual(first, []int64{6, 5, 4}) && reflect.DeepEqual(second, []int64{3, 2, 1}), "同一RR一致性快照应保持原页面集合"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	current, _, err := l.readPage(ctx, nil, 0)
	if err != nil {
		return err
	}
	if err = require(reflect.DeepEqual(current, []int64{3, 6, 4}), "快照结束后新读取应观察外部更新已提交"); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[snapshot repair] same-Tx first=%v second=%v after-Tx-current=%v external-update-committed=true\n", first, second, current)
	return nil
}
