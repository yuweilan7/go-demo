package lesson

import (
	"errors"
	"os"
	"path/filepath"
)

// auditClient 是应用 owner 创建的长寿命文件客户端，多个请求借用同一真实文件。
// 它不是 HTTP 客户端或数据库替身；本实验只证明所有权与关闭时机。
type auditClient struct{ file *os.File }

// Record 由请求调用；它只借用 file 写一条合成记录，不取得 Close 所有权。
func (c *auditClient) Record() error {
	_, err := c.file.WriteString(orderRecord)
	return err
}

// Close 只应由应用 owner 在全部请求完成后调用。关闭后 Record 会得到真实 ErrClosed。
func (c *auditClient) Close() error { return c.file.Close() }

// OwnershipResult 记录两个请求之间的可用性，以及临时目录已经删除的证据。
type OwnershipResult struct {
	BadSecondErr, FixedSecondErr error
	FixedContent                 string
	TempDir                      string
	Removed                      bool
}

// RunOwnership 是 E05 的应用 owner：创建客户端、把它借给请求、最后统一关闭。
// 请求只有使用权；如果在请求中 defer client.Close，后一个请求会失去应用资源。
func RunOwnership(t *Trace) (out OwnershipResult, err error) {
	t.Event("E05", "请求借用的长寿命文件客户端由应用 owner 关闭")
	out.TempDir, err = os.MkdirTemp("", "m06-ownership-")
	if err != nil {
		return out, err
	}
	defer func() {
		removeErr := os.RemoveAll(out.TempDir)
		err = errors.Join(err, removeErr)
		out.Removed = removeErr == nil
		t.Event("E05.owner", "临时目录删除=%t", out.Removed)
	}()

	badFile, err := os.Create(filepath.Join(out.TempDir, "bad.txt"))
	if err != nil {
		return out, err
	}
	badClient := &auditClient{file: badFile}
	// 应用先安排兜底，防止错误版请求自身失败时漏资源。重复 Close 是预期故障结果，
	// owner 根据 os.ErrClosed 识别“已经关闭”，不把它再当成本课意外错误。
	defer func() {
		if closeErr := badClient.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			err = errors.Join(err, closeErr)
		}
	}()
	if err = borrowedRequestBad(t, badClient); err != nil {
		return out, err
	}
	out.BadSecondErr = badClient.Record()
	t.Event("E05.bad.owner", "第二个请求 closed_cause=%t", errors.Is(out.BadSecondErr, os.ErrClosed))

	fixedPath := filepath.Join(out.TempDir, "fixed.txt")
	fixedFile, err := os.Create(fixedPath)
	if err != nil {
		return out, err
	}
	fixedClient := &auditClient{file: fixedFile}
	// 单独小函数拥有 fixedClient 的关闭权，返回后才能读文件并报告两条记录。
	err = func() (err error) {
		defer func() {
			closeErr := fixedClient.Close()
			err = errors.Join(err, closeErr)
			t.Event("E05.fixed.owner", "全部请求后应用关闭 close_ok=%t", closeErr == nil)
		}()
		if err = borrowedRequestFixed(t, fixedClient); err != nil {
			return err
		}
		out.FixedSecondErr = borrowedRequestFixed(t, fixedClient)
		return out.FixedSecondErr
	}()
	if err != nil {
		return out, err
	}
	data, readErr := os.ReadFile(fixedPath)
	if readErr != nil {
		return out, readErr
	}
	out.FixedContent = string(data)
	t.Event("E05.fixed.caller", "第二请求成功=%t records=%d", out.FixedSecondErr == nil, len(data)/len(orderRecord))
	return out, nil
}

// borrowedRequestBad 是实际错误入口：请求没创建 client，却自行安排其 Close。
// defer 到本请求函数返回时执行，不会等应用 owner 的所有其他请求结束。
func borrowedRequestBad(t *Trace, client *auditClient) (err error) {
	defer func() {
		err = errors.Join(err, client.Close())
		t.Event("E05.bad.request", "请求返回前错误地关闭借来的客户端")
	}()
	return client.Record()
}

// borrowedRequestFixed 只有借用权，所以只写记录，并把本次 Write error 交给应用。
func borrowedRequestFixed(t *Trace, client *auditClient) error {
	err := client.Record()
	t.Event("E05.fixed.request", "本请求写入完成，不取得应用 Close 所有权")
	return err
}
