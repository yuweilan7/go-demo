package orderbackend

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Config只由LoadConfig读取，秘密字段不可导出；绝不能fmt打印整个Config/DSN。
// 端点属于安装者，Resources只拥有本进程创建的client和精确合成对象。
type Config struct {
	MySQLAddress, RedisAddress, KafkaAddress, MySQLVersion, RedisVersion, KafkaVersion string
	database, user, password, redisUser, redisPassword                                 string
}

// readConfig读取小私有文件后立即关fd。JSON错误不输出正文，秘密只在内存存活。
func readConfig(path string, to any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return errors.New("配置文件不可读")
	}
	if len(b) > 1<<20 || json.Unmarshal(b, to) != nil {
		return errors.New("配置JSON无效/超过上限")
	}
	return nil
}

// LoadConfig由入口/后续M38调用，只接受已登记READY的三种loopback服务，不安装。
func LoadConfig() (c Config, err error) {
	p := os.Getenv("GOLEARN_SERVICE_INDEX")
	if p == "" {
		p = "D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json"
	}
	var index struct {
		Services map[string]struct {
			State, Host string
			Port        int
			Version     string `json:"server_version"`
			Credential  string `json:"credential_file"`
		}
	}
	if err = readConfig(p, &index); err != nil {
		return c, err
	}
	for _, name := range []string{"mysql", "redis", "kafka"} {
		s := index.Services[name]
		if s.State != "READY" || s.Host != "127.0.0.1" || s.Port < 1 || s.Port > 65535 || s.Credential == "" {
			return c, errors.New("必须使用READY loopback服务")
		}
		secretPath := s.Credential
		if !filepath.IsAbs(secretPath) {
			secretPath = filepath.Join(filepath.Dir(p), secretPath)
		}
		var secret struct {
			Username, User, Password, Database string
			DB                                 int
			Mechanism                          string `json:"sasl_mechanism"`
		}
		if err = readConfig(secretPath, &secret); err != nil {
			return c, err
		}
		addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
		switch name {
		case "mysql":
			if secret.Username == "" {
				secret.Username = secret.User
			}
			if secret.Username == "" || secret.Password == "" || secret.Database != "golearn_m20" {
				return c, ErrInvalid
			}
			c.MySQLAddress, c.MySQLVersion, c.database, c.user, c.password = addr, s.Version, secret.Database, secret.Username, secret.Password
		case "redis":
			if secret.Password == "" || secret.DB != 0 {
				return c, ErrInvalid
			}
			c.RedisAddress, c.RedisVersion, c.redisUser, c.redisPassword = addr, s.Version, secret.Username, secret.Password
		case "kafka":
			if secret.Mechanism != "none" {
				return c, errors.New("课程仅支持已登记PLAINTEXT none")
			}
			c.KafkaAddress, c.KafkaVersion = addr, s.Version
		}
	}
	return c, nil
}

// NewNamespace只生成16hex合成命名空间。它不意味着已经获得对象所有权；Provision
// 还必须证明精确对象不存在，然后在发送CREATE之前写attempt登记。
func NewNamespace() (string, error) {
	var b [8]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return "m37_" + hex.EncodeToString(b[:]), nil
}

// SafeWriter保护并发日志的Writer对象，尤其是测试bytes.Buffer；它不排序goroutine。
type SafeWriter struct {
	mu  sync.Mutex
	Out io.Writer
}

func (w *SafeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Out.Write(p)
}

// Manifest是无秘密精确账本。attempt与确认成功分开：响应丢失时仍能清理自己的名字。
// RegistryPath放模块忽略.local；读写者只属于本run。它不扫描共享服务前缀。
type Manifest struct {
	Namespace                    string
	Tables, Topics, Groups, Keys []string
}
type registry struct {
	mu   sync.Mutex
	path string
	data Manifest
}

