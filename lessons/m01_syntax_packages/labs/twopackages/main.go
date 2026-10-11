package main

import (
	"fmt"

	// 字符串定位 module+目录；policies 是本文件给 validation 包指定的 import 别名。
	// 默认名称来自 package validation，并不来自文件名 validate.go 或 module 最后一个词。
	policies "go-demo/lessons/m01_syntax_packages/labs/twopackages/validation"
)

// main 保留单文件版的 caller 逻辑，校验实现变成跨包 policies.Validate 调用。
// 两个普通包在两个目录，仍处于同一个 module，所以不需要额外 require 或 replace。
func main() {
	total, err := policies.Validate("ORD-100", "paid", 2, 1250)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("[L06 two-packages] id=ORD-100 total_cents=%d\n", total)
}
