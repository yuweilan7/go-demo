// 本示例通过可运行的输出学习 if、map 的 comma-ok 查询和 switch。
//
// 运行方式：
//
//	go run ./examples/basic-syntax/conditionals
package main

import "fmt"

func section(title string) {
	fmt.Printf("\n========== %s ==========\n", title)
}

func demonstrateIf() {
	section("1. if：条件不加括号，但代码块必须加大括号")

	localStr := "case3"

	// Go 的 if 条件必须是 bool，不会把 0、空字符串或 nil 自动当成 false。
	// 条件外层通常不写小括号，但分支的大括号不能省略。
	if localStr == "case3" {
		fmt.Println("进入 true 分支")
	} else {
		fmt.Println("进入 false 分支")
	}

	// if 可以在条件前先执行一条“简单语句”，二者以分号分隔。
	// length 的作用域覆盖 if、else if 和 else，但出了整个分支链就不可见。
	if length := len(localStr); length < 5 {
		fmt.Printf("%q 的长度为 %d，小于 5\n", localStr, length)
	} else if length == 5 {
		fmt.Printf("%q 的长度恰好为 %d\n", localStr, length)
	} else {
		fmt.Printf("%q 的长度为 %d，大于 5\n", localStr, length)
	}
	//fmt.Println(length) // 不能编译：undefined: length
}

func demonstrateMapLookup() {
	section("2. map 查询：value, ok 区分不存在与零值")

	dictionary := map[string]int{
		"apple":      1,
		"watermelon": 2,
		"free":       0,
	}

	// 查询 map 时只接收一个返回值，key 不存在也不会报错，而是得到值类型的零值。
	// int 的零值是 0，所以仅看 value 无法区分“orange 不存在”和“free 存在且值为 0”。
	missingValue := dictionary["orange"]
	zeroValue := dictionary["free"]
	fmt.Printf("只看值：orange=%d，free=%d（此时无法判断 key 是否存在）\n", missingValue, zeroValue)

	// comma-ok 查询的第二个结果才表示 key 是否存在。
	// 因此更准确的说法是“判断 key 是否存在”，而不是“判断 key 是否为空”。
	orangeNumber, orangeExists := dictionary["orange"]
	freeNumber, freeExists := dictionary["free"]
	fmt.Printf("comma-ok：orange -> value=%d, exists=%v\n", orangeNumber, orangeExists)
	fmt.Printf("comma-ok：free   -> value=%d, exists=%v\n", freeNumber, freeExists)

	// 把查询写在 if 的初始化语句里很常见；number 和 exists 只属于这个 if/else。
	if number, exists := dictionary["watermelon"]; exists {
		fmt.Printf("watermelon 存在，数量为 %d\n", number)
	} else {
		fmt.Println("watermelon 不存在")
	}
	// fmt.Println(number, exists) // 不能编译：二者已经超出作用域。
}

func demonstrateSwitch() {
	section("3. switch：匹配后默认结束，无需手写 break")

	localStr := "case3"
	switch localStr {
	case "case1":
		fmt.Println("匹配 case1")
	case "case2":
		fmt.Println("匹配 case2")
	case "case3":
		fmt.Println("匹配 case3")
	default:
		fmt.Println("没有 case 匹配，进入 default")
	}

	// 一个 case 可以列出多个表达式，以逗号分隔。
	day := "Saturday"
	switch day {
	case "Saturday", "Sunday":
		fmt.Printf("%s 是周末\n", day)
	default:
		fmt.Printf("%s 是工作日\n", day)
	}
}

func demonstrateFallthrough() {
	section("4. fallthrough：无条件执行紧邻的下一个 case")

	// Go 和传统 Java switch 的默认行为正好相反：
	// Java 的传统 case 如果不写 break，会自然贯穿到下一个 case；
	// Go 的 case 执行完默认自动结束，必须显式写 fallthrough 才会贯穿。
	value := "case1"
	switch value {
	case "case1":
		fmt.Println("先匹配并执行 case1")
		fallthrough
	case "case2":
		// 到达这里并不代表 value 等于 "case2"。
		// fallthrough 不会重新检查下一个 case 的表达式，只会直接执行其语句。
		fmt.Printf("fallthrough 后执行 case2 的代码，但 value 仍是 %q\n", value)
	case "case3":
		fmt.Println("case3 不会执行，因为 case2 末尾没有 fallthrough")
	}

	// fallthrough 只能出现在 case 的最后一条非空语句处，且不能用于最后一个 case。
	// 它不会像某些语言那样成为 switch 的默认行为；只有明确写出时才会发生贯穿。

	// 一个相对合理的用途是表达“高等级包含低等级的全部能力”。
	// gold 先获得自己的专属权益，然后无条件执行紧邻的 silver 和 basic 分支。
	memberLevel := "gold"
	benefits := make([]string, 0, 3)
	switch memberLevel {
	case "gold":
		benefits = append(benefits, "专属客服")
		fallthrough
	case "silver":
		benefits = append(benefits, "免运费")
		fallthrough
	case "basic":
		benefits = append(benefits, "积分")
	default:
		benefits = append(benefits, "无会员权益")
	}
	fmt.Printf("%s 会员的累计权益：%v\n", memberLevel, benefits)

	// 注意：这依赖 case 的排列顺序，而且 fallthrough 不检查下一个 case 的条件。
	// 一旦等级规则复杂，用数据表、函数或普通 if 往往比 fallthrough 更清楚、更安全。
}

func demonstrateExpressionlessSwitch() {
	section("5. 无表达式 switch：按顺序选择第一个为 true 的分支")

	temperature := 31
	// switch 后不写表达式等价于 switch true，适合表达互斥的条件链。
	switch {
	case temperature >= 35:
		fmt.Println("炎热")
	case temperature >= 25:
		fmt.Println("温暖")
	default:
		fmt.Println("凉爽")
	}
}

func main() {
	demonstrateIf()
	demonstrateMapLookup()
	demonstrateSwitch()
	demonstrateFallthrough()
	demonstrateExpressionlessSwitch()
}
