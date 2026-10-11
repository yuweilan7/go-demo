package lab

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// TestChild只被Launcher以明确-test.run和M30_CHILD_SPEC调用；普通test无副作用。
// os.Exit发生在Child正常收尾后，崩溃case由父强杀，不靠这里伪装崩溃。
func TestChild(t *testing.T) {
	path := os.Getenv("M30_CHILD_SPEC")
	if path == "" {
		t.Skip("仅作为独立OS进程实验入口")
	}
	if e := Child(path, os.Stdout); e != nil {
		t.Log(e)
		os.Exit(1)
	}
	os.Exit(0)
}

// TestRealMySQLKafka拥有整课Run，开关显式；默认SKIP不算实库证明。
// 重启进程使用同一个test/race binary，因此代码、库、race环境保持本轮实际版本。
func TestRealMySQLKafka(t *testing.T) {
	if os.Getenv("GOLEARN_M30_INTEGRATION") != "1" {
		t.Skip("需GOLEARN_M30_INTEGRATION=1，默认未执行真实MySQL+Kafka")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	if e = Run(ctx, os.Stdout, Launcher{Executable: exe, Args: []string{"-test.run=^TestChild$"}}); e != nil {
		t.Fatal(e)
	}
}

// TestStableEventID断言跨调用稳定和不同key隔离；它只是Go辅助断言，不能替代SQL唯一键。
func TestStableEventID(t *testing.T) {
	if eventID("order-a") != eventID("order-a") || eventID("order-a") == eventID("order-b") || len(eventID("order-a")) != 64 {
		t.Fatal("稳定eventID契约不成立")
	}
	if !prefixPattern.MatchString("m30_1234567890abcdef") || prefixPattern.MatchString("m30_x;DROP") {
		t.Fatal("标识符隔离无效")
	}
}

// TestGateReadyIsNotDone验证生产观测门不是完成信号。owner在ready存在后观察
// worker还在ctx.Done等待，随后取消并真join；不能用文件存在替代任务收尾。
func TestGateReadyIsNotDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "ready")
	done := make(chan struct{})
	var result error
	go func() { defer close(done); result = gate(ctx, path) }()
	defer func() {
		cancel()
		if e := joinDone(done, "gate test"); e != nil {
			t.Error(e)
		}
	}()
	observeCtx, observeCancel := context.WithTimeout(context.Background(), time.Second)
	defer observeCancel()
	if e := observe(observeCtx, func() (bool, error) {
		_, e := os.Stat(path)
		if errors.Is(e, os.ErrNotExist) {
			return false, nil
		}
		return e == nil, e
	}); e != nil {
		t.Fatal(e)
	}
	select {
	case <-done:
		t.Fatal("ready不能使gate结束")
	default:
	}
	cancel()
	if e := joinDone(done, "gate test"); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(result, context.Canceled) {
		t.Fatalf("取消原因丢失: %v", result)
	}
}

// TestProduceCorrelationFraming用本机net.Pipe辅助断言分段write和ACK相关ID解析。
// 它不运行Kafka、更不证明broker接受；那项证明只由完整集成E02的实broker读回提供。
func TestProduceCorrelationFraming(t *testing.T) {
	left, right := net.Pipe()
	f := &ProduceFault{}
	c := &faultConn{Conn: left, fault: f, requests: make(map[int32]int16)}
	deadline := time.Now().Add(2 * time.Second)
	if e := left.SetDeadline(deadline); e != nil {
		t.Fatal(e)
	}
	if e := right.SetDeadline(deadline); e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	var serverErr error
	go func() {
		defer close(done)
		defer right.Close()
		frame := make([]byte, 12)
		if _, serverErr = io.ReadFull(right, frame); serverErr != nil {
			return
		}
		reply := make([]byte, 8)
		binary.BigEndian.PutUint32(reply[:4], 4)
		binary.BigEndian.PutUint32(reply[4:], 42)
		_, serverErr = right.Write(reply)
	}()
	defer func() {
		left.Close()
		right.Close()
		if e := joinDone(done, "framing server"); e != nil {
			t.Error(e)
		}
	}()
	frame := make([]byte, 12)
	binary.BigEndian.PutUint32(frame[:4], 8)
	binary.BigEndian.PutUint32(frame[8:], 42)
	if _, e := c.Write(frame[:3]); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Write(frame[3:]); e != nil {
		t.Fatal(e)
	}
	_, e := c.Read(make([]byte, 8))
	if !errors.Is(e, io.EOF) || !f.Forwarded.Load() || !f.Dropped.Load() {
		t.Fatalf("Produce ACK未命中: %v", e)
	}
	if e = joinDone(done, "framing server"); e != nil {
		t.Fatal(e)
	}
	if serverErr != nil {
		t.Fatal(serverErr)
	}
}

