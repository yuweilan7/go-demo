package lesson

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
)

// Report 是可安全打印的白名单，不包括配置/密码/DSN/Redis命令参数。
// Cleaned 在最后的实际DEL/DROP/连接关闭核验后填写，前面的PASS不是收尾证据。
type Report struct {
	RunID, Prefix                string
	Keys, Tables                 []string
	Assertions                   int
	TasksStarted, TasksJoined    int
	SocketsOpened, SocketsClosed int
	Cleaned                      bool
}

// lab 是Run的局部owner；两个Client指针借给各实验和worker，只有Run最后Close。
// report/registry/out只由owner写，worker通过Result交结果，避免并发写日志或slice。
type lab struct {
	cfg           Config
	a, b          *redis.Client
	db            *sql.DB
	state, orders string
	report        *Report
	out           io.Writer
	stats         *socketStats
}

// socketStats 记录本课真实Redis连接，不管理隧道/服务。驱动可能并发拨号/关闭，
// mutex保护这些计数；不是业务互斥锁，也不能据此证明跨进程数据正确性。
type socketStats struct {
	mu             sync.Mutex
	opened, closed int
	err            error
}

// trackedConn嵌入net.Conn：未覆盖的Read/Write/Deadline直接交真实socket。
// once使驱动与owner可能重复Close时只关闭一次，并把第一次错误保留到总收尾。
type trackedConn struct {
	net.Conn
	stats *socketStats
	once  sync.Once
	err   error
}

func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.stats.mu.Lock()
		c.stats.closed++
		c.stats.err = errors.Join(c.stats.err, c.err)
		c.stats.mu.Unlock()
	})
	return c.err
}

// redisOptions固定本次实际库契约。MaxRetries=-1禁止未知写自动重放；每池最多2。
// v9.23独立pipeline池与maintenance后台功能在本课关闭，资源数量由主池解释。
func (l *lab) redisOptions() *redis.Options {
	return &redis.Options{
		Addr: l.cfg.RedisAddress, Username: l.cfg.redisUser, Password: l.cfg.redisPassword,
		DB: 0, Protocol: 2, MaxRetries: -1, DialerRetries: 1,
		PoolSize: 2, MaxActiveConns: 2, PipelinePoolSize: -1, PoolTimeout: time.Second,
		DialTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second,
		ContextTimeoutEnabled: true, DisableIdentity: true,
		MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled},
		Dialer: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			l.stats.mu.Lock()
			l.stats.opened++
			l.stats.mu.Unlock()
			return &trackedConn{Conn: conn, stats: l.stats}, nil
		},
	}
}

