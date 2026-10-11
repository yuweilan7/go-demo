package lesson

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
)

// Check 是单次业务断言的观察值，不保存 driver Cmd.Args（AUTH 参数可能有秘密）。
type Check struct {
	Name     string `json:"name"`
	Observed string `json:"observed"`
}

// Report 只保存安全白名单。Cleaned 与 ConnectionsOpen 是 Run 的 defer 实际填入的结果，
// 不是因为看到前面的 CHECK 输出就推断清理成功。
type Report struct {
	Started         time.Time `json:"started"`
	Address         string    `json:"address"`
	ServerVersion   string    `json:"server_version"`
	DriverVersion   string    `json:"driver_version"`
	Prefix          string    `json:"prefix"`
	Checks          []Check   `json:"checks"`
	Keys            []string  `json:"exact_keys"`
	Cleaned         bool      `json:"cleaned"`
	Remaining       int64     `json:"remaining_keys"`
	ConnectionsOpen int       `json:"connections_open"`
	ConnectionsMade int       `json:"connections_created"`
	TasksStarted    int       `json:"tasks_started"`
	TasksJoined     int       `json:"tasks_joined"`
	Completed       bool      `json:"completed"`
}

// lab 是实验的生命周期 owner。client 指针被业务函数/有限 worker 共享，
// registry 只由 owner 串行登记，worker 不改它；stats 由 mutex 保护连接 Close/dial 的并发。
type lab struct {
	cfg    Config
	client *redis.Client
	out    io.Writer
	report *Report
	stats  *socketStats
}

// options 由正常 client 与独占故障 client 共用。每个 client 都长寿命到自身实验退出，
// 不在 GET/SET 的请求函数里 Close。这里禁自动命令重试，避免 INCR 响应丢失后重复副作用。
func options(cfg Config, stats *socketStats) *redis.Options {
	return &redis.Options{
		Addr: cfg.Address, Username: cfg.Username, Password: cfg.Password, DB: cfg.DB,
		Protocol: 2, MaxRetries: -1, DialerRetries: 1,
		DialTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second,
		ContextTimeoutEnabled: true, PoolSize: 4, MaxActiveConns: 4, PoolTimeout: time.Second,
		// v9.23 默认有独立 pipeline pool；显式负数让本课 pipeline 用主池，连接上限可解释。
		PipelinePoolSize: -1, DisableIdentity: true,
		MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled},
		Dialer: func(ctx context.Context, network, address string) (net.Conn, error) {
			// ctx 是驱动借池/拨号传下来的 scope。DialContext 观察它，和 socket 读 deadline 分开。
			conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			return stats.track(conn), nil
		},
	}
}

// Run 由入口/集成测试同步调用，返回前负责所有任务、键和连接的收尾。
// named err 使 defer 能将清理失败 errors.Join 到业务失败上；不覆盖最初原因。
func Run(ctx context.Context, cfg Config, out io.Writer, all bool) (report Report, err error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Report{}, fmt.Errorf("生成隔离 run 失败：%w", err)
	}
	report = Report{Started: time.Now().UTC(), Address: cfg.Address,
		ServerVersion: cfg.ServerVersion, DriverVersion: "v9.23.0",
		Prefix: "golearn:m25:" + hex.EncodeToString(nonce[:]) + ":", Remaining: -1}
	stats := &socketStats{}
	l := &lab{cfg: cfg, out: out, report: &report, stats: stats}
	l.client = redis.NewClient(options(cfg, stats)) // 构造池；第一次命令才拨号/HELLO AUTH。
	defer func() {
		// 请求 ctx 可能已经超时；清理用独立新预算，仍只删除登记的精确键。
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if len(report.Keys) > 0 {
			if cleanupErr := l.client.Del(cleanupCtx, report.Keys...).Err(); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("精确键 DEL 清理：%w", cleanupErr))
			} else {
				report.Remaining, cleanupErr = l.client.Exists(cleanupCtx, report.Keys...).Result()
				if cleanupErr != nil || report.Remaining != 0 {
					err = errors.Join(err, fmt.Errorf("清理 EXISTS=%d error=%v", report.Remaining, cleanupErr))
				} else {
					report.Cleaned = true
				}
			}
		} else {
			report.Cleaned, report.Remaining = true, 0
		}
		// 所有实验函数在返回前已 join 自己的 worker。到这里才由 owner 关闭长寿命 client。
		err = errors.Join(err, l.client.Close())
		report.ConnectionsMade, report.ConnectionsOpen = stats.snapshot()
		if report.ConnectionsOpen != 0 || report.TasksStarted != report.TasksJoined {
			err = errors.Join(err, fmt.Errorf("资源未收尾：open=%d tasks=%d/%d", report.ConnectionsOpen, report.TasksJoined, report.TasksStarted))
		}
		report.Completed = err == nil
		fmt.Fprintf(out, "CLEANUP exact_keys=%d remaining=%d cleaned=%t sockets=%d/%d tasks=%d/%d completed=%t\n",
			len(report.Keys), report.Remaining, report.Cleaned, report.ConnectionsOpen, report.ConnectionsMade,
			report.TasksJoined, report.TasksStarted, report.Completed)
	}()
	fmt.Fprintf(out, "M25 redis=%s driver=v9.23.0 prefix=%s\n", cfg.Address, report.Prefix)
	info, err := l.client.Info(ctx, "server").Result()
	if err != nil {
		return report, fmt.Errorf("真实 INFO/AUTH 失败：%w", err)
	}
	if err := l.check("service_version", strings.Contains(info, "redis_version:"+cfg.ServerVersion+"\r\n"), cfg.ServerVersion); err != nil {
		return report, err
	}
	steps := []func(context.Context) error{l.basic, l.lostUpdate, l.atomicBoundaries, l.expiryWindow}
	if all {
		steps = append(steps, l.authentication, l.poolWait, l.socketBudgets)
	}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return report, err
		}
	}
	if err := l.verifyTTLs(ctx); err != nil {
		return report, err
	}
	return report, nil
}

