package contract

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"
	_ "time/tzdata" // 把 IANA 时区数据放进本课程序，Windows 不依赖外部 zoneinfo 安装。
)

// UTCInstant 表示一个已经解析并统一到 UTC 的时间点，而不是“本地钟表上的几个数字”。
// 嵌入 time.Time 可访问 Before/Equal 等方法，但这里显式覆盖 JSON 方法固定业务契约。
type UTCInstant struct{ time.Time }

var instantPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-](0[0-9]|1[0-9]|2[0-3]):[0-5][0-9])$`)

// ParseInstant 由 JSON 解码和时间实验调用，只接受明确带 Z/数字 offset 的 RFC3339 文本。
// time.Parse 的部分宽松兼容形式不属于本业务契约，所以先检查形状，再验证真实日期。
// 返回 UTC 会改变显示位置，却不会改变输入已表达的那个瞬间。
func ParseInstant(text string) (UTCInstant, error) {
	if !instantPattern.MatchString(text) {
		return UTCInstant{}, fmt.Errorf("时间必须是带 Z/offset 的严格 RFC3339，最多 9 位小数")
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return UTCInstant{}, fmt.Errorf("时间日期/时刻不合法: %w", err)
	}
	utc := parsed.UTC() // UTC 返回值副本；同时去除本就不存在的单调钟部分。
	if utc.Year() < 2000 || utc.Year() >= 2100 {
		return UTCInstant{}, fmt.Errorf("订单时间必须在 [2000,2100) 年范围内")
	}
	return UTCInstant{utc}, nil
}

// UnmarshalJSON 由 Field[UTCInstant] 调用；null 已由外层 Field 留下 Null 标记。
// 直接使用 UTCInstant 解码 null 也会失败，不把“无截止时间”误当成 year 1 零值。
func (instant *UTCInstant) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return fmt.Errorf("时间必须是 JSON 字符串: %w", err)
	}
	next, err := ParseInstant(text)
	if err != nil {
		return err
	}
	*instant = next
	return nil
}

// MarshalJSON 保证 Order 的输出为 UTC RFC3339Nano 文本；位置名称和单调钟都不上线。
// 格式化前校验年范围，防止手工构造的零值漏出本订单协议。
func (instant UTCInstant) MarshalJSON() ([]byte, error) {
	if instant.Year() < 2000 || instant.Year() >= 2100 {
		return nil, fmt.Errorf("不能序列化契约范围外的订单时间")
	}
	return json.Marshal(instant.UTC().Format(time.RFC3339Nano))
}

// Expired 是创建订单与跨时区实验共用的判定，截止时刻本身已经过期。
// Before 比较时间点，不比较格式化字符串；两个 Location 显示不同也可表达同一瞬间。
func Expired(now, expiresAt time.Time) bool {
	return !now.Before(expiresAt)
}

// ParseUnixMillis 为历史数字时间接口建立明确单位与业务范围。
// UnixMilli 本身接受很大的数字；[2000,2100) 检查能拦截“秒数误传成毫秒”。
// 这不是自动猜单位；调用者必须按字段 expires_at_unix_ms 的契约提供毫秒。
func ParseUnixMillis(milliseconds int64) (UTCInstant, error) {
	parsed := time.UnixMilli(milliseconds).UTC()
	if parsed.Year() < 2000 || parsed.Year() >= 2100 {
		return UTCInstant{}, fmt.Errorf("Unix 毫秒不在订单时间范围 [2000,2100)，检查是否误传秒")
	}
	return UTCInstant{parsed}, nil
}
