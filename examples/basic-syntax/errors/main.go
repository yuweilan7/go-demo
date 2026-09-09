// errors 用可运行的对照解释 Go 的 error：
// 1. error 只是一个只有 Error() string 方法的接口；
// 2. errors.New 创建的错误可以比较，只是每次调用得到的错误值彼此不同；
// 3. 错误文本适合给人看，程序应使用 errors.Is / errors.As 判断错误；
// 4. fmt.Errorf 配合 %w 可以保留错误链；
// 5. 自定义错误可以携带错误码、业务信息和底层原因。
package main

import (
	"errors"
	"fmt"
)

// error 是 Go 的预声明接口，定义等价于：
//
//	type error interface {
//		Error() string
//	}
//
// 一个类型只要拥有 Error() string 方法，就自动实现 error，不需要显式声明。
// error 接口本身不会自动记录调用栈，但具体错误类型仍可自行保存栈或其他信息。

// 哨兵错误（sentinel error）是一个被创建一次、反复复用的错误值。
// 调用方可以用 errors.Is 判断它，即使它后来被 %w 包装了多层。
var ErrNotPositive = errors.New("number is not positive")

// incrementPositive 演示 Go 常见的 (结果, error) 返回形式。
// 约定是：err == nil 表示成功；err != nil 时，其他返回值通常不应继续使用。
func incrementPositive(number int) (int, error) {
	if number <= 0 {
		// 这里返回 0，而不是人为约定的 -1。
		// 调用方应检查 err；0 只是 int 的零值，不承担表达错误的职责。
		return 0, ErrNotPositive
	}
	return number + 1, nil
}

// BusinessError 是一个带业务错误码的自定义错误。
// 使用指针接收者有两个好处：避免复制，并让 *BusinessError 成为唯一常用的错误形态，
// 从而减少 MyError 与 *MyError 混用导致类型断言失败的问题。
type BusinessError struct {
	Code    int
	Message string
	Cause   error
}

// 编译期检查：如果以后改坏了 Error 方法，编译器会在这里直接报错。
var _ error = (*BusinessError)(nil)

func (e *BusinessError) Error() string {
	// typed nil 也可能被装入 error 接口。处理 nil 接收者可以避免打印时 panic，
	// 但这并不会让一个装有 (*BusinessError)(nil) 的接口变成 nil。
	if e == nil {
		return "<nil BusinessError>"
	}
	return fmt.Sprintf("business error: code=%d, message=%s", e.Code, e.Message)
}

// Unwrap 把底层原因接入标准错误链。
// errors.Is 和 errors.As 会沿着这条链继续查找。
func (e *BusinessError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

var ErrInsufficientStock = errors.New("insufficient stock")

func createOrder(stock int) error {
	if stock <= 0 {
		businessErr := &BusinessError{
			Code:    1001,
			Message: "库存不足",
			Cause:   ErrInsufficientStock,
		}

		// %w 在增加上下文的同时保留 businessErr，形成错误链。
		return fmt.Errorf("create order: %w", businessErr)
	}
	return nil
}

// detailError 的底层类型是 slice，而 slice 不可比较。
// 它虽然可以实现 error，但把这种值装入接口后执行 == 会 panic。
// 实际项目通常让自定义错误使用指针类型，并用 errors.Is / errors.As 判断。
type detailError []string

func (e detailError) Error() string {
	return fmt.Sprintf("details: %v", []string(e))
}

// tryEqual 只为教学演示捕获接口比较产生的 panic。
// 正常业务代码不应靠 recover 来判断两个错误。
func tryEqual(left, right error) (equal, panicked bool) {
	defer func() {
		if recover() != nil {
			equal = false
			panicked = true
		}
	}()
	return left == right, false
}

func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}

func main() {
	section("1. error 是接口，也是普通返回值")

	result, err := incrementPositive(1)
	fmt.Printf("输入 1：result=%d, err=%v, err==nil=%t\n", result, err, err == nil)

	result, err = incrementPositive(-2)
	fmt.Printf("输入 -2：result=%d, err=%v, err==nil=%t\n", result, err, err == nil)
	fmt.Printf("错误的动态类型=%T，Error()=%q\n", err, err.Error())

	section("2. errors.New：能比较，但同文案不等于同错误")

	err1 := errors.New("hello")
	err2 := errors.New("hello")
	fmt.Printf("err1 == err2：%t（每次 New 都创建不同的错误值）\n", err1 == err2)
	fmt.Printf("err1.Error() == err2.Error()：%t（只是文案相同）\n",
		err1.Error() == err2.Error())

	first := ErrNotPositive
	second := ErrNotPositive
	fmt.Printf("复用同一个哨兵错误：first == second：%t\n", first == second)
	fmt.Printf("推荐的语义判断 errors.Is(first, ErrNotPositive)：%t\n",
		errors.Is(first, ErrNotPositive))

	// 相同文案可能代表完全不同的业务含义，所以比较 Error() 字符串很脆弱。
	usernameConflict := errors.New("conflict")
	versionConflict := errors.New("conflict")
	fmt.Printf("两个不同错误的文案相同：%t；语义匹配：%t\n",
		usernameConflict.Error() == versionConflict.Error(),
		errors.Is(usernameConflict, versionConflict))

	section("3. fmt.Errorf：%w 包装，%v 只格式化")

	wrapped := fmt.Errorf("increment -2: %w", ErrNotPositive)
	formattedOnly := fmt.Errorf("increment -2: %v", ErrNotPositive)
	fmt.Printf("wrapped：%v\n", wrapped)
	fmt.Printf("wrapped == ErrNotPositive：%t\n", wrapped == ErrNotPositive)
	fmt.Printf("errors.Is(wrapped, ErrNotPositive)：%t\n",
		errors.Is(wrapped, ErrNotPositive))
	fmt.Printf("errors.Is(formattedOnly, ErrNotPositive)：%t（%%v 没有保留错误链）\n",
		errors.Is(formattedOnly, ErrNotPositive))

	section("4. 自定义错误：errors.As 取类型，errors.Is 找原因")

	orderErr := createOrder(0)
	fmt.Printf("orderErr：%v\n", orderErr)

	// Go 1.26+ 可用泛型形式 errors.AsType，返回值不会出现“指向指针的指针”。
	// 较早版本通常写：
	//	var businessErr *BusinessError
	//	if errors.As(orderErr, &businessErr) { ... }
	if businessErr, ok := errors.AsType[*BusinessError](orderErr); ok {
		fmt.Printf("errors.AsType[*BusinessError]：ok=%t, code=%d, message=%q\n",
			ok, businessErr.Code, businessErr.Message)
	}
	fmt.Printf("errors.Is(orderErr, ErrInsufficientStock)：%t\n",
		errors.Is(orderErr, ErrInsufficientStock))

	section("5. 两个边界：不可比较动态值与 typed nil")

	var detail1 error = detailError{"field A"}
	var detail2 error = detailError{"field A"}
	equal, panicked := tryEqual(detail1, detail2)
	fmt.Printf("底层为 slice 的错误执行 ==：equal=%t, panicked=%t\n", equal, panicked)

	var nilBusinessErr *BusinessError
	var typedNil error = nilBusinessErr
	fmt.Printf("typed nil：动态类型=%T, typedNil==nil=%t, 打印值=%v\n",
		typedNil, typedNil == nil, typedNil)

	// 正确返回“没有错误”要直接返回 nil，而不是返回某个具体错误类型的 nil 指针。
	var noError error = nil
	fmt.Printf("真正的 nil error：noError==nil=%t\n", noError == nil)
}
