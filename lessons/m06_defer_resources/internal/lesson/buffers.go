package lesson

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// BufferResult 记录 caller error 与两个清理 error，避免“只看主返回 nil”漏掉写出失败。
type BufferResult struct {
	Name, Content           string
	BufferedBefore          int
	Err, FlushErr, CloseErr error
	Success                 bool
}

// BuffersResult 的四种路径均使用真实临时文件和 bufio.Writer。
// 故障由提前关闭真实文件制造，不使用假 writer 冒充文件系统错误。
type BuffersResult struct {
	Cases   []BufferResult
	TempDir string
	Removed bool
}

// RunBuffers 是 E04 的 owner。子函数返回后，读取实际文件内容与错误链，再报告成功。
// 文件系统可读到相同内容只证明本次 Write/Flush/Close 流程；没有执行 Sync 或耐久性测试。
func RunBuffers(t *Trace) (out BuffersResult, err error) {
	t.Event("E04", "缓冲写出、Close 顺序与主/清理错误保留")
	out.TempDir, err = os.MkdirTemp("", "m06-buffers-")
	if err != nil {
		return out, err
	}
	defer func() {
		removeErr := os.RemoveAll(out.TempDir)
		err = errors.Join(err, removeErr)
		out.Removed = removeErr == nil
		t.Event("E04.owner", "临时目录删除=%t", out.Removed)
	}()
	for _, mode := range []string{"bad", "fixed", "close-failure", "business-and-cleanup"} {
		path := filepath.Join(out.TempDir, mode+".txt")
		var r BufferResult
		var writeErr error
		if mode == "bad" {
			r, writeErr = writeWrongOrder(t, path)
		} else {
			r, writeErr = writeOrdered(t, path, mode)
		}
		r.Name, r.Err = mode, writeErr
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return out, readErr
		}
		r.Content = string(data)
		// 成功日志只放在写函数退出、Flush/Close 的失败已经进入 error 之后。
		// bad 的 caller 虽然拿到 nil，下面仍核验内容，暴露其虚假的成功信号。
		r.Success = writeErr == nil && r.Content == orderRecord
		t.Event("E04."+mode+".caller", "err_nil=%t bytes=%d success=%t business_cause=%t closed_cause=%t flush_failed=%t close_failed=%t",
			writeErr == nil, len(data), r.Success, errors.Is(writeErr, ErrBusiness), errors.Is(writeErr, os.ErrClosed), r.FlushErr != nil, r.CloseErr != nil)
		out.Cases = append(out.Cases, r)
	}
	return out, nil
}

// writeWrongOrder 由 RunBuffers 调用，故意先登记 Flush、后登记 Close。
// return 后按 LIFO 先关闭文件，随后 Flush 写这个已关闭的文件，数据仍留在缓冲区。
// err 没有从清理错误更新，caller 因此收到错误的 nil；out 保存证据以供独立断言。
func writeWrongOrder(t *Trace, path string) (out BufferResult, err error) {
	f, err := os.Create(path)
	if err != nil {
		return out, err
	}
	w := bufio.NewWriterSize(f, 4096)
	defer func() {
		out.FlushErr = w.Flush()
		t.Event("E04.bad.defer", "Flush 后执行 failed=%t closed_cause=%t（故意未传播）", out.FlushErr != nil, errors.Is(out.FlushErr, os.ErrClosed))
	}()
	defer func() {
		out.CloseErr = f.Close()
		t.Event("E04.bad.defer", "Close 先执行 ok=%t", out.CloseErr == nil)
	}()
	if _, err = w.WriteString(orderRecord); err != nil {
		return out, err
	}
	out.BufferedBefore = w.Buffered()
	t.Event("E04.bad.body", "错误地提前记成功 buffered=%d；尚未 Flush", out.BufferedBefore)
	return out, nil
}

// writeOrdered 由 RunBuffers 调用，拥有文件和其上层 writer，用一个 defer 明确顺序。
// 具名 err 先保存业务/Write 错误，再在清理中 Join，caller 收到完整错误原因集合。
// mode 只控制本课有界故障注入，不是可供生产请求选择的配置。
func writeOrdered(t *Trace, path, mode string) (out BufferResult, err error) {
	f, err := os.Create(path)
	if err != nil {
		return out, err
	}
	// 成功取得 f 后马上登记所有清理。w 尚未初始化，闭包稍后读取它；
	// 即使后续逻辑提前返回，至少 f.Close 必然被尝试。
	var w *bufio.Writer
	defer func() {
		if w != nil {
			out.FlushErr = w.Flush()
		}
		t.Event("E04."+mode+".defer", "先 Flush failed=%t", out.FlushErr != nil)
		out.CloseErr = f.Close()
		t.Event("E04."+mode+".defer", "后 Close failed=%t", out.CloseErr != nil)
		err = errors.Join(err, labeledError("Flush", out.FlushErr), labeledError("Close", out.CloseErr))
	}()
	w = bufio.NewWriterSize(f, 4096)
	if _, err = w.WriteString(orderRecord); err != nil {
		return out, err
	}
	out.BufferedBefore = w.Buffered()
	t.Event("E04."+mode+".body", "Write 只进入缓冲 buffered=%d；此时不记最终成功", out.BufferedBefore)
	if mode == "close-failure" {
		// 先真正 Flush，再提前 Close，故意使退出清理中的第二次 Close 返回 ErrClosed。
		// 此时内容已写出，仍不能丢弃 Close 错误并向 caller 报告完整流程成功。
		if err = w.Flush(); err != nil {
			return out, err
		}
		if err = f.Close(); err != nil {
			return out, err
		}
		t.Event("E04."+mode+".fault", "故意提前 Flush/Close，退出 Close 将是重复关闭")
	}
	if mode == "business-and-cleanup" {
		// 文件提前关闭，但 writer 仍持有未写出的字节。return ErrBusiness 先赋结果，
		// 退出 Flush 与第二次 Close 再产生真实文件 error，三者必须同时保留。
		if err = f.Close(); err != nil {
			return out, err
		}
		t.Event("E04."+mode+".fault", "故意提前 Close 并返回业务错误")
		return out, ErrBusiness
	}
	return out, nil
}

// labeledError 只在非 nil error 上包装操作名。%w 保留原因供 errors.Is/As 检查；
// 对 nil 不生成“有错误”的接口值，errors.Join 可以正确忽略没有发生的错误。
func labeledError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
