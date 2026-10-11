package txcourse

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// TestMySQL 复用完整真实入口：每个断言最终查询本 run 独占表，通信故障也是
// 同一个真实 MySQL。默认 test 不要求私有凭据；显式开启才算实库验收，race 同样开启。
func TestMySQL(t *testing.T) {
	if os.Getenv("GOLEARN_INTEGRATION") != "1" {
		t.Skip("本测试需要 READY 实库；以 GOLEARN_INTEGRATION=1 开启，skip 不算实库通过")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	var report bytes.Buffer // Run 内只有 join 后 owner 输出，没有并发写本 buffer。
	if err := Run(ctx, &report); err != nil {
		t.Fatalf("完整真实事务实验失败: %v\n%s", err, report.String())
	}
	t.Log(report.String())
	// 这两行核验清理报告；业务正确性在 Run 中由独立固定预期+SQL核验，
	// 不是用日志字符串代替 SELECT。真实表 DROP remaining0 和池 Close 已完成。
	if !bytes.Contains(report.Bytes(), []byte("[cleanup.tables] remaining=0")) ||
		!bytes.Contains(report.Bytes(), []byte("[cleanup.DB] open=0 inuse=0 idle=0")) {
		t.Fatal("缺少最终资源清理证据")
	}
}

// enteredContext 只用于独立同步断言；owner 进入 join 的 select 求值 Done 时
// 发通知。测试由此知道已到等待点，不使用 sleep 或 goroutine 总数猜调度。
type enteredContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *enteredContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

// TestResultReadyStillWaitsForFinished 实际运行 ownedTask wrapper，工作结果已被
// owner 接收并缓存，但 cleanup gate 尚未打开。独立观察者只检查真实 channel，
// 不把 t.joined 自报字段当作清理完成证据。
func TestResultReadyStillWaitsForFinished(t *testing.T) {
	workErr := errors.New("work failed")
	workCleanupErr := errors.New("fn resource cleanup failed")
	cleanupErr := errors.New("wrapper cleanup failed")
	ownerErr := errors.New("owner original failed")
	gate, cleanupEntered := make(chan struct{}), make(chan struct{})
	var release sync.Once
	var ownerFinished <-chan struct{}
	worker := startOwned(context.Background(), func(ctx context.Context) taskResult {
		<-ctx.Done() // 当前真实等待工作取消，不持有数据库资源。
		return taskResult{Err: errors.Join(workErr, ctx.Err()), CleanupErr: workCleanupErr}
	}, func() error {
		close(cleanupEntered)
		<-gate // 即使 Result 已交付，wrapper 仍在这条清理等待点。
		return cleanupErr
	})
	// 任一断言失败仍放 gate、取消、join；测试自己拥有这些故障门。
	t.Cleanup(func() {
		release.Do(func() { close(gate) })
		worker.cancel()
		ctx, cancel := shortContext()
		defer cancel()
		select {
		case <-worker.finished:
		case <-ctx.Done():
			t.Error("测试收尾仍未等到 worker Finished")
		}
		if ownerFinished != nil {
			select {
			case <-ownerFinished:
			case <-ctx.Done():
				t.Error("测试收尾仍未等到 owner Done")
			}
		}
	})
	worker.cancel()
	ctx, cancel := shortContext()
	defer cancel()
	select {
	case worker.pending = <-worker.result:
		worker.hasResult = true // 缓存归 owner，读取结果不能消费 finished 的广播。
	case <-ctx.Done():
		t.Fatal("工作取消后没有交付 Result")
	}
	select {
	case <-cleanupEntered:
	case <-ctx.Done():
		t.Fatal("wrapper 未到 cleanup gate")
	}
	probe := &enteredContext{Context: ctx, entered: make(chan struct{})}
	returned := make(chan error, 1)
	ownerDone := make(chan struct{})
	ownerFinished = ownerDone
	go func() {
		defer close(ownerDone)
		returned <- worker.join(probe, ownerErr)
	}()
	select {
	case <-probe.entered:
	case <-ctx.Done():
		t.Fatal("owner 没进入 Finished 等待点")
	}
	select {
	case e := <-returned:
		t.Fatalf("Result 已可读就提前返回，cleanup未完成: %v", e)
	default:
	}
	select {
	case <-worker.finished:
		t.Fatal("清理 gate 未开却广播 Finished")
	default:
	}
	release.Do(func() { close(gate) })
	select {
	case e := <-returned:
		for _, cause := range []error{ownerErr, workErr, workCleanupErr, cleanupErr, context.Canceled} {
			if !errors.Is(e, cause) {
				t.Errorf("Join 丢失原因 %v: %v", cause, e)
			}
		}
	case <-ctx.Done():
		t.Fatal("放行清理后 owner 未返回")
	}
	<-ownerDone // 收到 owner 结果仍等它最后 defer，所有测试任务有实际 Done。
	select {
	case <-worker.finished:
	default:
		t.Fatal("owner 返回时真实 Finished 尚未关闭")
	}
}

// TestJoinTimeoutRetainsOriginal 是真实的预算耗尽断言，gate 固定清理尚未完成。
// 20ms 用来触发 deadline，不用来猜线程顺序；超时不能报已 Joined，随后放门并
// 使用新预算真正回收 worker，不能让“测试了超时”变成实际泄漏。
func TestJoinTimeoutRetainsOriginal(t *testing.T) {
	original := errors.New("request failed before cleanup")
	cleanupErr := errors.New("cleanup failure retained")
	gate, entered := make(chan struct{}), make(chan struct{})
	var release sync.Once
	worker := startOwned(context.Background(), func(context.Context) taskResult { return taskResult{} }, func() error {
		close(entered)
		<-gate
		return cleanupErr
	})
	defer func() {
		release.Do(func() { close(gate) })
		worker.cancel()
		ctx, cancel := shortContext()
		defer cancel()
		select {
		case <-worker.finished:
		case <-ctx.Done():
			t.Error("预算故障后的兜底清理未完成")
		}
	}()
	outer, stop := shortContext()
	defer stop()
	select {
	case <-entered:
	case <-outer.Done():
		t.Fatal("cleanup 尚未启动")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := worker.join(ctx, original)
	cancel()
	if !errors.Is(err, original) || !errors.Is(err, ErrJoin) || !errors.Is(err, context.DeadlineExceeded) || worker.joined {
		t.Fatalf("预算耗尽分类/原错误错误: joined=%t err=%v", worker.joined, err)
	}
	select {
	case <-worker.finished:
		t.Fatal("gate 未放行时 Finished 不应关闭")
	default:
	}
	release.Do(func() { close(gate) })
	err = worker.join(outer, original)
	if !errors.Is(err, original) || !errors.Is(err, cleanupErr) || errors.Is(err, ErrJoin) || !worker.joined {
		t.Fatalf("新预算 join 未保留真实收尾: %v", err)
	}
}

// TestEarlyReturnStopsAndJoins 实际走生产使用的 deferred stopAndJoin：owner在
// 尚未读Result时提前失败，清理仍须取消worker、等真正Finished、保留原失败和
// fn/wrapper两层清理失败。这里只验证Go owner契约，SQL事实仍由实库测试提供。
func TestEarlyReturnStopsAndJoins(t *testing.T) {
	original := errors.New("owner early return")
	fnCleanup := errors.New("fn cleanup")
	wrapperCleanup := errors.New("wrapper cleanup")
	cleaned := make(chan struct{})
	var worker *ownedTask
	err := func() (err error) {
		worker = startOwned(context.Background(), func(ctx context.Context) taskResult {
			<-ctx.Done()
			return taskResult{Err: ctx.Err(), CleanupErr: fnCleanup}
		}, func() error {
			close(cleaned)
			return wrapperCleanup
		})
		defer worker.stopAndJoin(&err)
		return original
	}()
	for _, cause := range []error{original, context.Canceled, fnCleanup, wrapperCleanup} {
		if !errors.Is(err, cause) {
			t.Errorf("提前退出收尾丢失原因 %v: %v", cause, err)
		}
	}
	select {
	case <-worker.finished:
	default:
		t.Fatal("owner提前return之后worker实际尚未退出")
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("worker wrapper实际清理尚未发生")
	}
}

// TestPacketFraming 是纯 Go 的 TCP packet 辅助断言，不证明 MySQL 提交。
// 它验证代理在读碎片/截断/异常长度时的边界，真实 OK+表内容由 TestMySQL 验收。
func TestPacketFraming(t *testing.T) {
	expected := []byte{7, 0, 0, 0, 3, 'C', 'O', 'M', 'M', 'I', 'T'}
	packet, err := readPacket(oneByteReader{bytes.NewReader(expected)})
	if err != nil || !bytes.Equal(packet, expected) {
		t.Fatalf("TCP 拆包处理错误: %v %v", packet, err)
	}
	if _, err := readPacket(bytes.NewReader(expected[:6])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("截断 payload 必须返回真实读取失败: %v", err)
	}
	if _, err := readPacket(bytes.NewReader([]byte{255, 255, 255, 0})); err == nil {
		t.Fatal("本课不支持超过4MiB/分片大包，不能默默转发")
	}
	var out bytes.Buffer
	if err := writePacket(shortWriter{&out}, expected); err != nil || !bytes.Equal(out.Bytes(), expected) {
		t.Fatalf("短 Write 没完整写出 packet: %v", err)
	}
}

// oneByteReader 故意将每次读限制为1字节，检验 TCP 没有 message 边界这一事实。
type oneByteReader struct{ io.Reader }

func (r oneByteReader) Read(p []byte) (int, error) { return r.Reader.Read(p[:1]) }

// shortWriter 故意每次只接受一个字节，纯函数范围内检验 writePacket 的短写循环。
type shortWriter struct{ io.Writer }

func (w shortWriter) Write(p []byte) (int, error) { return w.Writer.Write(p[:1]) }

// TestProxyOwnerClosesAccept 验证尚无客户端的提前 return：Close 解除真实 Accept，
// stopAndJoin 等总 Finished，listener 已无法 Dial。没有访问/修改共享 MySQL。
func TestProxyOwnerClosesAccept(t *testing.T) {
	p, err := startProxy("127.0.0.1:1", dropCommitResponse)
	if err != nil {
		t.Fatal(err)
	}
	address := p.addr()
	if _, err := p.stopAndJoin(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.finished:
	default:
		t.Fatal("stopAndJoin 返回却没有真实 Finished")
	}
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("代理退出后 listener 仍可连接")
	}
}
