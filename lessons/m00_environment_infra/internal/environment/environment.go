// Package environment 负责本课的只读工具链观察与 TCP 证据。
// 它不修改 go env，不读取密码，也不安装或管理中间件。
package environment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// Endpoint 是索引中的一个登记事实，不代表端口真的开放。
// ReadOnlyTCP 只允许 Redis/PG 的本机 loopback 探测；退休/待安装组件不能误连。
type Endpoint struct {
	ID          string `json:"id"`
	Address     string `json:"local_address"`
	State       string `json:"state"`
	ReadOnlyTCP bool   `json:"readonly_tcp"`
}

// Evidence 把 TCP 握手与协议、业务分开，防止“端口通了，所以数据库验收通过”。
// 本课的 TCP 函数永远不能把 Authenticated 或 Business 设置为 true。
type Evidence struct {
	Connected     bool
	Authenticated bool
	Business      bool
	Detail        string
}

// Describe 由 cmd/demo.run 调用，w 是调用方提供的终端 writer。
// 编译信息来自正在运行的二进制，Go env 则来自当前目录下一次 go 命令的选择；
// 两者来源不同，刻意同时展示，才能发现“旧二进制 + 新工作目录”的误判。
func Describe(ctx context.Context, w io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("读取进程工作目录: %w", err)
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Errorf("当前程序没有 Go build info")
	}
	fmt.Fprintf(w, "[二进制] version=%s target=%s/%s package=%s module=%s\n",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, info.Path, info.Main.Path)
	fmt.Fprintf(w, "[进程] cwd=%s\n", cwd)

	// CommandContext 不经过 shell，因此参数不会变成 PowerShell/bash 表达式。
	// Output 同步等待子进程退出；ctx 到期时命令被终止，再由 Output 收集退出状态。
	// 此子进程只读取安全白名单字段，避免全量 go env 把带凭据的代理 URL 打出来。
	command := exec.CommandContext(ctx, "go", "env", "-json", "GOMOD", "GOWORK", "GOVERSION", "GOTOOLCHAIN", "CGO_ENABLED")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("等待只读 go env 子进程: %w", err)
	}
	var values map[string]string // JSON 对象解码后成为本次调用独占的 map，没有跨 goroutine 共享。
	if err := json.Unmarshal(output, &values); err != nil {
		return fmt.Errorf("解析 go env 输出: %w", err)
	}
	fmt.Fprintf(w, "[下一条 Go 命令] GOMOD=%s GOWORK=%q GOVERSION=%s GOTOOLCHAIN=%s CGO_ENABLED=%s\n",
		values["GOMOD"], values["GOWORK"], values["GOVERSION"], values["GOTOOLCHAIN"], values["CGO_ENABLED"])
	fmt.Fprintln(w, "[层次] module 管依赖和版本；package 是目录中共同编译的源码；main 包才有程序入口。")
	return nil
}

// ReadEndpoints 由 run 调用，只把无秘密 JSON 索引读入内存。
// 文件句柄由 os.ReadFile 在返回前关闭；Endpoint 是登记值副本，不是连接对象。
func ReadEndpoints(path string) ([]Endpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取端点索引: %w", err)
	}
	var index struct {
		Endpoints []Endpoint `json:"endpoints"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("解析端点索引: %w", err)
	}
	if err := ValidateEndpoints(index.Endpoints); err != nil {
		return nil, err
	}
	return index.Endpoints, nil
}

// ValidateEndpoints 在任何拨号前检查本课的权限边界。
// 调用者传入的 slice 是对数组的视图，本函数只读；seen 是本次校验私有的 map。
// 校验失败直接返回，不允许把配置错误变成对任意公网/退休服务的探测。
func ValidateEndpoints(endpoints []Endpoint) error {
	seen := make(map[string]bool)
	for _, endpoint := range endpoints {
		if endpoint.ID == "" || seen[endpoint.ID] {
			return fmt.Errorf("端点 id 为空或重复")
		}
		seen[endpoint.ID] = true
		if !endpoint.ReadOnlyTCP {
			continue
		}
		if endpoint.ID != "redis" && endpoint.ID != "postgres" {
			return fmt.Errorf("%s 不属于 M00 只读 TCP 范围", endpoint.ID)
		}
		host, port, err := net.SplitHostPort(endpoint.Address)
		if err != nil || host != "127.0.0.1" || strings.TrimSpace(port) == "" {
			return fmt.Errorf("%s 必须显式使用 127.0.0.1:port", endpoint.ID)
		}
	}
	return nil
}

// ProbeTCP 由 run 或独立测试同步调用。address 已被入口校验为本机 loopback。
// DialContext 是实际网络等待点；即使 TCP 成功，也没有发 AUTH/SQL 或任何业务写入。
// conn 由本函数独占，返回前关闭；调用方只获得 Evidence 值，不能继续复用连接。
func ProbeTCP(ctx context.Context, address string) Evidence {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		// 这里不用打印服务返回正文或秘密。net.OpError 的类别足够定位连接层，
		// 更具体的私有诊断交给显式基础设施探针，不把失败改称健康。
		return Evidence{Detail: fmt.Sprintf("connect failed (%T)", err)}
	}
	if err := conn.Close(); err != nil {
		return Evidence{Connected: true, Detail: "handshake succeeded; close reported an error"}
	}
	return Evidence{Connected: true, Detail: "handshake succeeded; connection closed; no protocol command sent"}
}
