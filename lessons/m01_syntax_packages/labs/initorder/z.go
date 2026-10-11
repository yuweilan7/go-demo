package main

import "fmt"

var base = trace("base", 10)

// trace 由包变量初始化调用，用返回值保持真实数值并输出初始化事件。
func trace(name string, value int) int {
	fmt.Printf("[L05 variable] %s=%d\n", name, value)
	return value
}

// init 与 a.go.init 都属于同一个包；显式文件顺序实验会改变两条 init 输出的次序。
func init() {
	fmt.Println("[L05 init] z.go.init")
}