// Run是入口/集成测试的同步调用层，拥有池、随机命名空间和所有专属SQL表。
// 具名err被defer按地址修改，使原业务失败与清理失败同时留在返回链。
func Run(ctx context.Context, cfg Config, scenario string, out io.Writer) (report Report, err error) {
	if scenario != "all" && scenario != "core" {
		return report, errors.New("scenario仅支持all/core")
	}
	run, err := randomToken()
	if err != nil {
		return report, err
	}
	report.RunID, report.Prefix = run, "golearn:m27:"+run+":"
	l := &lab{cfg: cfg, report: &report, out: out, stats: &socketStats{}}
	l.a, l.b = redis.NewClient(l.redisOptions()), redis.NewClient(l.redisOptions())
	defer l.finish(&err)
	// mysql.NewConfig产生driver安全默认值。DSN只传sql.Open，绝不打印。
	driver := mysql.NewConfig()
	driver.User, driver.Passwd, driver.DBName = cfg.mysqlUser, cfg.mysqlPassword, cfg.database
	driver.Net, driver.Addr = "tcp", cfg.MySQLAddress
	driver.Timeout, driver.ReadTimeout, driver.WriteTimeout = 2*time.Second, 5*time.Second, 2*time.Second
	driver.MultiStatements, driver.InterpolateParams = false, false
	driver.Params = map[string]string{"time_zone": "'+00:00'"}
	l.db, err = sql.Open("mysql", driver.FormatDSN())
	if err != nil {
		return report, err
	}
	l.db.SetMaxOpenConns(4)
	l.db.SetMaxIdleConns(2)
	l.db.SetConnMaxLifetime(2 * time.Minute)
	fmt.Fprintf(out, "M27 run=%s Redis=%s MySQL=%s DB=golearn_m20 scenario=%s\n", run, cfg.RedisAddress, cfg.MySQLAddress, scenario)
	info, err := l.a.Info(ctx, "server").Result()
	if err != nil {
		return report, err
	}
	if err = l.b.Ping(ctx).Err(); err != nil {
		return report, err
	}
	var version, database string
	if err = l.db.QueryRowContext(ctx, "SELECT VERSION(), DATABASE()").Scan(&version, &database); err != nil {
		return report, err
	}
	if err = l.check("real_versions", strings.Contains(info, "redis_version:"+cfg.RedisVersion+"\r\n") && version == cfg.MySQLVersion && database == "golearn_m20",
		fmt.Sprintf("Redis=%s MySQL=%s drivers=go-redis/v9.23.0,mysql/v1.10.1", cfg.RedisVersion, version)); err != nil {
		return report, err
	}
	if err = l.createTables(ctx); err != nil {
		return report, err
	}
	steps := []struct {
		name string
		run  func(context.Context) error
	}{
		{"1 过期后旧worker写入：GET检查不能覆盖后续SQL", l.staleUnsafe},
		{"2 持久授予序号与下游SQL fencing", l.staleFenced},
	}
	if scenario == "all" {
		steps = append(steps, []struct {
			name string
			run  func(context.Context) error
		}{
			{"3 竞争、错误DEL与比较释放/续租", l.unlocking},
			{"4 续租丢失、取消通知、清理与真实Finished", l.renewCancellation},
			{"5 锁申请失败分类与真实响应未知", l.acquireFailures},
			{"6 Lua固定窗口、隔离、拒绝、TTL与burst", l.rateLimits},
			{"7 顺序重试订单：锁与SQL幂等事务", l.ordersExperiment},
		}...)
	}
	for _, step := range steps {
		fmt.Fprintf(out, "\n=== %s ===\n", step.name)
		if err = step.run(ctx); err != nil {
			return report, fmt.Errorf("%s: %w", step.name, err)
		}
	}
	// 每个目前存在的课内键都必须有正TTL；过期/已释放键允许-2，不能遗留-1。
	for _, key := range report.Keys {
		ttl, ttlErr := l.a.PTTL(ctx, key).Result()
		if ttlErr != nil {
			return report, ttlErr
		}
		if ttl != time.Duration(-2) && (ttl <= 0 || ttl > 30*time.Second) {
			return report, fmt.Errorf("精确键TTL异常 key=%s ttl=%v", key, ttl)
		}
	}
	err = l.check("all_registered_keys_ttl", true, fmt.Sprintf("registered=%d; existing keys have positive TTL<=30s", len(report.Keys)))
	return report, err
}

// key 在发命令之前登记唯一完整键，即使SET回复丢失，owner也不会漏掉清理目标。
// caller是串行实验owner；worker只使用已登记的字符串，不修改registry。
func (l *lab) key(suffix string) string {
	key := l.report.Prefix + suffix
	for _, existing := range l.report.Keys {
		if existing == key {
			return key
		}
	}
	l.report.Keys = append(l.report.Keys, key)
	return key
}

// check验证独立业务事实并输出解释值；false是普通error，交给上层defer收尾。
func (l *lab) check(name string, condition bool, detail string) error {
	if !condition {
		return fmt.Errorf("assertion %s failed: %s", name, detail)
	}
	l.report.Assertions++
	fmt.Fprintf(l.out, "PASS %-33s %s\n", name, detail)
	return nil
}

// createTables只创建本run随机表；先查询同名不存在，再登记尝试，CREATE回复未知
// 也能对自己确实尝试过的名字做DROP。表标识符不能用?，因此先严格验证再加反引号。
func (l *lab) createTables(ctx context.Context) error {
	names := []string{"m27_" + l.report.RunID + "_state", "m27_" + l.report.RunID + "_orders"}
	for _, name := range names {
		quoted, err := quotedTable(name)
		if err != nil {
			return err
		}
		var exists int
		if err = l.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", l.cfg.database, name).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return errors.New("随机表名已存在，拒绝认领该对象")
		}
		l.report.Tables = append(l.report.Tables, name)
		var statement string
		if strings.HasSuffix(name, "_state") {
			l.state = quoted
			statement = "CREATE TABLE " + quoted + " (id INT PRIMARY KEY, issued_token BIGINT NOT NULL DEFAULT 0, last_token BIGINT NOT NULL DEFAULT 0, value_text VARCHAR(40) NOT NULL DEFAULT 'initial', effects INT NOT NULL DEFAULT 0, stock INT NOT NULL DEFAULT 2) ENGINE=InnoDB"
		} else {
			l.orders = quoted
			statement = "CREATE TABLE " + quoted + " (order_id VARCHAR(40) PRIMARY KEY, units INT NOT NULL) ENGINE=InnoDB"
		}
		if _, err = l.db.ExecContext(ctx, statement); err != nil {
			return err
		}
		fmt.Fprintf(l.out, "OWN table=%s\n", name)
	}
	_, err := l.db.ExecContext(ctx, "INSERT INTO "+l.state+" (id) VALUES (0),(1),(2),(3),(4),(5)")
	return err
}

