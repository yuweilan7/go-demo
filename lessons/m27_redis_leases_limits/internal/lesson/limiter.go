package lesson

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// windowLua实现“首个允许请求开始一段固定窗口”，不用客户端wall clock生成窗口。
// 第一次SET同时PX，后续只给允许请求INCR，拒绝不计数/不延长TTL。
// TIME只返回可解释的服务端观察时间，不参与授权；过期仍由Redis时钟管理。
// 所有校验在写之前；Lua原子执行不是遇错回滚，不能先写再凭error假定没副作用。
const windowLua = `local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local raw = redis.call('GET', KEYS[1])
local count = tonumber(raw or '0')
if not count or count < 0 then return redis.error_reply('invalid counter') end
if raw and redis.call('PTTL', KEYS[1]) < 0 then return redis.error_reply('counter missing TTL') end
local allowed = 0
if count < limit then
  if not raw then
    redis.call('SET', KEYS[1], 1, 'PX', window)
    count = 1
  else
    count = redis.call('INCR', KEYS[1])
  end
  allowed = 1
end
local clock = redis.call('TIME')
local now = tonumber(clock[1])*1000 + math.floor(tonumber(clock[2])/1000)
return {allowed, count, redis.call('PTTL', KEYS[1]), now}`

// decision区分业务拒绝、降级允许与基础设施error，caller可分别计数/返回429/503。
// TTL是本次真实回复的剩余窗口，不能拿它当精确毫秒调度保证或全局并发容量。
type decision struct {
	Allowed, Degraded              bool
	Count, TTLMillis, ServerMillis int64
	Cause                          error
}

// limitKey由owner生成并登记；用户身份/规范化接口共同隔离配额。
// 长度编码避免user+route拼接歧义，hash避免任意输入造成冒号层级/超长键。
func (l *lab) limitKey(user, route string) string {
	identity := fmt.Sprintf("%d:%s%d:%s", len(user), user, len(route), route)
	digest := sha256.Sum256([]byte(identity))
	return l.key(fmt.Sprintf("limit:%x", digest))
}

// allow执行真实Lua并解码RESP整数数组；返回false,nil才是正常额度拒绝。
// 任何error都不能伪装成配额用完，是否降级由调用者的业务策略显式决定。
func allow(ctx context.Context, client *redis.Client, key string, maximum int64, window time.Duration) (decision, error) {
	if maximum < 1 || maximum > 100 || window < time.Millisecond || window > 30*time.Second {
		return decision{}, errors.New("课程限流limit/window越界")
	}
	values, err := client.Eval(ctx, windowLua, []string{key}, maximum, window.Milliseconds()).Int64Slice()
	if err != nil {
		return decision{}, err
	}
	if len(values) != 4 || (values[0] != 0 && values[0] != 1) || values[1] < 1 || values[1] > maximum || values[2] < 0 {
		return decision{}, errors.New("Lua限流回复违反契约")
	}
	return decision{Allowed: values[0] == 1, Count: values[1], TTLMillis: values[2], ServerMillis: values[3]}, nil
}

// withPolicy把基础设施故障映射到声明过的业务语义：critical关闭准入，optional
// 允许降级并保留Cause。可选读路径的fail-open不代表下单也允许绕开限制。
func withPolicy(ctx context.Context, client *redis.Client, key string, critical bool) decision {
	result, err := allow(ctx, client, key, 3, 3*time.Second)
	if err == nil {
		return result
	}
	return decision{Allowed: !critical, Degraded: true, Cause: err}
}

