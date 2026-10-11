package lesson

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"time"
)

// Run 由 cmd/demo 调用。顺序调用各实验，不创建后台任务；唯一持有的文件由 RunSequence 清理。
// out 是入口传入的 stdout 或测试缓冲区，本函数不关闭调用者拥有的 Writer。
func Run(out io.Writer, part string) error {
	valid := map[string]bool{"core": true, "all": true, "generics": true, "options": true, "iter": true, "reflect": true, "library": true}
	if !valid[part] {
		return fmt.Errorf("unknown part %q", part)
	}
	core := part == "core" || part == "all"
	if core || part == "generics" {
		RunGenerics(out)
	}
	if core || part == "options" {
		if err := RunOptions(out); err != nil {
			return err
		}
	}
	if core || part == "iter" {
		if err := RunSequence(out, ".local"); err != nil {
			return err
		}
	}
	if core || part == "reflect" {
		if err := RunReflection(out); err != nil {
			return err
		}
	}
	if part == "all" || part == "library" {
		RunLibrary(out)
	}
	fmt.Fprintln(out, "DEMO finished; 所选实验已返回，文件在返回前关闭并删除")
	return nil
}

// RunGenerics 沿同一批订单观察类型参数、约束和真实标准库的签名。
// 回调直接在当前栈执行；失败 Result 不调用映射闭包，金额求和保留 Cents 类型。
func RunGenerics(out io.Writer) {
	fmt.Fprintln(out, "=== E1 泛型：订单结果与批量投影 ===")
	orders := SampleOrders()
	result := Success(orders[0])
	text := result.Map(func(order Order) string { return order.Summary() })
	fmt.Fprintf(out, "GENERIC Result types=%s -> %s value=%s\n", reflect.TypeOf(result), reflect.TypeOf(text), text.Value)
	original := errors.New("订单不存在")
	calls := 0
	failed := Failure[Order](original).Map(func(order Order) string {
		calls++
		return order.Summary()
	})
	fmt.Fprintf(out, "GENERIC failure mapper-calls=%d same-error=%t zero-value=%q\n", calls, errors.Is(failed.Err, original), failed.Value)

	amounts := MapSlice(orders, func(order Order) Cents { return order.Amount })
	fmt.Fprintf(out, "GENERIC amounts=%v total=%d type=%T unique-id=%v\n", amounts, Sum(amounts), Sum(amounts), Unique([]OrderID{101, 102, 101}))
	var printable Summarizer = orders[0] // 基本接口保存具体订单值，可执行 Summary。
	fmt.Fprintf(out, "GENERIC basic-interface dynamic=%T summary=%s\n", printable, printable.Summary())

	type OrderList []Order
	copied := slices.Clone(OrderList(orders))
	copied[0].Customer = "copy-only"
	fmt.Fprintf(out, "STD slices.Clone keeps-type=%T independent-value=%t\n", copied, orders[0].Customer == "alice")
	pointer := &orders[0]
	shallow := slices.Clone([]*Order{pointer})
	fmt.Fprintf(out, "STD slices.Clone pointer-element-shared=%t\n", shallow[0] == pointer)
	keys := slices.Sorted(maps.Keys(map[OrderID]Order{102: orders[1], 101: orders[0]}))
	fmt.Fprintf(out, "STD sorted-map-keys=%v (maps.Keys 本身不保证顺序)\n", keys)
	// 本次创建独立 PCG Source，r.N 是 Go 1.27 的真实泛型方法。
	// 断言只依赖范围与精确类型，不把固定 seed 的具体输出当所有版本保证。
	r := rand.New(rand.NewPCG(1, 2))
	number := r.N(OrderID(10))
	fmt.Fprintf(out, "STD Rand.N type=%T in-range=%t\n", number, number >= 0 && number < 10)
	caught := CatchPanic(func() { Unique([]any{[]int{1}}) })
	fmt.Fprintf(out, "BAD comparable-any panic=%v\n", caught)
	fmt.Fprintf(out, "FIX concrete-key=%v\n", Unique([]OrderID{101, 101, 102}))
}