// verifyTTLs 由 Run 在全部业务步骤完成后调用，逐个读取 registry 中的真实 PTTL。
// 缺键只允许三个本来不应写入的反例；其余20（core为15）个业务键必须已有合理正期限。
// 这项总断言发现任何修复漏了EXPIRE，而不是只凭某一次Expire.Err()==nil就说所有键安全。
func (l *lab) verifyTTLs(ctx context.Context) error {
	expectedAbsent := map[string]bool{"missing": true, "tx-queue-abort": true, "lua-after": true}
	existing, absent := 0, 0
	for _, key := range l.report.Keys {
		ttl, err := l.client.PTTL(ctx, key).Result()
		if err != nil {
			return err
		}
		if expectedAbsent[strings.TrimPrefix(key, l.report.Prefix)] {
			if ttl != time.Duration(-2) {
				return fmt.Errorf("本应未写入的键有副作用：key=%s PTTL=%v", key, ttl)
			}
			absent++
			continue
		}
		if ttl <= 0 || ttl > 90*time.Second {
			return fmt.Errorf("登记业务键缺正TTL或异常过期：key=%s PTTL=%v", key, ttl)
		}
		existing++
	}
	return l.check("registered_key_ttls", absent == 3 && existing == len(l.report.Keys)-3,
		fmt.Sprintf("existing=%d all_positive_ttl=true expected_absent=%d", existing, absent))
}

// key 由 owner 在命令发送前调用；即使响应丢失，也已登记需要清理的可能副作用。
func (l *lab) key(suffix string) string {
	key := l.report.Prefix + suffix
	l.report.Keys = append(l.report.Keys, key)
	return key
}

// check 把独立业务条件和观察值连在一起；false 返回错误给当前步骤，再触发 Run 的 defer。
func (l *lab) check(name string, condition bool, observed string) error {
	if !condition {
		return fmt.Errorf("断言 %s 未满足，观察=%s", name, observed)
	}
	l.report.Checks = append(l.report.Checks, Check{Name: name, Observed: observed})
	fmt.Fprintf(l.out, "CHECK %-28s %s\n", name, observed)
	return nil
}

// WriteReport 在 Run 全部 defer 执行后被 caller 调用；创建的是本课忽略的报告，不包含 Config。
func WriteReport(path string, report Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("报告编码：%w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

// socketStats 由各 client 的 Dialer 调用；记录真实本机 TCP socket 创建/Close，不推测服务端计数。
type socketStats struct {
	mu         sync.Mutex
	made, open int
}

// track 接收已拨号成功的连接，返回接口中的指针包装；包装与 driver 共享同一底层连接。
func (s *socketStats) track(conn net.Conn) net.Conn {
	s.mu.Lock()
	s.made++
	s.open++
	s.mu.Unlock()
	return &trackedConn{Conn: conn, stats: s}
}

// snapshot 由生命周期 owner 在 Close 后读计数，mutex 保证和 driver 的并发 Close 不竞争。
func (s *socketStats) snapshot() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.made, s.open
}

// trackedConn 通过嵌入 net.Conn 复用 Read/Write/deadline，单独包装 Close。
type trackedConn struct {
	net.Conn
	stats *socketStats
	once  sync.Once
	err   error
}

// Close 由 driver 或 owner 调用。sync.Once 避免超时淘汰和 client.Close 重复减计数。
func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.stats.mu.Lock()
		c.stats.open--
		c.stats.mu.Unlock()
	})
	return c.err
}
