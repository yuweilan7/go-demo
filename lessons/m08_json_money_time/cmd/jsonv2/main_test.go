package main

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"reflect"
	"testing"
)

// TestDefaultsAndCompatibility 验证正式 API 的实际字段形状，而不是只确认 v2 能编译。
// 以无序 JSON 对象比较，避免把成员打印顺序误当成协议保证。
func TestDefaultsAndCompatibility(t *testing.T) {
	empty := ""
	value := shape{Note: &empty}
	for _, tc := range []struct {
		name string
		run  func() ([]byte, error)
		want string
	}{
		{"v1", func() ([]byte, error) { return jsonv1.Marshal(value) }, `{"note":"","tags":null,"meta":null}`},
		{"v2", func() ([]byte, error) { return jsonv2.Marshal(value) }, `{"quantity":0,"enabled":false,"tags":[],"meta":{}}`},
		{"v2兼容", func() ([]byte, error) { return jsonv2.Marshal(value, jsonv1.DefaultOptionsV1()) }, `{"note":"","tags":null,"meta":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := tc.run()
			if err != nil {
				t.Fatal(err)
			}
			var actual, want map[string]any
			if err := jsonv1.Unmarshal(encoded, &actual); err != nil {
				t.Fatal(err)
			}
			if err := jsonv1.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, want) {
				t.Fatalf("字段形状改变: got=%s want=%s", encoded, tc.want)
			}
		})
	}
}

// TestV2InputBoundaries 验证重复名字默认拒绝、未知字段默认忽略与显式严格选项。
func TestV2InputBoundaries(t *testing.T) {
	var value struct {
		Quantity int64 `json:"quantity"`
	}
	duplicate := []byte(`{"quantity":1,"quantity":9}`)
	if err := jsonv2.Unmarshal(duplicate, &value); err == nil {
		t.Fatal("v2默认未拒绝重复")
	}
	if err := jsonv2.Unmarshal(duplicate, &value, jsonv1.DefaultOptionsV1()); err != nil || value.Quantity != 9 {
		t.Fatalf("v1兼容覆盖语义不符: %+v %v", value, err)
	}
	if err := jsonv2.Unmarshal(duplicate, &value, jsonv1.DefaultOptionsV1(), jsontext.AllowDuplicateNames(false)); err == nil {
		t.Fatal("后置禁重复选项未覆盖兼容选项")
	}
	value.Quantity = 0
	if err := jsonv2.Unmarshal([]byte(`{"QUANTITY":4}`), &value); err != nil || value.Quantity != 0 {
		t.Fatal("v2默认大小写严格语义不符")
	}
	if err := jsonv2.Unmarshal([]byte(`{"QUANTITY":4}`), &value, jsonv2.MatchCaseInsensitiveNames(true)); err != nil || value.Quantity != 4 {
		t.Fatal("显式忽略大小写选项不符")
	}
	if err := jsonv2.Unmarshal([]byte(`{"unknown":4}`), &value, jsonv2.RejectUnknownMembers(true)); err == nil {
		t.Fatal("v2严格未知字段未拒绝")
	}
	if err := jsonv2.Unmarshal([]byte(`{} {}`), &value); err == nil {
		t.Fatal("v2整份文档未拒绝尾随第二值")
	}
	if err := jsonv2.Unmarshal([]byte(`{"quantity":null}`), &value); err != nil || value.Quantity != 0 {
		t.Fatal("v2的标量null未清零")
	}
}
