// 测试放在 course_test 包，从调用者视角使用公开 API。
// 它们检查语义与真实副作用，不通过搜索日志里的 PASS 判定业务正确。
package course_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-demo/lessons/m05_errors_contracts/internal/course"
)

// TestErrorChainAndBothAPIs 检查 caller 是否还能取得原数据库原因。
// 同一个编译单元实际执行 As 与 AsType，避免把新 API 只写在不可编译的文档里。
func TestErrorChainAndBothAPIs(t *testing.T) {
	_, err := course.CreateOrder(context.Background(), "ORDER-05", course.BeforeWrite)
	if err == nil || !errors.Is(err, course.ErrDependency) {
		t.Fatalf("expected preserved dependency failure, got %v", err)
	}
	if _, ok := err.(*course.DatabaseError); ok {
		t.Fatal("outer wrapper must not be a direct database value")
	}
	var oldAPI *course.DatabaseError
	newAPI, ok := errors.AsType[*course.DatabaseError](err)
	// As 的 target 是 &oldAPI；它修改 oldAPI 的指针值，而不是让我们解包每一层。
	if !errors.As(err, &oldAPI) || !ok || newAPI == nil || oldAPI != newAPI {
		t.Fatalf("both APIs must find the same original object: old=%v new=%v ok=%t", oldAPI, newAPI, ok)
	}
	if oldAPI.Code != "40001" || oldAPI.Phase != "before-write" {
		t.Fatalf("unexpected database facts: %+v", oldAPI)
	}
	if errors.Unwrap(err) != oldAPI {
		t.Fatal("single Unwrap should reach repository's original cause")
	}
	lost := fmt.Errorf("legacy: %v", err)
	if errors.Is(lost, course.ErrDependency) {
		t.Fatal("string formatting must not invent a preserved dependency chain")
	}
	if _, ok := errors.AsType[*course.DatabaseError](lost); ok {
		t.Fatal("lost chain must not expose driver fields")
	}
}

// TestSameTextDoesNotDefineBusinessMeaning 证明程序不能以相同文案识别业务类别。
func TestSameTextDoesNotDefineBusinessMeaning(t *testing.T) {
	left, right := errors.New("conflict"), errors.New("conflict")
	if left.Error() != right.Error() || left == right || errors.Is(left, right) {
		t.Fatal("same text and distinct identity must both be observable")
	}
	wrapped := fmt.Errorf("create: %w", course.ErrConflict)
	if !errors.Is(wrapped, course.ErrConflict) {
		t.Fatal("the single reused sentinel must survive wrapping")
	}
}

// TestValueAndPointerTargets 检查两个合法 error 动态类型都不能被另一个形态冒充。
// errors.As/AsType 不把 T 与 *T 相互解引用转换，它们沿链匹配可赋值的类型。
func TestValueAndPointerTargets(t *testing.T) {
	var pointer error = &course.OperationError{Operation: "pointer fixture", Cause: &course.ValueCodeError{Code: "V05"}}
	var valueTarget course.ValueCodeError
	if errors.As(pointer, &valueTarget) {
		t.Fatal("*ValueCodeError is not ValueCodeError")
	}
	if _, ok := errors.AsType[course.ValueCodeError](pointer); ok {
		t.Fatal("generic target must obey the same type distinction")
	}
	var pointerTarget *course.ValueCodeError
	if !errors.As(pointer, &pointerTarget) || pointerTarget.Code != "V05" {
		t.Fatal("legacy pointer target should match")
	}
	value := fmt.Errorf("outer: %w", course.ValueCodeError{Code: "V05"})
	if _, ok := errors.AsType[*course.ValueCodeError](value); ok {
		t.Fatal("ValueCodeError is not *ValueCodeError")
	}
	if got, ok := errors.AsType[course.ValueCodeError](value); !ok || got.Code != "V05" {
		t.Fatal("generic value target should match")
	}
}

// TestTypedNilAndValidationContract 证明“类型匹配成功”仍可能得到 nil 指针。
// 修复必须发生在返回无错的入口，不能靠给 Error 方法增加 nil 保护代替。
func TestTypedNilAndValidationContract(t *testing.T) {
	bad := course.BadValidate("ORDER-05")
	matched, ok := errors.AsType[*course.RuleError](bad)
	if bad == nil || !ok || matched != nil {
		t.Fatalf("expected interface holding (*RuleError)(nil), got %T %v", bad, bad)
	}
	if err := course.Validate("ORDER-05"); err != nil {
		t.Fatalf("successful validation must return nil: %v", err)
	}
	invalid := course.Validate("")
	if rule, ok := errors.AsType[*course.RuleError](invalid); !ok || rule == nil || rule.Field != "order_id" {
		t.Fatal("invalid input must carry an actual rule object")
	}
}

