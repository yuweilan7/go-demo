package lockcourse

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestCreateAttemptOwnsUnconfirmedEffect必须显式启用真实MySQL。应用执行边界
// 实际CREATE成功后故意交错误给create caller；不是截断wire，也不冒称驱动丢OK。
// 独立SQL先确认副作用存在、清单已归属，再真实DROP并查询不存在；旧名字不认领。
func TestCreateAttemptOwnsUnconfirmedEffect(t *testing.T) {
	if os.Getenv("GOLEARN_INTEGRATION") != "1" {
		t.Skip("需要真实MySQL/GOLEARN_INTEGRATION=1；应用边界注入不是wire故障")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, _, err := openPool()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := db.Close(); e != nil {
			t.Error(e)
		}
		if st := db.Stats(); st.InUse != 0 || st.OpenConnections != 0 {
			t.Error("DB未最终归还", st)
		}
	}()
	var output bytes.Buffer
	s, err := newStore(db, &output)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := s.drop(); e != nil {
			t.Error("真实DDL断言最终清理", e)
		}
	}()
	unconfirmed := errors.New("application boundary discarded CREATE confirmation")
	err = s.createWithBoundary(ctx, func(ctx context.Context, query string) error {
		// 清单必须在真正发SQL之前已有本run名字；这一步暴露原“成功后登记”缺口。
		if len(s.names) != 1 || s.names[0] != s.stock {
			return errors.New("发送CREATE前未登记精确尝试")
		}
		if _, e := db.ExecContext(ctx, query); e != nil {
			return e
		}
		return unconfirmed
	})
	if !errors.Is(err, unconfirmed) {
		t.Fatal("上层应保留未确认失败", err)
	}
	var count int
	query := "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?"
	if err = db.QueryRowContext(ctx, query, s.stock).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("真实CREATE副作用应存在", count)
	}
	t.Logf("DDL application-boundary injection table=%s owned_attempts=%d actual_before_drop=%d", s.stock, len(s.names), count)
	if err = s.drop(); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, query, s.stock).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("确认未知的CREATE逃掉清理", count)
	}
	// 准备owner在同一独占随机名创建真实表；另一个待测store遇到它必须拒绝。
	// 准备表仍由s这个测试owner清理，blocked清单为空，不能用清理误删已有对象。
	if _, err = db.ExecContext(ctx, "CREATE TABLE `"+s.stock+"` (id INT PRIMARY KEY) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	blocked := &store{db: db, out: &output, stock: s.stock}
	if err = blocked.create(ctx); err == nil {
		t.Fatal("没有拒绝preflight已存在名字")
	}
	if len(blocked.names) != 0 {
		t.Fatal("已有名字被错误认领", blocked.names)
	}
	if err = blocked.drop(); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, query, s.stock).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("blocked清理误删了准备owner的表")
	}
	if err = s.drop(); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, query, s.stock).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("准备owner没精确清理", count)
	}
	t.Logf("DDL unconfirmed remaining=0; occupied-name owned_attempts=0 preserved_before_owner_drop=1; final_remaining=%d", count)
}

