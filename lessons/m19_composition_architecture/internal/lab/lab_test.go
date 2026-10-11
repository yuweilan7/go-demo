package lab_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go-demo/lessons/m19_composition_architecture/internal/app"
	"go-demo/lessons/m19_composition_architecture/internal/config"
	"go-demo/lessons/m19_composition_architecture/internal/lab"
	"go-demo/lessons/m19_composition_architecture/internal/memory"
)

func valid() config.Config {
	return config.Config{Listen: "127.0.0.1:0", Version: "independent-v1", Token: "test-public-token", RequestMS: 1000}
}

// TestRealHTTPContract 独立发送真实网络请求，直接检查 JSON/仓储状态，不调用 demo 的断言。
// 每个请求 state 编号应独立，幂等 key 对应的业务 ID 则相同，两种作用域不能混淆。
func TestRealHTTPContract(t *testing.T) {
	cfg := valid()
	a, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := lab.NewClient()
	t.Cleanup(func() {
		if err := lab.Cleanup(a, c, nil); err != nil {
			t.Error(err)
		}
	})
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan lab.Reply, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reply, err := c.Call(context.Background(), a.URL()+"/api/orders", http.MethodPost, cfg.Token, "concurrent", lab.GoodBody)
			if err != nil {
				failures <- err
				return
			}
			results <- reply
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	ids := make(map[uint64]bool)
	for reply := range results {
		if reply.Status != 201 || reply.Envelope.Code != "OK" || !reply.Envelope.Committed || reply.Envelope.OrderID != 1 || reply.Envelope.RequestID == 0 {
			t.Errorf("protocol: %s", reply.Body)
		}
		ids[reply.Envelope.RequestID] = true
	}
	if len(ids) != 8 {
		t.Fatalf("request state leaked/shared: ids=%v", ids)
	}
	for _, item := range []struct {
		key, body string
		status    int
		code      string
	}{
		{"concurrent", `{"sku":"sku-m17","qty":2}`, 409, "CONFLICT"},
		{"invalid", `{"sku":"sku-m17","qty":-1}`, 422, "ORDER_INVALID"},
		{"extra", lab.GoodBody + `{}`, 400, "INPUT"},
		{"unknown", `{"sku":"sku-m17","qty":1,"admin":true}`, 400, "INPUT"},
		{"", lab.GoodBody, 400, "KEY_REQUIRED"},
		{"too-large", strings.Repeat(" ", 300), 413, "INPUT"},
	} {
		reply, err := c.Call(context.Background(), a.URL()+"/api/orders", http.MethodPost, cfg.Token, item.key, item.body)
		if err != nil {
			t.Fatal(err)
		}
		if reply.Status != item.status || reply.Envelope.Code != item.code || reply.Envelope.Committed {
			t.Fatalf("error protocol: %s", reply.Body)
		}
	}
	rows := a.Repository().Snapshot()
	if len(rows) != 1 || rows[0].Owner != "alice" || rows[0].Qty != 1 {
		t.Fatalf("business effects: %+v", rows)
	}
	rows[0].Qty = 999
	if a.Repository().Snapshot()[0].Qty != 1 {
		t.Fatal("snapshot aliases shared record")
	}
	version, err := c.Call(context.Background(), a.URL()+"/version", http.MethodGet, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(version.Body, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["version"] != cfg.Version || fields["storage"] != "process-memory" || fields["token"] != nil || bytes.Contains(version.Body, []byte(cfg.Token)) {
		t.Fatalf("public config: %s", version.Body)
	}
}

// TestRequestBudgetReachedAtActualRepository 控制 request 已到 Commit 等待点。
// 不释放 gate，真实 handler 的请求预算到期只能走 DeadlineExceeded；响应504且零行。
func TestRequestBudgetReachedAtActualRepository(t *testing.T) {
	cfg := valid()
	cfg.RequestMS = 80
	g := memory.NewGate()
	r := memory.New(g)
	a, err := app.Build(cfg, app.Factories{OpenRepository: func() (*memory.Repository, error) { return r, nil }})
	if err != nil {
		g.Release()
		t.Fatal(err)
	}
	c := lab.NewClient()
	t.Cleanup(func() {
		g.Release()
		if err := lab.Cleanup(a, c, nil); err != nil {
			t.Error(err)
		}
	})
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reply, err := c.Call(ctx, a.URL()+"/api/orders", http.MethodPost, cfg.Token, "budget", lab.GoodBody)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-g.Entered:
	default:
		t.Fatal("request never reached repository")
	}
	if reply.Status != 504 || reply.Envelope.Code != "BUDGET" || reply.Envelope.Committed || len(r.Snapshot()) != 0 {
		t.Fatalf("deadline protocol/effects: status=%d body=%s rows=%v", reply.Status, reply.Body, r.Snapshot())
	}
}

// TestGlobalAndCancellation 实际运行跨实例错误/修复与取消屏障，外部测试包检查事件证明点。
// 更底层的业务和响应断言已由上述独立请求测试覆盖；这里覆盖故障入口及最终 cleanup。
func TestGlobalAndCancellation(t *testing.T) {
	var out bytes.Buffer
	if err := lab.GlobalScope(valid(), &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("rows_A=0 rows_B=1")) || !bytes.Contains(out.Bytes(), []byte("rows_A=1 rows_B=0")) {
		t.Fatalf("scope observations: %s", out.String())
	}
	if err := lab.Cancellation(valid(), &out); err != nil {
		t.Fatal(err)
	}
}

// TestCancelledObserverStillOwned 在 observer 已取消时触发观察失败。
// 这个单独状态断言提醒 learner：Wait 返回 Canceled 只说明观察者停止，channel 仍可未完成。
func TestCancelledObserverStillOwned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	unfinished := make(chan struct{})
	if err := lab.Wait(ctx, unfinished); !errors.Is(err, context.Canceled) {
		t.Fatalf("observer: %v", err)
	}
	select {
	case <-unfinished:
		t.Fatal("observer cancellation fabricated completion")
	default:
	}
	close(unfinished) // 本测试拥有 channel，显式完成；没有资源/worker 需要额外 join。
}

