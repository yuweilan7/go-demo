package course

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Config 是入口交给 Run 的值副本。秘密只进 driver，禁止用 %+v 或 JSON 打印对象。
// 本课消费 M20 已交付的连接契约，不导入/复制 M20 的实验和不认领它的表。
type Config struct {
	host, database, username, password, version string
	port                                        int
}

// ServiceIndexPath 只允许路径覆盖；环境变量不承载 DSN 或密码。
func ServiceIndexPath() string {
	if path := os.Getenv("GOLEARN_SERVICE_INDEX"); path != "" {
		return path
	}
	return "D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json"
}

// LoadConfig 由入口/集成测试调用，读取安装者的 READY 索引和私有凭据。
// 这里没有数据库副作用；状态、版本、真实读写仍在 Run 中独立核验。
func LoadConfig(path string) (Config, error) {
	var index struct {
		Services map[string]struct {
			State, Host string
			Port        int
			Version     string `json:"server_version"`
			Credential  string `json:"credential_file"`
		} `json:"services"`
	}
	if err := readJSON(path, &index); err != nil {
		return Config{}, fmt.Errorf("读取课程索引: %w", err)
	}
	service := index.Services["mysql"]
	if service.State != "READY" || service.Host != "127.0.0.1" || service.Port != 13306 || service.Version == "" {
		return Config{}, errors.New("需要 READY 的课程 MySQL loopback:13306，不执行未确认实例的 SQL")
	}
	var secret struct{ Username, Password, Database string }
	if err := readJSON(service.Credential, &secret); err != nil {
		return Config{}, fmt.Errorf("读取私有凭据: %w", err)
	}
	// 本次授权是唯一学习库；不把 golearn 前缀扩展成任意库管理权限。
	if secret.Database != "golearn_m20" || secret.Username == "" || secret.Password == "" {
		return Config{}, errors.New("凭据不符合 golearn_m20 的课程契约")
	}
	return Config{service.Host, secret.Database, secret.Username, secret.Password, service.Version, service.Port}, nil
}

// readJSON 拥有短命文件；具名 err 让 Close 错误与读取错误同时返回。
// 不把正文拼进日志，限制 1MiB 并拒绝尾随第二个 JSON 对象。
func readJSON(path string, target any) (err error) {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	if err = decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("JSON 必须是单个有界对象")
	}
	return nil
}

// driverConfig 由 Run 构造真实驱动。ParseTime 负责 DATETIME→time.Time，
// Loc 负责 Go 的位置，time_zone 负责服务端会话；三个设置不是同一件事。
func (config Config) driverConfig() *mysql.Config {
	d := mysql.NewConfig()
	d.User, d.Passwd, d.DBName = config.username, config.password, config.database
	d.Net, d.Addr = "tcp", net.JoinHostPort(config.host, strconv.Itoa(config.port))
	d.ParseTime, d.Loc = true, time.UTC
	d.Timeout, d.ReadTimeout, d.WriteTimeout = 3*time.Second, 8*time.Second, 3*time.Second
	d.Params = map[string]string{"charset": "utf8mb4", "time_zone": "'+00:00'", "sql_mode": "'STRICT_TRANS_TABLES,NO_ZERO_DATE,NO_ZERO_IN_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION'"}
	// ? 绑定值；禁多语句和客户端字符串插值，让每个 SQL 的边界清楚可见。
	d.InterpolateParams, d.MultiStatements = false, false
	return d
}

var ownedName = regexp.MustCompile(`^m24_[a-f0-9]{24}_(users|orders|types|pages|evolve|versions|audit)$`)

// quote 只接收本 run 生成的标识符。? 不能替代表名，因而采用严格白名单再加反引号。
func quote(name string) (string, error) {
	if !ownedName.MatchString(name) {
		return "", errors.New("拒绝非 M24 独占表标识符")
	}
	return "`" + name + "`", nil
}
