module example.com/m01/consumer

go 1.27.0

require example.com/m01/quote v1.0.0

require example.com/m01/rules v1.1.0 // indirect

// 修复放独立 -modfile 中，保持错误版可复跑，不改原 go.mod 或共享 go.work。
replace (
	example.com/m01/quote => ../quote
	example.com/m01/rules => ../rules
)