// TestCleanupErrorIdentity确保预期错误不能吞清理错误；两个身份必须仍被errors.Is发现。
func TestCleanupErrorIdentity(t *testing.T) {
	original := errors.New("原业务失败")
	closeCause := errors.New("独立清理失败")
	joined := errors.Join(original, cleanupError(closeCause))
	if !errors.Is(joined, original) || !errors.Is(joined, closeCause) || !errors.Is(joined, ErrCleanup) {
		t.Fatal("原失败或清理原因丢失")
	}
}

// TestOrderDuplicateRecoveryBoundary调用Place实际使用的恢复函数，证明分类边界。
// 这里构造1062/cleanup/UNKNOWN cause只作辅助错误树证据，不声称发生真实网络或
// Rollback故障；真实1062安全回滚/查询由完整集成E03另在MySQL核验。
func TestOrderDuplicateRecoveryBoundary(t *testing.T) {
	r := Request{Key: "original-key", SKU: "sku", Qty: 1}
	old := Event{ID: eventID(r.Key), Request: r.Key, SKU: r.SKU, Qty: r.Qty, Version: 1}
	duplicate := &mysql.MySQLError{Number: 1062, Message: "辅助唯一键原因"}
	wrapped := fmt.Errorf("普通包装: %w", duplicate)
	cleanupCause := errors.New("辅助rollback原因")
	unknownCause := errors.New("辅助commit原因")
	lookupCause := errors.New("辅助查询原因")
	otherSQL := &mysql.MySQLError{Number: 1205, Message: "辅助其他SQL原因"}
	for _, tc := range []struct {
		name      string
		cause     error
		found     bool
		lookupErr error
		conflict  bool
		wantQuery bool
		wantOK    bool
		preserve  []error
	}{
		{"wrapped1062", wrapped, true, nil, false, true, true, nil},
		{"1062+cleanup", errors.Join(wrapped, cleanupError(cleanupCause)), true, nil, false, false, false, []error{wrapped, duplicate, ErrCleanup, cleanupCause}},
		{"1062+unknown", errors.Join(wrapped, ErrUnknown, unknownCause), true, nil, false, false, false, []error{wrapped, duplicate, ErrUnknown, unknownCause}},
		{"1062+both", errors.Join(wrapped, cleanupError(cleanupCause), ErrUnknown, unknownCause), true, nil, false, false, false, []error{wrapped, duplicate, ErrCleanup, cleanupCause, ErrUnknown, unknownCause}},
		{"otherSQL", otherSQL, true, nil, false, false, false, []error{otherSQL}},
		{"lookupFailure", wrapped, false, lookupCause, false, true, false, []error{wrapped, duplicate, lookupCause}},
		{"originalMissing", wrapped, false, nil, false, true, false, []error{wrapped, duplicate}},
		{"payloadConflict", wrapped, true, nil, true, true, false, []error{wrapped, duplicate, ErrKeyConflict}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, recovered, err := recoverOrderDuplicate(r, tc.cause, func() (Event, bool, error) {
				calls++ // 同步查询替身仅检查恢复是否触及权威查询，不启动资源。
				result := old
				if tc.conflict {
					result.Qty++
				}
				return result, tc.found, tc.lookupErr
			})
			if recovered != tc.wantOK || (calls == 1) != tc.wantQuery || calls > 1 {
				t.Fatalf("恢复/查询边界: recovered=%v calls=%d err=%v", recovered, calls, err)
			}
			if recovered {
				if err != nil || got != old {
					t.Fatalf("原订单未恢复: got=%+v err=%v", got, err)
				}
				return
			}
			if err == nil {
				t.Fatal("失败被抹成成功")
			}
			for _, cause := range tc.preserve {
				if !errors.Is(err, cause) {
					t.Fatalf("原cause丢失: want=%v got=%v", cause, err)
				}
			}
			if !tc.wantQuery && err != tc.cause {
				t.Fatal("拒绝归一化时应原样返回整棵错误树")
			}
		})
	}
}
