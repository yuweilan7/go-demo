package course

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// reporter 只由实验边界使用，拥有写日志失败状态，不由业务层共享。
// 显示日志失败也必须反馈给 caller，不能只说“控制台应该已经打印了 PASS”。
type reporter struct {
	out io.Writer
	err error
}

// printf 接受入口提供的输出对象；一次失败后停止写，但保留首次错误交回 Run。
func (r *reporter) printf(format string, args ...any) {
	if r.err == nil {
		_, r.err = fmt.Fprintf(r.out, format, args...)
	}
}

// require 由实验调用，不依赖“文案看起来对”判成功；它检查显式布尔业务事实。
// 失败变成普通 error 回到 Run/main，没有 panic 或吞掉失败。
func require(ok bool, message string) error {
	if !ok {
		return fmt.Errorf("independent observation failed: %s", message)
	}
	return nil
}

// Run 被 cmd/demo 调用，负责选择有界实验、输出证据、汇总真实错误。
// ctx 由 main 创建；本函数没有 goroutine，网络实验自己的 owner 负责等待与清理。
func Run(ctx context.Context, output io.Writer, choice string) error {
	r := &reporter{out: output}
	labs := map[string]func() error{
		"core":    func() error { return runCore(ctx, r) },
		"io":      func() error { return runIO(r) },
		"cleanup": func() error { return runCleanup(r) },
		"unknown": func() error { return runUnknown(ctx, r) },
		"bad":     func() error { return runBad(r) },
	}
	choices := []string{choice}
	if choice == "all" {
		choices = []string{"core", "io", "cleanup", "unknown", "bad"}
	}
	for _, name := range choices {
		lab, exists := labs[name]
		if !exists {
			return fmt.Errorf("unknown -case=%q; choose all/core/io/cleanup/unknown/bad", name)
		}
		if err := lab(); err != nil {
			return errors.Join(fmt.Errorf("experiment %s: %w", name, err), r.err)
		}
	}
	r.printf("\nM05 PASS: 所选实验的状态与错误传播断言通过。\n")
	return r.err
}

