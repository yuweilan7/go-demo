module example.com/m01/consumer

go 1.27.0

require example.com/m01/quote v1.0.0

require example.com/m01/rules v1.1.0 // indirect

// 故意仅替换 quote：quote 自己的 rules replace 不会传递到 consumer。
replace example.com/m01/quote => ../quote
