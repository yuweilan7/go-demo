package course

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
)

// Config 是无秘密端点与内存中凭据的组合；禁止用 %+v 打印它。
// SASL 三字段只用于构造驱动，不写入日志、README 或调度状态。
type Config struct {
	Address, ServerVersion        string
	Mechanism, Username, Password string
}

// DefaultIndex 由 CLI 调用；环境变量覆盖入口适合独立 worktree 和以后迁移的路径。
// M00 的旧 endpoints.json 只是登记基线，本课读取安装者实时发布的服务索引。
func DefaultIndex() string {
	if path := os.Getenv("GOLEARN_SERVICE_INDEX"); path != "" {
		return path
	}
	return "D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json"
}

// ReadConfig 由 Run 同步调用。ReadFile 关闭句柄后才返回字节；Decode 的 &index
// 让解码器写入本函数的结构体。PENDING 在创建 Kafka client 之前拒绝。
func ReadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取服务索引: %w", err)
	}
	var index struct {
		Services map[string]struct {
			State          string `json:"state"`
			Host           string `json:"host"`
			Port           int    `json:"port"`
			ServerVersion  string `json:"server_version"`
			CredentialFile string `json:"credential_file"`
		} `json:"services"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return Config{}, fmt.Errorf("服务索引 JSON 格式错误")
	}
	s := index.Services["kafka"] // map 的读取给结构体值副本；没有连接或安装副作用。
	if s.State != "READY" {
		return Config{}, fmt.Errorf("Kafka state=%q；等待唯一安装者发布 READY，未创建客户端", s.State)
	}
	if s.Host != "127.0.0.1" || s.Port < 1 || s.Port > 65535 || s.ServerVersion == "" {
		return Config{}, fmt.Errorf("学习 Kafka 必须登记完整版本及 127.0.0.1:有效端口")
	}
	cfg := Config{Address: net.JoinHostPort(s.Host, fmt.Sprint(s.Port)), ServerVersion: s.ServerVersion, Mechanism: "none"}
	if s.CredentialFile == "" {
		return cfg, nil
	}
	private, err := os.ReadFile(s.CredentialFile)
	if err != nil {
		return Config{}, fmt.Errorf("读取独立凭据文件失败（不输出内容）: %w", err)
	}
	var auth struct {
		Mechanism string `json:"sasl_mechanism"`
		Username  string `json:"username"`
		Password  string `json:"password"`
	}
	if err := json.Unmarshal(private, &auth); err != nil {
		return Config{}, fmt.Errorf("凭据文件 JSON 格式错误（不输出内容）")
	}
	cfg.Mechanism = strings.ToLower(auth.Mechanism)
	cfg.Username, cfg.Password = auth.Username, auth.Password
	switch cfg.Mechanism {
	case "none":
	case "plain", "scram-sha-256", "scram-sha-512":
		if cfg.Username == "" || cfg.Password == "" {
			return Config{}, fmt.Errorf("SASL 凭据不完整")
		}
	default:
		return Config{}, fmt.Errorf("不支持的 SASL 机制；检查私有文件")
	}
	return cfg, nil
}
