package lesson

import (
	"context"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestThreeLayerReturnPoints 独立声明预期时间线，验证运行中的返回点与 LIFO，而非只看日志。
// err 分支必须让 L2.after-leaf 运行；panic 分支必须跳过它并保留 L1.after-service。
func TestThreeLayerReturnPoints(t *testing.T) {
	for _, mode := range []string{"success", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			got := RunChain(mode)
			want := []string{"L1.begin", "L2.begin", "L3.begin"}
			switch mode {
			case "success":
				want = append(want, "L3.success")
				if got.Err != nil {
					t.Fatalf("正常订单应成功: %v", got.Err)
				}
			case "error":
				want = append(want, "L3.return-error")
				if !errors.Is(got.Err, ErrInvalidOrder) {
					t.Fatalf("业务失败必须保留正常 error: %v", got.Err)
				}
			case "panic":
				want = append(want, "L3.panic")
				var failure *PanicFailure
				if !errors.As(got.Err, &failure) || failure.TaskID != "order-7001" || failure.Stage != "leaf.validate" || !strings.Contains(failure.Stack, "lesson.leaf") {
					t.Fatalf("恢复后必须保留任务、阶段与故障栈: %#v", got.Err)
				}
			}
			want = append(want, "L3.cleanup.second", "L3.cleanup.first")
			if mode != "panic" {
				want = append(want, "L2.after-leaf")
			}
			want = append(want, "L2.observer")
			if mode == "panic" {
				want = append(want, "L2.recovered")
			}
			want = append(want, "L2.cleanup", "L1.after-service")
			if !reflect.DeepEqual(got.Events, want) {
				t.Fatalf("展开/返回顺序错误\n实际 %v\n预期 %v", got.Events, want)
			}
		})
	}
}

// TestRecoverLocations 验证普通调用与转调 helper 失败，但 named deferred function 生效。
func TestRecoverLocations(t *testing.T) {
	want := []string{"ordinary=nil", "indirect.helper=nil", "outer.recovered", "caller.after-indirect", "direct.named=recovered", "caller.after-direct", "bare.outer=recovered", "caller.after-bare"}
	if got := RecoverRules(); !reflect.DeepEqual(got, want) {
		t.Fatalf("recover 位置的可观察行为不符: %v", got)
	}
	if got := DeferValues(); !reflect.DeepEqual(got, []string{"closure=finished", "argument=registered"}) {
		t.Fatalf("defer 捕获/读取时机不符: %v", got)
	}
	failure := RunRuntimePanic()
	if failure == nil {
		t.Fatal("运行时越界 panic 必须在相同 goroutine 恢复")
	}
	if _, ok := failure.Value.(runtime.Error); !ok || !strings.Contains(failure.Stack, "lesson.RunRuntimePanic") {
		t.Fatalf("必须保留 runtime.Error 动态值和运行时故障栈: %+v", failure)
	}
}

// TestEffectsAreNotRewound 验证业务状态：全部版本发货已发生，只有 safe 没有提交且明确失败。
// 这是内存状态实验，断言不会冒充数据库 rollback 或 broker 撤回证据。
func TestEffectsAreNotRewound(t *testing.T) {
	bad := RunEffects("swallow")
	if bad.Err != nil || bad.ExternalSent != 1 || bad.Committed != 1 || bad.RolledBack {
		t.Fatalf("错误恢复必须实际复现误报成功与继续提交: %+v", bad)
	}
	commit := RunEffects("commit-defer")
	if commit.Err == nil || commit.ExternalSent != 1 || commit.Committed != 1 {
		t.Fatalf("错误 defer 必须复现带着失败仍提交: %+v", commit)
	}
	safe := RunEffects("safe")
	var failure *PanicFailure
	if !errors.As(safe.Err, &failure) || failure.Stack == "" || safe.ExternalSent != 1 || safe.Committed != 0 || safe.Staged != 0 || !safe.RolledBack {
		t.Fatalf("修复必须失败、留栈、清理本地，且保留已发生外部事实: %+v", safe)
	}
}

