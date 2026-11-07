package gormcourse

import (
	"database/sql"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// Stock 是合成库存：SKU 是主键，Owner 是必须显式传入的归属条件。
// Quantity=0/Active=false 是合法业务值；不能靠 Updates(struct) 猜哪些字段被用户填写。
// 没有 UpdatedAt，才能清楚看到全零更新完全不发 UPDATE，而非只更新时间掩盖问题。
type Stock struct {
	SKU       string         `gorm:"primaryKey;size:80"`
	Owner     string         `gorm:"size:40;not null"`
	Quantity  int64          `gorm:"not null;check:quantity >= 0"`
	Active    bool           `gorm:"not null"`
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

// Order 是库存业务的第二步。Lines 是 has-many 关系；外键名称指向 Line.OrderID。
// Lab 是进程内 hook 配置，gorm:"-" 使它们不成为列。读取数据库不会还原它们。
type Order struct {
	ID       string `gorm:"primaryKey;size:80"`
	SKU      string `gorm:"size:80;not null"`
	Owner    string `gorm:"size:40;not null"`
	Quantity int64  `gorm:"not null"`
	Lines    []Line `gorm:"foreignKey:OrderID;references:ID"`
	Lab      *lab   `gorm:"-"`
}
type Line struct {
	ID      uint64 `gorm:"primaryKey"`
	OrderID string `gorm:"size:80;index;not null"`
	Owner   string `gorm:"size:40;not null"`
	SKU     string `gorm:"size:80;not null"`
}
type HookAudit struct {
	ID string `gorm:"primaryKey;size:80"`
}
type Outbox struct {
	ID   string `gorm:"primaryKey;size:80"`
	Body string `gorm:"size:100"`
	Sent bool
}

// AfterCreate 是 GORM create callback 同步调用的方法，o 是刚插入的 Order 指针。
// 参数 tx 来自 callMethod 的 NewDB session：清掉当前 INSERT 的条件，但保留同一
// Statement.ConnPool，所以下面 SQL 加入当前事务，不是向外层 DB 再借一条连接。
// 此时 INSERT 已执行、COMMIT 尚未执行；方法名里的 After 只表示在 INSERT 之后。
func (o *Order) AfterCreate(tx *gorm.DB) error {
	if o.Lab == nil {
		return nil
	} // 普通订单无 hook 副作用；显式故障入口才启用它。
	if _, ok := tx.Statement.ConnPool.(*sql.Tx); !ok {
		return errors.New("hook未借到真实sql.Tx")
	}
	l := o.Lab // 指针别名，访问本run的输出；本同步hook不创建goroutine。
	fmt.Fprintf(l.out, "[hook] order=%s pool=%T 未提交\n", o.ID, tx.Statement.ConnPool)
	if err := tx.Create(&HookAudit{ID: o.ID}).Error; err != nil {
		return err
	}
	// 订单、audit 和待发布事件共用当前事务，发布者只会看见已经提交的事件。
	return tx.Create(&Outbox{ID: o.ID, Body: o.ID}).Error
}
