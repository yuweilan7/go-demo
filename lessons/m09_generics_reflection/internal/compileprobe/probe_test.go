package compileprobe_test

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"go-demo/lessons/m09_generics_reflection/internal/compileprobe"
)

// TestCompilerBoundaries 对真实 Go 编译器验证接受/拒绝矩阵。
// 临时片段和目标文件只写本模块 .local，Run 返回前负责清理；测试不启动故障程序。
func TestCompilerBoundaries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := compileprobe.Run(ctx, filepath.Join("..", "..", ".local"), io.Discard); err != nil {
		t.Fatal(err)
	}
}
