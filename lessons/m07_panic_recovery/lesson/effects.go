package lesson

// EffectOutcome 描述一次合成订单处理的最终事实。
// ExternalSent 模拟已经被外部系统接受的发货请求；不声称等于实库/MQ 验收。
// Staged/Committed/RolledBack 是教学内存事务的状态，证明 recover 没有自动撤销赋值。
type EffectOutcome struct {
	Events       []string
	ExternalSent int
	Staged       int
	Committed    int
	RolledBack   bool
	Err          error
}

// effectState 只由一个同步调用链持有，函数收到的是同一对象的指针而非状态副本。
type effectState struct {
	trace
	externalSent int
	staged       int
	committed    int
	rolledBack   bool
}

// RunEffects 由 demo/test 调用，比较相同故障点下的三个边界。
// policy=swallow 会恢复却返回 nil；commit-defer 在失败时仍提交；safe 报失败并清理本地暂存。
func RunEffects(policy string) EffectOutcome {
	s := &effectState{}
	var err error
	switch policy {
	case "swallow":
		err = swallowFailure(s)
		if err == nil {
			// caller 只看 error，以为可以继续提交：恢复之后真正继续的是 caller 这一层。
			s.commit()
		}
	case "commit-defer":
		err = commitDespiteFailure(s)
	case "safe":
		err = reportFailure(s)
	}
	return EffectOutcome{s.events, s.externalSent, s.staged, s.committed, s.rolledBack, err}
}

// mutateThenPanic 是三个版本共用的故障，按顺序留下两个已经发生的状态改变。
// 发货已被外部接受之后才 panic；recover 是控制流机制，不是反向执行器。
func mutateThenPanic(s *effectState) {
	s.externalSent++
	s.add("external.accepted")
	s.staged++
	s.add("local.staged")
	panic("记账前转换失败")
}

// swallowFailure 是故意错误的版本，具名 err 初始为 nil，recover 后没有给它赋失败。
// caller 收到 nil，而发货次数已变成 1；这个函数自己不会执行 panic 后面的普通语句。
func swallowFailure(s *effectState) (err error) {
	defer func() {
		if recover() != nil {
			s.add("bad.swallowed-return-nil")
		}
	}()
	mutateThenPanic(s)
	return nil
}

// commitDespiteFailure 是第二个错误版本：记录了失败，仍在同一个 defer 中无条件提交。
// 这次不是 caller 误判，而是清理策略本身把失败路径当作提交路径。
func commitDespiteFailure(s *effectState) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = captureFailure("order-7001", "before-accounting", value)
			s.add("bad.recovered-with-error")
		}
		s.commit() // 错误点：defer 退出会执行不代表业务成功，提交必须有成功前提。
	}()
	mutateThenPanic(s)
	return nil
}

// reportFailure 是修复：向 caller 返回显式失败，同时恢复自己拥有的本地暂存状态。
// 这是人为写的 rollback，不是 recover 的附带行为；外部已接受的发货仍然为 1。
func reportFailure(s *effectState) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = captureFailure("order-7001", "before-accounting", value)
			s.add("safe.failed-with-stack")
			s.staged = 0
			s.rolledBack = true
			s.add("local.rolled-back")
		}
	}()
	mutateThenPanic(s)
	return nil
}

// commit 仅模拟把暂存值交付为本地提交事实，不使用 database/sql。
// README 将真正 Tx.Rollback、Commit 的未知结果问题留给 M21/M38 的实库实验。
func (s *effectState) commit() {
	s.committed += s.staged
	s.staged = 0
	s.add("local.committed")
}
