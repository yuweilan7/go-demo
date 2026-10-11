package lab

import (
	"fmt"
	"sync"
)

// Ledger 是本次进程的业务事实：哪个订单已发放一张合成优惠券。
// 它与 broker 的 ready/unacked/confirm 完全独立。mu 保护并发 worker；
// applied 与 effects 必须在同一个临界区更新，不能先查再在锁外发券。
// 这是进程内原子提交，不是磁盘/数据库事务；进程重启后幂等记忆会丢失。
type Ledger struct {
	mu      sync.Mutex
	applied map[string]bool
	effects map[string]int
}

// NewLedger 由每个实验建立一份独占账本；返回指针，因此 worker 持有同一对象，
// 不是复制 map 或 mutex。实验结束后只有无秘密断言进入报告。
func NewLedger() *Ledger {
	return &Ledger{applied: make(map[string]bool), effects: make(map[string]int)}
}

// Commit 由处理 worker 调用。id 来自 Delivery.MessageId；结果 applied=false 表示
// 本进程已经提交过该业务键，worker 仍应 ack 当前这一次 delivery。
// 使用 defer Unlock 让正常返回/提前返回都释放同一把锁。
func (l *Ledger) Commit(id string) (applied bool, err error) {
	if id == "" {
		return false, fmt.Errorf("业务幂等键不能为空")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.applied[id] {
		return false, nil
	}
	l.effects[id]++
	l.applied[id] = true
	return true, nil
}

// Count 由实验 owner 在协议操作之外读取真实业务效果，不以 Ack 的返回代替它。
func (l *Ledger) Count(id string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.effects[id]
}
