module example.com/modulelab/greeting

go 1.27.0

require example.com/modulelab/punctuation v0.0.0

// replace 让这个模块在单独测试时也使用相邻的本地模块，不需要访问网络。
// replace 只影响当前 main module；依赖方不会继承这里的 replace。
replace example.com/modulelab/punctuation => ../punctuation
