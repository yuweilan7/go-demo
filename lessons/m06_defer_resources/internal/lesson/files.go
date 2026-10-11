package lesson

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// orderRecord 是所有文件实验共用的合成业务数据，未读取或改写用户原有文件。
const orderRecord = "order=O-100 amount=99\n"

// ErrBudget 是本课主动设置的两个句柄上限，不是操作系统报出的文件描述符耗尽。
// 用它可稳定重现积压风险，同时绝不会为了演示而打开成千上万个文件。
var ErrBudget = errors.New("本次批处理已用满 2 个文件句柄预算")

// fileBudget 属于一条批处理调用链。live 是现在还由该 owner 持有的真实文件数，
// peak 是运行期间最高数；limit 控制示例上限，不代表 Windows 的系统限制。
type fileBudget struct {
	root, label                       string
	limit, live, peak, opened, closed int
	t                                 *Trace
}

// ownedFile 把一个真实 *os.File 与负责它的批处理 owner 联系起来。
// 这是计数包装器，不是虚构的数据库连接或事务；Close 仍调用真实文件 API。
type ownedFile struct {
	f     *os.File
	owner *fileBudget
	id    string
}

// acquire 由批处理函数调用。先检查课程预算，再真正 os.Create；只有成功取得句柄
// 才增加 live。失败时 caller 没有新资源，不能对 nil 文件安排 Close。
func (b *fileBudget) acquire(id string) (*ownedFile, error) {
	if b.live == b.limit {
		b.t.Event(b.label, "获取 %s 被预算拒绝 live=%d", id, b.live)
		return nil, ErrBudget
	}
	f, err := os.Create(filepath.Join(b.root, b.label+"-"+id+".txt"))
	if err != nil {
		return nil, fmt.Errorf("创建 %s: %w", id, err)
	}
	b.live++
	b.opened++
	if b.live > b.peak {
		b.peak = b.live
	}
	b.t.Event(b.label, "获取 %s live=%d", id, b.live)
	return &ownedFile{f: f, owner: b, id: id}, nil
}

// Close 由资源 owner 的退出清理或显式清理调用。真实 Close 失败会交回 caller；
// 只在成功关闭后减少 live，所以计数不会用“尝试过关闭”冒充“释放成功”。
func (f *ownedFile) Close() error {
	if err := f.f.Close(); err != nil {
		return fmt.Errorf("关闭 %s: %w", f.id, err)
	}
	f.owner.live--
	f.owner.closed++
	f.owner.t.Event(f.owner.label, "关闭 %s live=%d", f.id, f.owner.live)
	return nil
}

// BatchResult 在 batch 函数已经返回、所有 defer 执行后保存最终资源事实。
type BatchResult struct {
	Opened, Closed, Peak, Live int
	VerifiedRecords            int
	Err                        error
}

// ReceiverFileResult 在危险版内部函数返回后、外层兜底清理前记录哪个真实文件被关闭。
// 因此它能暴露资源积压，而不是只看到最后所有文件都被测试框架清理过。
type ReceiverFileResult struct{ OldClosed, NewClosed bool }

// FilesResult 包含三种批处理方式、第二次获取失败的清理，以及临时目录删除证据。
type FilesResult struct {
	Bad, Scoped, Explicit                    BatchResult
	CapturedReceiver, LateClosure            ReceiverFileResult
	StableReceivers                          ReceiverFileResult
	SecondAcquireFailed, FirstResourceClosed bool
	TempDir                                  string
	Removed                                  bool
}

