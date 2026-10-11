package gormcourse

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// paginate给traditional Scopes生成确定分页条件。调用前先检查page/size，避免负数
// 被GORM解释为取消Limit/Offset；页序必须ORDER BY唯一键，否则分页没有稳定定义。
func paginate(page, size int) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB { return db.Order("id ASC").Offset((page - 1) * size).Limit(size) }
}

// page由hiddenCases调用，返回页内容与独立总数，不让分页条件污染COUNT。
// 返回的slice是本次读取结果，底层元素只是业务值；它不拥有数据库连接。
func (l *lab) page(ctx context.Context, owner string, page, size int) ([]Order, int64, error) {
	if page < 1 || size < 1 || size > 100 {
		return nil, 0, errors.New("page>=1且size在1..100")
	}
	var total int64
	if err := l.db.WithContext(ctx).Model(&Order{}).Scopes(owned(owner)).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Order
	err := l.db.WithContext(ctx).Scopes(owned(owner), paginate(page, size)).Find(&rows).Error
	return rows, total, err
}

// hiddenCases由Run调用，借关联SELECT计数和底层SQL展示默认行为隐藏在哪。
// AutoMigrate只创建本run教学对象；生产schema演进属于M24，本课不改共享schema。
func (l *lab) hiddenCases(ctx context.Context) error {
	if err := l.seed(ctx, "soft", "alice", 3); err != nil {
		return err
	}
	deleted := l.db.WithContext(ctx).Where("sku = ? AND owner = ?", "soft", "alice").Delete(&Stock{})
	if err := errors.Join(deleted.Error, require(deleted.RowsAffected == 1, "软删除影响1行")); err != nil {
		return err
	}
	var visible []Stock
	if err := l.db.WithContext(ctx).Where("sku = ?", "soft").Find(&visible).Error; err != nil {
		return err
	}
	var physical, marked int64
	if err := l.pool.QueryRowContext(ctx, "SELECT COUNT(*),SUM(deleted_at IS NOT NULL) FROM "+quote(l.tables[0])+" WHERE sku=?", "soft").Scan(&physical, &marked); err != nil {
		return err
	}
	var unscoped []Stock
	if err := l.db.WithContext(ctx).Unscoped().Where("sku = ?", "soft").Find(&unscoped).Error; err != nil {
		return err
	}
	if err := require(len(visible) == 0 && physical == 1 && marked == 1 && len(unscoped) == 1, "软删除应物理存在且默认不可见"); err != nil {
		return err
	}
	fmt.Fprintln(l.out, "[soft-delete] default=0 raw=1 deleted_at非NULL=1 Unscoped=1；SQL是UPDATE并非物理DELETE")
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("assoc%02d", i)
		o := Order{ID: id, SKU: id, Owner: "assoc", Quantity: 1, Lines: []Line{{Owner: "assoc", SKU: "valid"}}}
		if i == 1 {
			o.Lines = append(o.Lines, Line{Owner: "foreign", SKU: "foreign-line"})
		}
		// 不开外键迁移不代表没有关联：Create仍会按GORM关系保存Lines。
		if err := l.db.WithContext(ctx).Create(&o).Error; err != nil {
			return err
		}
	}
	before := l.trace.count()
	var orders []Order
	if err := l.db.WithContext(ctx).Scopes(owned("assoc")).Order("id ASC").Find(&orders).Error; err != nil {
		return err
	}
	for i := range orders {
		// 故意N+1：range索引指向当前slice元素，逐订单真实发一次SELECT。
		if err := l.db.WithContext(ctx).Where("order_id = ?", orders[i].ID).Find(&orders[i].Lines).Error; err != nil {
			return err
		}
	}
	nplus := l.trace.count() - before
	before = l.trace.count()
	var preloaded []Order
	if err := l.db.WithContext(ctx).Scopes(owned("assoc")).Order("id ASC").Preload("Lines").Find(&preloaded).Error; err != nil {
		return err
	}
	preloadQueries := l.trace.count() - before
	unsafeLines := 0
	for _, o := range preloaded {
		unsafeLines += len(o.Lines)
	}
	before = l.trace.count()
	var safe []Order
	if err := l.db.WithContext(ctx).Scopes(owned("assoc")).Order("id ASC").Preload("Lines", "owner = ?", "assoc").Find(&safe).Error; err != nil {
		return err
	}
	safeQueries := l.trace.count() - before
	safeLines := 0
	for _, o := range safe {
		safeLines += len(o.Lines)
		for _, line := range o.Lines {
			if line.Owner != "assoc" {
				return errors.New("子关联泄漏归属")
			}
		}
	}
	if err := require(nplus == 4 && preloadQueries == 2 && safeQueries == 2 && unsafeLines == 4 && safeLines == 3, "N+1/Preload查询数及子关联归属必须独立成立"); err != nil {
		return err
	}
	p1, total, err := l.page(ctx, "assoc", 1, 2)
	if err != nil {
		return err
	}
	p2, total2, err := l.page(ctx, "assoc", 2, 2)
	if err != nil {
		return err
	}
	if err := require(total == 3 && total2 == 3 && len(p1) == 2 && len(p2) == 1 && p1[0].ID == "assoc01" && p1[1].ID == "assoc02" && p2[0].ID == "assoc03", "唯一ID排序分页/总数不正确"); err != nil {
		return err
	}
	if _, _, err := l.page(ctx, "assoc", 0, 2); err == nil {
		return errors.New("无效分页未拒绝")
	}
	fmt.Fprintf(l.out, "[associations] N+1=%d preload=%d unsafe_lines=%d scoped_preload=%d safe_lines=%d\n[paging] total=3 page1=assoc01,assoc02 page2=assoc03\n", nplus, preloadQueries, unsafeLines, safeQueries, safeLines)
	return nil
}