// RunOptions 先创建 Option，再改变源 map，分别观察应用时和构造后的别名边界。
// 每个错误由构造器同步返回，入口打印后继续修复对照；没有真实网络副作用。
func RunOptions(out io.Writer) error {
	fmt.Fprintln(out, "=== E2 options：配置捕获与快照 ===")
	defaults, err := NewClient()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "OPTION defaults endpoint=%s timeout=%s\n", defaults.Snapshot().Endpoint, defaults.Snapshot().Timeout)
	ordered, err := NewClient(WithTimeout(0), WithTimeout(time.Second))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "OPTION ordered final-timeout=%s\n", ordered.Snapshot().Timeout)
	_, invalid := NewClient(WithTimeout(0))
	_, nilOption := NewClient(nil)
	fmt.Fprintf(out, "BAD invalid-config=%v nil-option=%v\n", invalid, nilOption)

	headers := map[string]string{"X-Region": "sg"}
	badOption := WithHeadersBad(headers)
	safeOption := WithHeaders(headers) // 此调用现在已经拷贝 sg；还没有 Client。
	headers["X-Region"] = "changed-before-apply"
	bad, err := NewClient(badOption)
	if err != nil {
		return err
	}
	safe, err := NewClient(safeOption)
	if err != nil {
		return err
	}
	alias := NewClientBadAlias(headers)
	headers["X-Region"] = "changed-after-build"
	fmt.Fprintf(out, "BAD option-captured-map=%s client-alias=%s\n", bad.Snapshot().Headers["X-Region"], alias.Snapshot().Headers["X-Region"])
	fmt.Fprintf(out, "FIX captured-snapshot=%s final-build-snapshot=%s\n", safe.Snapshot().Headers["X-Region"], bad.Snapshot().Headers["X-Region"])
	view := safe.Snapshot()
	view.Headers["X-Region"] = "caller-tamper"
	fmt.Fprintf(out, "FIX getter-copy internal-header=%s\n", safe.Snapshot().Headers["X-Region"])
	explicit, err := NewClientConfig(Config{Endpoint: "https://orders.example.test", Timeout: time.Second, Headers: headers})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "OPTION explicit-config timeout=%s\n", explicit.Snapshot().Timeout)
	return nil
}

// CreateOrdersFile 为序列实验写三条真实文件记录，成功返回后文件由消费者入口拥有。
// 生成器自己负责 writer 的 Close，调用者再通过 FileLines 创建独立 reader。
func CreateOrdersFile(base string) (path string, err error) {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(base, "m09-orders-*.txt")
	if err != nil {
		return "", err
	}
	path = file.Name()
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil {
			err = errors.Join(err, os.Remove(path))
			path = ""
		}
	}()
	for _, order := range SampleOrders() {
		if _, err = fmt.Fprintln(file, order.Summary()); err != nil {
			return path, err
		}
	}
	return path, nil
}

