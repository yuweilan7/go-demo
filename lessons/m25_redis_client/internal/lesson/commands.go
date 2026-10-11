package lesson

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// cachedOrder 是合成缓存 DTO；这里没有订单数据库，所以不能据它宣称订单已提交。
// json tag 指定协议字段名，OrderID/State 是本次解码获得的值，不与 Redis 字节串共享内存。
type cachedOrder struct {
	OrderID string `json:"order_id"`
	State   string `json:"state"`
}

// readOrder 由 basic 调用：GET 负责 Redis 协议；Unmarshal 负责应用数据格式。
// bool 只表达“缓存是否存在”，error 表达通信/权限/类型/格式异常；miss 不是假订单。
func readOrder(ctx context.Context, client *redis.Client, key string) (cachedOrder, bool, error) {
	// Get 返回 *StringCmd。普通 client 会在 Get 内同步发送并读回复；Result 取已保存的值/error。
	// Val() 单取零值会吞掉 redis.Nil、WRONGTYPE 与 timeout，业务分支不能这么写。
	raw, err := client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return cachedOrder{}, false, nil // Redis 的 nil bulk reply：miss，可以回源，但本课不伪造回源。
	}
	if err != nil {
		return cachedOrder{}, false, fmt.Errorf("读取订单缓存：%w", err)
	}
	var order cachedOrder
	if err := json.Unmarshal([]byte(raw), &order); err != nil {
		return cachedOrder{}, true, fmt.Errorf("已有缓存 JSON 无法解码：%w", err)
	}
	if order.OrderID == "" || order.State == "" {
		return cachedOrder{}, true, errors.New("已有缓存缺业务必填字段")
	}
	return order, true, nil
}