// hookCounts用database/sql读真实audit/outbox，区分hook执行过与数据库已提交。
func (l *lab) hookCounts(ctx context.Context, id string, wantAudit, wantOutbox int64) error {
	var audit, outbox int64
	if err := l.pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quote(l.tables[3])+" WHERE id=?", id).Scan(&audit); err != nil {
		return err
	}
	if err := l.pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quote(l.tables[4])+" WHERE id=?", id).Scan(&outbox); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[hook.SELECT] id=%s audit=%d outbox=%d\n", id, audit, outbox)
	return require(audit == wantAudit && outbox == wantOutbox, "hook SQL回滚/提交不符合预期")
}

// relayOne在业务Transaction返回成功后调用；它只借db/broker，不启动后台任务。
// Publish确认后再标Sent会有“发布成功、标记未成”的重复窗口，业务消费需幂等。
// 本课演示单次界限，生产relay/恢复/幂等的完整组合由M30负责。
func (l *lab) relayOne(ctx context.Context, id string) error {
	var event Outbox
	if err := l.db.WithContext(ctx).Where("id = ? AND sent = ?", id, false).First(&event).Error; err != nil {
		return err
	}
	if err := l.broker.publish(ctx, event.Body); err != nil {
		return err
	}
	result := l.db.WithContext(ctx).Model(&Outbox{}).Where("id = ? AND sent = ?", id, false).Update("sent", true)
	return errors.Join(result.Error, require(result.RowsAffected == 1, "outbox标记应真实修改1行"))
}

// hookCases对照未提交hook发送真实MQ和同tx outbox修复，不以Slice日志代替broker。
// 外部confirm、Get/Ack、SQL回滚、outbox提交是不同证据，逐个断言。
func (l *lab) hookCases(ctx context.Context) error {
	for _, sku := range []string{"hook-unsafe", "hook-fixed-rollback", "hook-fixed-commit"} {
		if err := l.seed(ctx, sku, "hook", 5); err != nil {
			return err
		}
	}
	root := l.db.WithContext(ctx)
	err := root.Transaction(func(tx *gorm.DB) error {
		if err := l.reserve(tx, "hook-unsafe", "hook", 1); err != nil {
			return err
		}
		if err := tx.Create(&Order{ID: "hook-unsafe", SKU: "hook-unsafe", Owner: "hook", Quantity: 1, Lab: l, Mode: "unsafe-mq"}).Error; err != nil {
			return err
		}
		return ErrBusiness // hook已真实broker Ack；现在才发生业务失败，SQL回滚不能撤回事件。
	})
	if err := require(errors.Is(err, ErrBusiness), "hook危险case应业务失败"); err != nil {
		return err
	}
	if err := l.verify(ctx, "hook.unsafe-rollback", "hook-unsafe", "hook-unsafe", 5, 0); err != nil {
		return err
	}
	if err := l.hookCounts(ctx, "hook-unsafe", 0, 0); err != nil {
		return err
	}
	if err := l.broker.take("hook-unsafe"); err != nil {
		return err
	}
	err = root.Transaction(func(tx *gorm.DB) error {
		if err := l.reserve(tx, "hook-fixed-rollback", "hook", 1); err != nil {
			return err
		}
		if err := tx.Create(&Order{ID: "hook-fixed-rollback", SKU: "hook-fixed-rollback", Owner: "hook", Quantity: 1, Lab: l, Mode: "outbox"}).Error; err != nil {
			return err
		}
		return ErrBusiness
	})
	if err := require(errors.Is(err, ErrBusiness), "outbox回滚case应业务失败"); err != nil {
		return err
	}
	if err := l.verify(ctx, "hook.fixed-rollback", "hook-fixed-rollback", "hook-fixed-rollback", 5, 0); err != nil {
		return err
	}
	if err := l.hookCounts(ctx, "hook-fixed-rollback", 0, 0); err != nil {
		return err
	}
	if err := l.broker.empty(); err != nil {
		return err
	}
	err = root.Transaction(func(tx *gorm.DB) error {
		if err := l.reserve(tx, "hook-fixed-commit", "hook", 1); err != nil {
			return err
		}
		return tx.Create(&Order{ID: "hook-fixed-commit", SKU: "hook-fixed-commit", Owner: "hook", Quantity: 1, Lab: l, Mode: "outbox"}).Error
	})
	if err != nil {
		return err
	}
	if err := l.verify(ctx, "hook.fixed-commit", "hook-fixed-commit", "hook-fixed-commit", 4, 1); err != nil {
		return err
	}
	if err := l.hookCounts(ctx, "hook-fixed-commit", 1, 1); err != nil {
		return err
	}
	if err := l.broker.empty(); err != nil {
		return err
	} // Commit并不自动发MQ，必须显式relay。
	if err := l.relayOne(ctx, "hook-fixed-commit"); err != nil {
		return err
	}
	if err := l.broker.take("hook-fixed-commit"); err != nil {
		return err
	}
	var sent bool
	if err := l.pool.QueryRowContext(ctx, "SELECT sent FROM "+quote(l.tables[4])+" WHERE id=?", "hook-fixed-commit").Scan(&sent); err != nil {
		return err
	}
	return require(sent, "outbox Sent持久标记未成立")
}
