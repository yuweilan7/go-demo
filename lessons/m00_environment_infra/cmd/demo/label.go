package main

// lessonLabel 由同包的 run 调用，放在第二个文件是为了实际复现单文件遗漏。
// 它不需要 export：小写名字在同一 package 的其他文件仍可见。
func lessonLabel() string {
	return "M00：证明当前程序来自哪个工具链与 module"
}
