package gormcourse

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestRealMySQLAndAMQP由testing显式开启；Run内部的断言来自独立SELECT和broker协议。
// 默认SKIP只是避免普通test偷偷依赖服务器，不算实库验收；race也必须带integration=1。
func TestRealMySQLAndAMQP(t *testing.T) {
	if os.Getenv("GOLEARN_INTEGRATION") != "1" {
		t.Skip("真实MySQL+AMQP未开启：设置GOLEARN_INTEGRATION=1；SKIP不是实库PASS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	var output bytes.Buffer
	err := Run(ctx, &output)
	t.Log(output.String())
	if err != nil {
		t.Fatal(err)
	}
	// 验证整课输出里的真实结束证据都存在，避免入口漏执行后半部分仍被当成功。
	for _, proof := range []string{"[hook.unsafe-rollback.SELECT] quantity=5 orders=0", "[AMQP.get/ack] id=hook-unsafe", "[cancel.server-gone]", "passive404=true", "remaining=0", "[cleanup.pool] open=0 inuse=0 idle=0"} {
		if !strings.Contains(output.String(), proof) {
			t.Fatalf("缺最终证据: %s", proof)
		}
	}
}

// dryDB只借真实dialector构建语句，禁Ping/版本读和默认写事务，因此不会连服务器。
// 它只支持参数化辅助断言，不能代替Run里的真实库存/事务/MQ验证。
func dryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "unused:unused@tcp(127.0.0.1:1)/unused", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

// TestOwnerIsBoundParameter独立检查恶意owner字符串只在Vars中，不进入SQL语法位置。
// 与run的合成普通值不同，这个断言验证的是真实builder参数分离契约。
func TestOwnerIsBoundParameter(t *testing.T) {
	owner := "alice' OR 1=1 --"
	var rows []Stock
	result := dryDB(t).Scopes(owned(owner)).Where("sku = ?", "target").Find(&rows)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if strings.Contains(result.Statement.SQL.String(), owner) {
		t.Fatal("owner被拼进SQL")
	}
	found := false
	for _, value := range result.Statement.Vars {
		if value == owner {
			found = true
		}
	}
	if !found {
		t.Fatal("owner绑定参数丢失")
	}
}

// TestInvalidPageRejectsBeforeSQL不依赖nil lab.db可证明非法请求在执行SQL前已被拒绝。
func TestInvalidPageRejectsBeforeSQL(t *testing.T) {
	l := &lab{}
	for _, input := range [][2]int{{0, 2}, {1, 0}, {1, 101}} {
		if _, _, err := l.page(context.Background(), "owner", input[0], input[1]); err == nil {
			t.Fatal("无效分页被接受")
		}
	}
}

// TestLibraryCannotExtendSocketBudget用真实net.Pipe验证库式clear/extend不能抹去
// 已过期的owner期限。Read真实返回deadline错误，不靠计时sleep猜网络是否还阻塞。
func TestLibraryCannotExtendSocketBudget(t *testing.T) {
	for _, mode := range []string{"handshake-clear", "heartbeat-extend"} {
		t.Run(mode, func(t *testing.T) {
			left, right := net.Pipe()
			defer func() { _ = left.Close(); _ = right.Close() }()
			c := &boundedConn{Conn: left, budget: time.Now().Add(-time.Second)}
			var err error
			if mode == "handshake-clear" {
				err = c.SetDeadline(time.Time{})
			} else {
				err = c.SetReadDeadline(time.Now().Add(time.Hour))
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Read(make([]byte, 1))
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("库取消owner预算: %v", err)
			}
		})
	}
}

// noticedContext只用于确定join已经求值ctx.Done并进入select；观察channel不是调度sleep。
type noticedContext struct {
	context.Context
	noticed chan struct{}
	once    sync.Once
}

func (c *noticedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.noticed) })
	return c.Context.Done()
}