// TestHTTPForcedCloseJoinsAcceptedTail使用真实loopback HTTP。只接纳/bad，handler
// 业务完成却被最后defer门挡住；实际Shutdown耗尽、Close使client退出、Serve退出
// 后，close仍必须等handlerDone。放尾部门后原失败/Shutdown失败/尾部失败均保留。
func TestHTTPForcedCloseJoinsAcceptedTail(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	workGates := [2]*gate{newGate(), newGate()}
	tailGate := newGate()
	tailEntered := make(chan struct{})
	prior, tailFailure := errors.New("early owner failure"), errors.New("controlled handler tail cleanup failure")
	s, err := startSlowWithTail(ctx, workGates, func(i int) error {
		if i != 0 {
			return errors.New("测试不应接纳/fixed")
		}
		close(tailEntered)
		if e := tailGate.wait(context.Background()); e != nil {
			return e
		}
		return tailFailure
	})
	if err != nil {
		t.Fatal(err)
	}
	client := start(ctx, func(ctx context.Context) taskResult { return taskResult{Err: s.request(ctx, "/bad")} }, nil)
	var closing *task
	// 失败也开自有门：先等closing唯一owner退出（它负责client join），没有启动
	// closing时本defer自行close并join client；禁止两个caller并发读task.join状态。
	defer func() {
		for _, g := range workGates {
			g.open()
		}
		tailGate.open()
		cleanup, c := cleanupContext()
		defer c()
		if closing != nil {
			closing.cancel()
			if _, e := closing.join(cleanup); e != nil {
				t.Error("closing owner未退出", e)
			}
		}
		// closing如果提前panic/预算失败，仍在其真实退出之后重试fixture收尾；
		// 不与运行中的唯一closing owner并发操作task.join字段。
		if closing == nil || closing.joined {
			var e error
			s.close(&e)
			if e != nil && (!errors.Is(e, tailFailure) || errors.Is(e, ErrJoin) || errors.Is(e, ErrHandlerJoin) || errors.Is(e, context.DeadlineExceeded)) {
				t.Error(e)
			}
			if !client.joined {
				client.cancel()
				fresh, cancelFresh := cleanupContext()
				if _, e := client.join(fresh); e != nil {
					t.Error(e)
				}
				cancelFresh()
			}
		}
	}()
	if _, err = receive(ctx, s.entered[0]); err != nil {
		t.Fatal(err)
	}
	workGates[0].open()
	select {
	case <-tailEntered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	joinEntered := make(chan struct{})
	closing = start(ctx, func(context.Context) (r taskResult) {
		defer captureTaskPanic(&r)
		r.Err = prior
		s.closeWith(&r.Err, 20*time.Millisecond, func() (context.Context, context.CancelFunc) {
			// 这工厂只在Shutdown/Serve阶段之后调用；新的Background2秒预算，
			// observedContext通知真正求值handler等待点，而非根据sleep猜close进度。
			fresh, c := context.WithTimeout(context.Background(), 2*time.Second)
			return &observedContext{Context: fresh, entered: joinEntered}, c
		})
		fresh, c := cleanupContext()
		defer c()
		cr, e := client.join(fresh)
		r.Err = errors.Join(r.Err, e, cr.Err, cr.Cleanup)
		return
	}, nil)
	select {
	case <-joinEntered:
	case <-ctx.Done():
		t.Fatal("没有进入独立handler join", ctx.Err())
	}
	// TCP被强制关闭使client真实结束，Serve也完成；它们均不能替代handler尾部。
	select {
	case <-client.finished:
	case <-ctx.Done():
		t.Fatal("force Close未解除client I/O", ctx.Err())
	}
	select {
	case <-s.worker.finished:
	case <-ctx.Done():
		t.Fatal("Serve未退出", ctx.Err())
	}
	records := s.seal()
	if len(records) != 1 {
		t.Fatal("应只记录实际/bad，不能等待未启动/fixed", len(records))
	}
	select {
	case <-records[0].done:
		t.Fatal("尾部门没开却handlerDone")
	default:
	}
	select {
	case <-closing.finished:
		t.Fatal("close把TCP/Serve退出误当handler最终完成")
	default:
	}
	tailGate.open()
	r, err := closing.join(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, cause := range []error{prior, context.DeadlineExceeded, tailFailure} {
		if !errors.Is(r.Err, cause) {
			t.Fatalf("原/Shutdown/尾部错误丢失%v: %v", cause, r.Err)
		}
	}
	if errors.Is(r.Err, ErrHandlerJoin) || errors.Is(r.Err, ErrJoin) || r.Cleanup != nil {
		t.Fatal("放门后没有真实收尾", r)
	}
	select {
	case <-records[0].done:
	default:
		t.Fatal("handler最后defer未完成")
	}
	if !s.worker.joined || !client.joined {
		t.Fatal("Serve/client没有实际join")
	}
	t.Log("HTTP early failure: admitted=1 (/bad), /fixed never admitted; Shutdown=DeadlineExceeded; client/Serve finished before tail; owner only finished after tail gate; original+Shutdown+tail errors retained")
}
