package lesson_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	lesson "go-demo/lessons/m15_structured_concurrency"
	"golang.org/x/sync/errgroup"
)

// testContext由每项测试创建自己的预算，不用全局共享ctx；Cleanup在测试结束释放timer。
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// assertCatalog直接从订单的业务预期核对价格、缺价ID和金额合计18500。
// 不拿课程check/PASS作证，不从实现内部map生成预期，避免把相同错误复制到断言里。
func assertCatalog(t *testing.T, rows []lesson.Row) {
	t.Helper()
	want := []struct {
		id    string
		price int64
	}{{"SKU-A", 12500}, {"SKU-B", 1800}, {"SKU-C", 0}, {"SKU-D", 4200}}
	if len(rows) != 4 {
		t.Fatalf("结果数=%d，应为4", len(rows))
	}
	var sum int64
	for i, row := range rows {
		if row.ID != want[i].id || row.Price != want[i].price {
			t.Fatalf("行%d=%+v，业务预期=%+v", i, row, want[i])
		}
		if row.ID == "SKU-C" {
			if !errors.Is(row.Err, lesson.ErrUnavailable) {
				t.Fatalf("C缺价错误丢失: %v", row.Err)
			}
		} else {
			if row.Err != nil {
				t.Fatalf("成功SKU失败: %v", row.Err)
			}
			sum += row.Price
		}
	}
	if sum != 18500 {
		t.Fatalf("三项成功金额=%d，应为18500", sum)
	}
}

// TestActualLegacyPool从本仓库真实pool.go构建两个反例，不以重新实现的坏池替代。
// 成功日志同时要求实际RLock/writer关系与救援后join；所有child由Command.Run回收。
func TestActualLegacyPool(t *testing.T) {
	// 首次race编译原文件child可能超过普通业务10秒，因此此测试另给40秒构建/运行总预算。
	// child实际阻塞仍由harness4秒、进程监督20秒与独立清理2秒限制，没有放宽坏路径无限等待。
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	_, source, _, _ := runtime.Caller(0)
	moduleDir := filepath.Dir(source)
	out, err := lesson.LegacyProbe(ctx, moduleDir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, marker := range []string{"submitted=true writer-pending=true Submit=false Close=false", "queue drained -> RUnlock -> Lock -> workers joined", "worker waits Submit(inner)", "outer and all workers joined"} {
		if !strings.Contains(out, marker) {
			t.Fatalf("缺少真实同步观察%s\n%s", marker, out)
		}
	}
	t.Log(out)
}

// TestRealHTTPBatch验证真实协议、逆序完成仍正确配SKU、活跃上限和所有handler收尾。
func TestRealHTTPBatch(t *testing.T) {
	r, err := lesson.HTTPScenario(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	assertCatalog(t, r.Sequential)
	assertCatalog(t, r.Parallel)
	if fmt.Sprint(r.Completed) != "[SKU-B SKU-C SKU-D SKU-A]" {
		t.Fatalf("屏障完成顺序=%v", r.Completed)
	}
	if r.Counts.Max != 2 || r.Counts.Active != 0 || r.Counts.Started != 4 || r.Counts.Finished != 4 {
		t.Fatalf("HTTP资源结局=%+v", r.Counts)
	}
}

// TestRealHTTPCancel先等两项真实handler进入gate，再取消客户端批次，独立清理预算关闭服务。
// 它不以client返回Canceled推断server退出，而是在fixture.Close真正加入后核对handler计数。
func TestRealHTTPCancel(t *testing.T) {
	scope := testContext(t)
	fixture, err := lesson.NewHTTPFixture(true)
	if err != nil {
		t.Fatal(err)
	}
	client := lesson.NewHTTPClient(fixture.URL)
	ctx, cancel := context.WithCancel(scope)
	var owner sync.WaitGroup
	joined := make(chan struct{})
	var batchErr error
	owner.Go(func() { _, batchErr = lesson.FanOut(ctx, lesson.DemoIDs, 2, true, client.Fetch); close(joined) })
	t.Cleanup(func() {
		var cleanErr error
		lesson.Cleanup(&cleanErr, func() { cancel(); fixture.ReleaseAll() }, joined, owner.Wait)
		client.Close()
		cleanErr = errors.Join(cleanErr, fixture.Close())
		if cleanErr != nil {
			t.Error(cleanErr)
		}
	})
	for range 2 {
		select {
		case <-fixture.Entered:
		case <-scope.Done():
			t.Fatal(scope.Err())
		}
	}
	cancel()
	if err := lesson.Await(scope, joined, "HTTP取消owner"); err != nil {
		t.Fatal(err)
	}
	owner.Wait()
	if !errors.Is(batchErr, context.Canceled) {
		t.Fatalf("错误链=%v", batchErr)
	}
	if err := fixture.Close(); err != nil {
		t.Fatal(err)
	}
	counts := fixture.Counts()
	if counts.Active != 0 || counts.Started != 2 || counts.Finished != 2 {
		t.Fatalf("handler没有实际退出: %+v", counts)
	}
}

// TestRealHTTPResponseBoundary用真实loopback响应检查4096字节边界，不用纯函数mock替代Body读取。
// 合法JSON后面有超限空白/垃圾时必须拒绝，防止LimitReader制造的EOF掩盖未读后缀。
func TestRealHTTPResponseBoundary(t *testing.T) {
	valid := `{"id":"SKU-A","price":12500,"code":""}`
	cases := []struct {
		name, body        string
		tooLarge, invalid bool
	}{
		{"valid", valid, false, false},
		{"exact4096", valid + strings.Repeat(" ", 4096-len(valid)), false, false},
		{"overflowSpace", valid + strings.Repeat(" ", 4097-len(valid)), true, false},
		{"overflowGarbage", valid + strings.Repeat(" ", 4096-len(valid)) + "garbage", true, false},
		{"secondObject", valid + `{}`, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// handler只返回该测试固定字节，httptest.NewServer实际绑定TCP并执行HTTP handler。
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if _, err := io.WriteString(w, tc.body); err != nil {
					t.Errorf("真实响应写出: %v", err)
				}
			}))
			t.Cleanup(server.Close)
			client := lesson.NewHTTPClient(server.URL)
			t.Cleanup(client.Close)
			price, err := client.Fetch(testContext(t), "SKU-A")
			if tc.tooLarge {
				if !errors.Is(err, lesson.ErrResponseTooLarge) || price != 0 {
					t.Fatalf("超限响应被接受: len=%d price=%d err=%v", len(tc.body), price, err)
				}
				return
			}
			if tc.invalid {
				if err == nil {
					t.Fatal("第二个JSON对象被接受")
				}
				return
			}
			if err != nil || price != 12500 {
				t.Fatalf("完整合法响应失败: len=%d price=%d err=%v", len(tc.body), price, err)
			}
		})
	}
}

