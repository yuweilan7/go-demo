// reflection 用可观察的输出演示 Go 反射中的 Type、Value、Kind、寻址、修改和动态调用。
package main

import (
	"fmt"
	"reflect"
	"sort"
)

// Score 是一个“定义类型”：它拥有自己的类型身份，只是底层类型为 int。
// 这与别名 type AgeAlias = int 不同；别名 AgeAlias 和 int 是同一个类型。
type Score int

type AgeAlias = int

type Student struct {
	Name   string `json:"name" validate:"required"`
	Age    int    `json:"age" validate:"gte=0"`
	Score  Score  `json:"score"`
	secret string // 未导出字段也能被反射“看见”，但不能通过安全反射读取接口值或修改。
}

// Summary 使用值接收器，因此 Student 和 *Student 的方法集中都有它。
func (s Student) Summary(prefix string) string {
	return fmt.Sprintf("%s: %s/%d/%d", prefix, s.Name, s.Age, s.Score)
}

// Rename 使用指针接收器，因此只有 *Student 的方法集中有它。
func (s *Student) Rename(name string) {
	s.Name = name
}

// hidden 不会出现在具体类型通过 reflect.Type 暴露的方法列表中。
// 对非接口类型，反射只允许访问已导出方法。
func (s Student) hidden() string {
	return s.secret
}

func add(left, right int) (int, error) {
	return left + right, nil
}

func main() {
	demonstrateTypeValueAndKind()
	demonstrateNilValues()
	demonstrateStructAndTags()
	demonstrateContainers()
	demonstrateAddressabilityAndMutation()
	demonstrateMethodsAndCalls()
	demonstrateFunctionTypeAndCall()
}

func demonstrateTypeValueAndKind() {
	fmt.Println("=== 1. Type、Value 与 Kind ===")

	student := Student{Name: "张三", Age: 18, Score: 95, secret: "内部备注"}
	var dynamic any = student

	// TypeOf 和 ValueOf 接收 any，并观察接口中的动态值。
	// Type 描述“它是什么类型”，Value 表示“这一次运行时拿到的具体值”。
	t := reflect.TypeOf(dynamic)
	v := reflect.ValueOf(dynamic)
	fmt.Printf("TypeOf: String=%s, Name=%s, Kind=%s\n", t, t.Name(), t.Kind())
	fmt.Printf("ValueOf: Type=%s, Kind=%s, Interface=%+v\n", v.Type(), v.Kind(), v.Interface())

	// Value.String 不是通用的“取原值字符串”方法。对非 string 类型，它通常返回描述文本。
	// 真正要取回原值应使用 Interface，再做类型断言；已知 Kind 时也可用 Int/String 等方法。
	fmt.Printf("Value.String()=%q（不要把它当成通用值转换）\n", v.String())
	recovered, ok := v.Interface().(Student)
	fmt.Printf("Interface() 后断言 Student: ok=%t, Name=%s\n", ok, recovered.Name)

	var score Score = 100
	var alias AgeAlias = 20
	fmt.Printf("定义类型 Score: type=%s, name=%s, kind=%s\n",
		reflect.TypeOf(score), reflect.TypeOf(score).Name(), reflect.TypeOf(score).Kind())
	fmt.Printf("别名 AgeAlias:  type=%s, name=%s, kind=%s\n",
		reflect.TypeOf(alias), reflect.TypeOf(alias).Name(), reflect.TypeOf(alias).Kind())
	fmt.Println("Type 是精确身份，Kind 是大类：Score 与 int 的 Type 不同，但 Kind 都是 int")
}

func demonstrateNilValues() {
	fmt.Println("\n=== 2. nil 接口与带类型的 nil 指针 ===")

	// nil 接口既没有动态类型，也没有动态值。
	// TypeOf(nil) 返回 nil；ValueOf(nil) 返回零 Value，先用 IsValid 判断后再做其他操作。
	var empty any
	emptyType := reflect.TypeOf(empty)
	emptyValue := reflect.ValueOf(empty)
	fmt.Printf("nil 接口: TypeOf=%v, Value.IsValid=%t, Kind=%s\n",
		emptyType, emptyValue.IsValid(), emptyValue.Kind())

	// 接口本身不为 nil，因为它携带了动态类型 *Student；其中的动态值才是 nil。
	var pointer *Student
	var typedNil any = pointer
	typedNilValue := reflect.ValueOf(typedNil)
	fmt.Printf("带类型的 nil: TypeOf=%s, interface==nil=%t, Kind=%s, Value.IsNil=%t\n",
		reflect.TypeOf(typedNil), typedNil == nil, typedNilValue.Kind(), typedNilValue.IsNil())

	// 对无效 Value 调用 Interface、Type 等方法会 panic；IsNil 也只适用于特定 Kind。
	// 因此通用反射代码通常先检查 IsValid，再按 Kind 分支。
}

