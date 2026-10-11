package lesson

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestFilterOwnership 检查业务事实：返回可售库存按原顺序 A/C，调用方库存保持完整，
// 并且结果的 Stock/Tags/Meta 可独立修改。断言不依赖函数内部如何分配/循环。
func TestFilterOwnership(t *testing.T) {
	input := NewInventory()
	before := CloneProducts(input)
	result := FilterSnapshot(input)
	if !slices.Equal(skus(result), []string{"A", "C"}) {
		t.Fatalf("可售库存错误: %v", skus(result))
	}
	result[0].Stock = 99
	result[0].Tags[0] = "changed"
	result[0].Meta["warehouse"] = "changed"
	if !reflect.DeepEqual(input, before) {
		t.Fatalf("快照修改污染输入: got=%+v want=%+v", input, before)
	}
	// 反例也独立验证，确保这套合成输入确实能触发缓存污染，而不是只打印“错误”字样。
	bad := NewInventory()
	FilterInPlaceBad(bad)
	if !slices.Equal(skus(bad), []string{"A", "C", "C"}) {
		t.Fatalf("未复现预期覆盖: %v", skus(bad))
	}
}

// TestEmptyBoundaries 由 go test 调用，区分 nil、非 nil 空、全部缺货、嵌套 nil/空。
// 这些是 API 返回边界，不检查偶然的底层地址/扩容容量，也不会对空列表取 &s[0]。
func TestEmptyBoundaries(t *testing.T) {
	if FilterSnapshot(nil) != nil || CloneProducts(nil) != nil {
		t.Fatal("nil 输入应保留 nil")
	}
	for name, input := range map[string][]Product{
		"非nil空": make([]Product, 0),
		"全部缺货":  {{SKU: "B", Stock: 0}},
	} {
		t.Run(name, func(t *testing.T) {
			filtered := FilterSnapshot(input)
			if filtered == nil || len(filtered) != 0 {
				t.Fatalf("应返回非nil空结果: %#v", filtered)
			}
			owned := FilterOwned(CloneProducts(input))
			if owned == nil || len(owned) != 0 || cap(owned) != 0 {
				t.Fatalf("独占空结果契约错误: %#v cap=%d", owned, cap(owned))
			}
		})
	}
	nilFields := CloneProduct(Product{SKU: "N"})
	emptyFields := CloneProduct(Product{SKU: "E", Tags: []string{}, Meta: map[string]string{}})
	if nilFields.Tags != nil || nilFields.Meta != nil || emptyFields.Tags == nil || emptyFields.Meta == nil {
		t.Fatal("嵌套 nilness 不应改变")
	}
}

// TestCacheOwnsInputAndOutput 模拟两个请求先后修改读写对象，第三次读须看到最初库存。
// 它检查缓存隔离的业务契约，与“Put 里调用了 CloneProducts”这种实现细节断言不同。
func TestCacheOwnsInputAndOutput(t *testing.T) {
	cache := NewSnapshotCache()
	input := NewInventory()
	cache.Put("stock", input)
	input[0].Stock = 100
	input[0].Tags[0] = "caller"
	input[0].Meta["warehouse"] = "caller"
	read, ok := cache.Get("stock")
	if !ok || !reflect.DeepEqual(read, NewInventory()) {
		t.Fatalf("写入方污染缓存: %+v ok=%v", read, ok)
	}
	read[0].Stock = 101
	read[0].Tags[0] = "reader"
	read[0].Meta["warehouse"] = "reader"
	readAgain, _ := cache.Get("stock")
	if !reflect.DeepEqual(readAgain, NewInventory()) {
		t.Fatalf("读取方污染缓存: %+v", readAgain)
	}
	if missing, ok := cache.Get("absent"); ok || missing != nil {
		t.Fatalf("缺失键语义错误: %+v %v", missing, ok)
	}
	cache.Put("nil", nil)
	cache.Put("empty", []Product{})
	nilValue, nilOK := cache.Get("nil")
	emptyValue, emptyOK := cache.Get("empty")
	if !nilOK || nilValue != nil || !emptyOK || emptyValue == nil || len(emptyValue) != 0 {
		t.Fatal("缓存应区分缺失/nil快照/空快照")
	}
}

