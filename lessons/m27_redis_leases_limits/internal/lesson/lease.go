package lesson

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// acquireState把竞争/拒绝/未知分开；只有已读到OK且剩余本地保守期限为正才是granted。
// unknown是“不知道是否执行/仍有效”，不等于false，更不能带它去执行业务。
type acquireState string

const (
	granted   acquireState = "GRANTED"
	contended acquireState = "CONTENDED"
	rejected  acquireState = "REJECTED"
	unknown   acquireState = "UNKNOWN"
)

// lease是一任租约的值副本；Key/Owner字符串不会因另一任租约改变。
// Until是从发送前算的保守本机截止时间，只是调度预算，SQL拒旧必须另有Fence。
type lease struct {
	Key, Owner string
	Until      time.Time
	Fence      int64
}
type acquisition struct {
	State acquireState
	Lease lease
	Err   error
}

// acquire由实验调用，输入已登记key和正TTL，client是A/B真实Client。
// Do显式发SET key owner NX PX 毫秒，避免整秒TTL被SetNX库优化成EX而混淆材料。
func acquire(ctx context.Context, client *redis.Client, key string, ttl time.Duration) acquisition {
	owner, err := randomToken()
	if err != nil {
		return acquisition{State: rejected, Err: err}
	}
	if ttl < time.Millisecond || ttl > 30*time.Second {
		return acquisition{State: rejected, Err: errors.New("课程lease TTL必须1ms～30s")}
	}
	if err := ctx.Err(); err != nil {
		return acquisition{State: rejected, Err: err}
	}
	start := time.Now()
	value, err := client.Do(ctx, "SET", key, owner, "NX", "PX", ttl.Milliseconds()).Text()
	result := classifySET(value, err)
	result.Lease = lease{Key: key, Owner: owner, Until: start.Add(ttl)}
	if result.State == granted && !time.Now().Before(result.Lease.Until) {
		// OK到达时本机保守有效期已过；它曾获得过，不可当竞争失败或新任owner。
		result.State, result.Err = unknown, errors.New("SET曾返回OK但本地保守有效期已结束")
	}
	return result
}

// classifySET只针对单条原子SET，不能把它泛化到会先写后报错的Lua/事务。
// redis.Nil是NX未写，redis.Error是服务器拒绝；网络错误可能发生在SET已执行之后。
func classifySET(value string, err error) acquisition {
	if errors.Is(err, redis.Nil) {
		return acquisition{State: contended}
	}
	if err != nil {
		var serverError redis.Error
		if errors.As(err, &serverError) || errors.Is(err, redis.ErrClosed) || errors.Is(err, redis.ErrPoolTimeout) {
			return acquisition{State: rejected, Err: err}
		}
		return acquisition{State: unknown, Err: err}
	}
	if value != "OK" {
		return acquisition{State: unknown, Err: fmt.Errorf("非预期SET回复%q", value)}
	}
	return acquisition{State: granted}
}

// Redis8.2没有DELEX（8.4才有）。Lua中GET与DEL处于同一次原子脚本，其他命令
// 不会插在比较与删除之间；脚本不负责阻止旧worker的SQL/HTTP副作用。
const releaseLua = `if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`

// renewLua同样先比较owner；失去租约返回0，不能用普通PEXPIRE续上别人的锁。
const renewLua = `if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0`

// release把1/0原样给caller：1本次删掉，0已过期/已释放/别人持有。
// error仍可能是响应未知，禁止降成0；再次比较释放是幂等的，raw DEL不是。
func release(ctx context.Context, client *redis.Client, current lease) (int64, error) {
	if current.Key == "" || current.Owner == "" {
		return 0, errors.New("空lease不得触碰任何Redis键")
	}
	return client.Eval(ctx, releaseLua, []string{current.Key}, current.Owner).Int64()
}

// renew是同步Redis I/O；caller决定何时调用/怎样取消工作，返回1也不是永久授权。
func renew(ctx context.Context, client *redis.Client, current lease, ttl time.Duration) (int64, error) {
	if current.Key == "" || current.Owner == "" {
		return 0, errors.New("空lease不得触碰任何Redis键")
	}
	if ttl < time.Millisecond || ttl > 30*time.Second {
		return 0, errors.New("续租TTL越界")
	}
	return client.Eval(ctx, renewLua, []string{current.Key}, current.Owner, ttl.Milliseconds()).Int64()
}

// requireLease只在实验的正常申请位置调用，未知/竞争会带状态上交，禁止盲重试。
func requireLease(result acquisition) (lease, error) {
	if result.State != granted {
		if result.Err != nil {
			return lease{}, fmt.Errorf("需要确定租约，实际%s: %w", result.State, result.Err)
		}
		return lease{}, fmt.Errorf("需要确定租约，实际%s", result.State)
	}
	return result.Lease, nil
}

// expired读真实EXISTS到0才放行B，不根据本机sleep猜Redis已经过期。
func (l *lab) expired(ctx context.Context, key string) error {
	return observe(ctx, "Redis实际锁过期", func() (bool, error) {
		count, err := l.b.Exists(ctx, key).Result()
		return count == 0, err
	})
}
