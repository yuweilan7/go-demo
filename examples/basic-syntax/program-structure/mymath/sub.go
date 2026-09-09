package mymath

// Sub 返回 x 减去 y 的结果。
// add.go 和 sub.go 文件名不同，但它们都声明 package mymath，
// 因此编译时会被当成同一个包共同处理。
func Sub(x, y int) int {
	return x - y
}