// TestOwnedFilterClearsDiscardedReferences 检查独占修复的资源事实：废弃尾槽不再引用旧嵌套对象。
// 它只证明引用已清除，不把它宣称为 GC 已完成或进程 RSS 已下降。
func TestOwnedFilterClearsDiscardedReferences(t *testing.T) {
	input := NewInventory()
	result := FilterOwned(input)
	if !slices.Equal(skus(result), []string{"A", "C"}) || cap(result) != len(result) {
		t.Fatalf("独占结果错误: %+v cap=%d", result, cap(result))
	}
	if !reflect.DeepEqual(input[2], Product{}) {
		t.Fatalf("尾部过期引用未清理: %+v", input[2])
	}
}

// TestLoopAndErrorOwnership 把回调统一延迟到构造结束后调用，确定性验证 B 的错误归属。
// 同步执行不会制造数据竞争，因此这个测试也不声称验证了并发 map/err 同步方案。
func TestLoopAndErrorOwnership(t *testing.T) {
	r := ObserveLoops()
	if !slices.Equal(r.Declared, []string{"A", "B", "C"}) || !slices.Equal(r.Assigned, []string{"C", "C", "C"}) || !slices.Equal(r.Fixed, []string{"A", "B", "C"}) {
		t.Fatalf("当前语言循环语义错误: %+v", r)
	}
	bad, fixed := errorCallbacks()
	if bad[1] != "B:ok" || fixed[1] != "B 缺货" {
		t.Fatalf("失败归属不符合反例/修复: bad=%v fixed=%v", bad, fixed)
	}
}

// TestUTF8BusinessBoundary 用中文、空文本和不合法输入检查截断契约。
// emoji 用两个码点组成的国旗举例，说明码点截断不是字素截断，而不是假装已解决 UI 字符计数。
func TestUTF8BusinessBoundary(t *testing.T) {
	cases := []struct {
		name, input, want string
		count             int
		wantError         bool
	}{
		{name: "中文", input: "库存A", count: 2, want: "库存"},
		{name: "空串", input: "", count: 2, want: ""},
		{name: "零上限", input: "库存A", count: 0, want: ""},
		{name: "大上限", input: "库存A", count: 20, want: "库存A"},
		{name: "负数", input: "库存A", count: -1, wantError: true},
		{name: "坏字节", input: string([]byte{0xff}), count: 1, wantError: true},
		{name: "国旗拆码点", input: "🇨🇳", count: 1, want: "🇨"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PrefixRunes(tc.input, tc.count)
			if got != tc.want || (err != nil) != tc.wantError {
				t.Fatalf("got=%q err=%v want=%q wantError=%v", got, err, tc.want, tc.wantError)
			}
		})
	}
}

// TestNilMapPanicAndDefer 在测试自己的 goroutine 内 recover，防止预期 panic 杀死测试进程。
// recover 必须直接位于 defer 函数内；nilMapFailure 不会从写入后的下一句继续。
func TestNilMapPanicAndDefer(t *testing.T) {
	var output bytes.Buffer
	func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				t.Error("nil map 写入必须 panic")
			}
		}()
		nilMapFailure(&transcript{out: &output})
	}()
	if !strings.Contains(output.String(), "defer 已执行") || strings.Contains(output.String(), "不可达") {
		t.Fatalf("panic 展开/清理路径错误: %s", output.String())
	}
}

// brokenWriter 是日志失败的单一故障注入，只有本地 Writer，不替代库存或真实缓存验收。
type brokenWriter struct{ err error }

// Write 返回测试预设的错误；调用者可用 errors.Is 验证 Run 是否保留原始原因。
func (w brokenWriter) Write([]byte) (int, error) { return 0, w.err }

// TestRunFailureBoundary 证明正常完整入口能跑完，未知编号和日志失败会真实返回 error。
// 它不只查 PASS 文本：各实验在 Run 内有状态断言，任何关键失败都会使这里得到 error。
func TestRunFailureBoundary(t *testing.T) {
	if err := Run(io.Discard, "all"); err != nil {
		t.Fatal(err)
	}
	if err := Run(io.Discard, "unknown"); err == nil {
		t.Fatal("未知实验编号应返回错误")
	}
	want := errors.New("合成日志写入失败")
	if err := Run(brokenWriter{err: want}, "text"); !errors.Is(err, want) {
		t.Fatalf("未保留 Writer 错误: %v", err)
	}
}
