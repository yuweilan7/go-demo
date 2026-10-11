package lesson

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestRegistrationAndReceivers 直接检查运行后的观察值，不把打印文案当语言规则。
// 指针/闭包看到新值、实参保留旧值、旧函数仍被调用，以及 LIFO 都是独立断言。
func TestRegistrationAndReceivers(t *testing.T) {
	var log bytes.Buffer
	r := RunSemantics(NewTrace(&log))
	if r.SavedArgument != 10 || r.ClosureValue != 20 || r.PointerValue != 20 || r.FunctionWasReplaced {
		t.Fatalf("注册与晚读结果错误: %+v", r)
	}
	if !reflect.DeepEqual(r.Order, []int{3, 2, 1}) {
		t.Fatalf("不是后注册先执行: %v", r.Order)
	}
	if r.PointerReceiver != "A*" || r.ValueReceiver != "A" || r.LateReceiver != "B" {
		t.Fatalf("指针接收者/值副本/晚读接收者混淆: %+v", r)
	}
	// 求值的实际日志必须在“注册完成”之前；否则示例错误地把实参表达式也延迟了。
	evalAt := strings.Index(log.String(), "[E01.eval]")
	registeredAt := strings.Index(log.String(), "defer1 已注册")
	if evalAt < 0 || registeredAt < 0 || evalAt >= registeredAt {
		t.Fatal("实参表达式没有在注册时求值")
	}
}

// TestReturnSlotsAndErrorVisibility 检查 caller 收到的结果和清理所见的 error。
// 只有具名结果变量本身的修改改变 int 返回；显式 return err 能跨过遮蔽作用域传播。
func TestReturnSlotsAndErrorVisibility(t *testing.T) {
	var log bytes.Buffer
	r := RunReturns(NewTrace(&log))
	if r.Named != 15 || r.Anonymous != 10 || r.NamedLocal != 10 {
		t.Fatalf("结果槽时间线不符: %+v", r)
	}
	if !errors.Is(r.EarlyReturned, ErrBusiness) || r.EarlyObserved != nil {
		t.Fatal("危险版必须重现 caller 失败、清理却看到 nil")
	}
	if !errors.Is(r.LateReturned, ErrBusiness) || !errors.Is(r.LateObserved, ErrBusiness) {
		t.Fatal("退出闭包应读到最终业务错误")
	}
	if r.ShadowReturned != nil || r.ShadowObserved != nil {
		t.Fatal("遮蔽危险版应重现外层 err 没有被赋值")
	}
	if !errors.Is(r.FixedReturned, ErrBusiness) || !errors.Is(r.FixedObserved, ErrBusiness) {
		t.Fatal("显式 return err 必须传播内层失败")
	}
}

// TestRealFileLifetime 检查真实句柄的最高存量和最终释放，不只检查函数返回 nil。
// 预算拒绝是有界故障输入；不是 OS 耗尽测试。修复必须处理全部四条记录且 peak=1。
func TestRealFileLifetime(t *testing.T) {
	var log bytes.Buffer
	r, err := RunFiles(NewTrace(&log))
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(r.Bad.Err, ErrBudget) || r.Bad.Peak != 2 || r.Bad.Opened != 2 || r.Bad.Closed != 2 || r.Bad.Live != 0 || r.Bad.VerifiedRecords != 2 {
		t.Fatalf("危险版积压或退出关闭不符: %+v", r.Bad)
	}
	for _, fixed := range []BatchResult{r.Scoped, r.Explicit} {
		if fixed.Err != nil || fixed.Opened != 4 || fixed.Closed != 4 || fixed.Peak != 1 || fixed.Live != 0 || fixed.VerifiedRecords != 4 {
			t.Fatalf("逐轮释放未守住资源不变量: %+v", fixed)
		}
	}
	if !r.SecondAcquireFailed || !r.FirstResourceClosed {
		t.Fatal("第二次获取失败时，第一个真实资源必须已经关闭")
	}
	// 接收者捕获关闭 A 却漏 B；晚读闭包关闭 B 却漏 A。观察点必须在外层兜底前。
	if !r.CapturedReceiver.OldClosed || r.CapturedReceiver.NewClosed || r.LateClosure.OldClosed || !r.LateClosure.NewClosed {
		t.Fatalf("接收者/闭包的资源遗漏未被重现: %+v %+v", r.CapturedReceiver, r.LateClosure)
	}
	if !r.StableReceivers.OldClosed || !r.StableReceivers.NewClosed {
		t.Fatalf("每个资源登记稳定实参的修复仍有遗漏: %+v", r.StableReceivers)
	}
	assertRemoved(t, r.TempDir, r.Removed)
}

