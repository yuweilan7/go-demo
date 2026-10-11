package main

// main 由 go run . 的可执行包调用；format.go 与此文件组成同一个包。
// go run main.go 仅选此文件，找不到另一个文件的小写 formatReceipt，不是“未导出”。
func main() {
	formatReceipt("ORD-100", 2500)
}
