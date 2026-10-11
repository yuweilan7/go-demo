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

// Config 是单次 Run 读取的配置值副本。Password 只在内存传给驱动，
// json:"-" 防止误把结构体作为报告序列化时输出秘密；本课也不 fmt.Printf 整个 Config。
type Config struct {
	Address       string `json:"address"`
	ServerVersion string `json:"server_version"`
	Index         string `json:"index"`
	Username      string `json:"-"`
	Password      string `json:"-"`
	DB            int    `json:"db"`
}

// serviceIndex 匹配主 agent 唯一写入的私有索引；本模块只读取 redis 条目。
// json tag 连接 JSON 字段名与 Go 导出字段名，空指针字段在 PENDING 时不会变成伪造端点。
type serviceIndex struct {
	Services map[string]struct {
		State          string `json:"state"`
		Host           string `json:"host"`
		Port           int    `json:"port"`
		ServerVersion  string `json:"server_version"`
		CredentialFile string `json:"credential_file"`
	} `json:"services"`
}

// DefaultIndex 被入口调用，环境变量只改变读取位置，不写共享配置或开启隧道。
func DefaultIndex() string {
	if path := os.Getenv("GOLEARN_SERVICE_INDEX"); path != "" {
		return path
	}
	return "D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json"
}

// LoadConfig 在任何网络操作前读取索引和独立秘密 JSON；caller 是入口或真实集成测试。
// 错误只给阶段/字段名：JSON 解析错误可能包含输入片段，所以秘密解析错误不原样打印。
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取服务索引失败：%w", err)
	}
	var index serviceIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return Config{}, errors.New("服务索引 JSON 格式错误")
	}
	service, ok := index.Services["redis"]
	if !ok || service.State != "READY" {
		return Config{}, errors.New("Redis 索引尚未 READY；请由基础设施 owner 完成真实接入")
	}
	// 这是课程授权边界，不是 net.Dial 失败后的补救。只承认明确 IPv4 SSH loopback。
	if service.Host != "127.0.0.1" || service.Port < 1 || service.Port > 65535 {
		return Config{}, errors.New("Redis 入口必须是已登记的 127.0.0.1:合法端口")
	}
	if service.CredentialFile == "" || service.ServerVersion == "" {
		return Config{}, errors.New("READY 条目缺 credential_file 或 server_version")
	}
	credentialPath := service.CredentialFile
	if !filepath.IsAbs(credentialPath) {
		credentialPath = filepath.Join(filepath.Dir(path), credentialPath)
	}
	secretData, err := os.ReadFile(credentialPath)
	if err != nil {
		return Config{}, errors.New("读取 Redis 独立秘密文件失败")
	}
	var secret struct {
		Username string `json:"username"`
		Password string `json:"password"`
		DB       int    `json:"db"`
	}
	if err := json.Unmarshal(secretData, &secret); err != nil {
		return Config{}, errors.New("Redis 秘密 JSON 格式错误（不输出正文）")
	}
	if secret.Password == "" || secret.DB != 0 {
		return Config{}, errors.New("课程要求非空 password 与已登记 DB0；不能自行换库")
	}
	return Config{Address: net.JoinHostPort(service.Host, strconv.Itoa(service.Port)),
		ServerVersion: service.ServerVersion, Index: path,
		Username: secret.Username, Password: secret.Password, DB: secret.DB}, nil
}
