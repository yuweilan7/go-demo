package gormcourse

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// crudCases由Run同步调用，用真实行内容解释Error/RowsAffected/零值/归属边界。
// 各次GORM返回值是新的结果句柄；忽略它的Error不是Go抛异常，更不会自动停止下一行。
func (l *lab) crudCases(ctx context.Context) error {
	for _, row := range []Stock{{SKU: "zero", Owner: "alice", Quantity: 9, Active: true}, {SKU: "foreign", Owner: "bob", Quantity: 8, Active: true}, {SKU: "selected", Owner: "alice", Quantity: 6, Active: true}} {
		if err := l.db.WithContext(ctx).Create(&row).Error; err != nil {
			return err
		}
	}
	base := l.db.WithContext(ctx)
	// DryRun只用于看生成原式，不冒充SQL验收。Vars保持参数分离，InterpolateParams=false。
	dry := base.Session(&gorm.Session{DryRun: true}).Model(&Stock{}).Where("sku = ? AND owner = ?", "zero", "alice").Updates(map[string]any{"quantity": 0, "active": false})
	if dry.Error != nil {
		return dry.Error
	}
	fmt.Fprintf(l.out, "[SQL参数原式] %s Vars=%v（合成值；DryRun未执行）\n", dry.Statement.SQL.String(), dry.Statement.Vars)
	bad := base.Model(&Stock{}).Where("sku = ? AND owner = ?", "zero", "alice").Updates(Stock{Quantity: 0, Active: false})
	if bad.Error != nil {
		return bad.Error
	}
	// 这里rows=0并不是找不到：所有待更新字段被IsZero筛掉，未生成UPDATE。
	if err := l.stockState(ctx, "zero.bad", "zero", 9, true); err != nil {
		return err
	}
	if err := require(bad.RowsAffected == 0, "全零struct应没有UPDATE"); err != nil {
		return err
	}
	fixed := base.Model(&Stock{}).Where("sku = ? AND owner = ?", "zero", "alice").Updates(map[string]any{"quantity": 0, "active": false})
	if fixed.Error != nil {
		return fixed.Error
	}
	if err := require(fixed.RowsAffected == 1, "map显式零值必须修改1行"); err != nil {
		return err
	}
	if err := l.stockState(ctx, "zero.fixed-map", "zero", 0, false); err != nil {
		return err
	}
	selected := base.Model(&Stock{}).Where("sku = ? AND owner = ?", "selected", "alice").Select("Quantity", "Active").Updates(Stock{Quantity: 0, Active: false})
	if selected.Error != nil {
		return selected.Error
	}
	if err := require(selected.RowsAffected == 1, "Select零值应修改1行"); err != nil {
		return err
	}
	if err := l.stockState(ctx, "zero.fixed-select", "selected", 0, false); err != nil {
		return err
	}
	// 没有UpdatedAt、ClientFoundRows=false：重复写相同值合法但实际changed rows=0。
	unchanged := base.Model(&Stock{}).Where("sku = ?", "zero").Update("active", false)
	if err := errors.Join(unchanged.Error, require(unchanged.RowsAffected == 0, "同值UPDATE应changed rows=0")); err != nil {
		return err
	}
	blocked := base.Model(&Stock{}).Update("quantity", 1)
	if err := require(errors.Is(blocked.Error, gorm.ErrMissingWhereClause), "缺WHERE未被GORM保护拒绝"); err != nil {
		return err
	}
	if err := l.stockState(ctx, "global.blocked", "foreign", 8, true); err != nil {
		return err
	}
	// 故障入口有界只改本run表。WHERE 1=1有条件，因此保护不会判断它有无业务意义。
	global := base.Model(&Stock{}).Where("1 = 1").Update("quantity", 7)
	if err := errors.Join(global.Error, require(global.RowsAffected == 3, "本run全表风险应真实修改3行")); err != nil {
		return err
	}
	fmt.Fprintln(l.out, "[global.risk] WHERE1=1真实修改3行；AllowGlobalUpdate/RawSQL也可绕过保护，不能代替归属校验")
	// 漏Owner：alice请求竟修改bob的SKU；有sku WHERE仍不会被全表保护拒绝。
	leak := base.Model(&Stock{}).Where("sku = ?", "foreign").Update("quantity", 6)
	if err := errors.Join(leak.Error, require(leak.RowsAffected == 1, "漏归属故障必须真实修改bob行")); err != nil {
		return err
	}
	guard := base.Model(&Stock{}).Scopes(owned("alice")).Where("sku = ?", "foreign").Update("quantity", 5)
	if err := errors.Join(guard.Error, require(guard.RowsAffected == 0, "alice不能修改bob行")); err != nil {
		return err
	}
	if err := l.stockState(ctx, "owner.leak-then-guard", "foreign", 6, true); err != nil {
		return err
	}
	var one Stock
	first := base.Where("sku = ?", "absent").First(&one)
	var many []Stock
	find := base.Where("sku = ?", "absent").Find(&many)
	if err := require(errors.Is(first.Error, gorm.ErrRecordNotFound) && first.RowsAffected == 0 && find.Error == nil && find.RowsAffected == 0 && len(many) == 0, "First/Find空结果契约不同"); err != nil {
		return err
	}
	fmt.Fprintln(l.out, "[notfound] First=ErrRecordNotFound/0 Find=nil/0/empty；不查Error/RowsAffected会把零值误认为业务对象")
	return nil
}

