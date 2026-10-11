package contract

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"
)

const Version = "order.v1"

// ID 的 Go 底层类型是 string，JSON 也必须是字符串，避免 JavaScript/通用 map 的浮点损失。
// 这里合成的业务 ID 是 1..MaxUint64 的十进制正整数；request_id 则是独立的追踪字符串。
type ID string

// UnmarshalJSON 由创建订单解码调用。它先验证字符串与规范十进制形式再写接收者。
// 使用 ,string tag 能为具体整数加引号，但不能代替“ID 正数/不带前导零”的业务校验。
func (id *ID) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return fmt.Errorf("ID 必须是 JSON 字符串: %w", err)
	}
	number, err := strconv.ParseUint(text, 10, 64)
	if err != nil || number == 0 || strconv.FormatUint(number, 10) != text {
		return fmt.Errorf("ID 必须是 uint64 范围内的规范正整数字符串")
	}
	*id = ID(text)
	return nil
}

// CreateDTO 只承载“客户端原文含义”，不是已经验证的 Order。
// 大写字段可被其他包与 encoding/json 访问；tag 指定线上名字，与 Go 字段名无关。
// Field 保留数量/金额/截止时间的缺失与 null；Tags 必须显式传数组，不能传 null。
type CreateDTO struct {
	ContractVersion string            `json:"contract_version"`
	RequestID       string            `json:"request_id"`
	OrderID         ID                `json:"order_id"`
	UserID          ID                `json:"user_id"`
	SKU             string            `json:"sku"`
	Quantity        Field[int64]      `json:"quantity"`
	Currency        string            `json:"currency"`
	AmountMinor     Field[Minor]      `json:"amount_minor"`
	ExpiresAt       Field[UTCInstant] `json:"expires_at"`
	Note            *string           `json:"note"` // 创建场景约定缺失/null 都表示“尚无备注”。
	Tags            []string          `json:"tags"`
}

// PatchDTO 使用与创建相同的名字，但只开放三项可变字段。
// 不能把创建 DTO 直接复用为 PATCH：缺字段在两个接口中的含义不同。
type PatchDTO struct {
	Quantity Field[int64]    `json:"quantity"`
	Note     Field[string]   `json:"note"`
	Tags     Field[[]string] `json:"tags"`
}

// Order 是已经通过校验的业务值。AmountMinor 是整张订单总额，PATCH 数量需同比重算。
// Note 指针和 Tags slice 在结构体复制时仍可能共享底层对象，ApplyPatch 会显式复制它们。
// 时间对外统一 UTC；本课没有数据库，副作用只是在 demo 持有的订单变量中安装新值。
type Order struct {
	ContractVersion string     `json:"contract_version"`
	RequestID       string     `json:"request_id"`
	OrderID         ID         `json:"order_id"`
	UserID          ID         `json:"user_id"`
	SKU             string     `json:"sku"`
	Quantity        int64      `json:"quantity"`
	Currency        string     `json:"currency"`
	AmountMinor     Minor      `json:"amount_minor"`
	ExpiresAt       UTCInstant `json:"expires_at"`
	Note            *string    `json:"note"` // nil 必须输出 null，不使用 omitempty 藏掉状态。
	Tags            []string   `json:"tags"` // 已知空集合输出 []，而不是缺失/null。
}

var requestPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var skuPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// DecodeCreate 由入口调用，每次创建全新 DTO，失败时返回零值 DTO 并保留错误链。
// allowed 精确列出 contract v1 字段，大小写变体也被边界扫描器拒绝。
func DecodeCreate(raw []byte) (CreateDTO, error) {
	var dto CreateDTO
	err := DecodeStrict(raw, &dto, []string{"contract_version", "request_id", "order_id", "user_id", "sku", "quantity", "currency", "amount_minor", "expires_at", "note", "tags"})
	if err != nil {
		return CreateDTO{}, err
	}
	return dto, nil
}

