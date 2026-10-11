package cachecourse

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"
)

// TestLive是真实完整实验入口。默认明确SKIP，不把没有服务的纯Go测试算成MySQL/Redis
// 通过；显式开关后Run的业务断言、DROP/EXISTS和资源收尾都执行，失败由testing报告。
func TestLive(t *testing.T) {
	if os.Getenv("GOLEARN_M26_INTEGRATION") != "1" {
		t.Skip("实库未启用；设置 GOLEARN_M26_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	if err := Run(ctx, testWriter{t}); err != nil {
		t.Fatal(err)
	}
}

type testWriter struct{ t *testing.T }

// Write适配io.Writer，日志来自真实Run，测试不复制一套预期实现。
func (w testWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }

// TestCloneIndependent用嵌套对象修改验证复制边界；结构体赋值本身不足以通过断言。
func TestCloneIndependent(t *testing.T) {
	original := DTO{Labels: map[string]string{"source": "mysql"}, Tags: []string{"original"}}
	copy := original.Clone()
	copy.Labels["source"] = "changed"
	copy.Tags[0] = "changed"
	if original.Labels["source"] != "mysql" || original.Tags[0] != "original" {
		t.Fatal("Clone仍借用可变对象")
	}
}

// TestKeyDimensions核对不容易从日志看出的分隔符碰撞和契约版本，完全无网络。
func TestKeyDimensions(t *testing.T) {
	r := registry{prefix: "test:", keys: make(map[string]bool)}
	ids := []scope{{"A:u=B", "C", 1}, {"A", "B:u=C", 1}, {"A", "C", 1}, {"A", "C", 2}}
	seen := map[string]bool{}
	for _, id := range ids {
		k := r.key("view", id, "")
		if seen[k] {
			t.Fatalf("scope碰撞：%s", k)
		}
		seen[k] = true
	}
	for i := 0; i < 100; i++ {
		ttl := jitter(r.key("jitter", scope{"A", string(rune('a' + i)), 1}, ""))
		if ttl < 25*time.Second || ttl > 35*time.Second {
			t.Fatal(ttl)
		}
	}
}

// TestResultDoesNotJoin用已发布业务结果+固定cleanup门证明Done独立。这个辅助测试
// 只验证Go生命周期，不冒充实库；service body副作用在result发布前完成，wrapper仍活着。
func TestResultDoesNotJoin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	o := newOwner(ctx)
	var final error
	defer func() {
		// 唯一guard立即登记；即使Fatal触发Goexit，它仍以独立预算取消/放门/真join，
		// 并报告清理错误。后面主动join只是观察正常终态，不重复执行finish的停止副作用。
		o.finish(&final)
		if final != nil {
			t.Error(final)
		}
	}()
	cleanup := newGate()
	o.registerGate(cleanup)
	var g singleflight.Group
	ticket := startFlight(o, &g, "one", o.ctx, true, cleanup, func(context.Context) (DTO, error) { return DTO{Found: true}, nil })
	r, err := ticket.wait(ctx)
	if err != nil || !r.Row.Found {
		t.Fatal(err)
	}
	select {
	case <-ticket.done:
		t.Fatal("result不能使cleanup门后的任务结束")
	default:
	}
	cleanup.open()
	if err = o.join(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestCanceledCallerCleanup让caller先取消，body留在真屏障等待服务owner；返回Canceled
// 之后必须另finish，释放门、消费库Result并等所有Done，不能依赖caller退出自动join。
func TestCanceledCallerCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	o := newOwner(ctx)
	var final error
	defer func() {
		// 创建owner后立即登记：下面每个Fatal都必须先经过这一个guard。
		o.finish(&final)
		if final != nil {
			t.Error(final)
		}
		for _, task := range o.tasks {
			select {
			case <-task.done:
			default:
				t.Error("guard漏join候选body或drainer")
			}
		}
	}()
	seen, hold := newGate(), newGate()
	o.registerGate(hold)
	var g singleflight.Group
	ticket := startFlight(o, &g, "owned", o.ctx, true, nil, func(c context.Context) (DTO, error) { seen.open(); return DTO{}, await(c, hold.c, "测试body门") })
	if err := await(ctx, seen.c, "测试body进入"); err != nil {
		t.Fatal(err)
	}
	request, requestCancel := context.WithCancel(ctx)
	requestCancel()
	if _, err := ticket.wait(request); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-ticket.done:
		t.Fatal("取消caller错误地结束共享工作")
	default:
	}
	// 不在body另做finish；函数正常返回或任意Fatal都由同一个guard加入，再检查全部Done。
}