// basic 是 core 的第一步。caller Run 传入总 ctx，共用 l.client；本函数不拥有/关闭 client。
// 正常键都在写入时带 90s TTL；故意遗漏 TTL 的短窗口观察后立即修复，最终仍由 Run 精确删除。
func (l *lab) basic(ctx context.Context) error {
	orderKey, missingKey := l.key("order"), l.key("missing")
	if err := l.client.Set(ctx, orderKey, `{"order_id":"synthetic-25","state":"cached_only"}`, 90*time.Second).Err(); err != nil {
		return err
	}
	order, found, err := readOrder(ctx, l.client, orderKey)
	if err != nil {
		return err
	}
	if err := l.check("get_set_json", found && order.OrderID == "synthetic-25" && order.State == "cached_only", fmt.Sprintf("found=%t order=%+v", found, order)); err != nil {
		return err
	}
	_, found, err = readOrder(ctx, l.client, missingKey)
	if err := l.check("redis_nil_is_miss", err == nil && !found, fmt.Sprintf("found=%t error=%v", found, err)); err != nil {
		return err
	}
	badJSON := l.key("bad-json")
	if err := l.client.Set(ctx, badJSON, "{broken-json", 90*time.Second).Err(); err != nil {
		return err
	}
	_, found, err = readOrder(ctx, l.client, badJSON)
	var syntaxErr *json.SyntaxError
	// errors.As 沿 %w 错误链找到动态类型：格式错误不是 redis.Nil，更不能当 miss 静默回源。
	if err := l.check("json_error_is_not_miss", found && errors.As(err, &syntaxErr) && !errors.Is(err, redis.Nil), "found=true SyntaxError=true miss=false"); err != nil {
		return err
	}
	if err := l.client.Set(ctx, badJSON, `{"order_id":"repaired-25","state":"cache"}`, 90*time.Second).Err(); err != nil {
		return err
	}
	if _, found, err = readOrder(ctx, l.client, badJSON); err != nil || !found {
		return fmt.Errorf("JSON 修复后仍不能读：%w", err)
	}
	hashKey := l.key("order-hash")
	added, err := l.client.HSet(ctx, hashKey, "order_id", "hash-25", "state", "pending").Result()
	if err != nil {
		return err
	}
	if err := l.client.Expire(ctx, hashKey, 90*time.Second).Err(); err != nil {
		return err
	}
	state, err := l.client.HGet(ctx, hashKey, "state").Result()
	if err != nil {
		return err
	}
	fields, err := l.client.HGetAll(ctx, hashKey).Result()
	if err != nil {
		return err
	}
	if err := l.check("hash_fields", added == 2 && state == "pending" && fields["order_id"] == "hash-25" && len(fields) == 2, fmt.Sprintf("added=%d fields=%v", added, fields)); err != nil {
		return err
	}
	wrongType := l.client.Get(ctx, hashKey).Err()
	if err := l.check("wrongtype_is_server_error", redis.HasErrorPrefix(wrongType, "WRONGTYPE") && !errors.Is(wrongType, redis.Nil), fmt.Sprintf("%v", wrongType)); err != nil {
		return err
	}
	// 修复不是重试同一个 GET，而是选择契合 hash 类型的 HGET；数据格式属于业务契约。
	if state, err = l.client.HGet(ctx, hashKey, "state").Result(); err != nil || state != "pending" {
		return fmt.Errorf("WRONGTYPE 修复：state=%s error=%v", state, err)
	}
	noTTL := l.key("forgot-ttl")
	if err := l.client.Set(ctx, noTTL, "forgot", 0).Err(); err != nil { // 0 是无过期，不是立即到期。
		return err
	}
	ttl, err := l.client.TTL(ctx, noTTL).Result()
	if err != nil {
		return err
	}
	// 本版 DurationCmd 把 Redis -1/-2 保存为 duration(-1)/duration(-2)，打印为 -1ns/-2ns。
	// 这两个是状态哨兵；不能比较 -1*time.Second，也不能拿它们做真实剩余时长运算。
	if err := l.check("forgot_ttl", ttl == time.Duration(-1), fmt.Sprintf("TTL=%v (sentinel -1)", ttl)); err != nil {
		return err
	}
	if err := l.client.Expire(ctx, noTTL, 90*time.Second).Err(); err != nil {
		return err
	}
	ttl, err = l.client.TTL(ctx, noTTL).Result()
	if err != nil {
		return err
	}
	if err := l.check("expire_repairs_omission", ttl > 0 && ttl <= 90*time.Second, fmt.Sprintf("TTL=%v", ttl)); err != nil {
		return err
	}
	// 普通 SET 覆盖已有值会移除旧 TTL。缓存刷新时必须再次传 TTL 或明确用 redis.KeepTTL。
	if err := l.client.Set(ctx, noTTL, "overwrite", 0).Err(); err != nil {
		return err
	}
	ttl, err = l.client.TTL(ctx, noTTL).Result()
	if err != nil {
		return err
	}
	if err := l.check("set_overwrite_drops_ttl", ttl == time.Duration(-1), fmt.Sprintf("TTL=%v", ttl)); err != nil {
		return err
	}
	if err := l.client.Set(ctx, noTTL, "refresh-with-ttl", 90*time.Second).Err(); err != nil {
		return err
	}
	if err := l.client.Set(ctx, noTTL, "refresh-keep-ttl", redis.KeepTTL).Err(); err != nil {
		return err
	}
	pttl, err := l.client.PTTL(ctx, noTTL).Result()
	if err != nil {
		return err
	}
	if err := l.check("set_keep_ttl", pttl > 0 && pttl <= 90*time.Second, fmt.Sprintf("PTTL=%v", pttl)); err != nil {
		return err
	}
	unitKey := l.key("wrong-unit")
	// Duration 的底层单位是纳秒。接口若给数字30，time.Duration(30)只是30ns，不是30s。
	// 实库对照用误选的30ms与目标30s；不依赖纳秒到Redis毫秒的驱动最小值取整来猜时间。
	unitless := time.Duration(30)
	if err := l.check("duration_units", unitless == 30*time.Nanosecond && unitless != 30*time.Second, fmt.Sprintf("Duration(30)=%v intended=%v", unitless, 30*time.Second)); err != nil {
		return err
	}
	if err := l.client.Set(ctx, unitKey, "too-short", 30*time.Millisecond).Err(); err != nil {
		return err
	}
	pttl, err = l.client.PTTL(ctx, unitKey).Result()
	if err != nil {
		return err
	}
	if err := l.check("millisecond_ttl_mistake", (pttl >= 0 && pttl <= 30*time.Millisecond) || pttl == time.Duration(-2), fmt.Sprintf("PTTL=%v intended=30s", pttl)); err != nil {
		return err
	}
	if err := waitMissing(ctx, l.client, unitKey); err != nil {
		return err
	}
	ttl, err = l.client.TTL(ctx, unitKey).Result()
	if err != nil {
		return err
	}
	if err := l.check("actual_expiry", ttl == time.Duration(-2), fmt.Sprintf("GET=redis.Nil TTL=%v (sentinel -2)", ttl)); err != nil {
		return err
	}
	if err := l.client.Set(ctx, unitKey, "fixed-unit", 30*time.Second).Err(); err != nil {
		return err
	}
	pttl, err = l.client.PTTL(ctx, unitKey).Result()
	if err != nil {
		return err
	}
	return l.check("unit_repair", pttl > time.Second && pttl <= 30*time.Second, fmt.Sprintf("PTTL=%v", pttl))
}

// waitMissing 由 basic 调用，最多等3秒并以真实 GET redis.Nil 作为到期证明。
// 10ms ticker 只是再次观察的间隔，不充当并发就绪屏障；Redis TTL 自身才是被观察事件。
func waitMissing(parent context.Context, client *redis.Client, key string) error {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := client.Get(ctx, key).Result()
		if errors.Is(err, redis.Nil) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("未在预算内观察到过期：%w", ctx.Err())
		}
	}
}
