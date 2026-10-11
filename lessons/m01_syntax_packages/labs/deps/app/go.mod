module example.com/m01/app

go 1.27.0

require (
	example.com/m01/quote v1.0.0
	example.com/m01/receipt v1.0.0
)

require example.com/m01/rules v1.2.0 // indirect

// 所有合成模块都在本课内。版本标签用于观察 MVS，不代表这些 example.com 路径发布过。
replace (
	example.com/m01/quote => ../quote
	example.com/m01/receipt => ../receipt
	example.com/m01/rules => ../rules
)