// runCore 是建议用 10～20 分钟阅读并讲清的核心：同一订单沿错误链返回到边界。
// 每个反例只观察结果，不执行危险重试，也没有会让正常入口崩溃的 panic。
func runCore(ctx context.Context, r *reporter) error {
	r.printf("\n[CORE01] 同文案与哨兵身份\n")
	first, second := errors.New("conflict"), errors.New("conflict")
	r.printf("same_text=%t same_error=%t sentinel_match=%t\n",
		first.Error() == second.Error(), errors.Is(first, second), errors.Is(ErrConflict, ErrConflict))
	if err := require(first.Error() == second.Error() && !errors.Is(first, second), "distinct instances with same text"); err != nil {
		return err
	}

	r.printf("\n[CORE02] service → repository → database cause\n")
	_, wrapped := CreateOrder(ctx, "ORDER-05", BeforeWrite)
	lost := fmt.Errorf("legacy boundary: %v", wrapped) // 显式反例：文案保留，结构化链丢失。
	_, direct := wrapped.(*DatabaseError)              // 只检查最外层 *OperationError，不自动调用 Unwrap。
	var oldAPI *DatabaseError
	asOK := errors.As(wrapped, &oldAPI) // &oldAPI 的类型是 **DatabaseError，让 As 修改 oldAPI 指针变量。
	newAPI, asTypeOK := errors.AsType[*DatabaseError](wrapped)
	r.printf("boundary_error=%v\n", wrapped)
	r.printf("direct_assert=%t As=%t AsType=%t same_pointer=%t\n", direct, asOK, asTypeOK, oldAPI == newAPI)
	r.printf("good_dependency=%t lost_dependency=%t Unwrap_type=%T\n",
		errors.Is(wrapped, ErrDependency), errors.Is(lost, ErrDependency), errors.Unwrap(wrapped))
	if err := require(!direct && asOK && asTypeOK && newAPI != nil && oldAPI == newAPI && newAPI.Code == "40001" &&
		errors.Is(wrapped, ErrDependency) && !errors.Is(lost, ErrDependency), "cause survives only the preserved chain"); err != nil {
		return err
	}

	r.printf("\n[CORE03] T 和 *T 匹配的是不同动态类型\n")
	// 自定义 wrapper 明确保存 pointer 形态，供教学观察；业务代码应统一形态。
	// Go1.27 vet 会对 fmt.Errorf("%w", &ValueCodeError{...}) 提醒可能误匹配，
	// 因为 ValueCodeError 值本身已实现 error。用此显式对象不关闭或规避 vet 检查。
	pointerErr := &OperationError{Operation: "pointer fixture", Cause: &ValueCodeError{Code: "V05"}}
	_, valueOK := errors.AsType[ValueCodeError](pointerErr)
	pointerValue, pointerOK := errors.AsType[*ValueCodeError](pointerErr)
	if err := require(pointerOK && pointerValue != nil, "pointer target must match a non-nil object before reading Code"); err != nil {
		return err
	}
	valueErr := fmt.Errorf("wrapper: %w", ValueCodeError{Code: "V05"})
	_, valueFound := errors.AsType[ValueCodeError](valueErr)
	_, pointerFound := errors.AsType[*ValueCodeError](valueErr)
	r.printf("actual=*ValueCodeError search_T=%t search_pointer=%t code=%s\n", valueOK, pointerOK, pointerValue.Code)
	r.printf("actual=ValueCodeError search_T=%t search_pointer=%t\n", valueFound, pointerFound)
	if err := require(!valueOK && pointerOK && valueFound && !pointerFound, "T and *T are distinct error shapes"); err != nil {
		return err
	}

	r.printf("\n[CORE04] typed nil 与明确返回 nil\n")
	typedNil := BadValidate("ORDER-05")
	matchedNil, found := errors.AsType[*RuleError](typedNil)
	correct := Validate("ORDER-05")
	r.printf("bad_type=%T bad_equal_nil=%t AsType_ok=%t matched_pointer_nil=%t fixed_equal_nil=%t\n",
		typedNil, typedNil == nil, found, matchedNil == nil, correct == nil)
	if err := require(typedNil != nil && found && matchedNil == nil && correct == nil, "return real nil on successful validation"); err != nil {
		return err
	}

	r.printf("\n[CORE05] 业务边界策略，不执行重试\n")
	for _, scenario := range []Scenario{Normal, MissingSKU, Duplicate, BeforeWrite, BadSchema, LostCommitAck} {
		receipt, err := CreateOrder(ctx, "ORDER-05", scenario)
		decision := Classify(err)
		r.printf("scenario=%s order=%q kind=%s HTTP=%d RPC=%s message=%s next=%s\n",
			scenario, receipt.OrderID, decision.Kind, decision.HTTP, decision.RPC, decision.Message, decision.Retry)
	}
	// 不 sleep 等截止时间：创建一个已过期 deadline，返回路径可以确定复现。
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	expired, release := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer release()
	for _, item := range []struct {
		label string
		ctx   context.Context
		cause error
	}{{"canceled", canceled, context.Canceled}, {"deadline", expired, context.DeadlineExceeded}} {
		_, err := CreateOrder(item.ctx, "ORDER-05", Normal)
		d := Classify(err)
		r.printf("scenario=%s preserved=%t kind=%s next=%s\n", item.label, errors.Is(err, item.cause), d.Kind, d.Retry)
		if err := require(errors.Is(err, item.cause) && d.Kind == item.label, "cancellation category survives wrapping"); err != nil {
			return err
		}
	}
	return nil
}

// runIO 用同一段订单文本对照“丢数据”与“先用 n 再看 err”的契约。
func runIO(r *reporter) error {
	r.printf("\n[IO01] 一次 Read 可以同时返回有效字节和 EOF\n")
	bad, badErr := BadReadPayload(NewTailReader("ORDER-05\n", io.EOF))
	good, goodErr := ReadPayload(NewTailReader("ORDER-05\n", io.EOF), 4)
	partial, partialErr := ReadPayload(NewTailReader("ORDER-", io.ErrUnexpectedEOF), 4)
	r.printf("bad=%q bad_EOF=%t fixed=%q fixed_error=%v\n", bad, errors.Is(badErr, io.EOF), good, goodErr)
	r.printf("partial=%q preserved_unexpected_EOF=%t; 部分文本不等于完整业务成功\n", partial, errors.Is(partialErr, io.ErrUnexpectedEOF))
	return require(bad == "" && errors.Is(badErr, io.EOF) && good == "ORDER-05\n" && goodErr == nil &&
		partial == "ORDER-" && errors.Is(partialErr, io.ErrUnexpectedEOF), "read bytes survive the final error")
}

