// Package validation 是从单文件版本拆出的库包，不含 main 函数。
package validation

import "fmt"

// Validate 由相邻目录的 main 包调用；改为大写以跨过包边界。
// 此最小拆包实验只验证基本签名/导出，完整状态和数值边界见 order.Validate。
func Validate(id, status string, quantity uint8, unitCents int64) (int64, error) {
	if id == "" || status != "paid" || quantity == 0 {
		return 0, fmt.Errorf("订单不可出库")
	}
	return int64(quantity) * unitCents, nil
}
