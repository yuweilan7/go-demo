// Package lesson 把订单查询配置、批量结果和文件序列放在同一套业务数据中。
// 它服务于 cmd/demo 与独立断言；没有后台 goroutine 或网络客户端。
package lesson

import "fmt"

// OrderID 与 Cents 都是定义类型，拥有各自的类型身份，底层类型同为 int64。
// 业务代码不能把订单编号随意当作金额；~int64 约束则能允许这两类底层类型。
type OrderID int64
type Cents int64

// Order 是本课所有实验共用的合成订单。字段按值复制，没有隐藏连接或锁。
type Order struct {
	ID       OrderID
	Customer string
	Amount   Cents
}

// Summary 是普通值接收者方法，demo 的 Summarizer 接口调用它。
// o 是订单副本；本函数只返回显示文本，不修改订单，也不持有资源。
func (o Order) Summary() string {
	return fmt.Sprintf("订单%d/%s/%d分", o.ID, o.Customer, o.Amount)
}

// Summarizer 是仅包含方法的基本接口，可以作为运行时变量的类型。
// 接口值保存动态类型和动态值；这里没有类型集合项 ~T 或 T|U。
type Summarizer interface {
	Summary() string
}

// Result[T] 保留业务值的精确类型：Result[Order] 与 Result[string] 是不同类型。
// T any 允许任意类型，但函数体只能使用所有 T 都支持的操作。
// 这是本课的小封装，并不代替所有 Go API 的 (T, error) 返回约定。
type Result[T any] struct {
	Value T
	Err   error
}

// Success 由实验入口调用，T 可由 value 推导，返回给下一步转换函数。
func Success[T any](value T) Result[T] {
	return Result[T]{Value: value}
}

// Failure 用明确类型参数创建失败结果，因为仅由 error 无法推导出 T。
// Value 保持 T 的零值；本课调用者传非 nil 错误，Result 字段公开并不强制这个不变量。
func Failure[T any](err error) Result[T] {
	return Result[T]{Err: err}
}

// OK 使用接收者里的 T，不声明新的类型参数；Go 1.18 起就能写这种方法。
// r 按值复制，Err 接口也被复制，底层错误对象仍可共享。
func (r Result[T]) OK() bool { return r.Err == nil }

// MapResult 是旧版本也能写的包级泛型函数，由 demo 和 Result.Map 调用。
// T/U 分别是输入/输出类型；mapper 是调用者传入的同步函数值。
// 失败分支不调用 mapper，原始 error 交给上一层，因此 errors.Is 的错误链保持有效。
func MapResult[T, U any](r Result[T], mapper func(T) U) Result[U] {
	if r.Err != nil {
		return Failure[U](r.Err)
	}
	return Success(mapper(r.Value))
}

// Map 声明方法自己的 U，是 Go 1.27 新语法。接收者 T 与新参数 U 各有作用域。
// 调用 r.Map(func(Order) string {...}) 时，由接收者决定 T，由函数签名推导 U。
// 接口方法仍不能声明自己的类型参数；这里的泛型方法也不能实现普通接口方法。
func (r Result[T]) Map[U any](mapper func(T) U) Result[U] {
	return MapResult(r, mapper)
}

// IntegerAmount 是非基本接口，类型集合允许底层类型为 int 或 int64 的定义类型。
// 它只能作为类型约束，不能写 var amount IntegerAmount 作为运行时值容器。
// ~int64 包含 Cents；直接写 int64 则不包含 Cents。这里的 | 是类型集合的并集。
type IntegerAmount interface {
	~int | ~int64
}

// Sum 由订单金额实验调用，T 约束允许 +；total 是 T 的零值，返回仍为 T。
// 本课小金额不会溢出；泛型和约束不会自动引入金额溢出保护。
func Sum[T IntegerAmount](values []T) T {
	var total T
	for _, value := range values {
		total += value
	}
	return total
}

// MapSlice 由 demo 把 Order 切片投影为 Cents。S ~[]E 接受命名切片和普通切片。
// E 是元素类型，U 是投影结果类型；返回 []U 没有承诺保留输入的命名切片类型。
// mapper 每个元素同步调用一次；out 使用新底层数组，但若 U 为指针，复制的仍是指针。
func MapSlice[S ~[]E, E, U any](values S, mapper func(E) U) []U {
	out := make([]U, len(values))
	for index, value := range values {
		out[index] = mapper(value)
	}
	return out
}

// Unique 由 demo 去重订单编号，保留首次出现的顺序。
// comparable 让 T 可做 map key；Go 1.20 起 any 也满足此约束，但动态 []int 仍会 panic。
// 因此业务边界最好保留 OrderID/string 等具体键类型，别把约束当成任意值的安全保证。
func Unique[T comparable](values []T) []T {
	seen := make(map[T]struct{}, len(values))
	out := make([]T, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{} // 空结构体只表示“已见过”，不存第二份业务负载。
		out = append(out, value)
	}
	return out
}

// SampleOrders 为 demo 和断言返回每次独立的合成切片，实验间没有共享写入数据。
func SampleOrders() []Order {
	return []Order{{101, "alice", 1200}, {102, "bob", 2300}, {103, "alice", 700}}
}
