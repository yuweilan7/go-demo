package lab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// RunChild 只由cmd/fault的显式入口调用，不由正常HTTP服务暴露。每个进程只有一个故障。
// 子进程用真实loopback请求进入装有Recovery的Gin，再由当前handler创建另一个goroutine。
// panic发生在新goroutine，Recovery无法跨栈recover，runtime终止整个子进程。
func RunChild(mode string) (err error) {
	gin.SetMode(gin.ReleaseMode)
	root, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	engine := gin.New()
	if err := engine.SetTrustedProxies(nil); err != nil {
		return err
	}
	engine.Use(gin.RecoveryWithWriter(nil))
	engine.GET("/fault", func(c *gin.Context) {
		switch mode {
		case "goroutine-panic":
			go func() { panic("M18_CHILD_GOROUTINE_PANIC") }()
		case "copy-write":
			copy := c.Copy()
			// 故意不recover：Copy的writer没有底层ResponseWriter，写JSON会panic。
			// 原c不会被此goroutine读取；复制是在当前handler同步完成的。
			go func() { copy.JSON(200, Envelope{Code: "M18_COPY_WRITE"}) }()
		default:
			panic("未知故障入口")
		}
		// 当前请求goroutine只等待本进程预算，故不会把handler返回与panic发生赛跑。
		// 正常情况下runtime先因另一goroutine的panic退出；owner在外部限制整个子进程。
		<-root.Done()
	})
	endpoint, err := newEndpoint(root, engine)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, endpoint.Close()) }()
	client := NewClient()
	defer client.Close()
	_, err = client.Request(root, http.MethodGet, endpoint.URL+"/fault", "", "", "", "fault", nil)
	return err
}

type FaultResult struct {
	Mode   string
	Exit   int
	Marker bool
}

// Faults 由demo或独立测试调用；先把cmd/fault构建到本module已忽略的.local，再逐个执行。
// CommandContext限定每个子进程8秒，Run会等待进程退出并回收它；超时和非预期退出都报错。
// runtime.Caller定位当前源码所在module，避免go test在internal/lab工作目录把产物写到别处。
func Faults(parent context.Context) (results []FaultResult, err error) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return nil, errors.New("无法定位module")
	}
	module := filepath.Dir(filepath.Dir(filepath.Dir(source)))
	local := filepath.Join(module, ".local")
	if err := os.MkdirAll(local, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(local, "fault-")
	if err != nil {
		return nil, err
	}
	// Windows收尾前核对最终绝对目标仍在本module的.local里；不把可变工作目录或外部参数
	// 拼成递归删除目标。MkdirTemp返回的是刚由此owner创建的独占目录。
	relative, relativeErr := filepath.Rel(local, dir)
	if relativeErr != nil || !filepath.IsLocal(relative) || !strings.HasPrefix(filepath.Base(dir), "fault-") {
		return nil, fmt.Errorf("拒绝清理越界临时目录: relative=%q err=%v", relative, relativeErr)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }() // dir由当前owner创建且只含自身binary。
	binary := filepath.Join(dir, "fault.exe")
	buildCtx, cancelBuild := context.WithTimeout(parent, 60*time.Second)
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, "./cmd/fault")
	build.Dir = module
	output, buildErr := build.CombinedOutput()
	cancelBuild()
	if buildErr != nil {
		return nil, fmt.Errorf("构建故障子进程: %w: %s", buildErr, output)
	}
	for _, mode := range []string{"goroutine-panic", "copy-write"} {
		ctx, cancel := context.WithTimeout(parent, 8*time.Second)
		command := exec.CommandContext(ctx, binary, mode)
		output, runErr := command.CombinedOutput() // 当前owner在这里等待实际进程退出。
		timedOut := ctx.Err() != nil
		cancel()
		var exit *exec.ExitError
		if timedOut || !errors.As(runErr, &exit) {
			return results, fmt.Errorf("故障应异常退出而非超时: mode=%s err=%v output=%s", mode, runErr, output)
		}
		marker := strings.Contains(string(output), "panic:")
		if mode == "goroutine-panic" {
			marker = marker && strings.Contains(string(output), "M18_CHILD_GOROUTINE_PANIC")
		}
		if mode == "copy-write" {
			marker = marker && strings.Contains(string(output), "gin") && strings.Contains(string(output), "responseWriter")
		}
		if !marker {
			return results, fmt.Errorf("故障来源不符: mode=%s output=%s", mode, output)
		}
		results = append(results, FaultResult{Mode: mode, Exit: exit.ExitCode(), Marker: marker})
	}
	return results, nil
}
