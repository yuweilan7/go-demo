module example.com/m01/quote

go 1.27.0

require example.com/m01/rules v1.1.0

// 单独以 quote 为 main module 时适用；app/consumer 不继承这条 replace。
replace example.com/m01/rules => ../rules
