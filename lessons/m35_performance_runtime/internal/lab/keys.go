// Package lab 放置本课的合成订单逻辑与有界实验。
// cmd/demo 拥有子进程及文件；本包不访问数据库、不修改共享服务。
package lab

import (
	"bytes"
	"fmt"
	"strconv"
	"sync"
)

// Order 是每次请求传入的值。两个 int64 的副本足够表达本实验输入，
// 不需要为了“少复制”给调用方增加共享指针和修改权限。
type Order struct {
	ShopID  int64
	OrderID int64
}

// KeyFmt 是基线：调用者拿到可独立保存的订单键字符串。
// Sprintf 支持任意格式，但这里要解析格式、处理可变参数中的 interface 动态值。
// 是否为这些值分配内存由当前编译器及 fmt 实现决定，应看 -m 和 benchmem。
func KeyFmt(order Order) string {
	return fmt.Sprintf("shop:%d/order:%d", order.ShopID, order.OrderID)
}

// KeyAppend 是热点的单一改动：仍然返回相同的字符串，只替换构造算法。
// 两个有符号 int64 最长各 20 个 ASCII 字节，加固定前缀总长不超过 52；
// 64 字节局部数组足够容纳这组受类型约束的输入，不能套用到任意长度业务文本。
func KeyAppend(order Order) string {
	var storage [64]byte
	// storage[:] 建立指向局部数组的 slice 描述符，[:0] 令长度为 0、容量为 64。
	// append 在容量够用时写入同一数组；这里没有把数组借给别的 goroutine。
	buf := append(storage[:0], "shop:"...)
	buf = strconv.AppendInt(buf, order.ShopID, 10)
	buf = append(buf, "/order:"...)
	buf = strconv.AppendInt(buf, order.OrderID, 10)
	// 转为可独立保存的 string。我们需要这次所有权转移的复制，不能用 unsafe
	// 让返回字符串指向随后失效/被复用的可写缓冲。实际分配量由 benchmark 核验。
	return string(buf)
}

// HeaderAlias 由保留实验调用，只把 16 字节视图交给长期持有者。
// slice 值很小，但指向的是完整底层数组；GC 的回收单位不是这个 len。
func HeaderAlias(body []byte) []byte { return body[:16] }

// HeaderCapTrap 复现常见的无效修复。第三个下标限制容量，避免 append
// 覆盖后面的 body，却没有切断与原数组的指针关系，仍可能保留整个大对象。
func HeaderCapTrap(body []byte) []byte { return body[:16:16] }

// HeaderClone 才把 16 字节复制到自己的底层数组，长期持有者不再拥有原 body。
// bytes.Clone 返回独立数据；调用方仍要保证传入 slice 至少有 16 字节。
func HeaderClone(body []byte) []byte { return bytes.Clone(body[:16]) }

// Scratch 代表一次构造过程中独占的临时缓冲；它不是连接、句柄或业务缓存项。
// 池中的对象可能随时被丢弃，因此正确性不能依赖下次 Get 拿回这个对象。
type Scratch struct{ Data []byte }

// ResetForReuse 由当前拥有者在归还前调用。[:cap] 扩大清零范围，清掉曾写过
// 但已不在当前 len 内的秘密字节；清零是本课的隔离策略，不等于所有程序都必需。
// 调用后旧 slice 别名也能看到清零，因此旧持有者必须已经结束使用。
func (s *Scratch) ResetForReuse() {
	clear(s.Data[:cap(s.Data)])
	s.Data = s.Data[:0]
}

// KeyPool 由订单调用方长期拥有，sync.Pool 仅提供可丢弃的临时对象复用。
// 复用的分配/清零成本是否值得必须另测；本课不把它作为 KeyAppend 的第二项优化。
type KeyPool struct{ pool sync.Pool }

// NewKeyPool 在应用初始化时建立缓冲复用器，New 在池空时创建新对象。
// 返回后禁止复制 KeyPool（sync.Pool 使用后不能复制），调用者应持有这一个指针。
func NewKeyPool() *KeyPool {
	p := &KeyPool{}
	p.pool.New = func() any { return &Scratch{Data: make([]byte, 0, 64)} }
	return p
}

// Key 由单个订单请求调用。Get 到 Put 之间，只有这个调用拥有 s 的写权限。
// 并发请求可以共享 p，但不能共享当前 s；返回 string 在归还前已经完成复制。
func (p *KeyPool) Key(order Order) string {
	s := p.pool.Get().(*Scratch)
	// .(*Scratch) 是 interface 动态类型断言；New 和 Put 必须只存这个类型。
	// 本函数掌握池的所有写入入口，因此断言成立；这不是任意 interface 都安全。
	defer func() {
		// defer 在 Key 返回前执行：先独立复制结果，再清零，再交回池。
		// Put 后本请求不得访问 s，即使对象实际上仍在内存中。
		s.ResetForReuse()
		p.pool.Put(s)
	}()
	s.Data = append(s.Data, "shop:"...)
	s.Data = strconv.AppendInt(s.Data, order.ShopID, 10)
	s.Data = append(s.Data, "/order:"...)
	s.Data = strconv.AppendInt(s.Data, order.OrderID, 10)
	return string(s.Data)
}