// RunFiles 是 E03 的场景 owner。它创建唯一临时目录，把子场景所有错误与最终清理
// 错误交给 demo；正常/故障数据都仅在这个目录内，退出时删除目录。
func RunFiles(t *Trace) (out FilesResult, err error) {
	t.Event("E03", "真实文件、立即注册、循环延迟与每轮释放")
	out.TempDir, err = os.MkdirTemp("", "m06-files-")
	if err != nil {
		return out, fmt.Errorf("创建 E03 临时目录: %w", err)
	}
	defer func() {
		removeErr := os.RemoveAll(out.TempDir)
		err = errors.Join(err, removeErr)
		out.Removed = removeErr == nil
		t.Event("E03.owner", "临时目录删除=%t", out.Removed)
	}()

	for _, mode := range []string{"bad", "scoped", "explicit"} {
		budget := &fileBudget{root: out.TempDir, label: "E03." + mode, limit: 2, t: t}
		var batchErr error
		switch mode {
		case "bad":
			batchErr = batchBad(budget)
		case "scoped":
			batchErr = batchScoped(budget)
		case "explicit":
			batchErr = batchExplicit(budget)
		}
		result := BatchResult{Opened: budget.opened, Closed: budget.closed, Peak: budget.peak, Live: budget.live, Err: batchErr}
		// Close 已执行后再核验实际内容，避免把“调用了 Write”当成“记录确实可读”。
		for i := 1; i <= result.Opened; i++ {
			data, readErr := os.ReadFile(filepath.Join(out.TempDir, budget.label+"-"+fmt.Sprintf("%d", i)+".txt"))
			if readErr != nil {
				return out, readErr
			}
			if string(data) != orderRecord {
				return out, fmt.Errorf("%s 第 %d 条实际文件内容不符", mode, i)
			}
			result.VerifiedRecords++
		}
		t.Event(budget.label+".caller", "opened=%d closed=%d peak=%d live=%d budget_err=%t",
			result.Opened, result.Closed, result.Peak, result.Live, errors.Is(batchErr, ErrBudget))
		t.Event(budget.label+".content", "真实文件记录核验=%d", result.VerifiedRecords)
		switch mode {
		case "bad":
			out.Bad = result
			if !errors.Is(batchErr, ErrBudget) || result.Live != 0 || result.Closed != 2 {
				return out, fmt.Errorf("循环危险版未出现预期的有界积压: %w", batchErr)
			}
		case "scoped":
			out.Scoped = result
			if batchErr != nil {
				return out, batchErr
			}
		case "explicit":
			out.Explicit = result
			if batchErr != nil {
				return out, batchErr
			}
		}
	}
	out.SecondAcquireFailed, out.FirstResourceClosed, err = secondAcquireFailure(t, out.TempDir)
	if err != nil {
		return out, err
	}
	out.CapturedReceiver, err = receiverFileTrap(t, out.TempDir, "receiver")
	if err != nil {
		return out, err
	}
	out.LateClosure, err = receiverFileTrap(t, out.TempDir, "closure")
	if err != nil {
		return out, err
	}
	out.StableReceivers, err = receiverFileTrap(t, out.TempDir, "receiver-fixed")
	return out, err
}

// receiverFileTrap 由 RunFiles 调用，展示重新赋值资源变量时的两种相反错误和稳定接收者修复。
// 外层拥有 A、B 的兜底清理；危险版只在内层函数运行，观察到泄漏后立即收回所有资源。
func receiverFileTrap(t *Trace, root, mode string) (out ReceiverFileResult, err error) {
	label := "E03." + mode
	first, err := os.Create(filepath.Join(root, label+"-A.txt"))
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, closeUnlessAlreadyClosed(first)) }()
	second, err := os.Create(filepath.Join(root, label+"-B.txt"))
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, closeUnlessAlreadyClosed(second)) }()
	err = func() (err error) {
		current := first
		if mode == "closure" {
			// 闭包晚读 current。后续把它改成 B，就把 B 关闭而遗漏原先的 A。
			// 忽略 Close error 也是危险版缺陷，正确处理见 processOne/writeOrdered。
			defer func() { _ = current.Close() }()
		} else if mode == "receiver" {
			// 方法接收者 first 的指针立即保存。后来 current=B 不会让它替换为 B。
			// A 会关闭，但在同一函数新取得的 B 没有自己的清理登记。
			defer current.Close()
		} else {
			// 修复：每个资源取得自己的稳定实参。重新赋值 current 不会改变 owned。
			// 闭包只晚读最终结果 err，文件身份则在登记时固定，并保留关闭 error。
			defer func(owned *os.File) { err = errors.Join(err, owned.Close()) }(current)
		}
		current = second
		if mode == "receiver-fixed" {
			defer func(owned *os.File) { err = errors.Join(err, owned.Close()) }(current)
		}
		t.Event(label+".body", "登记后 current 从 A 改指 B")
		return nil
	}()
	// 原对象的 Read 是否匹配 ErrClosed，是独立 I/O 观察；打开的只写文件会产生
	// 其他读错误，但不会被误判成关闭。观察完毕才执行外层的双文件兜底清理。
	_, firstErr := first.Read(make([]byte, 1))
	_, secondErr := second.Read(make([]byte, 1))
	out.OldClosed, out.NewClosed = errors.Is(firstErr, os.ErrClosed), errors.Is(secondErr, os.ErrClosed)
	t.Event(label+".caller", "old_closed=%t new_closed=%t；外层随后兜底关闭残留", out.OldClosed, out.NewClosed)
	return out, err
}

