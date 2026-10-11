package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"go-demo/lessons/m04_interfaces_nil/internal/adapter"
)

// TestFileFailures 直接调用真实文件适配器，证明底层 I/O/解码原因仍被 %w 保留。
// 只读已有合成 fixture，不创建连接、临时文件或必须由别的 agent 清理的数据。
func TestFileFailures(t *testing.T) {
	missing, err := adapter.NewRepository("../../testdata/no-such-order-file.json")
	if err != nil {
		t.Fatal(err)
	}
	_, err = missing.Find(context.Background(), "ORDER-1001")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file error cause lost: %v", err)
	}
	invalid, err := adapter.NewRepository("../../testdata/invalid.json")
	if err != nil {
		t.Fatal(err)
	}
	_, err = invalid.Find(context.Background(), "ORDER-1001")
	var decodeError *json.UnmarshalTypeError
	if !errors.As(err, &decodeError) {
		t.Fatalf("expected actual JSON type error, got: %v", err)
	}
	if repo, err := adapter.NewRepository(""); repo != nil || err == nil {
		t.Fatalf("empty file configuration accepted: repo=%v err=%v", repo, err)
	}
}
