package lab

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Config 由入口只读加载。密码留在不可导出的字段，不提供日志/JSON出口；
// 业务函数借用 Config 值副本，但绝不能打印整个结构体或 driver DSN。
type Config struct {
	MySQLAddress, KafkaAddress, MySQLVersion, KafkaVersion, Database, User string
	password                                                               string
}

// IndexPath 供主进程和重启子进程使用；环境变量只传文件路径，不传凭据正文。
func IndexPath() string {
	if p := os.Getenv("GOLEARN_SERVICE_INDEX"); p != "" {
		return p
	}
	return "D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json"
}

// readJSON 是配置/无秘密崩溃spec的公共读入口。ReadFile已经关闭句柄；
// 解码失败只给固定原因，不将可能含秘密的正文放入error。
func readJSON(path string, target any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return errors.New("配置超过1MiB")
	}
	if json.Unmarshal(b, target) != nil {
		return errors.New("JSON格式无效，不输出正文")
	}
	return nil
}

// LoadConfig 由 Run/Child 同步调用，消费唯一安装者的 READY 契约。
// 本课不创建服务或隧道。数据库限定到已授权课程库，Kafka只支持当前none认证。
func LoadConfig() (Config, error) {
	var index struct {
		Services map[string]struct {
			State, Host string
			Port        int
			Version     string `json:"server_version"`
			Credential  string `json:"credential_file"`
		}
	}
	if err := readJSON(IndexPath(), &index); err != nil {
		return Config{}, err
	}
	m, k := index.Services["mysql"], index.Services["kafka"] // map取出的都是值副本。
	if m.State != "READY" || k.State != "READY" || m.Host != "127.0.0.1" || k.Host != "127.0.0.1" || m.Port <= 0 || k.Port <= 0 {
		return Config{}, errors.New("必须使用安装者READY的loopback MySQL/Kafka")
	}
	var cred struct{ User, Username, Password, Database string }
	if err := readJSON(m.Credential, &cred); err != nil {
		return Config{}, err
	}
	if cred.User == "" {
		cred.User = cred.Username
	}
	if cred.Database != "golearn_m20" || cred.User == "" || cred.Password == "" {
		return Config{}, errors.New("必须使用golearn_m20独立学习凭据")
	}
	var auth struct {
		Mechanism string `json:"sasl_mechanism"`
	}
	if err := readJSON(k.Credential, &auth); err != nil {
		return Config{}, err
	}
	if auth.Mechanism != "none" {
		return Config{}, errors.New("本课只支持登记的none认证，不猜测生产SASL")
	}
	return Config{net.JoinHostPort(m.Host, fmt.Sprint(m.Port)), net.JoinHostPort(k.Host, fmt.Sprint(k.Port)), m.Version, k.Version, cred.Database, cred.User, cred.Password}, nil
}

// openDB 由父/子进程owner创建，各自立刻登记Close。父池max2、子池max1，
// 任一实验父子同时运行也最多3条连接，留给并行课程恢复余量。
func openDB(ctx context.Context, cfg Config, max int) (*sql.DB, error) {
	d := mysql.NewConfig()
	d.User, d.Passwd, d.DBName, d.Net, d.Addr = cfg.User, cfg.password, cfg.Database, "tcp", cfg.MySQLAddress
	d.Timeout, d.ReadTimeout, d.WriteTimeout = 3*time.Second, 8*time.Second, 3*time.Second
	d.ParseTime, d.Logger = true, &mysql.NopLogger{} // Connector保留不能编码进DSN的Logger选项。
	c, err := mysql.NewConnector(d)
	if err != nil {
		return nil, errors.New("MySQL connector配置错误")
	}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(max)
	db.SetMaxIdleConns(1)
	var version, database string
	if err = db.QueryRowContext(ctx, "SELECT VERSION(),DATABASE()").Scan(&version, &database); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if version != cfg.MySQLVersion || database != cfg.Database {
		return nil, errors.Join(errors.New("MySQL实际版本/库与索引不符"), db.Close())
	}
	return db, nil
}
