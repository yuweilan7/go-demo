package main

import "fmt"

// main 只在所有包变量与 init 完成后调用，读取的 base/derived 已是 10/11。
func main() {
	fmt.Printf("[L05 main] base=%d derived=%d\n", base, derived)
}
