// Package compileprobe 用有界编译器子进程验证语言版本边界。
// 故意不能编译的片段仅存在于字符串中，不污染正常包的 go test ./...。
package compileprobe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// probe 描述单个独立语法/类型边界，source 没有导入，只依赖语言内建规则。
type probe struct {
	name   string
	lang   string
	source string
	accept bool
}

// Run 由 cmd/compilecheck 和 TestCompilerBoundaries 调用。
// base 是本模块 .local；每个编译过程最多 8 秒，并由入口等待返回和清理生成文件。
// -lang=go1.26 用当前编译器检查旧语言模式，不声称执行了 Go 1.26 二进制。
func Run(parent context.Context, base string, out io.Writer) (runErr error) {
	base, err := filepath.Abs(base)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(base, "compilecheck-")
	if err != nil {
		return err
	}
	// 递归删除之前核对绝对路径确实是 base 的子目录，而且不能是 base 本身。
	relative, err := filepath.Rel(base, dir)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("临时目录不在授权 base 中: %q", dir)
	}
	defer func() { runErr = errors.Join(runErr, os.RemoveAll(dir)) }()

	method := `package probe
	type Result[T any] struct{ Value T }
	func (r Result[T]) Map[U any](f func(T) U) Result[U] { return Result[U]{f(r.Value)} }
	var _ = Result[int]{1}.Map(func(n int) string { return "order" })
	`
	probes := []probe{
		{"method-go127", "go1.27", method, true},
		{"method-go126", "go1.26", method, false},
		// 修复旧项目：把需要额外 U 的转换放在包级函数，保留 Go 1.18 起的泛型语义。
		{"function-go126", "go1.26", `package probe
		type Result[T any] struct{ Value T }
		func Map[T,U any](r Result[T], f func(T) U) Result[U] { return Result[U]{f(r.Value)} }
		var _ = Map(Result[int]{1}, func(n int) string { return "order" })`, true},
		// 即使 Go 1.27，接口方法自己的类型参数仍被拒绝。
		{"generic-interface-method", "go1.27", `package probe
		type Mapper interface { Map[T any](T) T }`, false},
		// 普通接口需要一个具体签名；泛型方法声明不能自动充当 Map(int) int。
		{"generic-method-implements-interface", "go1.27", `package probe
		type Mapper interface { Map(int) int }
		type Client struct{}
		func (Client) Map[T any](x T) T { return x }
		var _ Mapper = Client{}`, false},
		{"type-set-as-value", "go1.27", `package probe
		type Amount interface { ~int | ~int64 }
		var _ Amount`, false},
		{"slice-not-comparable", "go1.27", `package probe
		func Key[T comparable](v T) T { return v }
		var _ = Key([]int{1})`, false},
		{"exact-type-excludes-defined-type", "go1.27", `package probe
		type Cents int64
		func Amount[T int64](v T) T { return v }
		var _ = Amount(Cents(12))`, false},
		{"tilde-allows-defined-type", "go1.27", `package probe
		type Cents int64
		func Amount[T ~int64](v T) T { return v }
		var _ = Amount(Cents(12))`, true},
		// 本调用没有实参，目标变量的 int 并不能推导仅位于返回值的 T。
		{"return-only-inference", "go1.27", `package probe
		func Empty[T any]() T { var zero T; return zero }
		var _ int = Empty()`, false},
		{"explicit-type-fixes-inference", "go1.27", `package probe
		func Empty[T any]() T { var zero T; return zero }
		var _ int = Empty[int]()`, true},
	}
	for _, item := range probes {
		path := filepath.Join(dir, item.name+".go")
		if err := os.WriteFile(path, []byte(item.source), 0o600); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(parent, 8*time.Second)
		command := exec.CommandContext(ctx, "go", "tool", "compile", "-lang="+item.lang,
			"-o", filepath.Join(dir, item.name+".o"), path)
		command.Dir = dir
		output, compileErr := command.CombinedOutput() // 当前入口同步等候这个编译子进程结束。
		cause := ctx.Err()
		cancel() // 每次编译结束立即释放 timeout timer，不把 11 个 defer 积在循环里。
		if cause != nil {
			return fmt.Errorf("%s compiler timeout/cancel: %w", item.name, cause)
		}
		if compileErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(compileErr, &exitErr) {
				return fmt.Errorf("%s 启动编译器失败: %w", item.name, compileErr)
			}
		}
		accepted := compileErr == nil
		if accepted != item.accept {
			return fmt.Errorf("%s accept=%t want=%t diagnostics=%s", item.name, accepted, item.accept, output)
		}
		fmt.Fprintf(out, "COMPILE %s lang=%s accepted=%t expected=%t PASS\n", item.name, item.lang, accepted, item.accept)
		if !accepted {
			// 只把临时根目录换成相对提示，保留编译器真实原因，不依赖完整错误文案断言。
			fmt.Fprintln(out, strings.ReplaceAll(strings.TrimSpace(string(output)), dir, "<scratch>"))
		}
	}
	fmt.Fprintln(out, "COMPILE all 11 boundaries PASS; scratch cleanup on return")
	return nil
}