// TestCleanupPreservesErrors独立验证提前失败路径的error树，工作错误和主错误不能覆盖。
// owner创建的worker只在ctx.Done等待，finish发取消并实际join之后才断言哨兵。
func TestCleanupPreservesErrors(t *testing.T) {
	primary := errors.New("原业务失败")
	cleanupError := errors.New("工作收尾失败")
	final := primary
	o := newOwner(context.Background())
	defer func() {
		// 唯一guard既保留原失败，也观察故意返回的工作清理错误，不留无人检查的变量。
		o.finish(&final)
		if !errors.Is(final, primary) || !errors.Is(final, cleanupError) || !errors.Is(final, context.Canceled) {
			t.Error(final)
		}
	}()
	entered := newGate()
	o.start(func(c context.Context) error { entered.open(); <-c.Done(); return errors.Join(c.Err(), cleanupError) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := await(ctx, entered.c, "清理worker进入"); err != nil {
		t.Fatal(err)
	}
}

// TestAdmissionCompletionBound是真正接纳/收尾helper的有限生命周期断言，不访问db。
// 把旧工作固定停在Active--之前，再试新接纳：只有旧permit仍被占用才能拒绝且Peak=2。
// 若把finishRead的<-slots移回函数开头，这个屏障之后新接纳就成功并会报告Peak=3；
// 因而它验证的是可触发原缺陷的转换边界，不是sleep多跑几次碰运气，也不伪称真实SQL。
func TestAdmissionCompletionBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := newSource(nil, "") // 仅共用source的预算helper；Queries必须始终为0。
	o := newOwner(ctx)
	defer func() {
		var cleanup error
		o.finish(&cleanup) // 所有gate先放行并join之后，才归还测试本身尚持有的permit。
		for i := 0; i < 2 && len(s.slots) > 0; i++ {
			s.finishRead(nil)
		}
		if cleanup != nil {
			t.Error(cleanup)
		}
		if s.snapshot().Active != 0 || len(s.slots) != 0 {
			t.Error("辅助预算也必须最终归零", s.snapshot())
		}
	}()
	for step := 0; step < 16; step++ {
		if err := s.admitRead(); err != nil {
			t.Fatal(err)
		}
		if err := s.admitRead(); err != nil {
			t.Fatal(err)
		}
		beforeCount, release := newGate(), newGate()
		o.registerGate(release)
		finished := o.start(func(context.Context) error {
			s.finishRead(func() {
				beforeCount.open()
				// 这项非ctx门由owner无条件打开；Fatal/超时路径仍先放门再独立预算join。
				<-release.c
			})
			return nil
		})
		if err := await(ctx, beforeCount.c, "旧工作计数收尾边界"); err != nil {
			t.Fatal(err)
		}
		if err := s.admitRead(); !errors.Is(err, ErrOverload) {
			t.Fatalf("step=%d：旧Active尚未扣时不得复用permit，err=%v counts=%+v", step, err, s.snapshot())
		}
		if stat := s.snapshot(); stat.Active != 2 || stat.Peak != 2 {
			t.Fatal("转换中计数必须保持容量2", stat)
		}
		release.open()
		if err := await(ctx, finished.done, "计数扣减和permit归还Done"); err != nil {
			t.Fatal(err)
		}
		if finished.err != nil {
			t.Fatal(finished.err)
		}
		if err := s.admitRead(); err != nil {
			t.Fatal("旧工作收尾后新接纳应成功", err)
		}
		if stat := s.snapshot(); stat.Active != 2 || stat.Peak != 2 {
			t.Fatal("新旧接替不能记录Peak=3", stat)
		}
		s.finishRead(nil)
		s.finishRead(nil)
		if stat := s.snapshot(); stat.Active != 0 || stat.Queries != 0 {
			t.Fatal("辅助转换结束后应归零且没有SQL", stat)
		}
	}
	if stat := s.snapshot(); stat.Peak != 2 || stat.Rejected != 16 {
		t.Fatal("16个确定性转换必须各拒绝1次且Peak始终2", stat)
	}
}

// TestEarlyFailureJoinsRegisteredTasks用一个会提前return的实际owner范围验证guard。
// 三worker各自登记进入门、等待ctx取消，再写自己的cleanup槽；caller只能在guard
// 真join之后读取这些槽。原失败和故意的工作收尾错误都要保留，不把cancel当退出证据。
func TestEarlyFailureJoinsRegisteredTasks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	primary := errors.New("owner提前失败")
	cleanupError := errors.New("worker收尾哨兵")
	var tasks []*task
	cleaned := [3]bool{}
	final := func() (err error) {
		o := newOwner(ctx)
		defer o.finish(&err) // 创建owner即登记唯一guard，下面提前return也不跳过。
		for i := range cleaned {
			entered := newGate()
			task := o.start(func(c context.Context) error {
				entered.open()
				<-c.Done()
				cleaned[i] = true // 每项只写自己的槽；owner等Done后才读，不以取消通知同步。
				return errors.Join(c.Err(), cleanupError)
			})
			tasks = append(tasks, task)
			if e := await(ctx, entered.c, "提前失败worker进入"); e != nil {
				return errors.Join(primary, e)
			}
		}
		return primary // body没有显式cancel/放门/join；这些都必须由guard完成。
	}()
	if !errors.Is(final, primary) || !errors.Is(final, cleanupError) || !errors.Is(final, context.Canceled) {
		t.Fatal("guard没有保留原失败/工作收尾/取消原因", final)
	}
	if len(tasks) != 3 {
		t.Fatal("应该登记三项任务", len(tasks))
	}
	for i, task := range tasks {
		select {
		case <-task.done:
		default:
			t.Fatal("提前返回后存在未join工作", i)
		}
		if !cleaned[i] {
			t.Fatal("Done前的收尾没有完成", i)
		}
	}
}

var _ io.Writer = testWriter{}