// TestHTTPShutdownBeforeHandler实际打开TCP却不发送请求，StateNew已登记、handler尚未Add。
// Shutdown两秒预算到期后Close终止此连接；先Serve join再连接Closed，再handler Wait，
// 正是旧代码handlers.Wait可能先看到0的边界。没有sleep推测handler是否已开始。
func TestHTTPShutdownBeforeHandler(t *testing.T) {
	fixture, err := lesson.NewHTTPFixture(true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.Close(); err != nil {
			t.Error(err)
		}
	})
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(fixture.URL, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Error(err)
		}
	})
	if err := lesson.Await(testContext(t), fixture.Accepted, "真实TCP StateNew"); err != nil {
		t.Fatal(err)
	}
	if fixture.Counts().Started != 0 || fixture.ConnectionCounts().Active != 1 {
		t.Fatalf("前置连接状态错误: handlers=%+v connections=%+v", fixture.Counts(), fixture.ConnectionCounts())
	}
	err = fixture.Close()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StateNew未发请求，应耗尽graceful预算: %v", err)
	}
	connections := fixture.ConnectionCounts()
	if connections.Active != 0 || connections.Accepted != 1 || connections.Closed != 1 || fixture.Counts().Started != 0 {
		t.Fatalf("预handler连接没有真实回收: %+v %+v", connections, fixture.Counts())
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if n, err := conn.Read(b[:]); n != 0 || err != io.EOF {
		t.Fatalf("client应观察真实对端关闭: n=%d err=%v", n, err)
	}
}

