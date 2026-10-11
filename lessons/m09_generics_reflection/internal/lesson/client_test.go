package lesson_test

import (
	"errors"
	"testing"
	"time"

	"go-demo/lessons/m09_generics_reflection/internal/lesson"
)

// TestOptionsSnapshotWindows 独立观察三项业务事实：创建时快照、构造后隔离、getter 隔离。
// 同一个选项复用到多个 Client 也应保持创建时的 sg，不受外部 map 改动影响。
func TestOptionsSnapshotWindows(t *testing.T) {
	headers := map[string]string{"X-Region": "sg"}
	badOption := lesson.WithHeadersBad(headers)
	safeOption := lesson.WithHeaders(headers)
	headers["X-Region"] = "before-build"
	bad, err := lesson.NewClient(badOption)
	if err != nil {
		t.Fatal(err)
	}
	safe, err := lesson.NewClient(safeOption)
	if err != nil {
		t.Fatal(err)
	}
	alias := lesson.NewClientBadAlias(headers)
	headers["X-Region"] = "after-build"
	if bad.Snapshot().Headers["X-Region"] != "before-build" || alias.Snapshot().Headers["X-Region"] != "after-build" {
		t.Fatal("两个危险窗口应分别可观察，最终 Clone 只修复构造后窗口")
	}
	view := safe.Snapshot()
	view.Headers["X-Region"] = "tampered"
	if safe.Snapshot().Headers["X-Region"] != "sg" {
		t.Fatal("安全 Client 不能受输入或 getter map 修改影响")
	}
	second, err := lesson.NewClient(safeOption)
	if err != nil || second.Snapshot().Headers["X-Region"] != "sg" {
		t.Fatalf("Option 复用应继续使用创建时快照: err=%v", err)
	}
}

// TestOptionsOrderAndValidation 验证顺序/默认值/错误链；最终校验让后一个有效值覆盖前一个 0。
func TestOptionsOrderAndValidation(t *testing.T) {
	defaults, err := lesson.NewClient()
	if err != nil || defaults.Snapshot().Timeout != 2*time.Second {
		t.Fatalf("默认 timeout: err=%v", err)
	}
	fixed, err := lesson.NewClient(lesson.WithTimeout(0), lesson.WithTimeout(time.Second))
	if err != nil || fixed.Snapshot().Timeout != time.Second {
		t.Fatalf("后者覆盖: err=%v", err)
	}
	for name, option := range map[string]lesson.Option{
		"nil": nil, "timeout": lesson.WithTimeout(0), "url": lesson.WithEndpoint("ftp://orders.example.test"),
	} {
		t.Run(name, func(t *testing.T) {
			if client, err := lesson.NewClient(option); err == nil || client != nil {
				t.Fatalf("无效配置不能返回可用 Client：client=%v err=%v", client, err)
			}
		})
	}
	original := errors.New("option declined")
	_, err = lesson.NewClient(func(config *lesson.Config) error { return original })
	if !errors.Is(err, original) {
		t.Fatalf("Option 错误必须包装保留: %v", err)
	}
}