// TestBoundaryMappingAndReplayClasses 固定本订单 API 的边界约定，检查三类处理结果。
// may-retry 只出现在 fixture 保证未写入且原因短暂的分支；本测试也不实际重试。
func TestBoundaryMappingAndReplayClasses(t *testing.T) {
	cases := []struct {
		name     string
		scenario course.Scenario
		want     course.Decision
	}{
		{"success", course.Normal, course.Decision{"ok", 201, "OK", "ack", "not-needed"}},
		{"missing", course.MissingSKU, course.Decision{"not-found", 404, "NOT_FOUND", "ack-with-business-rejection", "do-not-retry"}},
		{"conflict", course.Duplicate, course.Decision{"conflict", 409, "ALREADY_EXISTS", "ack-with-business-rejection", "do-not-retry"}},
		{"transient_before_write", course.BeforeWrite, course.Decision{"dependency", 503, "UNAVAILABLE", "bounded-requeue-if-idempotent", "may-retry"}},
		{"stable_schema_failure", course.BadSchema, course.Decision{"dependency", 503, "UNAVAILABLE", "park-for-operator", "do-not-retry"}},
		{"commit_ack_lost", course.LostCommitAck, course.Decision{"outcome-unknown", 503, "UNAVAILABLE", "reconcile-before-ack", "reconcile"}},
	}
	for _, tc := range cases {
		// t.Run 同步执行子测试（这里没有 t.Parallel）；子测试失败只标记所属案例。
		t.Run(tc.name, func(t *testing.T) {
			receipt, err := course.CreateOrder(context.Background(), "ORDER-05", tc.scenario)
			if got := course.Classify(err); got != tc.want {
				t.Fatalf("decision=%+v want=%+v error=%v", got, tc.want, err)
			}
			if tc.scenario == course.Normal {
				if receipt.OrderID != "ORDER-05" || err != nil {
					t.Fatal("success must provide the requested order")
				}
			} else if receipt.OrderID != "" || err == nil {
				t.Fatal("failed business API promises a zero receipt and non-nil error")
			}
		})
	}
}

// TestCancellationAndUnknownPriority 检查取消类别未因 wrapper/Join 丢失。
// 未知副作用优先于通信超时，是本订单边界明确制定的策略，不是 errors.Join 自带排序。
func TestCancellationAndUnknownPriority(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, release := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer release()
	for _, tc := range []struct {
		ctx  context.Context
		kind string
		err  error
	}{{canceled, "canceled", context.Canceled}, {expired, "deadline", context.DeadlineExceeded}} {
		_, err := course.CreateOrder(tc.ctx, "ORDER-05", course.Normal)
		if !errors.Is(err, tc.err) || course.Classify(err).Kind != tc.kind {
			t.Fatalf("lost cancellation category: %v", err)
		}
	}
	unknown := fmt.Errorf("after dispatch: %w", errors.Join(course.ErrOutcomeUnknown, context.DeadlineExceeded, course.ErrClose))
	if got := course.Classify(unknown); got.Retry != "reconcile" || got.Kind != "outcome-unknown" {
		t.Fatalf("timeout after dispatch must not permit replay: %+v", got)
	}
	if course.Classify(errors.New("order conflict")).Kind != "internal" {
		t.Fatal("an unrelated message must not become a conflict sentinel")
	}
}

// TestReaderPartialResults 检查数据契约，不把 error 一出现就丢掉 n 个有效字节。
func TestReaderPartialResults(t *testing.T) {
	raw := course.NewTailReader("ORDER-05\n", io.EOF)
	buffer := make([]byte, 64)
	n, err := raw.Read(buffer)
	if n != 9 || !errors.Is(err, io.EOF) || string(buffer[:n]) != "ORDER-05\n" {
		t.Fatal("fixture must actually return bytes and EOF in the same call")
	}
	good, goodErr := course.ReadPayload(course.NewTailReader("ORDER-05\n", io.EOF), 4)
	partial, partialErr := course.ReadPayload(course.NewTailReader("ORDER-", io.ErrUnexpectedEOF), 4)
	bad, badErr := course.BadReadPayload(course.NewTailReader("ORDER-05\n", io.EOF))
	if good != "ORDER-05\n" || goodErr != nil || partial != "ORDER-" || !errors.Is(partialErr, io.ErrUnexpectedEOF) {
		t.Fatal("valid bytes and the final error must be handled independently")
	}
	if bad != "" || !errors.Is(badErr, io.EOF) {
		t.Fatal("the dangerous version must demonstrate the loss")
	}
	budgetData, budgetErr := course.ReadPayload(course.NewTailReader("ORDER-05\n", io.EOF), 1)
	if budgetData != "ORDE" || !errors.Is(budgetErr, course.ErrReadBudget) {
		t.Fatal("bounded loop must return accumulated data and its budget failure")
	}
}

// zeroReader 是“0,nil 不表示 EOF”的辅助契约对象；不拥有任何文件资源。
type zeroReader struct{}

func (zeroReader) Read([]byte) (int, error) { return 0, nil }

// TestReaderNoProgressBound 确认异常 reader 不会令教学程序陷入循环。
func TestReaderNoProgressBound(t *testing.T) {
	if _, err := course.ReadPayload(zeroReader{}, 3); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("expected bounded no-progress failure, got %v", err)
	}
}

