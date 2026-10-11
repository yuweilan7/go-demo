package course

import (
	"errors"
	"strconv"
	"strings"
)

// decimalCents 由 readProduct/单测调用：DECIMAL(10,2) 的文本转为整数分。
// 不经过 float64；99999999.99 与 0.10 的十进制含义不承受二进制浮点舍入。
// 本课金额非负且恰好两位小数；这是业务边界，不声称适用于所有 DECIMAL schema。
func decimalCents(value string) (int64, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || len(parts[0]) < 1 || len(parts[0]) > 8 || len(parts[1]) != 2 {
		return 0, errors.New("金额必须符合非负 DECIMAL(10,2)")
	}
	for _, part := range parts {
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return 0, errors.New("金额含非十进制数字")
			}
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	fraction, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return whole*100 + fraction, nil
}