// runCleanup 创建实际回执文件，然后用明确标注的 FaultSink 展开各个故障阶段。
// 临时目录位于本模块 .local 下，函数退出会删除其专属目录，删除失败也作为 error 返回。
func runCleanup(r *reporter) (err error) {
	r.printf("\n[CLEAN01] 实际文件：Write → Flush → Sync → Close\n")
	if err := os.MkdirAll(".local", 0o700); err != nil {
		return fmt.Errorf("create local experiment root: %w", err)
	}
	tempDir, err := os.MkdirTemp(".local", "m05-receipt-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(tempDir)) }()
	path := filepath.Join(tempDir, "ORDER-05.txt")
	sink, err := NewFileSink(path)
	if err != nil {
		return err
	}
	if err := ExportReceipt(sink, "ORDER-05\n"); err != nil {
		return err
	}
	content, err := os.ReadFile(path) // 通过另一次真实读取验证写出内容，而不是只检查 error==nil。
	if err != nil {
		return err
	}
	r.printf("file_content=%q bytes=%d; 文件已关闭，专属临时目录将在退出时删除\n", content, len(content))
	if err := require(string(content) == "ORDER-05\n", "actual file content matches receipt"); err != nil {
		return err
	}

	r.printf("\n[CLEAN02] 保留 Write/Flush/Commit/Close 故障（FaultSink 辅助实验）\n")
	for _, item := range []struct {
		name    string
		payload string
		sink    *FaultSink
		cause   error
	}{
		{"write", strings.Repeat("x", 128), &FaultSink{WriteErr: ErrWrite}, ErrWrite},
		{"flush", "ORDER-05\n", &FaultSink{WriteErr: ErrFlush}, ErrFlush},
		{"commit", "ORDER-05\n", &FaultSink{CommitErr: ErrCommit}, ErrCommit},
		{"close", "ORDER-05\n", &FaultSink{CloseErr: ErrClose}, ErrClose},
	} {
		err := ExportReceipt(item.sink, item.payload)
		r.printf("stage=%s cause_preserved=%t commit_calls=%d close_calls=%d committed=%t\n",
			item.name, errors.Is(err, item.cause), item.sink.CommitCalls, item.sink.CloseCalls, item.sink.Committed)
		if err := require(errors.Is(err, item.cause) && item.sink.CloseCalls == 1, "output failure must be returned and Close attempted exactly once"); err != nil {
			return err
		}
	}

	r.printf("\n[CLEAN03] 主错误与清理错误是树的两个分支\n")
	joined := ExportReceipt(&FaultSink{CommitErr: ErrCommit, CloseErr: ErrClose}, "ORDER-05\n")
	_, multiple := joined.(interface{ Unwrap() []error })
	r.printf("commit_preserved=%t close_preserved=%t tree=%t errors.Unwrap(join)==nil=%t\n",
		errors.Is(joined, ErrCommit), errors.Is(joined, ErrClose), multiple, errors.Unwrap(joined) == nil)
	// Unwrap 只识别 Unwrap() error；Join 的 Unwrap() []error 仍被 Is/As/AsType 遍历。
	return require(errors.Is(joined, ErrCommit) && errors.Is(joined, ErrClose) && multiple &&
		errors.Unwrap(joined) == nil, "business and cleanup branches both survive")
}

// runUnknown 只在有上下文的边界输出一次原始失败与后续核实证据。
func runUnknown(ctx context.Context, r *reporter) error {
	r.printf("\n[UNKNOWN01] 真实 loopback HTTP：写入后响应丢失\n")
	report, err := RunUnknownExperiment(ctx)
	if err != nil {
		return err
	}
	for _, event := range report.Events {
		r.printf("%s\n", event)
	}
	_, urlCause := errors.AsType[*url.Error](report.OriginalErr)
	r.printf("first_error_has_url_cause=%t first_error_has_EOF=%t before_query_next=%s\n",
		urlCause, errors.Is(report.OriginalErr, io.EOF), report.BeforeQuery.Retry)
	r.printf("after_query confirmed=%t order=%s stored=%d POST=%d GET=%d; handlers 工作段已完成，server/transport 已清理\n",
		report.Confirmed, report.OrderID, report.StoredOrders, report.PostAttempts, report.Queries)
	return require(urlCause && errors.Is(report.OriginalErr, io.EOF) && report.BeforeQuery.Retry == "reconcile" &&
		report.Confirmed && report.StoredOrders == 1 && report.PostAttempts == 1 && report.Queries == 1,
		"lost response cannot authorize another create request")
}

// runBad 是明确命名的故障入口；所有坏结果都有即时返回，没有无上限重试或进程崩溃。
// 正常 all 入口也可执行这些安全对照，把“预期失败已发生”作为观察点。
func runBad(r *reporter) error {
	r.printf("\n[BAD01] Close 覆盖 Commit；修复改用 Join\n")
	badSink := &FaultSink{CommitErr: ErrCommit, CloseErr: ErrClose}
	goodSink := &FaultSink{CommitErr: ErrCommit, CloseErr: ErrClose}
	bad := BadExportReceipt(badSink, "ORDER-05\n")
	good := ExportReceipt(goodSink, "ORDER-05\n")
	r.printf("bad_commit=%t bad_close=%t fixed_commit=%t fixed_close=%t\n",
		errors.Is(bad, ErrCommit), errors.Is(bad, ErrClose), errors.Is(good, ErrCommit), errors.Is(good, ErrClose))
	return require(!errors.Is(bad, ErrCommit) && errors.Is(bad, ErrClose) &&
		errors.Is(good, ErrCommit) && errors.Is(good, ErrClose), "cleanup overwrite reproduced and repaired")
}