// TestExportStagesAndCleanupTree 检查每个故障之后的资源所有权与协议进度。
// 小文本在缓冲里成功，故底层 WriteErr 于 Flush 暴露；大文本则在 WriteString 时暴露。
func TestExportStagesAndCleanupTree(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		sink    *course.FaultSink
		cause   error
		stage   string
		commit  int
	}{
		{"write", strings.Repeat("x", 128), &course.FaultSink{WriteErr: course.ErrWrite}, course.ErrWrite, "write receipt text", 0},
		{"flush", "ORDER-05\n", &course.FaultSink{WriteErr: course.ErrFlush}, course.ErrFlush, "flush receipt bytes", 0},
		{"commit", "ORDER-05\n", &course.FaultSink{CommitErr: course.ErrCommit}, course.ErrCommit, "commit receipt", 1},
		{"close", "ORDER-05\n", &course.FaultSink{CloseErr: course.ErrClose}, course.ErrClose, "close receipt", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := course.ExportReceipt(tc.sink, tc.payload)
			if !errors.Is(err, tc.cause) || !strings.Contains(err.Error(), tc.stage) {
				t.Fatalf("failure must retain the structured cause and operation context: %v", err)
			}
			if tc.sink.CloseCalls != 1 || !tc.sink.Closed || tc.sink.CommitCalls != tc.commit {
				t.Fatalf("unexpected resource/progress facts: %+v", tc.sink)
			}
		})
	}
	goodSink := &course.FaultSink{CommitErr: course.ErrCommit, CloseErr: course.ErrClose}
	joined := course.ExportReceipt(goodSink, "ORDER-05\n")
	if !errors.Is(joined, course.ErrCommit) || !errors.Is(joined, course.ErrClose) || goodSink.Committed {
		t.Fatal("business failure and cleanup failure must both survive")
	}
	if _, ok := joined.(interface{ Unwrap() []error }); !ok || errors.Unwrap(joined) != nil {
		t.Fatal("Join is a multi-cause tree, not an Unwrap() error chain")
	}
	bad := course.BadExportReceipt(&course.FaultSink{CommitErr: course.ErrCommit, CloseErr: course.ErrClose}, "ORDER-05\n")
	if errors.Is(bad, course.ErrCommit) || !errors.Is(bad, course.ErrClose) {
		t.Fatal("the dangerous defer must demonstrate overwritten business failure")
	}
}

// TestActualFileAndClosedHandle 写入本模块内的临时目录，验证真实内容和关闭后的句柄状态。
// t.Cleanup 在测试结束执行，即使 t.Fatal 提前退出也会清理；它不代表业务事务回滚。
func TestActualFileAndClosedHandle(t *testing.T) {
	dir, err := os.MkdirTemp(".", ".m05-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove owned test directory: %v", err)
		}
	})
	path := filepath.Join(dir, "ORDER-05.txt")
	sink, err := course.NewFileSink(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := course.ExportReceipt(sink, "ORDER-05\n"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "ORDER-05\n" {
		t.Fatalf("actual file content=%q err=%v", content, err)
	}
	if _, err := sink.Write([]byte("extra")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("ExportReceipt owner must have closed the actual handle: %v", err)
	}
}

// TestRealHTTPLostResponseAndReconciliation 检查真实 TCP/HTTP 与服务端业务状态。
// 只收到 HTTP 200 不够：必须查询同一请求键，确认一笔订单和恰好一次 POST。
func TestRealHTTPLostResponseAndReconciliation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := course.RunUnknownExperiment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(report.OriginalErr, course.ErrOutcomeUnknown) || !errors.Is(report.OriginalErr, io.EOF) {
		t.Fatalf("actual lost-response chain must retain both facts: %v", report.OriginalErr)
	}
	if _, ok := errors.AsType[*url.Error](report.OriginalErr); !ok {
		t.Fatal("actual net/http error type must survive wrapping")
	}
	if report.BeforeQuery.Retry != "reconcile" || !report.Confirmed || report.OrderID != "ORDER-05" ||
		report.StoredOrders != 1 || report.PostAttempts != 1 || report.Queries != 1 {
		t.Fatalf("unexpected actual protocol/business facts: %+v", report)
	}
	if len(report.Events) != 3 || !strings.HasPrefix(report.Events[0], "server: POST") ||
		!strings.HasPrefix(report.Events[1], "client: POST failed") || !strings.HasPrefix(report.Events[2], "server: GET") {
		t.Fatalf("barrier-backed observed timeline=%v", report.Events)
	}
}

// brokenWriter 让边界输出失败。业务判断不能只因内部函数返回 nil 就掩盖写日志失败。
type brokenWriter struct{ cause error }

func (w brokenWriter) Write([]byte) (int, error) { return 0, w.cause }

// TestBoundaryOutputErrorReturned 确认 demo 的输出 error 最终交到 main。
func TestBoundaryOutputErrorReturned(t *testing.T) {
	want := errors.New("console unavailable")
	if err := course.Run(context.Background(), brokenWriter{want}, "core"); !errors.Is(err, want) {
		t.Fatalf("boundary output error was hidden: %v", err)
	}
}