// TestCloseCallerFinalDone 不用sleep：先接收预览结果，再等尾部屏障、Join入口广播。
// caller清理仍受gate阻塞时，owner绝不能完成；释放后分别等待caller.Done与owner.Done。
// 第二组还模拟owner提前观察失败，验证独立join保留原失败、Close失败和尾部清理失败。
func TestCloseCallerFinalDone(t *testing.T) {
	for _, item := range []struct {
		name                          string
		ownerErr, callErr, cleanupErr error
	}{
		{name: "normal"},
		{name: "early-observer-failure", ownerErr: errors.New("original observer failure"), callErr: errors.New("close failed"), cleanupErr: errors.New("caller tail cleanup failed")},
	} {
		t.Run(item.name, func(t *testing.T) {
			tail := make(chan struct{})
			tailEntered := make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(tail) }) }
			var operationCtx context.Context
			caller := lab.StartCloseCaller(func(ctx context.Context) error { operationCtx = ctx; return item.callErr }, func() error { close(tailEntered); <-tail; return item.cleanupErr })
			// 创建后立即接管最终退出，断言提前失败也会开门并用新预算真正等Done。
			t.Cleanup(func() {
				release()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = caller.Join(ctx, nil)
				select {
				case <-caller.Done():
				default:
					t.Error("caller cleanup did not finish")
				}
			})
			observe, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			select {
			case result := <-caller.Result():
				if !errors.Is(result, item.callErr) {
					t.Fatalf("preview=%v", result)
				}
			case <-observe.Done():
				t.Fatal(observe.Err())
			}
			if err := lab.Wait(observe, tailEntered); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(operationCtx.Err(), context.Canceled) {
				t.Fatal("caller tail entered before cancel")
			}
			select {
			case <-caller.Done():
				t.Fatal("result delivery fabricated caller completion")
			default:
			}
			ownerResult := make(chan error, 1)
			ownerDone := make(chan struct{})
			go func() {
				defer close(ownerDone) // owner自身的最后广播；下面注册的cancel先执行。
				joinCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				ownerResult <- caller.Join(joinCtx, item.ownerErr)
			}()
			t.Cleanup(func() {
				release()
				ctx, stop := context.WithTimeout(context.Background(), 4*time.Second)
				defer stop()
				if err := lab.Wait(ctx, ownerDone); err != nil {
					t.Error(err)
				}
			})
			if err := lab.Wait(observe, caller.Joining()); err != nil {
				t.Fatal(err)
			}
			// Joining表示owner已进入Join分支；Done仍被真实gate卡住，ctx也尚未到期，
			// 所以这个owner没有可成功返回的完成事实，而不是“可能还没被调度”。
			select {
			case <-ownerDone:
				t.Fatal("owner returned while caller tail was gated")
			default:
			}
			select {
			case result := <-ownerResult:
				t.Fatalf("owner published early: %v", result)
			default:
			}
			release()
			final, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			if err := lab.Wait(final, caller.Done()); err != nil {
				t.Fatal(err)
			}
			if err := lab.Wait(final, ownerDone); err != nil {
				t.Fatal(err)
			}
			result := <-ownerResult // 已等待owner的最终Done，消费其结果不再冒充join。
			if item.ownerErr == nil && result != nil {
				t.Fatal(result)
			}
			for _, reason := range []error{item.ownerErr, item.callErr, item.cleanupErr} {
				if reason != nil && !errors.Is(result, reason) {
					t.Fatalf("lost final reason %v in %v", reason, result)
				}
			}
		})
	}
	// 同一个helper必须真正接入实际HTTP/App关闭实验，不只让纯函数替身通过。
	var output bytes.Buffer
	if err := lab.ResultBeforeDone(valid(), &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte("close_caller_result=nil close_caller_done=false")) || !bytes.Contains(output.Bytes(), []byte("close_caller_done=true owner_joined=true")) {
		t.Fatalf("actual close ownership: %s", output.String())
	}
}
