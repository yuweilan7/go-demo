package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go-demo/lessons/m08_json_money_time/internal/contract"
)

const createJSON = `{"contract_version":"order.v1","request_id":"req-m08-001","order_id":"9007199254740993","user_id":"42","sku":"SKU-M08","quantity":1,"currency":"CNY","amount_minor":"299","expires_at":"2026-10-12T10:00:00+08:00","note":"gift","tags":["new"]}`

// jsonDemo 由 run 调用，先观察 v1 信息丢失，再沿真实“解码→校验→安装值”路径修复。
// raw/input 是不可变的合成字符串；DTO 每次单独创建，不能复用上一请求的字段状态。
func jsonDemo() error {
	fmt.Println("\n[E01-A] v1 零值、null 与缺失")
	type plain struct {
		Quantity int64    `json:"quantity"`
		Note     *string  `json:"note"`
		Tags     []string `json:"tags"`
	}
	for _, raw := range []string{`{}`, `{"quantity":null,"tags":null}`, `{"quantity":0,"tags":[]}`} {
		var value plain // :=/var 在本次循环体中声明，下一输入不复用这一对象。
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return err
		}
		fmt.Printf("raw=%s -> quantity=%d note_nil=%t tags_nil=%t len=%d\n", raw, value.Quantity, value.Note == nil, value.Tags == nil, len(value.Tags))
	}
	reused := plain{Quantity: 7}
	if err := json.Unmarshal([]byte(`{"quantity":null}`), &reused); err != nil {
		return err
	}
	fmt.Printf("已有 quantity=7 再解 null -> quantity=%d（v1 标量 null 不改原值）\n", reused.Quantity)
	for _, raw := range []string{`{}`, `{"quantity":null}`, `{"quantity":0}`} {
		var optional struct {
			Quantity *int64 `json:"quantity"`
		}
		if err := json.Unmarshal([]byte(raw), &optional); err != nil {
			return err
		}
		if optional.Quantity == nil {
			fmt.Printf("普通 *int64 raw=%s -> nil\n", raw)
		} else {
			fmt.Printf("普通 *int64 raw=%s -> 非nil，*quantity=%d\n", raw, *optional.Quantity)
		}
	}

	fmt.Println("\n[E01-B] 导出字段、tag 与 omitempty 只控制输出")
	type view struct {
		Quantity     int64    `json:"quantity,omitempty"`
		Tags         []string `json:"tags,omitempty"`
		Note         *string  `json:"note,omitempty"`
		ServerSecret string   `json:"-"`
		internal     string   // 小写字段不导出，encoding/json 看不到，不能用 tag 把它变导出。
	}
	emptyText := ""
	encoded, err := json.Marshal(view{Note: &emptyText, Tags: []string{}, ServerSecret: "synthetic", internal: "private"})
	if err != nil {
		return err
	}
	fmt.Printf("v1 omitempty（零数量/空tags省略，非nil空备注保留）=%s\n", encoded)
	for _, tags := range [][]string{nil, {}} {
		encoded, err := json.Marshal(struct {
			Tags []string `json:"tags"`
		}{Tags: tags})
		if err != nil {
			return err
		}
		fmt.Printf("不省略 tags_nil=%t -> %s\n", tags == nil, encoded)
	}

	fmt.Println("\n[E01-C] 默认宽松行为与契约拒绝")
	for _, raw := range []string{`{"quantity":1,"quantity":9}`, `{"quantity":1,"quanTity":9}`, `{"quantitty":3}`, `{"quantity":1} {"quantity":9}`, `{"quantity":1} trailing`} {
		var unsafe plain
		// NewDecoder 模拟请求 body 流；故意只调用一次 Decode，观察第二段数据未被检查。
		decoder := json.NewDecoder(strings.NewReader(raw))
		unsafeErr := decoder.Decode(&unsafe)
		_, strictErr := contract.DecodePatch([]byte(raw))
		fmt.Printf("raw=%s\n  单次v1 Decode quantity=%d err=%v\n  契约DecodePatch err=%v\n", raw, unsafe.Quantity, unsafeErr, strictErr)
	}
	dto, err := contract.DecodeCreate([]byte(createJSON))
	if err != nil {
		return err
	}
	now := time.Date(2026, 10, 12, 1, 59, 0, 0, time.UTC)
	order, err := contract.BuildOrder(dto, now)
	if err != nil {
		return err
	}
	encoded, err = json.Marshal(order)
	if err != nil {
		return err
	}
	fmt.Printf("创建通过 -> %s\n", encoded)
	invalidJSON := strings.Replace(createJSON, `"quantity":1`, `"quantity":0`, 1)
	invalidDTO, decodeErr := contract.DecodeCreate([]byte(invalidJSON))
	_, businessErr := contract.BuildOrder(invalidDTO, now)
	fmt.Printf("quantity=0 -> 解码err=%v；业务校验err=%v\n", decodeErr, businessErr)

	fmt.Println("\n[E01-D] PATCH 三态与原订单副作用")
	unitPrice := contract.Money{Currency: "CNY", AmountMinor: 299} // 服务端可信 SKU 单价，不从 PATCH 接收。
	for _, raw := range []string{`{}`, `{"quantity":null}`, `{"quantity":0}`, `{"quantity":2}`} {
		patch, err := contract.DecodePatch([]byte(raw))
		if err != nil {
			return err
		}
		candidate, applyErr := contract.ApplyPatch(order, patch, unitPrice)
		fmt.Printf("raw=%s -> Present=%t Null=%t Value=%d apply_err=%v", raw, patch.Quantity.Present, patch.Quantity.Null, patch.Quantity.Value, applyErr)
		if applyErr == nil {
			// 只有此分支可把 candidate 安装到持有订单的变量/真正 repository。
			// 这里打印候选值，下一输入仍以同一原订单测试，不让反例互相污染。
			fmt.Printf(" candidate_quantity=%d candidate_amount=%d", candidate.Quantity, candidate.AmountMinor)
		}
		fmt.Printf(" original_quantity=%d original_amount=%d\n", order.Quantity, order.AmountMinor)
	}
	for _, raw := range []string{`{}`, `{"note":null}`, `{"note":""}`, `{"tags":null}`, `{"tags":[]}`} {
		patch, err := contract.DecodePatch([]byte(raw))
		if err != nil {
			return err
		}
		candidate, applyErr := contract.ApplyPatch(order, patch, unitPrice)
		if applyErr != nil {
			fmt.Printf("raw=%s -> 拒绝=%v\n", raw, applyErr)
			continue
		}
		encoded, err := json.Marshal(candidate)
		if err != nil {
			return err
		}
		fmt.Printf("raw=%s -> %s\n", raw, encoded)
	}
	// 模拟业务 owner 的“成功才安装”边界，不冒充数据库 Commit。
	// 第一条请求计算成功后更新 state；第二条失败时只读到错误，不能覆盖已持有订单。
	state := order
	for _, raw := range []string{`{"quantity":2}`, `{"quantity":0}`} {
		patch, err := contract.DecodePatch([]byte(raw))
		if err != nil {
			return err
		}
		candidate, applyErr := contract.ApplyPatch(state, patch, unitPrice)
		if applyErr == nil {
			state = candidate
		}
		fmt.Printf("内存owner安装 raw=%s err=%v final_quantity=%d final_amount=%d\n", raw, applyErr, state.Quantity, state.AmountMinor)
	}
	return nil
}
