package course

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
)

var (
	ErrWrite  = errors.New("receipt write failed")
	ErrFlush  = errors.New("receipt flush failed")
	ErrCommit = errors.New("receipt commit failed")
	ErrClose  = errors.New("receipt close failed")
)

// ReceiptSink 是本课显式定义的输出协议：Write 接受字节，Commit 确认输出阶段，Close 清理。
// 它并不是数据库 sql.Tx；真实 FileSink 的 Commit 调用 os.File.Sync。
// 交给 ExportReceipt 后，sink 的 Close 所有权归 ExportReceipt，caller 不再重复关闭。
type ReceiptSink interface {
	io.Writer
	Commit() error
	Close() error
}

// FileSink 持有实际 os.File 指针，只有当前串行导出流程使用。
// fd 由 NewFileSink 成功打开，必须最终交给 ExportReceipt 或由创建者自行 Close。
type FileSink struct{ fd *os.File }

// NewFileSink 由 cleanup 实验调用；path 位于本实验的临时目录。
// 打开失败时没有成功获取文件资源，caller 只需处理错误，不应 defer nil.Close。
func NewFileSink(path string) (*FileSink, error) {
	fd, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open receipt: %w", err)
	}
	return &FileSink{fd: fd}, nil
}

// Write 由 bufio.Writer 调用，将缓冲字节交到真实文件；不会在这里记录日志。
func (s *FileSink) Write(p []byte) (int, error) { return s.fd.Write(p) }

// Commit 由 exportBody 在 Flush 成功后调用，让文件系统执行 Sync。
// Sync 成功是这个 OS/文件系统调用的确认，不是数据库事务提交，也不是断电实验证据。
func (s *FileSink) Commit() error { return s.fd.Sync() }

// Close 由 ExportReceipt 的 defer 调用，释放底层文件句柄。
func (s *FileSink) Close() error { return s.fd.Close() }

// FaultSink 是辅助清理实验的可观察替身：不是实库、磁盘故障或远程驱动。
// Accepted 是 Write 已接受的字节，Committed 与 Closed 是不同阶段的本地事实。
// 即使 CloseErr 非 nil，它也会标记清理已被尝试；不能据此推导外部资源已释放。
type FaultSink struct {
	WriteErr    error
	CommitErr   error
	CloseErr    error
	Accepted    []byte
	Committed   bool
	Closed      bool
	WriteCalls  int
	CommitCalls int
	CloseCalls  int
}

// Write 由缓冲 writer 调用；故障在接受任何新字节前触发，返回 0,error。
// 未失败时 append 复制 p，满足 io.Writer 不保留调用方 slice 的契约。
func (s *FaultSink) Write(p []byte) (int, error) {
	s.WriteCalls++
	if s.WriteErr != nil {
		return 0, s.WriteErr
	}
	s.Accepted = append(s.Accepted, p...)
	return len(p), nil
}

// Commit 由 exportBody 调用，失败时 Committed 保持 false。
// 这个 fixture 明确知道结果；真实驱动 Commit 通信失败可能没有这种确定性。
func (s *FaultSink) Commit() error {
	s.CommitCalls++
	if s.CommitErr != nil {
		return s.CommitErr
	}
	s.Committed = true
	return nil
}

// Close 由导出函数的 defer 调用。调用次数是独立断言的资源所有权证据。
func (s *FaultSink) Close() error {
	s.CloseCalls++
	s.Closed = true
	return s.CloseErr
}

// ExportReceipt 由边界调用，取得 sink 的关闭责任并把所有失败交还 caller。
// err 是具名结果参数：return 先赋结果，再执行 defer，最后交给调用方。
func ExportReceipt(sink ReceiptSink, payload string) (err error) {
	defer func() {
		// 闭包在函数退出时读取 err；不是 defer 登记时提前求值的 err 副本。
		// closeErr 只在这个 if 范围内存在，不会用 := 遮蔽外层具名结果 err。
		if closeErr := sink.Close(); closeErr != nil {
			// Join 保留主操作和清理两个原因。主操作成功时也必须报告 Close 失败。
			err = errors.Join(err, fmt.Errorf("close receipt: %w", closeErr))
		}
	}()
	return exportBody(sink, payload)
}

// BadExportReceipt 仅由危险实验调用。输入和执行体与修复版本相同，便于定位差异。
// 错误点在 defer：直接把具名返回 err 改成 Close 错误，原业务失败被覆盖。
func BadExportReceipt(sink ReceiptSink, payload string) (err error) {
	defer func() {
		if closeErr := sink.Close(); closeErr != nil {
			err = closeErr // 调用方再也找不到 Commit 原因；禁止用于正常导出。
		}
	}()
	return exportBody(sink, payload)
}

// exportBody 由两个导出入口调用，只负责串行写出协议；关闭责任在外层。
// writer 是本次调用的缓冲对象，64 字节容量不改变底层 sink 的所有权。
func exportBody(sink ReceiptSink, payload string) error {
	writer := bufio.NewWriterSize(sink, 64)
	if _, err := io.WriteString(writer, payload); err != nil {
		// 小文本只进入缓冲可能成功；大文本可能在 WriteString 期间触及底层而失败。
		// 这里不能吞掉错误，否则 caller 会把部分输出误认成完整回执。
		return fmt.Errorf("write receipt text: %w", err)
	}
	if err := writer.Flush(); err != nil {
		// Flush 真的把缓冲字节交给 sink。此前 WriteString 成功不证明这步完成。
		return fmt.Errorf("flush receipt bytes: %w", err)
	}
	if err := sink.Commit(); err != nil {
		// Flush 成功只证明底层接受了字节，还要检查显式 Commit 的返回契约。
		return fmt.Errorf("commit receipt: %w", err)
	}
	return nil
}
