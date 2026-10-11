// 这个小 fixture 独立声明 go 1.21，用本机 Go 1.27 编译器实际执行旧语言语义。
// 没有 require/replace，不升级旧 module，不用 goroutine 去碰巧复现循环捕获问题。
package main

import (
	"fmt"
	"runtime"
)

// main 由 runtime 调用，在这个 go1.21 module 内创建回调、结束循环、调用回调并验证结果。
// values/函数切片/指针切片都属于本次进程，不启动任务或持有外部资源，结束即无后续业务活动。
func main() {
	values := []int{1, 2, 3}
	var readers, fixed []func() int
	var pointers []*int
	for _, value := range values {
		// go 1.21 的 := 循环仍复用同一个 value 变量，因此地址和闭包都指向同一位置。
		pointers = append(pointers, &value)
		readers = append(readers, func() int { return value })
		snapshot := value // 每次执行循环体创建局部新变量，是旧版本可用的修复。
		fixed = append(fixed, func() int { return snapshot })
	}
	// 所有回调在循环结束后才同步调用，三个旧回调读取最终 value=3，顺序确定。
	badValues, fixedValues := make([]int, 0, 3), make([]int, 0, 3)
	for i := range readers {
		badValues = append(badValues, readers[i]())
		fixedValues = append(fixedValues, fixed[i]())
	}
	fmt.Printf("runtime=%s module-language=go1.21\n", runtime.Version())
	fmt.Printf("旧 := 闭包=%v; &v 全相同=%v; 元素地址相同=%v\n", badValues, pointers[0] == pointers[1] && pointers[1] == pointers[2], pointers[0] == &values[0])
	fmt.Printf("循环体快照修复=%v\n", fixedValues)
	// 独立断言旧结果及修复结果，失败用 panic 标明 fixture 的关键版本证明未成立。
	if badValues[0] != 3 || badValues[1] != 3 || badValues[2] != 3 || pointers[0] != pointers[1] || fixedValues[0] != 1 || fixedValues[1] != 2 || fixedValues[2] != 3 {
		panic("go1.21 版本语义断言失败")
	}
	fmt.Println("go1.21 fixture PASS：编译器版本与模块语言语义分别核对")
}
