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
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

const defaultIndex = "D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json"

// Config 是 LoadConfig 交给 Run 的值副本。密码只在内存中交给 driver，
// 本类型没有 String/JSON 日志出口；调用方不能用 %+v 打印它。
type Config struct {
	Host          string
	Port          int
	ServerVersion string
	Database      string
	User          string
	password      string
}

// ServiceIndexPath 由入口/实库测试调用。环境变量只覆盖路径，不携带 DSN/密码。
func ServiceIndexPath() string {
	if value := os.Getenv("GOLEARN_SERVICE_INDEX"); value != "" {
		return value
	}
	return defaultIndex
}

// LoadConfig 只消费安装者的只读索引和凭据，不安装、启动或修改服务。
// READY 是安装者的接入承诺，Run 仍须亲自核对版本、业务写读和资源释放。
func LoadConfig(path string) (Config, error) {
	var index struct {
		Services map[string]json.RawMessage `json:"services"`
		MySQL    json.RawMessage            `json:"mysql"`
	}
	if err := readJSON(path, &index); err != nil {
		return Config{}, fmt.Errorf("读取服务索引（未执行数据库实验）: %w", err)
	}
	raw := index.Services["mysql"]
	if len(raw) == 0 {
		raw = index.MySQL
	}
	var service struct {
		State          string `json:"state"`
		Host           string `json:"host"`
		Port           int    `json:"port"`
		ServerVersion  string `json:"server_version"`
		CredentialFile string `json:"credential_file"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &service) != nil {
		return Config{}, errors.New("索引缺少合法 mysql 条目")
	}
	if !strings.EqualFold(service.State, "READY") {
		return Config{}, errors.New("MySQL 尚未 READY；实库实验保持未验证")
	}
	// 本机 SSH 转发是本课唯一接入；拒绝意外远端地址，避免误连业务实例。
	if service.Host != "127.0.0.1" && service.Host != "::1" {
		return Config{}, errors.New("MySQL 端点必须是安装者提供的 loopback 转发")
	}
	if service.Port < 1 || service.Port > 65535 || service.ServerVersion == "" || service.CredentialFile == "" {
		return Config{}, errors.New("MySQL 索引缺少 port/version/credential_file")
	}
	var credential struct {
		User     string `json:"user"`
		Username string `json:"username"`
		Password string `json:"password"`
		Database string `json:"database"`
	}
	if err := readJSON(service.CredentialFile, &credential); err != nil {
		return Config{}, fmt.Errorf("读取私有凭据: %w", err)
	}
	if credential.User == "" {
		credential.User = credential.Username
	}
	if credential.User == "" || credential.Password == "" || !databaseName.MatchString(credential.Database) {
		return Config{}, errors.New("凭据必须含 user/password 和 golearn_ 前缀的独立课程 database")
	}
	return Config{Host: service.Host, Port: service.Port, ServerVersion: service.ServerVersion,
		Database: credential.Database, User: credential.User, password: credential.Password}, nil
}

var databaseName = regexp.MustCompile(`^golearn_[a-z0-9_]{1,50}$`)
var tableName = regexp.MustCompile(`^m20_[a-f0-9]{24}$`)

// readJSON 的 caller 传入自己要解析的类型指针。解码错误只保留位置/类型信息，
// 不把文件正文、凭据对象或 DSN 拼进错误。限制大小避免误读巨大文件。
func readJSON(path string, target any) (err error) {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 1<<20 {
		return errors.New("JSON 文件超过1 MiB")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	if err = decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON 必须只有一个对象且不超过 1 MiB")
	}
	return nil
}

// driverConfig 由 openPool 调用，使用 mysql.NewConfig 的安全默认值构造参数。
// FormatDSN 只用于 sql.Open，禁止打印。参数绑定使用驱动真实 prepared 协议，
// InterpolateParams=false 不把用户值拼进 SQL 字符串；MultiStatements=false 禁多语句。
func (config Config) driverConfig() *mysql.Config {
	driver := mysql.NewConfig()
	driver.User, driver.Passwd, driver.DBName = config.User, config.password, config.Database
	driver.Net, driver.Addr = "tcp", net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	driver.ParseTime, driver.Loc = true, time.UTC
	driver.Timeout, driver.ReadTimeout, driver.WriteTimeout = 3*time.Second, 8*time.Second, 3*time.Second
	driver.InterpolateParams, driver.MultiStatements, driver.ClientFoundRows = false, false, false
	driver.Params = map[string]string{"charset": "utf8mb4", "time_zone": "'+00:00'"}
	return driver
}

// quotedTable 是唯一允许把标识符放进 SQL 的出口。? 只能绑定“值”，不能绑定表名；
// 表名只接受本课 crypto/rand 生成的 24 位十六进制后缀，不能来自任意用户输入。
func quotedTable(name string) (string, error) {
	if !tableName.MatchString(name) {
		return "", errors.New("拒绝非本课生成的表标识符")
	}
	return "`" + name + "`", nil
}
