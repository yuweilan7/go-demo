package orderbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache借用Redis池。known和精确账本保护不同资源：known防止本进程重复预检，
// registry在副作用前持久登记；Cache本身不Close，最终owner是Resources。
type Cache struct {
	client *redis.Client
	reg    *registry
	prefix string
	mu     sync.Mutex
	known  map[string]bool
}

// keys带认证owner、orderID和schema；query owner来自网络身份，不能从body获得。
// 首次可能写的键必须EXISTS=0后登记，碰撞拒绝，不能把未知已有键当自己的清理对象。
func (c *Cache) keys(ctx context.Context, owner, id string) (generation, data string, err error) {
	if (owner != "alice" && owner != "bob") || !idPattern.MatchString(id) {
		return "", "", ErrInvalid
	}
	base := c.prefix + "owner=" + owner + ":order=" + id + ":schema=1"
	generation, data = base+":generation", base+":data"
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range []string{generation, data} {
		if !c.known[key] {
			n, e := c.client.Exists(ctx, key).Result()
			if e != nil {
				return "", "", e
			}
			if n != 0 {
				return "", "", ErrOwnership
			}
			if e = c.reg.add("key", key); e != nil {
				return "", "", e
			}
			c.known[key] = true
		}
	}
	return
}

// Lua把代际比较与SET放在同一Redis命令，消除GET generation后再SET的竞态。
// 300s代际TTL大于本实验120s寿命；不是无限期ABA保护，生产须另定生命周期策略。
const fillScript = `local v=redis.call('GET',KEYS[1]) or '0'
if v~=ARGV[1] then return 0 end
redis.call('SET',KEYS[2],ARGV[2],'PX',30000);return 1`
const invalidateScript = `redis.call('INCR',KEYS[1]);redis.call('PEXPIRE',KEYS[1],300000)
redis.call('DEL',KEYS[2]);return redis.call('GET',KEYS[1])`

// Query由Service/GET调用，先实际GET，miss后SQL读取；AfterRead在SQL后/填充前观察。
// 失效前读到的旧快照可以返回给这个请求，但Lua拒绝它污染下一请求的缓存。
// Redis错误明确返回503，本课不新增无限回源/重试机制。SQL与Redis并非原子事务。
func (c *Cache) Query(ctx context.Context, s *Store, owner, id string, h Hooks) (v View, hit bool, err error) {
	gk, dk, err := c.keys(ctx, owner, id)
	if err != nil {
		return v, false, err
	}
	raw, e := c.client.Get(ctx, dk).Bytes()
	if e == nil {
		if len(raw) > 4096 || json.Unmarshal(raw, &v) != nil || v.Owner != owner || v.ID != id {
			return View{}, false, ErrConflict
		}
		fmt.Fprintf(s.Out, "CACHE_HIT owner=%s order=%s delivery=%s\n", owner, id, v.DeliveryStatus)
		return v, true, nil
	}
	if !errors.Is(e, redis.Nil) {
		return v, false, e
	}
	gen, e := c.client.Get(ctx, gk).Result()
	if errors.Is(e, redis.Nil) {
		gen = "0"
	} else if e != nil {
		return v, false, e
	}
	if v, err = s.Read(ctx, owner, id); err != nil {
		return v, false, err
	}
	if h.AfterRead != nil {
		if err = h.AfterRead(ctx, v); err != nil {
			return v, false, err
		}
	}
	b, e := json.Marshal(v)
	if e != nil {
		return v, false, e
	}
	if len(b) > 4096 {
		return v, false, ErrInvalid
	}
	n, e := c.client.Eval(ctx, fillScript, []string{gk, dk}, gen, b).Int()
	fmt.Fprintf(s.Out, "CACHE_FILL owner=%s order=%s delivery=%s generation=%s accepted=%t\n", owner, id, v.DeliveryStatus, gen, n == 1)
	return v, false, e
}

// Invalidate只在SQL效果Commit后调用；duplicate也调用它，以修复上次效果成功但
// 失效失败的窗口。失效成功之后才允许offset确认，失败保持原进度等待恢复。
func (c *Cache) Invalidate(ctx context.Context, owner, id string) error {
	gk, dk, e := c.keys(ctx, owner, id)
	if e != nil {
		return e
	}
	_, e = c.client.Eval(ctx, invalidateScript, []string{gk, dk}).Result()
	return e
}

// Inspect是只读实验接口，读取实际键/TTL/代际；不自动创建键或写缓存。
func (c *Cache) Inspect(ctx context.Context, owner, id string) (exists int64, generation string, ttl int64, err error) {
	gk, dk, e := c.keys(ctx, owner, id)
	if e != nil {
		return 0, "", 0, e
	}
	exists, e = c.client.Exists(ctx, dk).Result()
	if e != nil {
		return 0, "", 0, e
	}
	generation, e = c.client.Get(ctx, gk).Result()
	if errors.Is(e, redis.Nil) {
		generation = "0"
	} else if e != nil {
		return 0, "", 0, e
	}
	t, e := c.client.PTTL(ctx, dk).Result()
	return exists, generation, t.Milliseconds(), e
}

// UnsafeFillForLesson仅供E03真实反例：忽略代际，向自己的登记键SET旧快照。
// 正常Service/Query/consumer绝不调用它。它与修复共用同一View/key/Redis，避免用
// 假缓存解释“旧请求晚回填”。在复用业务的M38中也不能把这个观察入口当业务写法。
func (c *Cache) UnsafeFillForLesson(ctx context.Context, v View) error {
	_, key, e := c.keys(ctx, v.Owner, v.ID)
	if e != nil {
		return e
	}
	payload, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return c.client.Set(ctx, key, payload, 30*time.Second).Err()
}
