package course

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

var ErrReadBudget = errors.New("read call budget exhausted")

// TailReader 是合法的 io.Reader fixture，专门让最后一段数据和错误同次返回。
// 它不连接外部服务；数据与最终错误由 IO 实验的入口创建。
// offset 属于这个实例，只有调用者串行读取，不能把它跨 goroutine 无锁共享。
type TailReader struct {
	data   []byte
	offset int
	tail   error
}

// NewTailReader 把字符串转成该 reader 专属字节 slice，不保留调用者的可变缓冲。
// tail 必须是非 nil 的结束信号，例如 io.EOF 或 io.ErrUnexpectedEOF。
func NewTailReader(data string, tail error) *TailReader {
	if tail == nil {
		tail = io.EOF
	}
	return &TailReader{data: []byte(data), tail: tail}
}

// Read 由 ReadPayload 或 BadReadPayload 调用，把数据复制到它们拥有的 p。
// 返回 n>0 的同一次调用也可以返回非 nil err；这是 Reader 的正式契约。
// copy 不保留 p；有效范围只到 p[:n]，p 剩余空间不属于本次返回数据。
func (r *TailReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.offset == len(r.data) {
		return 0, r.tail
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	if r.offset == len(r.data) {
		return n, r.tail
	}
	return n, nil
}

// ReadPayload 由 IO 演示或调用者读取订单文本；返回内容可能和失败同时有效。
// 它拥有局部缓冲和累计结果，不负责关闭传入 reader，关闭权仍在创建 reader 的 owner。
// maxReads 限制 Read 的调用次数；本课 reader 的每次 Read 都同步立即返回。
// 次数上限不能打断外部 reader 自身的阻塞：网络 I/O 仍需 deadline/context 契约。
func ReadPayload(r io.Reader, maxReads int) (string, error) {
	var result bytes.Buffer
	buffer := make([]byte, 4) // 每次最多取 4 字节，便于看到同一次的 n 与 err。
	for call := 0; call < maxReads; call++ {
		n, err := r.Read(buffer)
		if n > 0 {
			// 必须先消费已读数据，再判断错误；Write 给 bytes.Buffer 复制这些字节。
			// 不能保存 buffer[:n] 的别名用于后来处理，下一次 Read 会复用该底层数组。
			_, _ = result.Write(buffer[:n]) // bytes.Buffer.Write 契约是完整写入且 error 总为 nil。
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// EOF 表示输入正常结束；最后的 n>0 字节已经收下，故返回成功。
				return result.String(), nil
			}
			// 非 EOF 错误保留链，同时交回已经读取的部分数据。
			// caller 可以诊断/展示它，但不能自动声称“完整订单已读完”。
			return result.String(), fmt.Errorf("read order payload: %w", err)
		}
		if n == 0 {
			// 0,nil 不代表 EOF；本实验选择立即报告无进展，避免 fixture 造成无限循环。
			return result.String(), io.ErrNoProgress
		}
	}
	return result.String(), ErrReadBudget
}

// BadReadPayload 只用于显式标出的危险对照，不用它读取真实业务文件。
// 它复现把“err!=nil 时丢掉其他结果”套到所有 API 的错误习惯。
func BadReadPayload(r io.Reader) (string, error) {
	buffer := make([]byte, 64)
	n, err := r.Read(buffer)
	if err != nil {
		return "", err // 错误点：n>0 的最后一段字节没有被消费。
	}
	return string(buffer[:n]), nil
}
