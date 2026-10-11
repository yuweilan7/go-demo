package lesson

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// 这个教学harness与现有package main的Pool在同一个临时编译包内，才能观察包内锁与队列。
// 嵌入的是本课手写harness，原pool.go每次从仓库读取，逐字节复制，不修改其逻辑。
//
//go:embed legacy_harness.txt
var legacyHarness []byte

// LegacyProbe 是默认入口第一组实际反例。moduleDir来自入口确定的本课绝对目录。
// 原示例不能直接import(package main)，所以只在本课.local临时目录编译原文件+harness。
// build/child都有独立上限，不通过shell启动，不遗留go run孙进程。Run本身Wait并回收child。
func LegacyProbe(parent context.Context, moduleDir string) (output string, resultErr error) {
	sourcePath := filepath.Clean(filepath.Join(moduleDir, "..", "..", "examples", "advanced-syntax", "goroutine-pool", "pool.go"))
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	local := filepath.Join(moduleDir, ".local")
	if err := os.MkdirAll(local, 0700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(local, "legacy-")
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(stage)) }() // stage已解析为本课.local内新建路径。
	if err := os.WriteFile(filepath.Join(stage, "pool.go"), source, 0600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(stage, "harness.go"), legacyHarness, 0600); err != nil {
		return "", err
	}
	executable := filepath.Join(stage, "legacy")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	buildCtx, cancelBuild := context.WithTimeout(parent, 30*time.Second)
	defer cancelBuild()
	args := []string{"build"}
	// race构建标签由go test/run -race设置；child也按同样参数编译，不能只检测父进程。
	if legacyRace {
		args = append(args, "-race")
	}
	args = append(args, "-o", executable, "pool.go", "harness.go")
	build := exec.CommandContext(buildCtx, "go", args...)
	build.Dir = stage
	buildStarted := time.Now()
	if raw, err := build.CombinedOutput(); err != nil {
		return string(raw), fmt.Errorf("原Pool+harness build: %w", err)
	}
	// race二进制的Windows启动/检测器退出不属于harness的业务等待；给进程监督20秒。
	// main实际开始后，harness另有4秒观察和各2秒救援预算，不能把启动慢误判成Pool死锁。
	childCtx, cancelChild := context.WithTimeout(parent, 20*time.Second)
	defer cancelChild()
	child := exec.CommandContext(childCtx, executable)
	child.Dir = stage
	// 只有child进程使用退出等待设置；继承其他GORACE选项，保留默认race错误码66。
	if legacyRace {
		child.Env = append(os.Environ(), "GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0")
	}
	var raw bytes.Buffer
	child.Stdout = &raw
	child.Stderr = &raw
	childStarted := time.Now()
	err = child.Run()
	output = fmt.Sprintf("legacy source sha256=%x race=%t build-and-run=%s child=%s child-ctx=%v\n%s", sha256.Sum256(source), legacyRace, time.Since(buildStarted).Round(time.Millisecond), time.Since(childStarted).Round(time.Millisecond), childCtx.Err(), raw.String())
	if err != nil {
		return output, fmt.Errorf("原Pool child: %w", err)
	}
	return output, nil
}
