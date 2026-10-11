package lesson

import (
	"fmt"
	"reflect"
	"strconv"
)

// Query 是订单查询 DTO，反射绑定只处理 query tag 标识的平面 string/int 字段。
// tag 是类型上的静态元数据；反射能读 tag，实际业务含义由 BindFlat 解释。
type Query struct {
	Customer string `query:"customer" json:"customer"`
	Limit    int    `query:"limit" json:"limit"`
	secret   string `query:"secret"` // 可看见元数据，不代表可以 Set 或 Interface。
}

// Summary 被 reflect.Type/Value 的方法读取实验调用。
// 值接收者让 Query 和 *Query 的导出方法集中都出现 Summary。
func (q Query) Summary() string { return fmt.Sprintf("%s/limit=%d", q.Customer, q.Limit) }

// SetLimit 由绑定到 *Query 的反射方法值调用，只出现在 *Query 方法集中。
// q 指向原 DTO，赋值会修改原对象；动态 Call 的参数形状必须先检查。
func (q *Query) SetLimit(limit int) { q.Limit = limit }

// NilStatus 由入口检查 any 的动态值，返回是否 nil；不支持 nil 的 Kind 返回显式 error。
// TypeOf(nil)==nil；ValueOf(nil) 是无效 Value，不能对它直接 IsNil。
func NilStatus(input any) (bool, error) {
	value := reflect.ValueOf(input)
	if !value.IsValid() {
		return true, nil // 这里是“无动态类型/值”的 nil 接口。
	}
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil(), nil
	default:
		return false, fmt.Errorf("kind=%s 不支持 IsNil", value.Kind())
	}
}

// assignment 只保存本次绑定的“将写入哪个字段、写什么值”，不公开给调用者。
// reflect.Value 指向原 DTO 字段，newValue 是独立暂存值；准备阶段不写原对象。
type assignment struct {
	field    reflect.Value
	newValue reflect.Value
}

// BindFlat 由 demo/test 接收外部字符串参数，dst 必须是非 nil 的结构体指针。
// 本函数不支持嵌套、指针字段、slice、方法注入或任意自动转换，不是万能绑定框架。
// 它先验证并转换全部字段，再统一写入，避免 customer 写成功后 limit 失败的部分修改。
func BindFlat(dst any, input map[string]string) error {
	pointer := reflect.ValueOf(dst)
	if !pointer.IsValid() || pointer.Kind() != reflect.Pointer || pointer.IsNil() {
		return fmt.Errorf("dst 必须是非 nil 的结构体指针")
	}
	value := pointer.Elem() // 解引用复制的指针，取得原结构体可寻址 Value。
	if value.Kind() != reflect.Struct {
		return fmt.Errorf("dst 必须指向 struct，实际 kind=%s", value.Kind())
	}
	definition := value.Type()
	pending := make([]assignment, 0, len(input))
	known := make(map[string]bool)
	for index := 0; index < definition.NumField(); index++ {
		fieldType := definition.Field(index)
		key, tagged := fieldType.Tag.Lookup("query")
		if !tagged || key == "" || key == "-" {
			continue
		}
		if known[key] {
			return fmt.Errorf("duplicate query tag %q", key)
		}
		known[key] = true
		raw, requested := input[key]
		if !requested {
			continue // 未提供的字段维持原值，本函数没有默认值系统。
		}
		field := value.Field(index)
		if !fieldType.IsExported() || !field.CanSet() {
			return fmt.Errorf("field %s 不可设置", fieldType.Name)
		}
		// New(Type).Elem 创建同一精确类型的可设置暂存值，避免命名 string/int 类型被换成别的 Type。
		converted := reflect.New(fieldType.Type).Elem()
		switch field.Kind() {
		case reflect.String:
			converted.SetString(raw)
		case reflect.Int:
			// Bits 来自字段 Type，ParseInt 检查溢出；仅处理 int，其他整数 Kind 明确拒绝。
			number, err := strconv.ParseInt(raw, 10, fieldType.Type.Bits())
			if err != nil {
				return fmt.Errorf("field %s: %w", fieldType.Name, err)
			}
			converted.SetInt(number)
		default:
			return fmt.Errorf("field %s: unsupported kind=%s", fieldType.Name, field.Kind())
		}
		pending = append(pending, assignment{field: field, newValue: converted})
	}
	for key := range input {
		if !known[key] {
			return fmt.Errorf("unknown query key %q", key)
		}
	}
	for _, write := range pending {
		write.field.Set(write.newValue) // 至此所有转换已成功，才开始修改调用方原 DTO。
	}
	return nil
}

// BindQueryBad 固定两项字段，故意在逐项转换时立即写入，重现非原子绑定。
// 调用者传入有效 *Query；错误不是语法错误，而是后续 limit 失败留下 customer 的写入。
func BindQueryBad(dst *Query, input map[string]string) error {
	value := reflect.ValueOf(dst).Elem()
	value.FieldByName("Customer").SetString(input["customer"])
	limit, err := strconv.Atoi(input["limit"])
	if err != nil {
		return fmt.Errorf("limit: %w", err)
	}
	value.FieldByName("Limit").SetInt(int64(limit))
	return nil
}

// MethodNames 由反射实验读取当前精确类型的方法集。
// 对非接口类型，reflect 只枚举导出方法；结果按方法名字排序，这是反射 API 契约。
func MethodNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumMethod())
	for index := 0; index < t.NumMethod(); index++ {
		names = append(names, t.Method(index).Name)
	}
	return names
}
