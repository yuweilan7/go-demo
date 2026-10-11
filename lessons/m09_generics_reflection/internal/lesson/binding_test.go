package lesson_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"

	"go-demo/lessons/m09_generics_reflection/internal/lesson"
)

// TestNilKinds 验证无效 Value 与 typed nil，并遍历 IsNil 支持的动态 Kind。
// 接口槽本身的 Value 要由 &empty.Elem 取得；ValueOf(empty) 直接取得动态值，nil 时无效。
func TestNilKinds(t *testing.T) {
	var pointer *lesson.Query
	var channel chan int
	var function func()
	var dictionary map[string]string
	var values []int
	for _, input := range []any{nil, pointer, channel, function, dictionary, values} {
		isNil, err := lesson.NilStatus(input)
		if err != nil || !isNil {
			t.Fatalf("nil 输入 %T: nil=%t err=%v", input, isNil, err)
		}
	}
	var empty any
	slot := reflect.ValueOf(&empty).Elem()
	if slot.Kind() != reflect.Interface || !slot.IsNil() {
		t.Fatal("可寻址接口槽应显示 Interface Kind/nil，与 nil 动态值的零 Value 不同")
	}
	if isNil, err := lesson.NilStatus(&lesson.Query{}); err != nil || isNil {
		t.Fatalf("非 nil 指针: nil=%t err=%v", isNil, err)
	}
	if _, err := lesson.NilStatus(42); err == nil {
		t.Fatal("int 不支持 IsNil，应通过 error 拒绝而非 panic")
	}
	for _, input := range []any{nil, 42} {
		if caught := lesson.CatchPanic(func() { reflect.ValueOf(input).IsNil() }); caught == nil {
			t.Fatalf("危险 IsNil(%T) 应 panic", input)
		}
	}
}

// TestBindingAllOrNothing 以调用方原 DTO 是否改变为断言，不仅检查 error 文案。
// 无效数字、溢出、未知字段和未导出字段必须不留下 customer 的部分写入。
func TestBindingAllOrNothing(t *testing.T) {
	original := lesson.Query{Customer: "alice", Limit: 2}
	for name, input := range map[string]map[string]string{
		"number":   {"customer": "bob", "limit": "oops"},
		"overflow": {"customer": "bob", "limit": "999999999999999999999999999"},
		"unknown":  {"customer": "bob", "other": "x"},
		"private":  {"customer": "bob", "secret": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			actual := original
			if err := lesson.BindFlat(&actual, input); err == nil || actual != original {
				t.Fatalf("绑定失败须不改原对象: actual=%+v err=%v", actual, err)
			}
		})
	}
	bad := original
	if err := lesson.BindQueryBad(&bad, map[string]string{"customer": "bob", "limit": "oops"}); err == nil || bad.Customer != "bob" || bad.Limit != 2 {
		t.Fatalf("危险版本应该留下部分写入: %+v err=%v", bad, err)
	}
	fixed := original
	if err := lesson.BindFlat(&fixed, map[string]string{"customer": "bob", "limit": "3"}); err != nil || fixed.Customer != "bob" || fixed.Limit != 3 {
		t.Fatalf("有效绑定: %+v err=%v", fixed, err)
	}
}

// TestBindingRejectsUnsupportedShapes 验证受限反射工具的输入边界，不能默默扩展成任意框架。
func TestBindingRejectsUnsupportedShapes(t *testing.T) {
	var pointer *lesson.Query
	for _, input := range []any{nil, pointer, lesson.Query{}, new(int)} {
		if err := lesson.BindFlat(input, nil); err == nil {
			t.Fatalf("无效 dst %T 应拒绝", input)
		}
	}
	unsupported := struct {
		Enabled bool `query:"enabled"`
	}{}
	if err := lesson.BindFlat(&unsupported, map[string]string{"enabled": "true"}); err == nil || unsupported.Enabled {
		t.Fatalf("bool Kind 不能被当成已支持字段: err=%v", err)
	}
	duplicate := struct {
		First  string `query:"name"`
		Second string `query:"name"`
	}{}
	if err := lesson.BindFlat(&duplicate, map[string]string{"name": "alice"}); err == nil || duplicate.First != "" {
		t.Fatalf("重复 tag 不能留下第一字段写入: err=%v", err)
	}
}