// BuildOrder 由创建流程在 DecodeCreate 成功后调用；now 由调用方注入，让到期测试可重现。
// 它负责业务约束而不是 JSON 语法，结果成功后才能交给 repository/本课内存变量。
func BuildOrder(dto CreateDTO, now time.Time) (Order, error) {
	if dto.ContractVersion != Version {
		return Order{}, fmt.Errorf("contract_version 必须是 %q", Version)
	}
	if !requestPattern.MatchString(dto.RequestID) || dto.OrderID == "" || dto.UserID == "" || !skuPattern.MatchString(dto.SKU) {
		return Order{}, fmt.Errorf("request_id/order_id/user_id/sku 缺失或格式不符")
	}
	if !dto.Quantity.Present || dto.Quantity.Null || dto.Quantity.Value < 1 || dto.Quantity.Value > 1000 {
		return Order{}, fmt.Errorf("quantity 必须显式为 1..1000 的整数，不能缺失/null/0")
	}
	if !dto.AmountMinor.Present || dto.AmountMinor.Null || dto.AmountMinor.Value <= 0 {
		return Order{}, fmt.Errorf("amount_minor 必须显式为正的最小单位整数字符串")
	}
	money := Money{Currency: dto.Currency, AmountMinor: dto.AmountMinor.Value}
	if err := money.Validate(); err != nil {
		return Order{}, err
	}
	if !dto.ExpiresAt.Present || dto.ExpiresAt.Null || Expired(now, dto.ExpiresAt.Value.Time) {
		return Order{}, fmt.Errorf("expires_at 必须显式给出未来时刻")
	}
	if dto.Tags == nil {
		return Order{}, fmt.Errorf("tags 必须显式给出非 null 数组；没有标签请传 []")
	}
	if err := validateTags(dto.Tags); err != nil {
		return Order{}, err
	}
	return Order{
		ContractVersion: Version, RequestID: dto.RequestID, OrderID: dto.OrderID,
		UserID: dto.UserID, SKU: dto.SKU, Quantity: dto.Quantity.Value,
		Currency: money.Currency, AmountMinor: money.AmountMinor,
		ExpiresAt: dto.ExpiresAt.Value, Note: cloneNote(dto.Note), Tags: slices.Clone(dto.Tags),
	}, nil
}

// DecodePatch 和 DecodeCreate 一样为本次请求分配新 DTO，不复用上一请求的 Present 标记。
func DecodePatch(raw []byte) (PatchDTO, error) {
	var dto PatchDTO
	if err := DecodeStrict(raw, &dto, []string{"quantity", "note", "tags"}); err != nil {
		return PatchDTO{}, err
	}
	return dto, nil
}

// ApplyPatch 由演示入口调用，existing 是原订单值副本，unitPrice 是服务端可信的 SKU 单价。
// 客户端不能通过 PATCH 自己修改金额；数量变化后服务端用整型单价重新计算总额。
// 所有校验/计算完成后才返回新 Order；失败返回错误，调用方不安装新值，原订单不变。
func ApplyPatch(existing Order, patch PatchDTO, unitPrice Money) (Order, error) {
	updated := existing
	updated.Tags = slices.Clone(existing.Tags)
	updated.Note = cloneNote(existing.Note)
	if patch.Quantity.Present {
		if patch.Quantity.Null || patch.Quantity.Value < 1 || patch.Quantity.Value > 1000 {
			return Order{}, fmt.Errorf("PATCH quantity 不能是 null/0，范围为 1..1000")
		}
		if unitPrice.Currency != existing.Currency {
			return Order{}, fmt.Errorf("SKU 单价币种与订单币种不一致")
		}
		total, err := Multiply(unitPrice, patch.Quantity.Value)
		if err != nil {
			return Order{}, fmt.Errorf("重算订单总额: %w", err)
		}
		updated.Quantity, updated.AmountMinor = patch.Quantity.Value, total.AmountMinor
	}
	if patch.Note.Present {
		if patch.Note.Null {
			updated.Note = nil // 本契约约定显式 null 清除备注。
		} else {
			note := patch.Note.Value // 空字符串也是真实值，不用 != "" 判断“是否提供”。
			updated.Note = &note
		}
	}
	if patch.Tags.Present {
		if patch.Tags.Null {
			return Order{}, fmt.Errorf("PATCH tags 不能是 null；清空请传 []")
		}
		if err := validateTags(patch.Tags.Value); err != nil {
			return Order{}, err
		}
		updated.Tags = slices.Clone(patch.Tags.Value)
	}
	return updated, nil
}

// validateTags 由创建和更新共用；[] 是合法空集合，元素则是有界、非空的 SKU 风格文本。
func validateTags(tags []string) error {
	if len(tags) > 8 {
		return fmt.Errorf("tags 最多 8 个")
	}
	for _, tag := range tags {
		if !skuPattern.MatchString(tag) {
			return fmt.Errorf("非法 tag %q", tag)
		}
	}
	return nil
}

// cloneNote 把可选字符串的值复制到新地址，避免返回的订单与原订单共享可写指针。
// Go 可安全返回局部变量地址；它的存活期由逃逸分析和 GC 管理，不会变成悬空地址。
func cloneNote(note *string) *string {
	if note == nil {
		return nil
	}
	copy := *note
	return &copy
}
