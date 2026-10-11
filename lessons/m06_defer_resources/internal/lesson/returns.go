package lesson

import "errors"

// ErrBusiness 是本课合成订单的业务失败，用 errors.Is 检查原因而不是匹配日志文案。
var ErrBusiness = errors.New("订单 O-100 校验失败")

// ReturnResult 分开记录 caller 的结果和清理函数观察到的 err。
// 二者不一致正是错误版的业务风险：caller 失败，退出日志却记录成功。
type ReturnResult struct {
	Named, Anonymous, NamedLocal   int
	EarlyReturned, EarlyObserved   error
	LateReturned, LateObserved     error
	ShadowReturned, ShadowObserved error
	FixedReturned, FixedObserved   error
}

// RunReturns 由 demo 调用，依次运行结果槽与 err 时间线。纯函数实验不持有外部资源。
func RunReturns(t *Trace) ReturnResult {
	t.Event("E02", "return 求值、结果赋值、defer 修改、最后返回")
	r := ReturnResult{}
	r.Named = namedReturn(t)
	r.Anonymous = anonymousReturn(t)
	r.NamedLocal = namedButLocal(t)
	t.Event("E02.caller", "named=%d anonymous=%d named_local=%d", r.Named, r.Anonymous, r.NamedLocal)
	r.EarlyReturned = captureTooEarly(t, &r.EarlyObserved)
	r.LateReturned = captureAtExit(t, &r.LateObserved)
	r.ShadowReturned = shadowResult(t, &r.ShadowObserved)
	r.FixedReturned = explicitReturn(t, &r.FixedObserved)
	t.Event("E02.caller", "early(return_err=%t cleanup_err=%t) late(return_err=%t cleanup_err=%t)",
		r.EarlyReturned != nil, r.EarlyObserved != nil, r.LateReturned != nil, r.LateObserved != nil)
	t.Event("E02.caller", "shadow(return_err=%t cleanup_err=%t) fixed(return_err=%t cleanup_err=%t)",
		r.ShadowReturned != nil, r.ShadowObserved != nil, r.FixedReturned != nil, r.FixedObserved != nil)
	return r
}

// namedReturn 由 RunReturns 调用。result 是可在函数体内读写的具名结果变量，初值 0。
// return 表达式完成后先给 result 赋值，defer 再改同一结果变量，caller 最后收到 15。
func namedReturn(t *Trace) (result int) {
	local := 10
	defer func() {
		t.Event("E02.named.defer", "退出时 result=%d local=%d，result+=5", result, local)
		result += 5
	}()
	return returnExpression(t, "named", local)
}

// anonymousReturn 没有可由闭包命名访问的结果槽。return 表达式的 int 值 10 已确定后，
// defer 修改的是另一个局部变量 local，caller 收到的 int 仍是 10。
func anonymousReturn(t *Trace) int {
	local := 10
	defer func() {
		local += 5
		t.Event("E02.anonymous.defer", "local 改为 %d；已确定的 int 结果没有改变", local)
	}()
	return returnExpression(t, "anonymous", local)
}

// namedButLocal 即使有具名结果，修改目标也必须是结果本身。local 与 result 是两个变量。
func namedButLocal(t *Trace) (result int) {
	local := 10
	defer func() {
		local += 5
		t.Event("E02.named_local.defer", "local=%d result=%d；只改 local", local, result)
	}()
	return returnExpression(t, "named_local", local)
}

// returnExpression 模拟 return 里的函数调用，并在 defer 执行前留下求值日志。
func returnExpression(t *Trace, label string, value int) int {
	t.Event("E02."+label+".return_expr", "表达式求得 %d", value)
	return value
}

// observeCleanup 是被延迟调用的结果观察者。observed 由 caller 创建；e 是调用实参副本。
// 它只记录结果，没有模拟数据库事务，也没有用 e 的真假决定回滚。
func observeCleanup(t *Trace, label string, e error, observed *error) {
	*observed = e
	t.Event("E02."+label+".cleanup", "观察到 err_non_nil=%t", e != nil)
}

// captureTooEarly 故意重现 defer cleanup(err)。error 是接口值，这里保存的是 nil 副本。
// 后续 err=ErrBusiness 改了具名结果变量，不会改已经保存的延迟调用实参。
func captureTooEarly(t *Trace, observed *error) (err error) {
	defer observeCleanup(t, "early", err, observed)
	err = ErrBusiness
	t.Event("E02.early.body", "注册时 err=nil，现在 err=业务错误")
	return err
}

// captureAtExit 的延迟闭包到退出时读取具名 err，读取点落在 return 结果赋值之后。
// 这是需要记录最终 error 时的修复；闭包仍不能自动替代 Close/Flush 的错误处理。
func captureAtExit(t *Trace, observed *error) (err error) {
	defer func() { observeCleanup(t, "late", err, observed) }()
	err = ErrBusiness
	t.Event("E02.late.body", "闭包已注册，现在 err=业务错误")
	return err
}

// validateOrder 为遮蔽实验生成确定的失败。调用者必须传播这个 error，不能只打印。
func validateOrder() (string, error) { return "", ErrBusiness }

// shadowResult 故意让内层短变量声明创建新的 err，然后吞掉失败。
// if 的初始化部分属于内层作用域，:= 不会重用函数结果 err；外层 err 一直是 nil。
func shadowResult(t *Trace, observed *error) (err error) {
	defer func() { observeCleanup(t, "shadow", err, observed) }()
	if _, err := validateOrder(); err != nil {
		t.Event("E02.shadow.body", "内层 err 是业务错误，只打印后离开 if")
		// 这里若写裸 return 会编译失败：result parameter err not in scope at return。
		// 本例离开 if 后裸 return 合法，但外层结果 err 仍为 nil，错误因此被吞掉。
	}
	return
}

// explicitReturn 保留内层 err，但明确 return err。return 的表达式先读取内层变量，
// 然后把该值赋给函数的具名结果 err；退出闭包随后读取外层结果，看到真实失败。
func explicitReturn(t *Trace, observed *error) (err error) {
	defer func() { observeCleanup(t, "fixed", err, observed) }()
	if _, err := validateOrder(); err != nil {
		t.Event("E02.fixed.body", "内层失败通过 return err 赋给具名结果")
		return err
	}
	return nil
}