// add在外部副作用前被调用；先追加内存名单并原子替换本地账本，写失败就不发CREATE。
// 错误仍保留内存名单供当前owner收尾，不能因登记文件失败丢掉已登记资源。
func (r *registry) add(kind, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var list *[]string
	switch kind {
	case "table":
		list = &r.data.Tables
	case "topic":
		list = &r.data.Topics
	case "group":
		list = &r.data.Groups
	case "key":
		list = &r.data.Keys
	default:
		return ErrInvalid
	}
	for _, v := range *list {
		if v == name {
			return nil
		}
	}
	*list = append(*list, name)
	b, e := json.MarshalIndent(r.data, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(r.path), 0700); e != nil {
		return e
	}
	if e = os.WriteFile(r.path+".tmp", b, 0600); e != nil {
		return e
	}
	return os.Rename(r.path+".tmp", r.path)
}
func (r *registry) snapshot() Manifest {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.data
	d.Tables = append([]string(nil), d.Tables...)
	d.Topics = append([]string(nil), d.Topics...)
	d.Groups = append([]string(nil), d.Groups...)
	d.Keys = append([]string(nil), d.Keys...)
	return d
}

// Resources拥有共享SQL池/Redis池/admin-producer客户端。App借用它们；先App.Close
// join所有handler/任务，再CleanupOwned，再Resources.Close。这样关闭不会截断使用者。
// OpenResources不创建业务对象，因此后续同一业务恢复可打开已有namespace而不收编它。
type Resources struct {
	SQL             *sql.DB
	Store           *Store
	Cache           *Cache
	Broker          *Broker
	Config          Config
	Namespace       string
	Out             io.Writer
	reg             *registry
	closeOnce       sync.Once
	closeErr        error
	requestSequence atomic.Uint64
	requestPrefix   string
}

// NextRequestID属于Resources生命周期，App重建不归零。每次fresh构造还生成独立随机
// instance前缀，避免同namespace恢复把计数归零后复用旧requestID。它只用于追踪，
// 持久幂等仍按owner+request_key执行；随机碰撞概率不等同数据库唯一约束。
func (r *Resources) NextRequestID() string {
	return r.requestPrefix + "-req-" + strconv.FormatUint(r.requestSequence.Add(1), 10)
}

// OpenResources由组合根调用。每次成功取得资源就进入defer保护，构造失败/panic
// 先关闭已取得对象，再交回错误；不等待尚未启动的App/consumer任务。
func OpenResources(ctx context.Context, c Config, namespace, registryPath string, out io.Writer) (r *Resources, err error) {
	if !namespacePattern.MatchString(namespace) || registryPath == "" || out == nil {
		return nil, ErrInvalid
	}
	instance, e := NewNamespace()
	if e != nil {
		return nil, e
	}
	r = &Resources{Config: c, Namespace: namespace, requestPrefix: namespace + "-" + strings.TrimPrefix(instance, "m37_"), Out: &SafeWriter{Out: out}, reg: &registry{path: registryPath, data: Manifest{Namespace: namespace}}}
	transferred := false
	defer func() {
		if !transferred {
			clean := r.Close()
			if p := recover(); p != nil {
				cause, ok := p.(error)
				if !ok {
					cause = fmt.Errorf("构造panic: %v", p)
				}
				panic(errors.Join(cause, clean))
			}
			err = errors.Join(err, clean)
			r = nil
		}
	}()
	d := driver.NewConfig()
	d.User, d.Passwd, d.DBName, d.Net, d.Addr = c.user, c.password, c.database, "tcp", c.MySQLAddress
	d.Timeout, d.ReadTimeout, d.WriteTimeout = 2*time.Second, 5*time.Second, 2*time.Second
	d.ParseTime = true
	d.Logger = &driver.NopLogger{}
	connector, e := driver.NewConnector(d)
	if e != nil {
		return r, errors.New("MySQL配置错误")
	}
	r.SQL = sql.OpenDB(connector)
	r.SQL.SetMaxOpenConns(4)
	r.SQL.SetMaxIdleConns(1)
	var version, database string
	if e = r.SQL.QueryRowContext(ctx, "SELECT VERSION(),DATABASE()").Scan(&version, &database); e != nil {
		return r, e
	}
	if version != c.MySQLVersion || database != c.database {
		return r, errors.New("实际MySQL版本/数据库不匹配")
	}
	db, e := gorm.Open(mysql.New(mysql.Config{Conn: r.SQL, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), SkipDefaultTransaction: true})
	if e != nil {
		return r, e
	}
	if r.Store, e = NewStore(db, r.SQL, namespace, r.Out); e != nil {
		return r, e
	}
	rc := redis.NewClient(&redis.Options{Addr: c.RedisAddress, Username: c.redisUser, Password: c.redisPassword, DB: 0, MaxRetries: -1, PoolSize: 2, MaxActiveConns: 2, DialTimeout: 2 * time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, PoolTimeout: time.Second, ContextTimeoutEnabled: true})
	r.Cache = &Cache{client: rc, reg: r.reg, prefix: "golearn:m37:" + strings.TrimPrefix(namespace, "m37_") + ":", known: map[string]bool{}}
	info, e := rc.Info(ctx, "server").Result()
	if e != nil {
		return r, e
	}
	if !strings.Contains(info, "redis_version:"+c.RedisVersion+"\r\n") {
		return r, errors.New("Redis实际版本不符")
	}
	if r.Broker, e = OpenBroker(ctx, c, namespace, r.reg, r.Out); e != nil {
		return r, e
	}
	fmt.Fprintf(r.Out, "RESOURCES_OPEN namespace=%s mysql=%s redis=%s kafka_index=%s sql_max_open=4\n", namespace, version, c.RedisVersion, c.KafkaVersion)
	transferred = true
	return r, nil
}