// TestErrgroupWaitAndAdmission核对两个机制边界，不根据执行耗时判断有没有阻塞。
func TestErrgroupWaitAndAdmission(t *testing.T) {
	r, err := lesson.GroupScenario(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if !r.AdmissionBlocked || !r.WaitBlocked || !errors.Is(r.First, lesson.ErrBackend) || r.Counts.Active != 0 || r.Counts.Finished != 2 {
		t.Fatalf("group协议=%+v", r)
	}
}

// TestWeightedAdmission证明第三项尚未创建业务工作、两种权重合计不超过3、取消后全部归还。
func TestWeightedAdmission(t *testing.T) {
	c, rows, batchErr, err := lesson.SemaphoreScenario(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxUnits != 3 || c.Max != 2 || c.Units != 0 || c.Active != 0 || c.Started != 2 || c.Finished != 2 {
		t.Fatalf("加权资源=%+v", c)
	}
	if !errors.Is(batchErr, context.Canceled) || len(rows) != 3 || rows[2].ID != "SKU-D" || !errors.Is(rows[2].Err, lesson.ErrNotStarted) {
		t.Fatalf("未派发第三项应保留D: rows=%+v err=%v", rows, batchErr)
	}
}

// TestBoundedFanOutBusinessInvariant用128个SKU验收稳定ID、独立整数金额、部分业务失败与上限。
// 这里Fetch是明确的内存单测替身；真实HTTP已由上面两个测试另行验收，未声称实库。
func TestBoundedFanOutBusinessInvariant(t *testing.T) {
	for _, limit := range []int{1, 3, 8} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			ctx := testContext(t)
			ids := make([]string, 128)
			for i := range ids {
				ids[i] = strconv.Itoa(i + 1)
			}
			tracker := &lesson.Tracker{}
			fetch := func(_ context.Context, id string) (int64, error) {
				leave := tracker.Enter(1)
				defer leave()
				n, _ := strconv.Atoi(id)
				if n%7 == 0 {
					return 0, lesson.ErrUnavailable
				}
				return int64(n) * 100, nil
			}
			rows, err := lesson.FanOut(ctx, ids, limit, true, fetch)
			if err != nil {
				t.Fatal(err)
			}
			var got, want int64
			failures := 0
			for i, row := range rows {
				n := i + 1
				if row.ID != strconv.Itoa(n) {
					t.Fatalf("ID配错=%+v", row)
				}
				if n%7 == 0 {
					failures++
					if !errors.Is(row.Err, lesson.ErrUnavailable) || row.Price != 0 {
						t.Fatalf("失败SKU=%+v", row)
					}
				} else {
					want += int64(n) * 100
					got += row.Price
					if row.Err != nil || row.Price != int64(n)*100 {
						t.Fatalf("业务价格=%+v", row)
					}
				}
			}
			c := tracker.Counts()
			// 1..128共825600分，去掉7的倍数119700分，独立业务合计705900。
			if failures != 18 || got != want || got != 705900 || c.Max > limit || c.Active != 0 || c.Started != 128 || c.Finished != 128 {
				t.Fatalf("独立合计/容量/资源失败 got=%d want=%d failures=%d counts=%+v", got, want, failures, c)
			}
		})
	}
}

// TestPoolProtocols逐条核对所有权转移：失败Submit没有Ticket，成功Submit恰有终态，最后worker退出。
func TestPoolProtocols(t *testing.T) {
	paths := []struct {
		name string
		run  func(context.Context) (lesson.PoolReport, error)
	}{{"close", lesson.PoolCloseScenario}, {"cancel", lesson.PoolCancelSubmitScenario}, {"partial", lesson.PoolPartialScenario}, {"first", lesson.PoolFirstErrorScenario}, {"consumer", lesson.PoolConsumerExitScenario}, {"recursive", lesson.PoolRecursiveFixScenario}}
	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			r, err := path.run(testContext(t))
			if err != nil {
				t.Fatal(err)
			}
			if r.Counts.Active != 0 || r.Counts.Units != 0 || r.Counts.Started != r.Counts.Finished {
				t.Fatalf("owner未真实收尾=%+v", r.Counts)
			}
			switch path.name {
			case "close", "cancel":
				if len(r.Rows) != 2 || r.Rows[0].ID != "SKU-A" || r.Rows[0].Price != 12500 || r.Rows[1].ID != "SKU-B" || r.Rows[1].Price != 1800 || r.Counts.Started != 2 {
					t.Fatalf("已接纳A/B必须成功drain=%+v", r)
				}
				want := lesson.ErrClosed
				if path.name == "cancel" {
					want = context.Canceled
				}
				if !errors.Is(r.Rejected, want) {
					t.Fatalf("拒绝归属=%v", r.Rejected)
				}
			case "partial":
				var panicErr *lesson.PanicError
				if len(r.Rows) != 3 || !errors.As(r.Rows[0].Err, &panicErr) || !errors.Is(r.Rows[1].Err, lesson.ErrUnavailable) || r.Rows[2].Price != 4200 || r.Rows[2].Err != nil || !errors.Is(r.Aggregate, lesson.ErrUnavailable) {
					t.Fatalf("失败汇总/最后成功=%+v", r)
				}
			case "first":
				if !r.Blocked || !errors.Is(r.Aggregate, lesson.ErrBackend) || len(r.Rows) != 3 || r.Rows[2].ID != "SKU-D" || r.Rows[2].Price != 0 || !errors.Is(r.Rows[2].Err, lesson.ErrBackend) || r.Counts.Started != 2 {
					t.Fatalf("首错/排队项未执行=%+v", r)
				}
			case "consumer":
				if len(r.Rows) != 2 || r.Counts.Started != 1 {
					t.Fatalf("已接纳结果数=%+v", r)
				}
				for _, row := range r.Rows {
					if !errors.Is(row.Err, context.Canceled) || row.Price != 0 {
						t.Fatalf("取消仍产生价格=%+v", row)
					}
				}
			case "recursive":
				if !errors.Is(r.Rejected, lesson.ErrQueueFull) || len(r.Rows) != 3 || r.Rows[2].ID != "SKU-D" || r.Rows[2].Price != 4200 || r.Counts.Started != 3 {
					t.Fatalf("递归拒绝后外部owner安排=%+v", r)
				}
			}
		})
	}
}

