package main

import "fmt"

// derived 的初始值依赖另一个文件的 base；即使 a.go 先交给编译器，也先初始化 base。
var derived = trace("derived", base+1)

// init 自动执行，依“文件交给编译器的顺序”排序，不作为业务依赖注入。
func init() {
	fmt.Println("[L05 init] a.go.init")
}
