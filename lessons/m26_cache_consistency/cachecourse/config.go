package cachecourse

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

// config 只在 Run 的内存中存在，两个 password 不导出，也不输出 Config/DSN。
// MySQL 和 Redis 都属于安装者；本课的 owner 只拥有自己构造的客户端。
type config struct {
	mysqlAddr, mysqlVersion, database, mysqlUser, mysqlPassword string
	redisAddr, redisVersion, redisUser, redisPassword           string
}

// loadConfig 由 Run 调用，读取现有 READY 索引以及指向的秘密文件。
// 环境变量只覆盖索引路径；JSON 解析失败不输出私有正文或可能包含输入的解析错误。
func loadConfig() (config, error) {
	path := os.Getenv("GOLEARN_SERVICE_INDEX")
	if path == "" {
		path = "D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json"
	}
	var index struct {
		Services map[string]struct {
			State          string `json:"state"`
			Host           string `json:"host"`
			Port           int    `json:"port"`
			Version        string `json:"server_version"`
			CredentialFile string `json:"credential_file"`
		} `json:"services"`
	}
	if err := readConfig(path, &index); err != nil {
		return config{}, err
	}
	c := config{}
	for _, name := range []string{"mysql", "redis"} {
		s, ok := index.Services[name]
		if !ok || s.State != "READY" || s.Host != "127.0.0.1" || s.Port < 1 || s.Port > 65535 || s.Version == "" || s.CredentialFile == "" {
			return c, fmt.Errorf("%s 必须是已登记 READY IPv4 loopback 端点", name)
		}
		secretPath := s.CredentialFile
		if !filepath.IsAbs(secretPath) {
			secretPath = filepath.Join(filepath.Dir(path), secretPath)
		}
		var secret struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Database string `json:"database"`
			DB       int    `json:"db"`
		}
		if err := readConfig(secretPath, &secret); err != nil {
			return c, fmt.Errorf("读取 %s 私有配置：%w", name, err)
		}
		if secret.Password == "" {
			return c, errors.New("学习端点必须鉴权")
		}
		addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
		if name == "mysql" {
			if secret.Database != "golearn_m20" || secret.Username == "" {
				return c, errors.New("MySQL 只允许 golearn_m20 限定账号")
			}
			c.mysqlAddr, c.mysqlVersion, c.database, c.mysqlUser, c.mysqlPassword = addr, s.Version, secret.Database, secret.Username, secret.Password
		} else {
			if secret.DB != 0 {
				return c, errors.New("Redis 仅允许已登记 DB0")
			}
			c.redisAddr, c.redisVersion, c.redisUser, c.redisPassword = addr, s.Version, secret.Username, secret.Password
		}
	}
	return c, nil
}

// readConfig 同步读取小配置文件；os.ReadFile 自己关闭 fd，返回的字节不落盘。
func readConfig(path string, target any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return errors.New("配置文件不可读")
	}
	if len(b) > 1<<20 {
		return errors.New("配置文件超过1MiB")
	}
	if err := json.Unmarshal(b, target); err != nil {
		return errors.New("配置 JSON 无效（不输出正文）")
	}
	return nil
}

// clients 由 Run 创建，并由 Run Close。sql.OpenDB 创建池对象，实际连接在查询时借用；
// redis.NewClient 也是长寿命池。本课源库 MaxOpen=4，绝不为缓存故障临时扩池。
func (c config) clients() (*sql.DB, *redis.Client, error) {
	d := mysql.NewConfig()
	d.User, d.Passwd, d.DBName, d.Net, d.Addr = c.mysqlUser, c.mysqlPassword, c.database, "tcp", c.mysqlAddr
	d.Timeout, d.ReadTimeout, d.WriteTimeout = 3*time.Second, 6*time.Second, 3*time.Second
	d.Logger = &mysql.NopLogger{}
	connector, err := mysql.NewConnector(d)
	if err != nil {
		return nil, nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(1)
	return db, c.redisClient(false), nil
}

// redisClient 给正常链路或 AUTH 故障实验构造独占客户端。故障只改这个客户端的口令，
// 不修改共享 ACL/服务状态；禁用自动重试，防止教学请求或降级的次数被暗中放大。
func (c config) redisClient(badAuth bool) *redis.Client {
	password := c.redisPassword
	if badAuth {
		password = "m26-deliberately-invalid-credential"
	}
	return redis.NewClient(&redis.Options{Addr: c.redisAddr, Username: c.redisUser, Password: password, DB: 0,
		MaxRetries: -1, PoolSize: 4, MaxActiveConns: 4, DialTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second,
		WriteTimeout: 2 * time.Second, PoolTimeout: 500 * time.Millisecond, ContextTimeoutEnabled: true})
}
