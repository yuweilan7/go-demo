package order

import (
	"errors"
	"fmt"
	"sync"
)

var (
	ErrNotFound       = errors.New("订单不存在")
	ErrAmountMismatch = errors.New("支付金额与订单金额不符")
)

// Snapshot 记录能独立断言的业务事实：状态、累计入账分和入账次数。
// 所有字段都是值，返回副本后测试读取它不需要继续持有 Store 的锁。
type Snapshot struct {
	ID            string
	ExpectedCents int64
	State         string
	CreditedCents int64
	CreditCount   int
}

// Store 是每次实验/每个测试独占的内存存储。mu 保护整笔订单的检查和修改。
// 指针接收者让方法操作同一个 Store；创建后不能复制带 Mutex 的 Store 值。
type Store struct {
	mu     sync.Mutex
	orders map[string]Snapshot
}

// NewStore 由入口或测试创建一笔待支付订单。返回的指针归调用方所有，没有全局数据。
// 本课程固定使用已合法的种子金额；非法种子立即返回错误，不能留下半初始化业务对象。
func NewStore(id string, cents int64) (*Store, error) {
	if !validID(id) || cents <= 0 || cents > MaxCents {
		return nil, fmt.Errorf("%w: 无效的种子订单", ErrInvalidOrder)
	}
	return &Store{orders: map[string]Snapshot{id: {ID: id, ExpectedCents: cents, State: "pending"}}}, nil
}

// View 由测试、演示和 handler 调用；锁保护 map 读取，返回结构体副本给调用方观察副作用。
// “在锁内复制”提供一致的一次快照，不意味着多次 View 之间不会发生新的支付。
func (s *Store) View(id string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock() // 函数返回或 panic 展开时释放本次获取的锁。
	value, ok := s.orders[id]
	if !ok {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return value, nil
}

// ApplyPayment 由真实 HTTP handler 和业务单测调用。金额核验通过后才修改状态和入账。
// 同一个订单重复支付相同金额返回现有结果，CreditCount 不再增加；本规则不是通用支付幂等协议。
func (s *Store) ApplyPayment(payment Payment) (Snapshot, error) {
	s.mu.Lock() // 可能在这里等别的请求释放锁；锁和 defer 的作用范围覆盖全部判断及修改。
	defer s.mu.Unlock()
	value, ok := s.orders[payment.ID] // map 取出的 Snapshot 是副本，改变 value 不会自动改变 map。
	if !ok {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrNotFound, payment.ID)
	}
	if payment.Cents != value.ExpectedCents {
		// 错误分支还没有写 map，所以“收到错误”与“拒绝后业务不变”可以分别验证。
		return Snapshot{}, fmt.Errorf("%w: got=%d want=%d", ErrAmountMismatch, payment.Cents, value.ExpectedCents)
	}
	if value.State == "paid" {
		return value, nil
	}
	value.State = "paid"
	value.CreditedCents = payment.Cents
	value.CreditCount = 1
	s.orders[payment.ID] = value // 这是实际内存副作用；函数返回前仍持锁，其他请求看不到半完成字段。
	return value, nil
}

// ApplyPaymentWrong 是 E02 显式调用的错误提交，绝不由正常 handler 使用。
// 它每一次 map 访问都加锁，因而可以没有 data race；业务错误是“先提交状态、后核验金额”。
func (s *Store) ApplyPaymentWrong(payment Payment) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.orders[payment.ID]
	if !ok {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrNotFound, payment.ID)
	}
	value.State = "paid"
	s.orders[payment.ID] = value // 错误点：错误返回不会自动撤销已经写入 map 的状态。
	if payment.Cents != value.ExpectedCents {
		return Snapshot{}, fmt.Errorf("%w: got=%d want=%d", ErrAmountMismatch, payment.Cents, value.ExpectedCents)
	}
	value.CreditedCents = payment.Cents
	value.CreditCount++ // 这条坏路径还会重复入账；修复版在检查后先判断 paid。
	s.orders[payment.ID] = value
	return value, nil
}