// RunSequence 拥有临时文件的路径和删除责任，FileLines 负责每次 reader 的关闭。
// range / Pull2 / 坏包装各用一次新的 trace；没有 goroutine 调度或 sleep 猜测。
func RunSequence(out io.Writer, base string) (runErr error) {
	fmt.Fprintln(out, "=== E3 iterator：yield、停止与真实文件清理 ===")
	path, err := CreateOrdersFile(base)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, os.Remove(path)) }()
	trace := &SequenceTrace{}
	count := 0
	for line, err := range FileLines(path, trace) {
		if err != nil {
			return err
		}
		count++
		fmt.Fprintf(out, "ITER consumer line=%s\n", line)
		if count == 2 {
			break // break 先使 yield=false，FileLines return/defer 完成后才走下面的打印。
		}
	}
	if trace.CloseErr != nil {
		return trace.CloseErr
	}
	fmt.Fprintf(out, "ITER break read=%d open=%d closed=%d events=%v\n", trace.LinesRead, trace.OpenCount, trace.CloseCount, trace.Events)

	pullTrace := &SequenceTrace{}
	// Pull2 返回 next/stop 两个函数值；owner 在同一函数中顺序使用，并立即注册 stop。
	// 调 next 只取一项后，底层迭代调用仍悬在 yield；stop 让它继续并返回、执行 Close。
	if err := func() error {
		next, stop := iter.Pull2(FileLines(path, pullTrace))
		defer stop()
		line, err, ok := next()
		if err != nil {
			return err // next 的第二项是文件错误，不把打印了 error 当作处理完成。
		}
		if !ok {
			return errors.New("三行订单文件的第一项意外为空")
		}
		fmt.Fprintf(out, "ITER pull one ok=%t err=%v line=%s closed-before-stop=%d\n", ok, err, line, pullTrace.CloseCount)
		stop()
		_, _, after := next()
		fmt.Fprintf(out, "ITER pull stopped closed=%d next-ok=%t\n", pullTrace.CloseCount, after)
		return nil
	}(); err != nil {
		return err
	}
	if pullTrace.CloseErr != nil {
		return pullTrace.CloseErr
	}

	badTrace := &SequenceTrace{}
	caught := CatchPanic(func() {
		for range ContinueAfterStopBad(FileLines(path, badTrace)) {
			break
		}
	})
	fmt.Fprintf(out, "BAD iterator-after-stop panic=%v read=%d closed=%d\n", caught, badTrace.LinesRead, badTrace.CloseCount)
	if badTrace.CloseErr != nil {
		return badTrace.CloseErr
	}
	for index, order := range slices.All(SampleOrders()) {
		fmt.Fprintf(out, "STD Seq2 index=%d id=%d\n", index, order.ID)
	}
	return nil
}

