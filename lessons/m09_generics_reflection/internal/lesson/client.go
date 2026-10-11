package lesson

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"time"
)

// Config 也提供显式构造途径，适合已从 YAML/JSON 读入完整配置的调用者。
// string/Duration 都是值；Headers 是 map 值，结构体复制不会复制底层 map。
type Config struct {
	Endpoint string
	Timeout  time.Duration
	Headers  map[string]string
}

// Client 只模拟“配置构造”而非真实 HTTP 请求，避免把构造成功误当业务通信成功。
// cfg 不导出；安全构造器和 Snapshot 在两侧复制 map，调用者拿不到内部别名。
type Client struct{ cfg Config }

// Option 是命名函数类型，参数是本次构造的 Config 指针，error 交还 NewClient。
// WithXXX 返回函数值时尚未配置 Client；只有 NewClient 逐个调用时才修改 cfg。
type Option func(*Config) error

// DefaultConfig 返回独立默认值，不共享全局 map；NewClient 每次从此处开始。
func DefaultConfig() Config {
	return Config{Endpoint: "https://orders.example.test", Timeout: 2 * time.Second}
}

// NewClient 由 demo 调用。opts ...Option 表示可变参数，函数内 opts 是 []Option。
// config 指针仅在同步应用选项期间交给闭包；本 API 约定 Option 不能私存此指针。
// 后一个选项可以覆盖前一个，全部应用后才校验最终配置，不启动 I/O 或 goroutine。
func NewClient(opts ...Option) (*Client, error) {
	cfg := DefaultConfig()
	for index, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("option[%d]: nil option", index)
		}
		if err := opt(&cfg); err != nil {
			return nil, fmt.Errorf("option[%d]: %w", index, err)
		}
	}
	return NewClientConfig(cfg)
}

// NewClientConfig 由 NewClient 和显式 Config 路径共用。cfg 为结构体值副本。
// 校验成功后复制 map，构造后的 Client 不受原 Headers 后续修改影响。
// 它无法保证任意恶意 Option 不偷偷保存 *Config；上述 Option 约定仍是边界。
func NewClientConfig(cfg Config) (*Client, error) {
	parsed, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("endpoint: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("endpoint 必须包含 http(s) scheme 和 host")
	}
	if cfg.Timeout <= 0 {
		return nil, errors.New("timeout 必须大于 0")
	}
	cfg.Headers = maps.Clone(cfg.Headers) // Clone 是浅复制；string 元素无可变对象别名。
	return &Client{cfg: cfg}, nil
}

// WithEndpoint 捕获本次调用的 string 值；调用者重绑自己的变量不会重绑此参数。
// 闭包只在 NewClient 调用时写入指向的构造配置；最终 URL 校验由构造器负责。
func WithEndpoint(endpoint string) Option {
	return func(cfg *Config) error {
		cfg.Endpoint = endpoint
		return nil
	}
}

// WithTimeout 捕获 duration 值。允许先写 0 再用后一个有效选项覆盖，校验在最后。
func WithTimeout(timeout time.Duration) Option {
	return func(cfg *Config) error {
		cfg.Timeout = timeout
		return nil
	}
}

// WithHeadersBad 故意保留危险写法：捕获的是 map 描述值，与调用者指向同一底层 map。
// 外部修改元素后，闭包应用时读到修改后的数据；值传递没有产生 map 内容快照。
// NewClientConfig 的最后复制能隔离构造之后的修改，却不能修复“创建 Option 到应用”窗口。
func WithHeadersBad(headers map[string]string) Option {
	return func(cfg *Config) error {
		cfg.Headers = headers
		return nil
	}
}

// WithHeaders 是修复：选项创建时就克隆 map，固定选项所代表的配置快照。
// 每次应用时再克隆，让同一个 Option 复用到两个构造器时也不分享可变 map。
// 克隆并非任意深复制；本 API 限定 map[string]string，因此浅复制已经足够。
func WithHeaders(headers map[string]string) Option {
	snapshot := maps.Clone(headers)
	return func(cfg *Config) error {
		cfg.Headers = maps.Clone(snapshot)
		return nil
	}
}

// NewClientBadAlias 显式故障入口：省略最终快照，使 Client 与调用者继续分享 Headers。
// 它仅用于 demo 对照和断言；正常构造全部走 NewClient/NewClientConfig。
func NewClientBadAlias(headers map[string]string) *Client {
	cfg := DefaultConfig()
	cfg.Headers = headers
	return &Client{cfg: cfg}
}

// Snapshot 由 demo 读取配置，返回值里的 map 也复制，避免 getter 暴露内部 map。
// c 是复制的指针，仍指向同一个 Client；这里只读 cfg，不修改它。
func (c *Client) Snapshot() Config {
	cfg := c.cfg
	cfg.Headers = maps.Clone(cfg.Headers)
	return cfg
}
