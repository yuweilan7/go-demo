package txcourse

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
)

const defaultIndex = "D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json"

// Config 是只读接入信息；password 私有，禁止打印整个 Config 或 DSN。
// 本课不安装、不停止共享服务；index 只指向安装者已经 READY 的学习实例。
type Config struct {
	Host, Version, Database, User string
	Port                          int
	password                      string
}

// LoadConfig 由 Run 调用，从安装者索引找到凭据文件。环境变量只覆盖文件路径，
// 不携带密码。错误不包含文件正文；file owner 是 readJSON，返回前它关闭文件。
func LoadConfig() (Config, error) {
	path := os.Getenv("GOLEARN_SERVICE_INDEX")
	if path == "" {
		path = defaultIndex
	}
	// 带下划线的 JSON 名称不等于 Go 字段名，单独声明明确的解码契约。
	var raw struct {
		Services struct {
			MySQL struct {
				State          string `json:"state"`
				Host           string `json:"host"`
				Port           int    `json:"port"`
				Version        string `json:"server_version"`
				CredentialFile string `json:"credential_file"`
			} `json:"mysql"`
		} `json:"services"`
	}
	if err := readJSON(path, &raw); err != nil {
		return Config{}, fmt.Errorf("读取服务索引: %w", err)
	}
	s := raw.Services.MySQL // 值副本，只读字段，不写共享索引。
	if s.State != "READY" || s.Host != "127.0.0.1" || s.Port < 1 || s.Port > 65535 || s.Version == "" {
		return Config{}, errors.New("拒绝非 READY 的 loopback MySQL 学习端点")
	}
	var credential struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Database string `json:"database"`
	}
	if err := readJSON(s.CredentialFile, &credential); err != nil {
		return Config{}, fmt.Errorf("读取私有凭据: %w", err)
	}
	if credential.Database != "golearn_m20" || credential.Username == "" || credential.Password == "" {
		return Config{}, errors.New("凭据必须是既有 golearn_m20 学习库的限定账号")
	}
	return Config{s.Host, s.Version, credential.Database, credential.Username, s.Port, credential.Password}, nil
}

// readJSON 仅用来读取两份小 JSON；调用方传指针，decoder 填充其指向对象。
// 具名 err 让退出清理可用 Join 保留读取失败和 Close 失败，不能让后者覆盖前者。
func readJSON(path string, target any) (err error) {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	if err = d.Decode(target); err != nil {
		return err
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return errors.New("JSON 只能含一个对象")
	}
	return nil
}

// openPool 由 Run/取消/代理实验调用，每次 caller 是这个 DB 的唯一 Close owner。
// addrOverride 仅用于本课 loopback 代理。SQL 值仍用 ?，不启用多语句或参数插值。
func openPool(c Config, addrOverride string, max int) (*sql.DB, error) {
	d := mysql.NewConfig()
	d.User, d.Passwd, d.DBName = c.User, c.password, c.Database
	d.Net, d.Addr = "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	if addrOverride != "" {
		d.Addr = addrOverride
	}
	d.Timeout, d.ReadTimeout, d.WriteTimeout = 3*time.Second, 6*time.Second, 3*time.Second
	d.MultiStatements, d.InterpolateParams = false, false
	// 本课代理只解析未压缩明文 packet；端点是本机 loopback 隧道。
	// 这不是生产 TLS 建议，代理也不支持 TLS/compression/大包分片。
	d.TLSConfig = "false"
	d.Logger = &mysql.NopLogger{} // v1.10.1 实际 Logger API；故障由 error 返回，不重复打印 socket 日志。
	d.Params = map[string]string{"time_zone": "'+00:00'"}
	connector, err := mysql.NewConnector(d) // 保留 Logger 等不能编码进 DSN 的真实配置。
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(max)
	db.SetMaxIdleConns(1) // 仅保留一条连接，减少反复认证；每阶段活跃最多3，最终 owner Close。
	return db, nil
}

// shortContext 给独立清理/查账建立新预算；不能继承已经取消的业务 ctx。
func shortContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 6*time.Second)
}
