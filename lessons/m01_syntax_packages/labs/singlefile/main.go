package main

import "fmt"

// validate 是拆包前的最小前身，由同文件 main 调用；和核心实验使用同一订单事实。
// 返回两个值按位置接收。这里只有读取参数和计算，无仓库或外部资源。
func validate(id, status string, quantity uint8, unitCents int64) (int64, error) {
	if id == "" || status != "paid" || quantity == 0 {
		return 0, fmt.Errorf("订单不可出库")
	}
	return int64(quantity) * unitCents, nil
}

// main 是单文件版 caller；错误立即返回，正确结果才输出。
func main() {
	total, err := validate("ORD-100", "paid", 2, 1250)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("[L06 single] id=ORD-100 total_cents=%d\n", total)
}