// rateLimits启动12个有限worker在同一start门等待，Lua实际保证一个窗口仅3个允许。
// owner先登记所有键、再启动任务，workers不写report/out/registry；结果以Finished收集。
func (l *lab) rateLimits(ctx context.Context) (err error) {
	key := l.limitKey("alice", "POST /orders")
	g := newGroup(ctx, l.report)
	start := make(chan struct{})
	open := onceRelease(start)
	g.releases = []func(){open}
	defer g.finish(&err)
	for i := 0; i < 12; i++ {
		g.start(fmt.Sprintf("limit request %d", i), func(workCtx context.Context) outcome {
			if err := await(workCtx, start, "并发限流启动门"); err != nil {
				return outcome{Err: err}
			}
			result, err := allow(workCtx, l.a, key, 3, 3*time.Second)
			if err != nil {
				return outcome{Err: err}
			}
			if result.Allowed {
				return outcome{Value: 1}
			}
			return outcome{}
		}, nil)
	}
	open()
	accepted := int64(0)
	for _, t := range g.tasks {
		result, joinErr := g.join(ctx, t)
		if err = errors.Join(result.Err, joinErr); err != nil {
			return err
		}
		accepted += result.Value
	}
	count, err := l.b.Get(ctx, key).Int64()
	if err != nil {
		return err
	}
	ttl, err := l.b.PTTL(ctx, key).Result()
	if err != nil {
		return err
	}
	if err = l.check("concurrent_limit_real_counter", accepted == 3 && count == 3 && ttl > 0 && ttl <= 3*time.Second,
		fmt.Sprintf("requests=12 allowed=%d rejected=%d GET=%d PTTL=%v", accepted, 12-accepted, count, ttl)); err != nil {
		return err
	}
	before := ttl
	rejectedResult, err := allow(ctx, l.b, key, 3, 3*time.Second)
	if err != nil {
		return err
	}
	after, err := l.b.PTTL(ctx, key).Result()
	if err != nil {
		return err
	}
	if err = l.check("reject_not_increment_or_extend", !rejectedResult.Allowed && rejectedResult.Count == 3 && after > 0 && after <= before,
		fmt.Sprintf("allowed=false count=%d TTL_before=%v after=%v", rejectedResult.Count, before, after)); err != nil {
		return err
	}
	for _, identity := range [][2]string{{"bob", "POST /orders"}, {"alice", "GET /catalog"}} {
		isolated := l.limitKey(identity[0], identity[1])
		result, err := allow(ctx, l.b, isolated, 3, 3*time.Second)
		if err != nil {
			return err
		}
		if err = l.check("user_route_isolation", isolated != key && result.Allowed && result.Count == 1,
			fmt.Sprintf("user=%s route=%s count=%d", identity[0], identity[1], result.Count)); err != nil {
			return err
		}
	}
	if err = l.boundaryBurst(ctx); err != nil {
		return err
	}
	// 真client被本课owner关闭后，真实驱动返回ErrClosed；没有破坏服务或制造假Redis。
	// 这验证“本客户端不可用”策略，不声称完成共享服务宕机/跨节点网络分区验收。
	closedClient := redis.NewClient(l.redisOptions())
	if err = closedClient.Close(); err != nil {
		return err
	}
	failureKey := l.limitKey("unavailable", "POST /orders")
	critical := withPolicy(ctx, closedClient, failureKey, true)
	optional := withPolicy(ctx, closedClient, failureKey, false)
	exists, err := l.a.Exists(ctx, failureKey).Result()
	if err != nil {
		return err
	}
	if err = l.check("unavailable_business_policy", !critical.Allowed && optional.Allowed && critical.Degraded && optional.Degraded &&
		errors.Is(critical.Cause, redis.ErrClosed) && errors.Is(optional.Cause, redis.ErrClosed) && exists == 0,
		"critical deny; optional allow+degraded; Cause=ErrClosed; no key written"); err != nil {
		return err
	}
	// 第二种故障通过独占client真实AUTH被拒复现接入不可用；密码只在内存改变。
	// 没改Redis账号/ACL，没有伪造服务端消息，也不把鉴权故障声称成进程宕机。
	badOptions := l.redisOptions()
	badOptions.Password += "-m27-deliberately-invalid"
	badClient := redis.NewClient(badOptions)
	defer func() { err = errors.Join(err, badClient.Close()) }()
	criticalAuth := withPolicy(ctx, badClient, failureKey, true)
	optionalAuth := withPolicy(ctx, badClient, failureKey, false)
	exists, err = l.a.Exists(ctx, failureKey).Result()
	if err != nil {
		return err
	}
	return l.check("real_AUTH_failure_policy", !criticalAuth.Allowed && optionalAuth.Allowed &&
		redis.HasErrorPrefix(criticalAuth.Cause, "WRONGPASS") && redis.HasErrorPrefix(optionalAuth.Cause, "WRONGPASS") && exists == 0,
		"real AUTH rejected; critical deny; optional allow+degraded; no quota key; credentials omitted")
}

// boundaryBurst演示首请求固定窗口边界：开窗请求早先用1额度，窗口末用余下2，
// 过期后新窗口再用3，因此本次5请求集中在小于2秒内仍不违反各窗口3的规则。
// 不是滚动任意2秒最多3。实际PTTL与EXISTS控制边界，不sleep猜到期。
func (l *lab) boundaryBurst(ctx context.Context) error {
	key := l.limitKey("burst", "POST /orders")
	first, err := allow(ctx, l.a, key, 3, 2*time.Second)
	if err != nil || !first.Allowed {
		return errors.Join(err, errors.New("burst开窗失败"))
	}
	if err = observe(ctx, "实际窗口尾部PTTL<=900ms", func() (bool, error) {
		ttl, err := l.b.PTTL(ctx, key).Result()
		if err != nil {
			return false, err
		}
		if ttl <= 0 {
			return false, errors.New("未在窗口尾部观测到有效窗口")
		}
		return ttl <= 900*time.Millisecond, nil
	}); err != nil {
		return err
	}
	var oldStart int64
	for i := 0; i < 2; i++ {
		result, err := allow(ctx, l.a, key, 3, 2*time.Second)
		if err != nil {
			return err
		}
		if !result.Allowed || result.Count != int64(i+2) {
			return errors.New("窗口尾部计数不符合旧窗口")
		}
		if i == 0 {
			oldStart = result.ServerMillis
		}
	}
	if err = l.expired(ctx, key); err != nil {
		return err
	}
	var end int64
	for i := 0; i < 3; i++ {
		result, err := allow(ctx, l.b, key, 3, 2*time.Second)
		if err != nil {
			return err
		}
		if !result.Allowed || result.Count != int64(i+1) {
			return errors.New("新窗口未从1开始或超过限额")
		}
		end = result.ServerMillis
	}
	return l.check("fixed_window_boundary_burst", end-oldStart >= 0 && end-oldStart < 2000,
		fmt.Sprintf("old-tail=2 new-window=3; 5 accepted in Redis TIME delta=%dms (<2000ms); opener excluded", end-oldStart))
}
