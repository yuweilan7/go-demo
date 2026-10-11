// Package contract 定义订单接口进入业务层前的数据边界。
// 本包沿用 encoding/json 的 v1 API；JSON v2 的对照放在独立入口，不改变此处协议。
package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

// Field 是 PATCH 所需的“三态字段”，T 是这个字段实际业务值的类型。
// Present=false 表示请求没有这个名字；Present=true、Null=true 表示显式 null；
// Present=true、Null=false 才读取 Value，Value 可以合法地等于 T 的零值。
// 普通 *int64 只有 nil/非 nil，使用全新 DTO 时不能区分缺字段和显式 null。
type Field[T any] struct {
	Present bool
	Null    bool
	Value   T
}

// UnmarshalJSON 由 encoding/json 在“字段实际出现”时调用，包括出现 null 时。
// *Field[T] 是指针接收者：这里修改解码器持有的原字段，而不是修改它的副本。
// 缺字段时根本不调用本方法，因此调用者必须为每次请求创建全新的 DTO。
func (f *Field[T]) UnmarshalJSON(raw []byte) error {
	next := Field[T]{Present: true}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		next.Null = true // Value 保持零值；Null 标志才是“删除/拒绝”的业务依据。
	} else if err := json.Unmarshal(raw, &next.Value); err != nil {
		// 暂存到 next，失败时不把半解码值赋给 f；上层仍必须丢弃整个失败 DTO。
		return fmt.Errorf("字段值类型不符: %w", err)
	}
	*f = next
	return nil
}

// DecodeStrict 由 DecodeCreate/DecodePatch 调用，把有限大小的请求交给 v1 解码器。
// out 是请求专属 DTO 的非 nil 指针；allowed 是本协议的精确顶层字段名。
// 本函数不写数据库、不创建 goroutine；bytes.Reader 只读内存，没有 Close 资源。
func DecodeStrict(raw []byte, out any, allowed []string) error {
	if len(raw) > 16*1024 {
		return fmt.Errorf("请求超过本课 16 KiB 上限")
	}
	if !utf8.Valid(raw) {
		// v1 默认会替换非法 UTF-8；这里在原始字节边界主动拒绝，避免悄悄改写业务文本。
		return fmt.Errorf("请求包含非法 UTF-8")
	}
	names := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		names[name] = true
	}
	// v1 默认允许重复名字，且匹配 struct 字段时忽略大小写。
	// 先逐 token 检查，不能先解到 map 再查重复：map 已经丢掉第一次出现的名字。
	if err := checkObject(raw, names); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields() // 对具体 DTO 的未知字段也拒绝；它本身并不拒绝重复名字。
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("JSON 解码: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		// Decode 面向 JSON 流，一次成功只证明读到一个值；接口契约只允许一份文档。
		if err == nil {
			return fmt.Errorf("一份请求中出现了第二个 JSON 值")
		}
		return fmt.Errorf("JSON 尾随数据: %w", err)
	}
	return nil
}

// checkObject 检查顶层对象、每层对象的重复名字、精确字段名与文档终点。
// 名字由 Token 解转义后比较，所以 quantity 与 \u0071uantity 也算重复。
func checkObject(raw []byte, allowed map[string]bool) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber() // 扫描只关心结构，不让大数字先被 float64 转换。
	first, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("JSON 起始: %w", err)
	}
	if first != json.Delim('{') {
		return fmt.Errorf("请求顶层必须是 JSON 对象")
	}
	if err := walkObject(decoder, 1, allowed); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("一份请求中出现了第二个 JSON 值")
		}
		return fmt.Errorf("JSON 尾随数据: %w", err)
	}
	return nil
}

// walkObject 只由扫描器调用。seen 属于当前对象，嵌套对象各自重新创建 seen。
// allowed 非 nil 时检查顶层契约名字；嵌套字段的语义仍由具体 DTO 解码器检查。
func walkObject(decoder *json.Decoder, depth int, allowed map[string]bool) error {
	if depth > 16 {
		return fmt.Errorf("JSON 嵌套超过本课 16 层上限")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("对象名字: %w", err)
		}
		name, ok := token.(string) // 接口断言取出 Token 的动态 string 值。
		if !ok {
			return fmt.Errorf("对象名字必须是字符串")
		}
		if seen[name] {
			return fmt.Errorf("重复 JSON 名字 %q", name)
		}
		seen[name] = true
		if allowed != nil && !allowed[name] {
			return fmt.Errorf("未知或大小写不符的字段 %q", name)
		}
		if err := walkValue(decoder, depth+1); err != nil {
			return fmt.Errorf("字段 %q: %w", name, err)
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return fmt.Errorf("对象未正确结束: %v", err)
	}
	return nil
}

// walkValue 消耗一个值；对象递归检查名字，数组逐元素检查，标量到此已读完。
// 深度和请求字节数都受限，所以故意错误的输入也不会无限递归或无限等待。
func walkValue(decoder *json.Decoder, depth int) error {
	if depth > 16 {
		return fmt.Errorf("JSON 嵌套超过本课 16 层上限")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delim {
	case '{':
		return walkObject(decoder, depth, nil)
	case '[':
		for decoder.More() {
			if err := walkValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("数组未正确结束: %v", err)
		}
		return nil
	default:
		return fmt.Errorf("不期待的结束符 %q", delim)
	}
}