// TestMethodAndJSONBoundaries 验证方法集、绑定 receiver 与真实 JSON 解码的不同失败语义。
// 反射 Call 错误参数会 panic；JSON 类型错误以 error 返回，但可能已经修改先前字段。
func TestMethodAndJSONBoundaries(t *testing.T) {
	query := lesson.Query{Customer: "alice", Limit: 2}
	if _, ok := reflect.TypeOf(query).MethodByName("SetLimit"); ok {
		t.Fatal("值类型的反射方法集中不应出现指针接收者 SetLimit")
	}
	bound := reflect.ValueOf(&query).MethodByName("SetLimit")
	if bound.Type().NumIn() != 1 {
		t.Fatal("bound 方法不再要求 receiver 实参")
	}
	caught := lesson.CatchPanic(func() { bound.Call([]reflect.Value{reflect.ValueOf("3")}) })
	if caught == nil || query.Limit != 2 {
		t.Fatal("不匹配的 Call 参数必须在修改前失败")
	}
	bound.Call([]reflect.Value{reflect.ValueOf(3)})
	if query.Limit != 3 {
		t.Fatal("bound receiver 应修改原 DTO")
	}
	if err := json.Unmarshal([]byte(`{"customer":"bob","limit":4}`), &query); err != nil || query.Customer != "bob" || query.Limit != 4 {
		t.Fatalf("真实 json tag 绑定结果=%+v err=%v", query, err)
	}
	var nilQuery *lesson.Query
	var invalid *json.InvalidUnmarshalError
	if err := json.Unmarshal([]byte(`{}`), nilQuery); !errors.As(err, &invalid) {
		t.Fatalf("nil dst 应返回真实 JSON API 错误: %v", err)
	}
	query = lesson.Query{Customer: "alice", Limit: 2}
	var mismatch *json.UnmarshalTypeError
	if err := json.Unmarshal([]byte(`{"customer":"bob","limit":"oops"}`), &query); !errors.As(err, &mismatch) || query.Customer != "bob" || query.Limit != 2 {
		t.Fatalf("JSON 类型失败的部分绑定事实=%+v err=%v", query, err)
	}
}

// TestRealLibraryContract 验证真实锁定库的调用次数与错误身份，而非把自写循环当作库。
// 被取消的 ctx 不会阻止 Retry 的第一次 operation；本课 Operation 没有外部写入副作用。
func TestRealLibraryContract(t *testing.T) {
	report := lesson.RunLibrary(io.Discard)
	if report.SuccessCalls != 2 || report.Value.ID != 101 || report.FailureCalls != 2 || report.Failure == nil {
		t.Fatalf("Retry 业务事实: %+v", report)
	}
	if report.CancellationCalls != 1 || !errors.Is(report.Cancellation, context.Canceled) {
		t.Fatalf("当前库取消边界: %+v", report)
	}
}

// TestJSONCandidatePublication 检查调用层修复：失败只改候选副本，不发布给正在使用的 DTO。
// Query 只有值字段，结构体复制足以隔离；含 map/slice/指针时还需要明确复制其可变内容。
func TestJSONCandidatePublication(t *testing.T) {
	original := lesson.Query{Customer: "alice", Limit: 2}
	candidate := original
	if err := json.Unmarshal([]byte(`{"customer":"bob","limit":"oops"}`), &candidate); err == nil {
		original = candidate
	}
	if original.Customer != "alice" || original.Limit != 2 || candidate.Customer != "bob" {
		t.Fatalf("错误不能发布候选值: original=%+v candidate=%+v", original, candidate)
	}
}
