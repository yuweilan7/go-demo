// Package mymath 提供供教学使用的简单整数运算。
// 包名通常使用简短的小写单词，不推荐写成 myMath 或 mathClass。
package mymath

// Add 返回 x 与 y 的和。
// Add 首字母大写，因此它是“已导出”的，其他包可以调用它。
func Add(x, y int) int {
	return x + y
}

// AddAbsolute 先把两个参数转换为绝对值，再返回它们的和。
// 这个导出函数用于演示：同一个包中的代码可以调用小写开头的 abs。
func AddAbsolute(x, y int) int {
	return abs(x) + abs(y)
}

// abs 返回整数的绝对值。
// abs 首字母小写，所以它只对 mymath 包内部可见，main 包不能直接调用。
func abs(number int) int {
	if number < 0 {
		return -number
	}
	return number
}