// TestWorkerOwnsRecoveryAndCleanup 验证恢复和共享状态完整性是两个独立事实。
// owner 收到 FAILED 并 join；repair=false 仍残留预占，repair=true 明确释放。
func TestWorkerOwnsRecoveryAndCleanup(t *testing.T) {
	for _, repair := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		result, err := RunWorker(ctx, repair)
		cancel()
		wantReserved := 1
		if repair {
			wantReserved = 0
		}
		if err != nil || !result.Joined || result.State != "FAILED" || result.Reserved != wantReserved || result.Failure == nil || !strings.Contains(result.Failure.Stack, "lesson.runTask") {
			t.Fatalf("worker 必须交付失败栈和实际清理状态: %+v err=%v", result, err)
		}
	}
}

// TestRealHTTPRecovery 分别证明默认 server 中断、middleware 500、已发 200 前缀遭中断。
// 测试连接真实 TCP server；不以 HTTP 200 或进程存活代替业务成功。
func TestRealHTTPRecovery(t *testing.T) {
	for _, mode := range []string{"builtin", "middleware", "partial"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			got, err := RunHTTP(ctx, mode)
			if err != nil || got.ExternalSent != 1 {
				t.Fatalf("真实请求必须留下唯一外部效果: %+v err=%v", got, err)
			}
			switch mode {
			case "builtin":
				if got.Status != 0 || got.RequestErr == "" || !got.ServerLogged || got.Failure != nil {
					t.Fatalf("默认 net/http 应留 server 栈并中断连接: %+v", got)
				}
			case "middleware":
				if got.Status != 500 || got.RequestErr != "" || got.ReadErr != "" || got.Failure == nil || got.Failure.Stack == "" || !strings.Contains(got.Body, "订单处理失败") || got.ServerLogged {
					t.Fatalf("未提交响应时 middleware 应明确 500 并保留失败: %+v", got)
				}
			case "partial":
				if got.Status != 200 || got.Body != "accepted-prefix\n" || got.ReadErr != "unexpected EOF" || got.Failure == nil || !got.PartialAborted || !got.ServerLogged {
					t.Fatalf("已发送前缀不可改为完整 500，应中断而非成功: %+v", got)
				}
			}
		})
	}
}

// TestChildEntry 是子进程专属入口。父测试的普通运行没有选择器，直接跳过。
// 通过 -test.run 只运行此测试，危险函数不会在负责验收的父进程中执行。
func TestChildEntry(t *testing.T) {
	name := os.Getenv(ChildCaseEnv)
	if name == "" {
		t.Skip("仅在 M07 故障子进程中执行")
	}
	RunChildCase(name)
	os.Exit(0) // nil 实验已返回，立即交付该二进制退出码；不让 testing 再输出额外结果。
}

// TestActualProcessExitCodes 用本次真实 test 二进制启动子进程，逐个等待并回收。
// -race 运行也会走这些边界，fatal 分支是确定的 mutex 误用，未制造数据竞争。
func TestActualProcessExitCodes(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unrecovered", "goroutine", "fatal", "exit", "nil"} {
		t.Run(name, func(t *testing.T) {
			result, err := RunChild(executable, []string{"-test.run=^TestChildEntry$"}, name, "0")
			if err != nil {
				t.Fatal(err)
			}
			if err := CheckChild(result, false); err != nil {
				t.Fatal(err)
			}
			t.Logf("真实二进制 case=%s exit=%d timeout=%t", name, result.ExitCode, result.TimedOut)
		})
	}
	t.Run("nil-legacy-panicnil-1", func(t *testing.T) {
		result, err := RunChild(executable, []string{"-test.run=^TestChildEntry$"}, "nil", "1")
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckChild(result, true); err != nil {
			t.Fatal(err)
		}
		t.Logf("panicnil=1 exit=%d recover=nil 已验证", result.ExitCode)
	})
}