// TestFlushCloseAndJoinedCauses 把真实文件内容与 error 原因分别检查。
// 写出失败和主业务失败互不覆盖；即使内容已可读，重复 Close 的失败也不能假报成功。
func TestFlushCloseAndJoinedCauses(t *testing.T) {
	var log bytes.Buffer
	r, err := RunBuffers(NewTrace(&log))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Cases) != 4 {
		t.Fatalf("缺少写出路径: %d", len(r.Cases))
	}
	bad, good, closeFailure, both := r.Cases[0], r.Cases[1], r.Cases[2], r.Cases[3]
	if bad.Err != nil || bad.Content != "" || !errors.Is(bad.FlushErr, os.ErrClosed) || bad.Success {
		t.Fatalf("危险版应暴露虚假 nil 和空文件: %+v", bad)
	}
	if good.Err != nil || good.Content != orderRecord || good.FlushErr != nil || good.CloseErr != nil || !good.Success {
		t.Fatalf("修复版没有完整写出并清理: %+v", good)
	}
	if closeFailure.Content != orderRecord || !errors.Is(closeFailure.Err, os.ErrClosed) || closeFailure.Success {
		t.Fatalf("Close 错误被忽略: %+v", closeFailure)
	}
	if !errors.Is(both.Err, ErrBusiness) || !errors.Is(both.Err, os.ErrClosed) || both.Content != "" || both.Success {
		t.Fatalf("主错误或清理错误丢失: %+v", both)
	}
	// 两个标签都必须存在：errors.Is(os.ErrClosed) 单独只能证明至少一个清理失败。
	if !strings.Contains(both.Err.Error(), "Flush:") || !strings.Contains(both.Err.Error(), "Close:") {
		t.Fatalf("Flush/Close 没有各自保留: %v", both.Err)
	}
	assertRemoved(t, r.TempDir, r.Removed)
}

// TestBorrowedClientOwnership 验证错误请求关闭共享文件后下一请求失败；修复后的两个请求
// 真正写出两条记录，再由应用 owner 关闭。这只验证文件客户端所有权，不验证 HTTP/SQL。
func TestBorrowedClientOwnership(t *testing.T) {
	var log bytes.Buffer
	r, err := RunOwnership(NewTrace(&log))
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(r.BadSecondErr, os.ErrClosed) || r.FixedSecondErr != nil || r.FixedContent != orderRecord+orderRecord {
		t.Fatalf("借用与关闭边界不符: %+v", r)
	}
	assertRemoved(t, r.TempDir, r.Removed)
}

// TestProcessBoundary 使用当前测试二进制启动有界 child，直接观察真实 Exit/Fatal。
// 不在测试主进程调用 Exit；父进程可以检查返回码、标记文件、panic 清理与最终目录删除。
func TestProcessBoundary(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	r, err := RunProcess(NewTrace(&log), executable, []string{"-test.run=^TestHelperProcess$"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.PanicRecovered || !r.PanicFileClosed || r.PanicAfterLine || r.PanicContent != "before-panic\n" {
		t.Fatalf("普通 panic 栈展开或恢复位置不符: %+v", r)
	}
	if len(r.Cases) != 4 {
		t.Fatalf("缺少退出路径: %d", len(r.Cases))
	}
	for i, want := range []struct {
		code    int
		cleanup bool
	}{{0, true}, {23, false}, {1, false}, {24, true}} {
		if r.Cases[i].ExitCode != want.code || r.Cases[i].CleanupRan != want.cleanup {
			t.Fatalf("子进程 %s 的退出事实不符: %+v", r.Cases[i].Mode, r.Cases[i])
		}
	}
	assertRemoved(t, r.TempDir, r.Removed)
}

// TestHelperProcess 是父测试的独立 child 入口。普通 go test 时环境未设置，立即返回。
// child 根据 mode 执行真实 RunChild；normal 返回测试框架，其他模式自行结束 child。
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("M06_CHILD_MODE")
	if mode == "" {
		return
	}
	if err := RunChild(mode, os.Getenv("M06_CHILD_DIR")); err != nil {
		t.Fatal(err)
	}
}

// assertRemoved 由每个资源实验测试调用。Removed 是函数自报，Stat 是独立文件系统核验，
// 两者必须一致；这能发现只有“清理成功”日志、目录其实仍遗留的错误实现。
func assertRemoved(t *testing.T, path string, reported bool) {
	t.Helper()
	_, err := os.Stat(path)
	if !reported || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("临时目录未真正删除: %s, %v", path, err)
	}
}
