// Package config 只负责把入口选择的文件/环境转换成已验证的配置值。
// 它不创建连接、不启动服务器，也不把配置塞进请求 context。
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 是 composition root 保存的值；复制它会复制字符串和整数，不复制网络资源。
// Token 只供 router 构造教学身份校验；Safe 才是可以写日志/版本接口的字段集合。
type Config struct {
	Listen    string `json:"listen"`
	Version   string `json:"version"`
	Token     string `json:"token"`
	RequestMS int    `json:"request_timeout_ms"`
}

type Public struct {
	Listen    string `json:"listen"`
	Version   string `json:"version"`
	RequestMS int    `json:"request_timeout_ms"`
	Storage   string `json:"storage"`
}

// Load 由 cmd/demo 的入口调用。优先级是默认值 < 文件 < 已出现的环境变量。
// lookup 是读取环境的入口函数；测试可传局部 map 读取器，避免修改进程全局环境。
// path 为空就不读文件；成功只返回 Config，不持有文件，也没有任何 HTTP 副作用。
func Load(path string, lookup func(string) (string, bool)) (cfg Config, err error) {
	cfg = Config{Listen: "127.0.0.1:0", Version: "m19-local-v1", RequestMS: 250}
	if path != "" {
		file, openErr := os.Open(path)
		if openErr != nil {
			return Config{}, fmt.Errorf("open config: %w", openErr)
		}
		// file 取得成功就注册清理。具名 err 允许 defer 同时保留解码和 Close 两个原因。
		defer func() { err = errors.Join(err, file.Close()) }()
		data, readErr := io.ReadAll(io.LimitReader(file, 4097))
		if readErr != nil {
			return Config{}, fmt.Errorf("read config: %w", readErr)
		}
		if len(data) > 4096 {
			return Config{}, errors.New("config exceeds 4096 bytes")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		// if 的 := 创建仅在这个 if 中可见的 err；return 的 error 仍会赋给具名结果 err，
		// 因此外层 defer 能把 Close 错误与解码错误一起交给 Load 的 caller。
		if err := decoder.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("decode config: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return Config{}, errors.New("config must contain exactly one JSON object")
		}
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}
	for _, entry := range []struct {
		key    string
		target *string
	}{
		{"M19_LISTEN", &cfg.Listen}, {"M19_VERSION", &cfg.Version}, {"M19_TOKEN", &cfg.Token},
	} {
		if value, exists := lookup(entry.key); exists {
			*entry.target = value
		}
	}
	if value, exists := lookup("M19_REQUEST_TIMEOUT_MS"); exists {
		// Parse 错误不回显 value；配置可能从秘密文件或环境来，错误文案只给字段名。
		ms, parseErr := strconv.Atoi(value)
		if parseErr != nil {
			return Config{}, errors.New("M19_REQUEST_TIMEOUT_MS must be an integer")
		}
		cfg.RequestMS = ms
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate 同时由 Load 和 app.Build 调用，防止直接构造 Config 绕过必需项检查。
// 课程仅允许 loopback 和动态端口；这个限制属于实验范围，不是 Go 的服务器限制。
func (c Config) Validate() error {
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("listen must be a loopback host:port")
	}
	number, parseErr := strconv.Atoi(port)
	if parseErr != nil || number < 0 || number > 65535 {
		return errors.New("listen port must be within 0..65535")
	}
	if strings.TrimSpace(c.Version) == "" {
		return errors.New("version is required")
	}
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("token is required")
	}
	if c.RequestMS < 1 || c.RequestMS > 5000 {
		return errors.New("request_timeout_ms must be within 1..5000")
	}
	return nil
}

func (c Config) RequestTimeout() time.Duration { return time.Duration(c.RequestMS) * time.Millisecond }
func (c Config) Safe() Public {
	return Public{Listen: c.Listen, Version: c.Version, RequestMS: c.RequestMS, Storage: "process-memory"}
}
