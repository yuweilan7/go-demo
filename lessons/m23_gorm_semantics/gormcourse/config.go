// Package gormcourse 用真实 MySQL 展开 GORM 的业务副作用。
package gormcourse

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// endpoint/secret 只存在内存中，字段名按安装者 JSON 契约匹配。
// 非导出的 config 防止入口误打印 DSN；本课不复制秘密、不写接入索引。
type endpoint struct {
	State, Host    string
	Port           int
	Version        string `json:"server_version"`
	CredentialFile string `json:"credential_file"`
}
type secret struct{ Username, Password, Database string }
type config struct {
	mysql     endpoint
	sqlSecret secret
}

// loadConfig 由 Run 调用，只读取 READY 索引和其中登记的 MySQL 凭据。
// ReadFile 返回文件内容的 byte slice；Unmarshal 借用 target 指针填充结构体。
// 错误只报告来源类别，不输出文件内容、password 或 URI。
func loadConfig() (config, error) {
	path := os.Getenv("GOLEARN_SERVICE_INDEX")
	if path == "" {
		path = "D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json"
	}
	var index struct{ Services map[string]endpoint }
	if err := readJSON(path, &index); err != nil {
		return config{}, errors.New("读取服务索引失败")
	}
	mysql := index.Services["mysql"]
	if mysql.State != "READY" || mysql.Host != "127.0.0.1" || mysql.Port < 1 || mysql.Port > 65535 || mysql.Version == "" {
		return config{}, errors.New("只允许已登记 READY loopback MySQL")
	}
	c := config{mysql: mysql}
	if err := readJSON(c.mysql.CredentialFile, &c.sqlSecret); err != nil {
		return config{}, errors.New("读取 MySQL 私有凭据失败")
	}
	if c.sqlSecret.Database != "golearn_m20" || c.sqlSecret.Username == "" || c.sqlSecret.Password == "" {
		return config{}, errors.New("凭据不符合限定课程库契约")
	}
	return c, nil
}

// readJSON 是小配置文件的解码入口，不拥有返回后的数据库资源。
// os.ReadFile 内部关闭文件；我们限制正文尺寸，拒绝错误文件成为无限内存输入。
func readJSON(path string, target any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return errors.New("配置超过1MiB")
	}
	return json.Unmarshal(b, target)
}

// openPool 由 Run 唯一持有 Close 权；GORM 只借用此 *sql.DB。
// 使用 Connector 保留 NopLogger 等不编码到 DSN 的 driver 配置，不把密码拼进日志。
func openPool(c config) (*sql.DB, error) {
	d := mysqldriver.NewConfig()
	d.Net, d.Addr = "tcp", net.JoinHostPort(c.mysql.Host, strconv.Itoa(c.mysql.Port))
	d.User, d.Passwd, d.DBName = c.sqlSecret.Username, c.sqlSecret.Password, c.sqlSecret.Database
	d.ParseTime = true // DeletedAt 的 SQL datetime 要被 driver 读成 time.Time。
	d.Timeout, d.ReadTimeout, d.WriteTimeout = 3*time.Second, 8*time.Second, 3*time.Second
	d.MultiStatements, d.InterpolateParams, d.ClientFoundRows = false, false, false
	d.Logger = &mysqldriver.NopLogger{}
	d.Params = map[string]string{"time_zone": "'+00:00'"} // 仅本连接会话，绝非修改全局时区。
	connector, err := mysqldriver.NewConnector(d)
	if err != nil {
		return nil, fmt.Errorf("创建课程 connector: %w", err)
	}
	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(4) // holder + worker + observer最多3，绝不把共享32连接全部占满。
	pool.SetMaxIdleConns(1)
	return pool, nil
}