// stockState绕过被测ORM，以真实SELECT核对数量和active。返回值交给case决定失败。
func (l *lab) stockState(ctx context.Context, label, sku string, wantQty int64, wantActive bool) error {
	var qty int64
	var active bool
	if err := l.pool.QueryRowContext(ctx, "SELECT quantity,active FROM "+quote(l.tables[0])+" WHERE sku=?", sku).Scan(&qty, &active); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[%s.SELECT] qty=%d active=%v\n", label, qty, active)
	return require(qty == wantQty && active == wantActive, "实库零值/归属结果错误")
}

// owned是traditional Scopes工厂，caller传入当前认证归属，闭包捕获字符串值。
// scope接收当前*gorm.DB并返回加条件的句柄；没有自动租户上下文，必须明确调用它。
func owned(owner string) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB { return db.Where("owner = ?", owner) }
}

// chainCases对照会修改Statement的传统链与存操作列表的generics链。
// Session复制语句不复制数据库行，也不新开Tx；NewDB会丢当前条件却仍借同一连接池。
func (l *lab) chainCases(ctx context.Context) error {
	for _, row := range []Stock{{SKU: "chain1", Owner: "chain", Quantity: 1, Active: true}, {SKU: "chain2", Owner: "chain", Quantity: 2, Active: true}} {
		if err := l.db.WithContext(ctx).Create(&row).Error; err != nil {
			return err
		}
	}
	base := l.db.WithContext(ctx).Model(&Stock{}).Where("owner = ?", "chain")
	var a, b []Stock
	if err := base.Where("quantity = ?", 1).Find(&a).Error; err != nil {
		return err
	}
	if err := base.Where("quantity = ?", 2).Find(&b).Error; err != nil {
		return err
	}
	if err := require(len(a) == 1 && len(b) == 0, "传统链复用应复现quantity1 AND quantity2污染"); err != nil {
		return err
	}
	clean := l.db.WithContext(ctx).Model(&Stock{}).Where("owner = ?", "chain").Session(&gorm.Session{})
	if err := clean.Where("quantity = ?", 1).Find(&a).Error; err != nil {
		return err
	}
	if err := clean.Where("quantity = ?", 2).Find(&b).Error; err != nil {
		return err
	}
	if err := require(len(a) == 1 && len(b) == 1 && b[0].SKU == "chain2", "Session必须让两条查询保留归属且独立"); err != nil {
		return err
	}
	var all []Stock
	if err := clean.Session(&gorm.Session{NewDB: true}).Find(&all).Error; err != nil {
		return err
	}
	if err := require(len(all) == 5, "NewDB应清掉owner范围，不能当归属安全修复"); err != nil {
		return err
	}
	// G[Stock]的T固定返回类型。First返回(Stock,error)，Find返回([]Stock,error)，
	// Update在锁定v1.31.2中返回(int,error)，不能按部分官网旧例接成单error。
	g := gorm.G[Stock](l.db).Where("owner = ?", "chain")
	ga, err := g.Where("quantity = ?", 1).Find(ctx)
	if err != nil {
		return err
	}
	gb, err := g.Where("quantity = ?", 2).Find(ctx)
	if err != nil {
		return err
	}
	if err := require(len(ga) == 1 && len(gb) == 1, "generics复用应无传统链污染"); err != nil {
		return err
	}
	rows, err := gorm.G[Stock](l.db).Where("sku = ? AND owner = ?", "chain2", "chain").Select("Quantity", "Active").Updates(ctx, Stock{Quantity: 0, Active: false})
	if err := errors.Join(err, require(rows == 1, "generics Select Updates零值影响1行")); err != nil {
		return err
	}
	rows, err = gorm.G[Stock](l.db).Where("sku = ? AND owner = ?", "chain1", "chain").Update(ctx, "active", false)
	if err := errors.Join(err, require(rows == 1, "generics Update真实返回rows/error")); err != nil {
		return err
	}
	_, err = gorm.G[Stock](l.db).Where("sku = ?", "absent").First(ctx)
	if err := require(errors.Is(err, gorm.ErrRecordNotFound), "generics First空结果必须返回error"); err != nil {
		return err
	}
	if err := l.stockState(ctx, "generics.zero", "chain2", 0, false); err != nil {
		return err
	}
	fmt.Fprintln(l.out, "[chain] polluted=1/0 Session=1/1 generics=1/1 NewDB丢归属=5；更新返回rows/error均已核对")
	return nil
}
