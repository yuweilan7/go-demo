module example.com/modulelab/app

go 1.27.0

// app 的源码直接导入 greeting，所以它没有 indirect 标记。
require example.com/modulelab/greeting v0.0.0

// app 没有直接 import punctuation，但构建依赖图需要它，因此 tidy 标记为 indirect。
require example.com/modulelab/punctuation v0.0.0 // indirect

// v0.0.0 在这里只是满足 require 语法的占位版本；真正源码由右侧本地目录提供。
// app 必须声明两个 replace，因为依赖模块 greeting 中的 replace 不会传递给 main module。
replace (
	example.com/modulelab/greeting => ../greeting
	example.com/modulelab/punctuation => ../punctuation
)