// TestResultIsNotFinalDone先让Result到达、把wrapper卡在清理门，再确定owner进入join。
// 打开门前owner不得返回；Done后原工作、工作清理、wrapper清理均需保留。
func TestResultIsNotFinalDone(t *testing.T) {
	workErr, workCleanup, wrapperCleanup := errors.New("work"), errors.New("conn-close"), errors.New("wrapper-close")
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	task := startOwned(context.Background(), func(context.Context) workResult { return workResult{Err: workErr, CleanupErr: workCleanup} }, func() error { <-gate; return wrapperCleanup })
	defer func() { release(); _ = task.stopAndJoin(nil) }() // 断言失败也放门、取消、独立预算真join。
	select {
	case result := <-task.result:
		task.pending = &result
	case <-time.After(2 * time.Second):
		t.Fatal("Result未到达")
	}
	joinCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	observed := &noticedContext{Context: joinCtx, noticed: make(chan struct{})}
	ownerDone := make(chan error, 1)
	ownerFinished := make(chan struct{})
	// 测试自己的join观察者也是任务：结果channel不代表goroutine已经最终退出。
	// 先登记放门与独立预算等待，后创建goroutine。断言失败也等它退出后才运行
	// 更早的task.stopAndJoin，避免两个owner同时写task.pending/joined。
	defer func() {
		release()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cleanupCancel()
		select {
		case <-ownerFinished:
		case <-cleanupCtx.Done():
			t.Error("join观察者最终退出未确认")
		}
	}()
	go func() {
		defer close(ownerFinished)
		res, e := task.join(observed)
		ownerDone <- errors.Join(res.Err, res.CleanupErr, e)
	}()
	select {
	case <-observed.noticed:
	case <-joinCtx.Done():
		t.Fatal("join未进入等待")
	}
	select {
	case <-ownerDone:
		t.Fatal("只见Result就提前返回")
	default:
	}
	release()
	select {
	case err := <-ownerDone:
		for _, want := range []error{workErr, workCleanup, wrapperCleanup} {
			if !errors.Is(err, want) {
				t.Fatalf("丢错误: %v", want)
			}
		}
	case <-joinCtx.Done():
		t.Fatal("放门后owner未退出")
	}
}

// TestJoinBudgetRetainsFailure固定清理门未开、使用已取消join上下文，因此不会猜耗时。
// 第一次join失败必须保留original并标ErrJoin；随后新预算真join，不遗留任务。
func TestJoinBudgetRetainsFailure(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	task := startOwned(context.Background(), func(context.Context) workResult { return workResult{} }, func() error { <-gate; return nil })
	defer func() { release(); _ = task.stopAndJoin(nil) }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, joinErr := task.join(ctx)
	original := errors.New("owner失败")
	err := errors.Join(original, joinErr)
	if !errors.Is(err, original) || !errors.Is(err, ErrJoin) || !errors.Is(err, context.Canceled) || task.joined {
		t.Fatal("join提前成功或丢原失败")
	}
	release()
	if err := task.stopAndJoin(nil); err != nil {
		t.Fatal(err)
	}
}

// TestEarlyReturnCancelsAndJoins执行生产同一个stopAndJoin保护路径，明确验证取消后
// worker真正Done，而非只看cancel已经调用。工作与两层清理失败也一起保留。
func TestEarlyReturnCancelsAndJoins(t *testing.T) {
	original, workCleanup, wrapperCleanup := errors.New("owner"), errors.New("work-cleanup"), errors.New("wrapper-cleanup")
	var task *ownedTask
	err := func() (err error) {
		task = startOwned(context.Background(), func(ctx context.Context) workResult {
			<-ctx.Done()
			return workResult{Err: ctx.Err(), CleanupErr: workCleanup}
		}, func() error { return wrapperCleanup })
		defer func() { err = task.stopAndJoin(err) }()
		return original
	}()
	for _, want := range []error{original, context.Canceled, workCleanup, wrapperCleanup} {
		if !errors.Is(err, want) {
			t.Fatalf("丢失败: %v", want)
		}
	}
	select {
	case <-task.done:
	default:
		t.Fatal("owner返回时任务尚未Done")
	}
}
