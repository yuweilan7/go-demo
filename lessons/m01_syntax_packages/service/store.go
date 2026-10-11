package service

// Receipt 记录一次已经写入本实验内存仓库的出库结果。ID/Quantity/TotalCents 是可观察业务事实。
type Receipt struct {
	ID         string
	Quantity   uint8
	TotalCents int64
}

// Store 由 cmd/demo 或测试创建并拥有。records 小写，包外只能经 Snapshot 观察。
// var store Store 的零值已经可用：nil slice 可以 append。本课仅在单 goroutine 中使用它。
type Store struct {
	records []Receipt
}

// save 仅由 Service 调用，把一份 receipt 值追加到同一个 Store 对象。
// (s *Store) 中 *Store 是指针类型，复制指针仍找到 caller 创建的那个仓库。
// 这是同步的本地内存副作用，不是数据库、磁盘持久化或消息交付证明；进程结束数据消失。
func (s *Store) save(receipt Receipt) {
	s.records = append(s.records, receipt)
}

// Snapshot 由入口和外部测试调用；返回独立 slice，避免 caller 直接改写内部出库记录。
// M01 只需识别返回 []Receipt；底层数组和别名的完整语义放到 M02。
func (s *Store) Snapshot() []Receipt {
	return append([]Receipt(nil), s.records...)
}
