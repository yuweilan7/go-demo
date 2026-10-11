// Package lab 把课堂编排、真实 AMQP 资源和本进程业务账本放在一起。
// 这里的 Go channel 只传递停止/屏障信号；消息队列始终是远端 RabbitMQ。
package lab

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Config 是入口从安装者的只读索引得到的连接信息。Password 不导出日志；
// URL 在内存中临时构造，不能将 Config 或 URL 直接用 %+v 打印。
type Config struct {
	Host          string
	Port          int
	User          string
	Password      string
	Vhost         string
	ServerVersion string
}

// LoadConfig 由 cmd/demo 和显式 integration test 调用。安装者拥有索引/秘密文件，
// 本函数只读；失败返回不含文件正文、密码或连接 URI 的错误给入口。
func LoadConfig() (Config, error) {
	path := os.Getenv("GOLEARN_SERVICE_INDEX")
	if path == "" {
		path = "D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, errors.New("无法读取安装者服务索引，请确认 RabbitMQ READY")
	}
	// 索引使用 services.rabbitmq；同时接受以 rabbitmq 为顶层字段的精简登记。
	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil {
		return Config{}, errors.New("服务索引不是有效 JSON")
	}
	raw := root["rabbitmq"]
	if s := root["services"]; s != nil {
		var services map[string]json.RawMessage
		if err := json.Unmarshal(s, &services); err != nil {
			return Config{}, errors.New("services 字段必须是对象")
		}
		raw = services["rabbitmq"]
	}
	var record struct {
		State          string `json:"state"`
		Host           string `json:"host"`
		Port           int    `json:"port"`
		ServerVersion  string `json:"server_version"`
		CredentialFile string `json:"credential_file"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &record) != nil {
		return Config{}, errors.New("索引缺少有效 rabbitmq 登记")
	}
	if record.State != "READY" {
		return Config{}, fmt.Errorf("RabbitMQ state=%q，真实实验尚不能运行", record.State)
	}
	// 只连接安装者提供的本机 loopback 隧道，拒绝把合成故障指向未登记主机。
	if ip := net.ParseIP(record.Host); ip == nil || !ip.IsLoopback() || record.Port <= 0 || record.Port > 65535 {
		return Config{}, errors.New("RabbitMQ 登记必须是有效 loopback 地址/端口")
	}
	b, err = os.ReadFile(record.CredentialFile)
	if err != nil {
		return Config{}, errors.New("无法读取 RabbitMQ 私有凭据文件")
	}
	var secret struct {
		Username string `json:"username"`
		User     string `json:"user"`
		Password string `json:"password"`
		Vhost    string `json:"vhost"`
	}
	if json.Unmarshal(b, &secret) != nil {
		return Config{}, errors.New("RabbitMQ 私有凭据 JSON 结构无效")
	}
	if secret.Username == "" {
		secret.Username = secret.User
	}
	if secret.Username == "" || secret.Password == "" || secret.Vhost == "" {
		return Config{}, errors.New("RabbitMQ 凭据缺少 username/password/vhost")
	}
	// 字段名写在字面量里，让连接配置的来源清楚，不靠字段排列顺序猜含义。
	return Config{
		Host: record.Host, Port: record.Port, User: secret.Username,
		Password: secret.Password, Vhost: secret.Vhost, ServerVersion: record.ServerVersion,
	}, nil
}

// uri 仅给DialConfig使用。官方URI.String转义账号/密码/vhost，并保留vhost的
// 前导slash：/golearn_m28与golearn_m28是两个不同vhost，不能凭普通URL.Path猜映射。
func (c Config) uri() string {
	u := amqp.URI{Scheme: "amqp", Host: c.Host, Port: c.Port, Username: c.User, Password: c.Password, Vhost: c.Vhost}
	return u.String()
}