func demonstrateStructAndTags() {
	fmt.Println("\n=== 3. 读取结构体字段定义、值与标签 ===")

	student := Student{Name: "张三", Age: 18, Score: 95, secret: "内部备注"}
	t := reflect.TypeOf(student)
	v := reflect.ValueOf(student)

	fmt.Printf("结构体 %s 一共有 %d 个字段\n", t.Name(), t.NumField())
	for index := 0; index < t.NumField(); index++ {
		fieldType := t.Field(index)  // 字段的定义：名称、类型、tag、是否导出等元数据。
		fieldValue := v.Field(index) // 这个 student 实例在该字段上的值。

		jsonName, hasJSONTag := fieldType.Tag.Lookup("json")
		valueText := "<不能通过 Interface 读取>"
		if fieldValue.CanInterface() {
			valueText = fmt.Sprintf("%v", fieldValue.Interface())
		}

		fmt.Printf("字段[%d] name=%-6s type=%-10s kind=%-6s exported=%-5t value=%-25s json=%q(tag存在=%t)\n",
			index,
			fieldType.Name,
			fieldType.Type,
			fieldType.Type.Kind(),
			fieldType.IsExported(),
			valueText,
			jsonName,
			hasJSONTag,
		)
	}

	// NumField/Field 只适用于 Struct。通用代码必须先判断 Kind，否则调用会 panic。
	if t.Kind() == reflect.Struct {
		if ageField, found := t.FieldByName("Age"); found {
			fmt.Printf("FieldByName(Age): offset=%d, validate=%q\n",
				ageField.Offset, ageField.Tag.Get("validate"))
		}
	}
}

func demonstrateContainers() {
	fmt.Println("\n=== 4. map、slice、array 的反射访问 ===")

	grades := map[int]uint32{2: 200, 1: 100}
	mapValue := reflect.ValueOf(grades)
	keys := mapValue.MapKeys()
	// Go 的 map 迭代顺序没有保证。这里排序只为让教学输出稳定，不代表 MapKeys 自带顺序。
	sort.Slice(keys, func(left, right int) bool {
		return keys[left].Int() < keys[right].Int()
	})
	for _, key := range keys {
		value := mapValue.MapIndex(key)
		fmt.Printf("map: key(type=%s,value=%d) -> value(type=%s,value=%d)\n",
			key.Type(), key.Int(), value.Type(), value.Uint())
	}

	numbers := []int{1, 2, 3}
	sliceValue := reflect.ValueOf(numbers)
	fmt.Printf("slice: len=%d, 第 0 项=%d, CanAddr=%t, CanSet=%t\n",
		sliceValue.Len(), sliceValue.Index(0).Int(),
		sliceValue.Index(0).CanAddr(), sliceValue.Index(0).CanSet())
	sliceValue.Index(0).SetInt(10)
	fmt.Println("通过 slice 元素的 Value 修改后，原切片 =", numbers)

	array := [3]int{4, 5, 6}
	arrayCopyValue := reflect.ValueOf(array)
	fmt.Printf("按值传入 array: 第 0 项 CanAddr=%t, CanSet=%t（反射看到的是数组副本）\n",
		arrayCopyValue.Index(0).CanAddr(), arrayCopyValue.Index(0).CanSet())

	arrayValue := reflect.ValueOf(&array).Elem()
	arrayValue.Index(0).SetInt(40)
	fmt.Println("传入 *array 再 Elem 后可修改，原数组 =", array)

	// 原文遗漏了几种情况：Value.Len 还支持 Chan、Map，以及指向数组的指针。
	channel := make(chan int, 3)
	channel <- 1
	fmt.Printf("Len 也适用于 string/map/chan/*array: %d/%d/%d/%d\n",
		reflect.ValueOf("Go").Len(),
		mapValue.Len(),
		reflect.ValueOf(channel).Len(),
		reflect.ValueOf(&array).Len())
}

