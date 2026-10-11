// Package quote 是 app 的直接依赖，声明 rules 的最低要求为 v1.1.0。
package quote

import "example.com/m01/rules"

// Describe 由 app/consumer 调用，返回规则实现与订单 ID，让间接依赖真实参与运行。
func Describe(id string) string {
	return id + " quote via " + rules.Describe()
}