// closeUnlessAlreadyClosed 只用于故障实验外层的兜底：危险版可能已经关闭其中一个文件。
// 普通业务路径不会这样吞掉重复 Close；E04 专门把重复关闭作为 error 交回 caller。
func closeUnlessAlreadyClosed(file *os.File) error {
	err := file.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

// batchBad 故意把每个 Close 延迟到“整个批处理函数”返回。for 的一次迭代结束不会
// 执行本函数的 defer。第三个文件被预算拒绝后，已经登记的第二、第一文件才倒序关闭。
func batchBad(b *fileBudget) (err error) {
	for i := 1; i <= 4; i++ {
		f, openErr := b.acquire(fmt.Sprintf("%d", i))
		if openErr != nil {
			b.t.Event(b.label, "准备错误返回，返回前 live=%d", b.live)
			return openErr
		}
		// 在成功获取后立刻登记，避免后面的 Write 失败漏掉这个句柄。
		// 直接保存 f 作为实参，免得一个可重新赋值的外层变量导致清理错对象。
		defer func(owned *ownedFile) { err = errors.Join(err, owned.Close()) }(f)
		if _, err = f.f.WriteString(orderRecord); err != nil {
			return err
		}
		b.t.Event(b.label, "第 %d 轮结束 live=%d；尚未退出 batchBad", i, b.live)
	}
	return nil
}

// batchScoped 把资源生命期缩到 processOne 一次调用。processOne 返回前 Close 已完成，
// 下一轮才开始获取，因此四条记录可以全部处理且 peak=1。
func batchScoped(b *fileBudget) error {
	for i := 1; i <= 4; i++ {
		if err := processOne(b, fmt.Sprintf("%d", i)); err != nil {
			return err
		}
		b.t.Event(b.label, "第 %d 轮完成，小函数已返回 live=%d", i, b.live)
	}
	return nil
}

// processOne 由 batchScoped 调用，拥有且只拥有本轮文件。具名 err 让退出 Close 的失败
// 也能参与最终返回；errors.Join 保留 Write 主错误和 Close 清理错误的各自原因。
func processOne(b *fileBudget, id string) (err error) {
	f, err := b.acquire(id)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	_, err = f.f.WriteString(orderRecord)
	return err
}

// batchExplicit 是另一种修复：同一函数每轮明确关闭。这里所有 Write 返回路径都先
// 经过 Close；如果业务分支增多，很容易遗漏，因此通常优先使用 processOne 的结构。
func batchExplicit(b *fileBudget) error {
	for i := 1; i <= 4; i++ {
		f, err := b.acquire(fmt.Sprintf("%d", i))
		if err != nil {
			return err
		}
		_, writeErr := f.f.WriteString(orderRecord)
		closeErr := f.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return err
		}
		b.t.Event(b.label, "第 %d 轮显式关闭后 live=%d", i, b.live)
	}
	return nil
}

// secondAcquireFailure 是复制文件失败路径的最小版本。源文件打开成功后立即登记 Close；
// 目标路径的父目录不存在，os.Create 产生真实 PathError，源文件仍须在返回前关闭。
func secondAcquireFailure(t *Trace, root string) (failed, closed bool, err error) {
	sourcePath := filepath.Join(root, "source.txt")
	if err := os.WriteFile(sourcePath, []byte(orderRecord), 0o600); err != nil {
		return false, false, err
	}
	var source *os.File
	// 这个小函数拥有 source。外层保留指针用于退出后验证句柄确实不可用。
	err = func() (err error) {
		source, err = os.Open(sourcePath)
		if err != nil {
			return err
		}
		defer func() {
			closeErr := source.Close()
			err = errors.Join(err, closeErr)
			t.Event("E03.acquire_failure", "源文件退出关闭 close_ok=%t", closeErr == nil)
		}()
		t.Event("E03.acquire_failure", "源文件获取成功并注册 Close")
		destination, createErr := os.Create(filepath.Join(root, "missing-parent", "destination.txt"))
		if createErr != nil {
			t.Event("E03.acquire_failure", "目标获取失败，源文件清理由已注册 defer 负责")
			return createErr
		}
		// 此分支本课不预期发生，但若外部意外创建目录，仍关闭已获得的新句柄。
		return destination.Close()
	}()
	failed = errors.Is(err, os.ErrNotExist)
	if source != nil {
		// Windows 的 Stat 直接查底层句柄，关闭后的错误不一定匹配 os.ErrClosed。
		// 用真实 Read 检查文件 I/O 的关闭状态，不把 Stat 的平台实现当语言保证。
		_, readErr := source.Read(make([]byte, 1))
		closed = errors.Is(readErr, os.ErrClosed)
	}
	t.Event("E03.acquire_failure.caller", "target_failed=%t source_closed=%t", failed, closed)
	if !failed || !closed {
		return failed, closed, fmt.Errorf("获取失败清理事实不符合预期: %w", err)
	}
	// 目标失败是本实验的预期输入；已经验证原始原因，课程返回成功。
	return failed, closed, nil
}