func demonstrateAddressabilityAndMutation() {
	fmt.Println("\n=== 5. CanAddr 与 CanSet：可寻址不等于可修改 ===")

	student := Student{Name: "张三", Age: 18, Score: 95, secret: "内部备注"}

	// 按值传入时，反射持有的是一份副本；其字段不可寻址、不可设置。
	copyField := reflect.ValueOf(student).FieldByName("Name")
	fmt.Printf("ValueOf(student).Name: CanAddr=%t, CanSet=%t\n",
		copyField.CanAddr(), copyField.CanSet())

	// 传指针并 Elem 解引用后，反射能沿着指针找到原结构体。
	// 导出字段 Name 同时满足可寻址、可设置，所以 SetString 会修改原对象。
	studentValue := reflect.ValueOf(&student).Elem()
	nameField := studentValue.FieldByName("Name")
	fmt.Printf("ValueOf(&student).Elem().Name: CanAddr=%t, CanSet=%t\n",
		nameField.CanAddr(), nameField.CanSet())
	if nameField.CanSet() && nameField.Kind() == reflect.String {
		nameField.SetString("李四")
	}

	// 未导出字段 secret 是可寻址的，但安全反射仍禁止设置它。
	secretField := studentValue.FieldByName("secret")
	fmt.Printf("未导出 secret: CanAddr=%t, CanSet=%t, CanInterface=%t\n",
		secretField.CanAddr(), secretField.CanSet(), secretField.CanInterface())
	fmt.Printf("修改后的 student = %+v\n", student)

	// 直接对 copyField 或 secretField 调 SetString 都会 panic。
	// 所以写通用工具时，真正的修改门槛是 CanSet，并且还要核对 Kind/Type 是否匹配。
}

func demonstrateMethodsAndCalls() {
	fmt.Println("\n=== 6. 方法集与运行时动态调用 ===")

	valueType := reflect.TypeOf(Student{})
	pointerType := reflect.TypeOf(&Student{})
	fmt.Printf("Student 导出方法=%v\n", exportedMethodNames(valueType))
	fmt.Printf("*Student 导出方法=%v\n", exportedMethodNames(pointerType))
	fmt.Println("方法按名称排序；非接口类型的反射方法列表只包含导出方法")

	// Name 对没有名称的复合类型（例如 *Student、[]int）返回空串，并不会 panic。
	// NumField 才要求 Kind 必须是 Struct，因此指针要先 Elem。
	fmt.Printf("指针类型: String=%s, Name=%q, Kind=%s, Elem.Name=%s, Elem.NumField=%d\n",
		pointerType, pointerType.Name(), pointerType.Kind(),
		pointerType.Elem().Name(), pointerType.Elem().NumField())

	student := &Student{Name: "张三", Age: 18, Score: 95}

	// Type.MethodByName 得到的是“未绑定接收器”的方法；调用 Func 时接收器是第一个实参。
	if method, found := pointerType.MethodByName("Rename"); found {
		method.Func.Call([]reflect.Value{
			reflect.ValueOf(student),
			reflect.ValueOf("李四"),
		})
	}
	fmt.Println("Type.MethodByName 调用 Rename 后:", student.Name)

	// Value.MethodByName 得到的是绑定到 student 的方法值，调用时不再传接收器。
	boundRename := reflect.ValueOf(student).MethodByName("Rename")
	if boundRename.IsValid() {
		boundRename.Call([]reflect.Value{reflect.ValueOf("王五")})
	}
	fmt.Println("Value.MethodByName 调用 Rename 后:", student.Name)

	boundSummary := reflect.ValueOf(student).MethodByName("Summary")
	if boundSummary.IsValid() {
		results := boundSummary.Call([]reflect.Value{reflect.ValueOf("学生")})
		fmt.Println("动态调用 Summary 的返回值:", results[0].String())
	}

	// 找不到方法时，Value.MethodByName 返回无效 Value；参数个数或类型不匹配时 Call 会 panic。
	missing := reflect.ValueOf(student).MethodByName("Missing")
	fmt.Println("不存在的方法 IsValid =", missing.IsValid())
}

func exportedMethodNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumMethod())
	for index := 0; index < t.NumMethod(); index++ {
		names = append(names, t.Method(index).Name)
	}
	return names
}

func demonstrateFunctionTypeAndCall() {
	fmt.Println("\n=== 7. 检查并动态调用函数 ===")

	functionType := reflect.TypeOf(add)
	fmt.Printf("函数类型=%s, Kind=%s, 入参=%d, 出参=%d\n",
		functionType, functionType.Kind(), functionType.NumIn(), functionType.NumOut())
	for index := 0; index < functionType.NumIn(); index++ {
		fmt.Printf("  入参[%d]: %s\n", index, functionType.In(index))
	}
	for index := 0; index < functionType.NumOut(); index++ {
		fmt.Printf("  出参[%d]: %s\n", index, functionType.Out(index))
	}

	results := reflect.ValueOf(add).Call([]reflect.Value{
		reflect.ValueOf(20),
		reflect.ValueOf(22),
	})
	fmt.Printf("动态调用 add(20, 22): sum=%d, errorIsNil=%t\n",
		results[0].Int(), results[1].IsNil())

	fmt.Println("反射把不少编译期错误推迟到运行时；能用接口、泛型或普通函数表达时，通常优先不用反射")
}