// Provision只供创建run的owner调用；OpenResources的恢复使用者不调用它。
// 所有表先逐名证明不存在，再登记attempt再发送DDL；不IF NOT EXISTS收编旧对象。
func (r *Resources) Provision(ctx context.Context) error {
	definitions := []string{
		"(sku VARCHAR(64) PRIMARY KEY,quantity BIGINT NOT NULL CHECK(quantity>=0))",
		"(order_id CHAR(64) PRIMARY KEY,owner VARCHAR(16) NOT NULL,request_key VARCHAR(64) NOT NULL,request_id VARCHAR(64) NOT NULL,sku VARCHAR(64) NOT NULL,quantity BIGINT NOT NULL CHECK(quantity>0),amount_minor BIGINT NOT NULL CHECK(amount_minor>0),currency CHAR(3) NOT NULL,status VARCHAR(16) NOT NULL,aggregate_version BIGINT NOT NULL,UNIQUE(owner,request_key))",
		"(event_id CHAR(64) PRIMARY KEY,order_id CHAR(64) NOT NULL UNIQUE,payload TEXT NOT NULL,sent BOOLEAN NOT NULL DEFAULT FALSE)",
		"(event_id CHAR(64) PRIMARY KEY,payload TEXT NOT NULL,topic VARCHAR(128) NOT NULL,partition_id INT NOT NULL,first_offset BIGINT NOT NULL)",
		"(order_id CHAR(64) PRIMARY KEY,event_id CHAR(64) NOT NULL UNIQUE,quantity BIGINT NOT NULL,effect_count BIGINT NOT NULL CHECK(effect_count=1))",
	}
	for _, suffix := range TableSuffixes() {
		var n int
		if e := r.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", r.Store.Table(suffix)).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			return errors.New("精确表已存在，拒绝收编或清理")
		}
	}
	for i, suffix := range TableSuffixes() {
		name := r.Store.Table(suffix)
		if e := r.reg.add("table", name); e != nil {
			return e
		}
		if _, e := r.SQL.ExecContext(ctx, "CREATE TABLE `"+name+"` "+definitions[i]+" ENGINE=InnoDB DEFAULT CHARSET=ascii COLLATE=ascii_bin"); e != nil {
			return e
		}
	}
	if _, e := r.SQL.ExecContext(ctx, "INSERT INTO "+r.Store.table("stock")+" VALUES (?,10)", SKU); e != nil {
		return e
	}
	return r.Broker.Provision(ctx)
}

