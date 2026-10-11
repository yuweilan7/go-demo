package main

import "fmt"

// formatReceipt 由同包 main 调用，不需要大写。小写限制是包边界，不是文件边界。
// 本文件自己导入 fmt；import 名称是文件作用域，其他文件不能借用这个 fmt 名称。
func formatReceipt(id string, cents int64) {
	fmt.Printf("[L04 package] id=%s total_cents=%d files=main.go+format.go\n", id, cents)
}