// TestCleanupPreservesPrimaryAndJoins在业务已经失败时，独立预算仍能放行并加入实际任务。
// 检查tracker归零与主错误都保留；读取一个任务结果不能替代这里wg.Wait的作用。
func TestCleanupPreservesPrimaryAndJoins(t *testing.T) {
	gate := lesson.NewGate()
	tracker := &lesson.Tracker{}
	var worker sync.WaitGroup
	started, done := make(chan struct{}), make(chan struct{})
	worker.Go(func() { leave := tracker.Enter(1); defer leave(); close(started); <-gate.C; close(done) })
	if err := lesson.Await(testContext(t), started, "清理测试工作"); err != nil {
		gate.Release()
		worker.Wait()
		t.Fatal(err)
	}
	primary := lesson.ErrBackend
	lesson.Cleanup(&primary, gate.Release, done, worker.Wait)
	if !errors.Is(primary, lesson.ErrBackend) || tracker.Counts().Active != 0 {
		t.Fatalf("原错/真实join未保留: %v %+v", primary, tracker.Counts())
	}
}

// TestCleanupPreservesTimeout只测试清理错误链，没有启动工作；未关闭信号代表有界等待失败。
// 不把deadline当成真实join，也不留下用于制造超时的goroutine。
func TestCleanupPreservesTimeout(t *testing.T) {
	primary := lesson.ErrBackend
	lesson.Cleanup(&primary, func() {}, make(chan struct{}), func() { t.Error("未加入却调用actualWait") })
	if !errors.Is(primary, lesson.ErrBackend) || !errors.Is(primary, context.DeadlineExceeded) {
		t.Fatalf("主失败和清理失败应同时可检查: %v", primary)
	}
}

// TestGroupAPIContracts直接核对锁定库的成功Wait取消派生ctx与零limit的拒绝，避免只靠README。
func TestGroupAPIContracts(t *testing.T) {
	g, ctx := errgroup.WithContext(testContext(t))
	g.SetLimit(0)
	if g.TryGo(func() error { t.Error("limit0不应启动"); return nil }) {
		t.Fatal("limit0接纳了任务")
	}
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("成功Wait仍应取消派生ctx: %v", ctx.Err())
	}
}

// TestInvalidInputs在任何任务启动前拒绝非法并发数、重复ID与超大权重。
func TestInvalidInputs(t *testing.T) {
	ctx := testContext(t)
	never := func(context.Context, string) (int64, error) { t.Error("非法输入不应调用Fetch"); return 0, nil }
	for _, ids := range [][]string{{"same", "same"}, {""}} {
		if _, err := lesson.FanOut(ctx, ids, 2, true, never); err == nil {
			t.Fatal("非法SKU被接纳")
		}
	}
	if _, err := lesson.FanOut(ctx, []string{"a"}, 0, true, never); err == nil {
		t.Fatal("limit0被接纳")
	}
	if _, err := lesson.WeightedBatch(ctx, []lesson.WeightedSKU{{ID: "a", Weight: 4}}, 3, true, never); err == nil {
		t.Fatal("超capacity权重被接纳")
	}
	if _, err := lesson.NewPool(ctx, 1, 0, false); err == nil {
		t.Fatal("本修复队列不支持0容量，应明确拒绝")
	}
}