// CleanupOwned用全新预算删除登记精确名单，再独立读回；不沿用已取消请求ctx。
// caller必须先等App最终Done；这个方法不负责停止仍在访问表的后台任务。
func (r *Resources) CleanupOwned() error { return r.cleanupManifest(r.reg.snapshot()) }

// CleanupOwnedVia由原创建者调用：即使原client已关闭，原Resources仍持原attempt账本
// 并拥有清理责任，只借fresh同namespace客户端执行自己的精确清理。不把旧名单加入
// fresh registry。恢复使用者自身CleanupOwned不会删这些借用对象，原owner最后调用。
// 调用前必须等所有新旧App.Done，不能在恢复consumer仍活跃时删表/topic。
func (r *Resources) CleanupOwnedVia(fresh *Resources) error {
	if r == nil || fresh == nil || r.Namespace != fresh.Namespace {
		return ErrInvalid
	}
	return fresh.cleanupManifest(r.reg.snapshot())
}

// cleanupManifest只接本实例owner账本或CleanupOwnedVia的原owner授权，单独预算执行。
func (r *Resources) cleanupManifest(d Manifest) (err error) {
	if d.Namespace != r.Namespace {
		return ErrInvalid
	}
	if e := d.Validate(); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if r.Broker != nil {
		err = errors.Join(err, r.Broker.cleanupManifest(ctx, d))
	}
	if r.Cache != nil {
		for _, key := range d.Keys {
			e := r.Cache.client.Del(ctx, key).Err()
			if e == nil {
				var n int64
				n, e = r.Cache.client.Exists(ctx, key).Result()
				if e == nil && n != 0 {
					e = errors.New("精确Redis键仍存在")
				}
			}
			err = errors.Join(err, e)
		}
	}
	for i := len(d.Tables) - 1; i >= 0; i-- {
		name := d.Tables[i]
		_, e := r.SQL.ExecContext(ctx, "DROP TABLE IF EXISTS `"+name+"`")
		if e == nil {
			var n int
			e = r.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", name).Scan(&n)
			if e == nil && n != 0 {
				e = errors.New("精确表仍存在")
			}
		}
		err = errors.Join(err, e)
	}
	fmt.Fprintf(r.Out, "CLEANUP_OWNED tables=%d keys=%d topics=%d groups=%d verified=%t\n", len(d.Tables), len(d.Keys), len(d.Topics), len(d.Groups), err == nil)
	return CleanupCause(err)
}

// Close只回收client/池，不删除数据。sync.Once让保护defer复用同一终态；没有启动
// app时也可以直接Close。SQL Stats在Close后核验0，不用NumGoroutine猜资源结束。
func (r *Resources) Close() error {
	r.closeOnce.Do(func() {
		if r.Broker != nil {
			r.closeErr = errors.Join(r.closeErr, r.Broker.Client.Close())
		}
		if r.Cache != nil {
			r.closeErr = errors.Join(r.closeErr, r.Cache.client.Close())
		}
		if r.SQL != nil {
			r.closeErr = errors.Join(r.closeErr, r.SQL.Close())
			st := r.SQL.Stats()
			r.closeErr = errors.Join(r.closeErr, Assert(st.OpenConnections == 0 && st.InUse == 0, "SQL closed open=%d inuse=%d", st.OpenConnections, st.InUse))
			fmt.Fprintf(r.Out, "CLIENTS_CLOSED sql_open=%d sql_inuse=%d redis_closed=%t\n", st.OpenConnections, st.InUse, r.closeErr == nil)
		}
	})
	return CleanupCause(r.closeErr)
}
func (r *Resources) Manifest() Manifest { return r.reg.snapshot() }
