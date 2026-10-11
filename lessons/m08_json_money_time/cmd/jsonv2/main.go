// jsonv2 是 Go 1.27 正式 JSON v2 API 的独立对照入口。
// Go 1.27 默认可用，无需 GOEXPERIMENT=jsonv2；nojsonv2 会关闭它，核心 demo 仍用 v1 API。
package main

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"time"
)

// shape 是同一份输出数据，用来揭示直接改 import 对线上字段形状的影响。
// v1 omitempty 看 Go 的空值；v2 omitempty 看将编码成的 JSON 是否为 null/空字符串/空容器。
type shape struct {
	Quantity int64             `json:"quantity,omitempty"`
	Enabled  bool              `json:"enabled,omitempty"`
	Note     *string           `json:"note,omitempty"`
	Tags     []string          `json:"tags"`
	Meta     map[string]string `json:"meta"`
	ZeroTime time.Time         `json:"zero_time,omitzero"` // 两版都支持 omitzero；time.Time.IsZero 决定省略。
}

// main 把意料中的失败当成实验观察；run 的 error 只代表无法完成实验的异常失败。
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run 由 main 同步调用，不切换订单核心协议，只对合成 DTO 运行两种库 API。
// 所有 Marshal/Unmarshal 的副作用只发生在本函数局部变量，不会触及别的模块。
func run() error {
	fmt.Printf("[E04] runtime=%s 正式encoding/json/v2 对照\n", runtime.Version())
	empty := ""
	value := shape{Note: &empty}
	one, err := jsonv1.Marshal(value)
	if err != nil {
		return err
	}
	two, err := jsonv2.Marshal(value)
	if err != nil {
		return err
	}
	legacy, err := jsonv2.Marshal(value, jsonv1.DefaultOptionsV1())
	if err != nil {
		return err
	}
	fmt.Printf("v1=%s\nv2默认=%s\nv2+DefaultOptionsV1=%s\n", one, two, legacy)

	for _, raw := range []string{`{"quantity":1,"quantity":9}`, `{"QUANTITY":4}`, `{"quantitty":4}`, `{"quantity":1} {"quantity":2}`} {
		var first, second, compatible, strict struct {
			Quantity int64 `json:"quantity"`
		}
		v1err := jsonv1.Unmarshal([]byte(raw), &first)
		v2err := jsonv2.Unmarshal([]byte(raw), &second)
		compaterr := jsonv2.Unmarshal([]byte(raw), &compatible, jsonv1.DefaultOptionsV1())
		strictErr := jsonv2.Unmarshal([]byte(raw), &strict, jsonv2.RejectUnknownMembers(true))
		// 错误时目的值可能已经部分改变，所以只打印，不把它安装到业务状态。
		fmt.Printf("raw=%s\n  v1 quantity=%d err=%v\n  v2 quantity=%d err=%v\n  v2兼容 quantity=%d err=%v\n  v2拒未知 quantity=%d err=%v\n", raw, first.Quantity, v1err, second.Quantity, v2err, compatible.Quantity, compaterr, strict.Quantity, strictErr)
	}
	var opted struct {
		Quantity int64 `json:"quantity"`
	}
	if err := jsonv2.Unmarshal([]byte(`{"QUANTITY":4}`), &opted, jsonv2.MatchCaseInsensitiveNames(true)); err != nil {
		return err
	}
	duplicateErr := jsonv2.Unmarshal([]byte(`{"quantity":1,"quantity":9}`), &opted, jsonv1.DefaultOptionsV1(), jsontext.AllowDuplicateNames(false))
	fmt.Printf("显式MatchCaseInsensitiveNames -> quantity=4；兼容选项后再禁重复 -> err=%v\n", duplicateErr)
	first, second := struct {
		Quantity int64 `json:"quantity"`
	}{7}, struct {
		Quantity int64 `json:"quantity"`
	}{7}
	if err := jsonv1.Unmarshal([]byte(`{"quantity":null}`), &first); err != nil {
		return err
	}
	if err := jsonv2.Unmarshal([]byte(`{"quantity":null}`), &second); err != nil {
		return err
	}
	fmt.Printf("已有quantity=7再解null -> v1=%d v2=%d\n", first.Quantity, second.Quantity)
	invalidUTF8 := []byte{'"', 0xff, '"'}
	var firstText, secondText string
	v1err := jsonv1.Unmarshal(invalidUTF8, &firstText)
	v2err := jsonv2.Unmarshal(invalidUTF8, &secondText)
	fmt.Printf("非法UTF8 -> v1文本=%q err=%v；v2 err=%v\n", firstText, v1err, v2err)
	duration := time.Second
	v1duration, err := jsonv1.Marshal(duration)
	if err != nil {
		return err
	}
	_, v2durationErr := jsonv2.Marshal(duration)
	compatibleDuration, err := jsonv2.Marshal(duration, jsonv1.DefaultOptionsV1())
	if err != nil {
		return err
	}
	fmt.Printf("Duration v1=%s（纳秒）v2默认err=%v；v2兼容=%s\n", v1duration, v2durationErr, compatibleDuration)

	// 只在这个故障实验用反射构造重复 tag，避免把坏 DTO 放进正常业务类型。
	// StructOf 返回一个运行时 struct 类型；反射不是订单解码必须掌握的实现技术。
	conflictType := reflect.StructOf([]reflect.StructField{
		{Name: "A", Type: reflect.TypeFor[int](), Tag: `json:"amount"`},
		{Name: "B", Type: reflect.TypeFor[int](), Tag: `json:"amount"`},
	})
	conflict := reflect.New(conflictType).Elem()
	conflict.Field(0).SetInt(1)
	conflict.Field(1).SetInt(2)
	conflictV1, conflictV1Err := jsonv1.Marshal(conflict.Interface())
	_, conflictV2Err := jsonv2.Marshal(conflict.Interface())
	fmt.Printf("两个字段同tag -> v1=%s err=%v；v2 err=%v\n", conflictV1, conflictV1Err, conflictV2Err)
	fmt.Println("E04 完成：变更默认行为前，必须按 contract_version 验收字段形状和失败路径。")
	return nil
}