// RunReflection 用 Query 展示 Type/Value/Kind、nil、可设值、tag 与方法绑定。
// 所有故障在当前调用链有界捕获；安全 BindFlat 的 error 直接返回给入口处理。
func RunReflection(out io.Writer) error {
	fmt.Fprintln(out, "=== E4 reflect：边界检查与平面绑定 ===")
	amount := Cents(1200)
	fmt.Fprintf(out, "REFLECT amount type=%s kind=%s value=%d\n", reflect.TypeOf(amount), reflect.ValueOf(amount).Kind(), reflect.ValueOf(amount).Int())
	var empty any
	var pointer *Query
	var boxed any = pointer
	nilEmpty, _ := NilStatus(empty)
	nilPointer, _ := NilStatus(boxed)
	fmt.Fprintf(out, "REFLECT nil-interface type=%v valid=%t nil=%t\n", reflect.TypeOf(empty), reflect.ValueOf(empty).IsValid(), nilEmpty)
	fmt.Fprintf(out, "REFLECT typed-nil interface==nil=%t type=%s nil=%t\n", boxed == nil, reflect.TypeOf(boxed), nilPointer)
	for _, item := range []any{empty, 42} {
		caught := CatchPanic(func() { reflect.ValueOf(item).IsNil() })
		fmt.Fprintf(out, "BAD IsNil input=%T panic=%v\n", item, caught)
	}
	_, kindError := NilStatus(42)
	fmt.Fprintf(out, "FIX NilStatus int-error=%v\n", kindError)

	query := Query{Customer: "alice", Limit: 2, secret: "internal"}
	copyField := reflect.ValueOf(query).FieldByName("Customer")
	originalField := reflect.ValueOf(&query).Elem().FieldByName("Customer")
	secret := reflect.ValueOf(&query).Elem().FieldByName("secret")
	fmt.Fprintf(out, "REFLECT copy set=%t original set=%t secret addr/set/interface=%t/%t/%t\n", copyField.CanSet(), originalField.CanSet(), secret.CanAddr(), secret.CanSet(), secret.CanInterface())
	caught := CatchPanic(func() { copyField.SetString("lost") })
	fmt.Fprintf(out, "BAD set-value-copy panic=%v original=%s\n", caught, query.Customer)
	field, _ := reflect.TypeOf(query).FieldByName("Customer")
	fmt.Fprintf(out, "REFLECT tag query=%q json=%q field-type=%s\n", field.Tag.Get("query"), field.Tag.Get("json"), field.Type)

	input := map[string]string{"customer": "bob", "limit": "oops"}
	bad := query
	badErr := BindQueryBad(&bad, input)
	fixed := query
	fixedErr := BindFlat(&fixed, input)
	fmt.Fprintf(out, "BAD partial-binding err=%t result=%s\n", badErr != nil, bad.Summary())
	fmt.Fprintf(out, "FIX failed-binding err=%t unchanged=%t result=%s\n", fixedErr != nil, fixed == query, fixed.Summary())
	input["limit"] = "3"
	if err := BindFlat(&fixed, input); err != nil {
		return err
	}
	fmt.Fprintf(out, "FIX successful-binding result=%s\n", fixed.Summary())
	nilBind := BindFlat(pointer, input)
	fmt.Fprintf(out, "REFLECT nil-destination-error=%v hidden-field-error=%v\n", nilBind, BindFlat(&fixed, map[string]string{"secret": "leak"}))

	fmt.Fprintf(out, "REFLECT methods Query=%v *Query=%v\n", MethodNames(reflect.TypeOf(query)), MethodNames(reflect.TypeOf(&query)))
	unbound, found := reflect.TypeOf(&fixed).MethodByName("SetLimit")
	bound := reflect.ValueOf(&fixed).MethodByName("SetLimit")
	if !found || !bound.IsValid() || unbound.Type.NumIn() != 2 || bound.Type().NumIn() != 1 {
		return fmt.Errorf("SetLimit 方法集/签名不符")
	}
	// Type 的 Func 没绑定对象，第一个实参是 receiver；Value 的方法值已绑定 &fixed。
	unbound.Func.Call([]reflect.Value{reflect.ValueOf(&fixed), reflect.ValueOf(4)})
	badCall := CatchPanic(func() { bound.Call([]reflect.Value{reflect.ValueOf("6")}) })
	fmt.Fprintf(out, "BAD reflect.Call wrong-type panic=%v unchanged-limit=%d\n", badCall, fixed.Limit)
	bound.Call([]reflect.Value{reflect.ValueOf(6)})
	fmt.Fprintf(out, "REFLECT unbound-inputs=%d bound-inputs=%d final-limit=%d missing-valid=%t\n", unbound.Type.NumIn(), bound.Type().NumIn(), fixed.Limit, reflect.ValueOf(&fixed).MethodByName("Missing").IsValid())

	// 真实标准库 JSON 解码展示 tag 的实际消费者；仅元数据打印无法证明绑定发生。
	if err := json.Unmarshal([]byte(`{"customer":"alice","limit":2}`), &fixed); err != nil {
		return err
	}
	invalidJSONDestination := json.Unmarshal([]byte(`{"customer":"bob"}`), pointer)
	fmt.Fprintf(out, "STD json.Unmarshal result=%s nil-pointer-error=%T\n", fixed.Summary(), invalidJSONDestination)
	jsonBad := fixed
	jsonErr := json.Unmarshal([]byte(`{"customer":"bob","limit":"oops"}`), &jsonBad)
	fmt.Fprintf(out, "BAD json-type-error=%T partial-result=%s\n", jsonErr, jsonBad.Summary())
	candidate := fixed
	// 解码写候选副本；只有 err==nil 才把结果交回原对象。这是调用层的发布边界。
	if err := json.Unmarshal([]byte(`{"customer":"bob","limit":"oops"}`), &candidate); err == nil {
		fixed = candidate
	}
	fmt.Fprintf(out, "FIX json-decode-candidate original=%s rejected-candidate=%s\n", fixed.Summary(), candidate.Summary())
	return nil
}
