// 显式故障输入：用于观察“小写包内可见”，与同包跨文件调用对照。
package main

import "go-demo/lessons/m01_syntax_packages/service"

// main 在编译期试图调用另一个包未导出的 save 方法，Go 不允许跨越该边界。
func main() {
	var store service.Store
	store.save(service.Receipt{ID: "ORD-100"}) // 错误：save 小写；正确 caller 为 service.Ship。
}
