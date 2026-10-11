package lesson

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

// Config 是 LoadConfig 返回给入口的一份值。秘密字段不导出、不提供 String，
// 报告只输出端点/版本，不能 fmt.Printf 整个 config 或 driver DSN。
type Config struct {
	RedisAddress, MySQLAddress string
	RedisVersion, MySQLVersion string
	redisUser, redisPassword   string
	mysqlUser, mysqlPassword   string
	database                   string
}

// DefaultIndex 只选择读取位置，不在本课写基础设施索引、创建隧道或安装服务。
func DefaultIndex() string {
	if path := os.Getenv("GOLEARN_SERVICE_INDEX"); path != "" {
		return path
	}
	return "D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json"
}

// LoadConfig 在网络调用前核对两个 READY 条目，并从其 credential_file 加载秘密。
// caller 为入口/真实集成测试；密码留在内存中交给真实驱动，不写本课配置副本。
func LoadConfig(path string) (Config, error) {
	var index struct {
		Services map[string]struct {
			State, Host    string
			Port           int
			Version        string `json:"server_version"`
			CredentialFile string `json:"credential_file"`
		} `json:"services"`
	}
	if err := readJSON(path, &index); err != nil {
		return Config{}, fmt.Errorf("读取服务索引: %w", err)
	}
	config := Config{}
	for _, name := range []string{"redis", "mysql"} {
		service, ok := index.Services[name]
		if !ok || service.State != "READY" || service.Host != "127.0.0.1" ||
			service.Port < 1 || service.Port > 65535 || service.Version == "" || service.CredentialFile == "" {
			return Config{}, fmt.Errorf("%s 必须 READY 且为安装者登记的IPv4 loopback入口", name)
		}
		secretPath := service.CredentialFile
		if !filepath.IsAbs(secretPath) {
			secretPath = filepath.Join(filepath.Dir(path), secretPath)
		}
		var secret struct {
			User, Username, Password, Database string
			DB                                 int
		}
		if err := readJSON(secretPath, &secret); err != nil {
			// 解码错误可能包含片段，因此只输出阶段，不包装秘密文件错误。
			return Config{}, fmt.Errorf("%s 私有凭据读取/解析失败（不输出正文）", name)
		}
		address := net.JoinHostPort(service.Host, strconv.Itoa(service.Port))
		if secret.Password == "" {
			return Config{}, fmt.Errorf("%s 私有凭据password为空", name)
		}
		if name == "redis" {
			if secret.DB != 0 {
				return Config{}, errors.New("Redis仅允许已登记DB0")
			}
			config.RedisAddress, config.RedisVersion = address, service.Version
			config.redisUser, config.redisPassword = secret.Username, secret.Password
		} else {
			if secret.User == "" {
				secret.User = secret.Username
			}
			if secret.User == "" || secret.Database != "golearn_m20" {
				return Config{}, errors.New("MySQL仅允许课程账号和golearn_m20，本课使用独占m27表")
			}
			config.MySQLAddress, config.MySQLVersion = address, service.Version
			config.mysqlUser, config.mysqlPassword, config.database = secret.User, secret.Password, secret.Database
		}
	}
	return config, nil
}

// readJSON 同步读小配置并解码到 caller 的指针。os.ReadFile 的文件句柄由标准库
// 在返回前关闭；不把密码放进任何错误/输出。本课私有文件不是用户上传的大文件。
func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("JSON配置超过1MiB")
	}
	if json.Unmarshal(data, target) != nil {
		return errors.New("JSON配置格式错误")
	}
	return nil
}