var safeTable = regexp.MustCompile(`^m27_[a-f0-9]{24}_(state|orders)$`)

// quotedTable拒绝用户输入/其他课表名；返回的字符串只能用于本课生成DDL/SQL标识符。
func quotedTable(name string) (string, error) {
	if !safeTable.MatchString(name) {
		return "", errors.New("拒绝非本课随机表名")
	}
	return "`" + name + "`", nil
}

// finish最后执行：每个实验已join自己的worker，本层再清理表/键，最后关闭池。
// 请求ctx已失败时仍用独立8秒预算。精确列表替代SCAN/FLUSH，保护共享Redis。
func (l *lab) finish(retErr *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var cleanupErr error
	if l.db != nil {
		for _, name := range l.report.Tables {
			quoted, err := quotedTable(name)
			if err == nil {
				_, err = l.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+quoted)
			}
			cleanupErr = errors.Join(cleanupErr, err)
			var remaining int
			err = l.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", l.cfg.database, name).Scan(&remaining)
			if err == nil && remaining != 0 {
				err = fmt.Errorf("DROP后仍有表%s", name)
			}
			cleanupErr = errors.Join(cleanupErr, err)
			fmt.Fprintf(l.out, "CLEAN table=%s remaining=%d error=%v\n", name, remaining, err)
		}
	}
	remaining := int64(0)
	if len(l.report.Keys) > 0 {
		cleanupErr = errors.Join(cleanupErr, l.a.Del(ctx, l.report.Keys...).Err())
		var err error
		remaining, err = l.a.Exists(ctx, l.report.Keys...).Result()
		cleanupErr = errors.Join(cleanupErr, err)
		if remaining != 0 {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("清理后remaining keys=%d", remaining))
		}
	}
	cleanupErr = errors.Join(cleanupErr, l.a.Close(), l.b.Close())
	if l.db != nil {
		cleanupErr = errors.Join(cleanupErr, l.db.Close())
		stats := l.db.Stats()
		if stats.OpenConnections != 0 || stats.InUse != 0 {
			cleanupErr = errors.Join(cleanupErr, errors.New("DB.Close后仍有活动连接"))
		}
		fmt.Fprintf(l.out, "CLEAN DB open=%d inuse=%d idle=%d\n", stats.OpenConnections, stats.InUse, stats.Idle)
	}
	l.stats.mu.Lock()
	l.report.SocketsOpened, l.report.SocketsClosed = l.stats.opened, l.stats.closed
	cleanupErr = errors.Join(cleanupErr, l.stats.err)
	l.stats.mu.Unlock()
	if l.report.SocketsOpened != l.report.SocketsClosed || l.report.TasksStarted != l.report.TasksJoined {
		cleanupErr = errors.Join(cleanupErr, errors.New("任务/Redis连接未全部收尾"))
	}
	l.report.Cleaned = cleanupErr == nil
	*retErr = errors.Join(*retErr, cleanupErr)
	fmt.Fprintf(l.out, "CLEAN exact_keys=%d remaining=%d tasks=%d/%d sockets=%d/%d cleaned=%t\n", len(l.report.Keys), remaining,
		l.report.TasksJoined, l.report.TasksStarted, l.report.SocketsClosed, l.report.SocketsOpened, l.report.Cleaned)
}

// randomToken为每次尝试生成crypto/rand的12字节随机owner；十六进制是可比较文本。
// 它防止误删另一任租约，随机性不代表可排序，不能拿它当fencing版本。
func randomToken() (string, error) {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("生成owner随机token: %w", err)
	}
	return hex.EncodeToString(bytes[:]), nil
}
