package cachecourse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const maxDTOBytes = 4096

var ErrLargeDTO = errors.New("拒绝大key：DTO超过4096字节")
var ErrVersionChanged = errors.New("版本查找与数据读取之间已变化，拒绝填错版本键")

// registry 保存完整精确键名，由Run唯一清理。key首次触碰前调用register，锁保护并发
// fill。map只是本进程账本，不是Redis命名空间或跨进程锁，不能用SCAN猜删除范围。
type registry struct {
	mu     sync.Mutex
	keys   map[string]bool
	prefix string
}

// register由key构造/preflight调用，只给账本加入字符串值；r指向同一个互斥保护map。
func (r *registry) register(key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[key] = true
	return key
}

// list返回键名slice副本，Run拿它逐名DEL/EXISTS；worker不会借用或修改这个数组。
func (r *registry) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.keys))
	for k := range r.keys {
		out = append(out, k)
	}
	return out
}

// key由业务scope及实验场景构造。QueryEscape避免冒号/分隔符输入碰撞；租户、用户、
// schema不可只选其中一个。suffix可追加权威业务version或代际字段，但不会代替鉴权。
func (r *registry) key(scene string, id scope, suffix string) string {
	return r.register(fmt.Sprintf("%s%s:t=%s:u=%s:s=%d%s", r.prefix, scene, url.QueryEscape(id.Tenant), url.QueryEscape(id.User), id.Schema, suffix))
}

// cache 借用client和registry，不拥有Close。Get把真实redis.Nil与损坏JSON分开，
// 正常cache-aside只对miss回源；AUTH等故障需由上层显式选择有预算的降级。
type cache struct {
	client *redis.Client
	keys   *registry
}

// get由aside/断言调用，同步等待真实GET再解码；hit=true+error是存在但损坏，不能当miss。
func (c cache) get(ctx context.Context, key string) (DTO, bool, error) {
	raw, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return DTO{}, false, nil
	}
	if err != nil {
		return DTO{}, false, fmt.Errorf("缓存GET：%w", err)
	}
	if len(raw) > maxDTOBytes {
		return DTO{}, true, errors.New("缓存DTO超过4096字节")
	}
	var d DTO
	if err := json.Unmarshal(raw, &d); err != nil {
		return DTO{}, true, fmt.Errorf("缓存JSON损坏：%w", err)
	}
	return d, true, nil
}

// put 由miss或实验调用，JSON字节属于本次请求；超过4KiB拒绝缓存，调用者仍可从
// MySQL得到正确业务数据。这里选择bypass大DTO，既没有无限保存，也没有截断JSON。
func (c cache) put(ctx context.Context, key string, d DTO, ttl time.Duration) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if len(b) > maxDTOBytes {
		return ErrLargeDTO
	}
	return c.client.Set(ctx, key, b, ttl).Err()
}

// aside是最小cache-aside路径：GET命中直接返回，miss→真实SELECT→SET。
// 它没有跨MySQL/Redis事务；旧SELECT可以在新写DELETE之后再SET，E02专门证明。
func (c cache) aside(ctx context.Context, s *source, id scope, key string, observed, hold *gate) (DTO, error) {
	d, hit, err := c.get(ctx, key)
	if err != nil {
		return DTO{}, err
	}
	if hit {
		return d, nil
	}
	d, err = s.Read(ctx, id, observed, hold)
	if err != nil {
		return DTO{}, err
	}
	ttl := jitter(key)
	if !d.Found {
		ttl = 2 * time.Second
	}
	if err = c.put(ctx, key, d, ttl); errors.Is(err, ErrLargeDTO) {
		// profile业务允许大DTO直接来自SQL，绕过缓存；下次仍可能回源，所以预算仍必需。
		return d, nil
	} else if err != nil {
		return DTO{}, err
	}
	return d, nil
}

// versioned由E02当前版本reader调用。expected来自真实currentVersion查询；另一次
// Read可能已见更高版本，必须拒绝向expected地址填它，交caller决定有限重读/失败。
func (c cache) versioned(ctx context.Context, s *source, id scope, expected int, key string) (DTO, error) {
	d, hit, err := c.get(ctx, key)
	if err != nil {
		return DTO{}, err
	}
	if !hit {
		d, err = s.Read(ctx, id, nil, nil)
		if err != nil {
			return DTO{}, err
		}
	}
	if !d.Found || d.Version != expected {
		return DTO{}, ErrVersionChanged
	}
	if !hit {
		err = c.put(ctx, key, d, jitter(key))
	}
	return d, err
}

// jitter由key稳定映射到25..35秒。真实SET的PTTL证明期限分散；同key所有进程一致
// 不是随机概率试验，更不保证热点永不过期。本课需要可重复结果，生产应按策略选熵。
func jitter(key string) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return time.Duration(25+h.Sum32()%11) * time.Second
}

// fenceFill在Redis单条短Lua中“比较代际→写入”，消除比较与SET之间再插入invalidate。
// reader先GET generation（不存在=0），writer在MySQL已Commit后INCR generation并DEL。
// 这只拒绝已观察到的失效之前的旧填充；它不把两套服务拼成强一致事务。
const fenceFill = `local current=redis.call('GET',KEYS[1]) or '0'
if current~=ARGV[1] then return 0 end
redis.call('SET',KEYS[2],ARGV[2],'PX',ARGV[3]); return 1`
const fenceInvalidate = `redis.call('INCR',KEYS[1]); redis.call('PEXPIRE',KEYS[1],90000)
redis.call('DEL',KEYS[2]); return redis.call('GET',KEYS[1])`

// generation由reader在SQL前调用，返回的是字符串值副本，不是可变共享计数器。
func (c cache) generation(ctx context.Context, key string) (string, error) {
	v, err := c.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "0", nil
	}
	return v, err
}

// fillFence只在本课注册的两个精确键上Eval，带有限TTL和有限JSON，不运行长脚本。
// 返回1才表示填充被允许；0是发现代际已变，caller保留旧请求快照但不污染下一请求。
func (c cache) fillFence(ctx context.Context, generationKey, dataKey, generation string, d DTO) (bool, error) {
	b, err := json.Marshal(d)
	if err != nil {
		return false, err
	}
	if len(b) > maxDTOBytes {
		return false, errors.New("fence DTO过大")
	}
	n, err := c.client.Eval(ctx, fenceFill, []string{generationKey, dataKey}, generation, b, strconv.FormatInt(jitter(dataKey).Milliseconds(), 10)).Int()
	return n == 1, err
}
