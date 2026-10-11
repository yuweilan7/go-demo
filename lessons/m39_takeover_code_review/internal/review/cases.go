package review

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	driver "github.com/go-sql-driver/mysql"
	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// request同步驱动真实TCP HTTP。每次独占Transport以免系统代理影响loopback，
// Body一取得就由defer负责Close；函数结束时关闭自己的idle池，不留外层共享client。
// 完整JSON仍不能证明server handler最后defer退出，gateway.owner另行join。
func request(ctx context.Context, g *gateway, method, path, owner, key string, quantity int64) (code int, env b.Envelope, err error) {
	var body io.Reader
	if method == http.MethodPost {
		raw, e := json.Marshal(b.Input{SKU: b.SKU, Quantity: quantity})
		if e != nil {
			return 0, env, e
		}
		body = bytes.NewReader(raw)
	}
	q, e := http.NewRequestWithContext(ctx, method, g.url+path, body)
	if e != nil {
		return 0, env, e
	}
	q.Header.Set("Authorization", "Bearer m39-"+owner)
	q.Header.Set("Idempotency-Key", key)
	q.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
	res, e := client.Do(q)
	if e != nil {
		return 0, env, e
	}
	defer func() { err = errors.Join(err, res.Body.Close()) }()
	raw, e := io.ReadAll(io.LimitReader(res.Body, 8193))
	if e != nil {
		return res.StatusCode, env, e
	}
	if len(raw) > 8192 {
		return res.StatusCode, env, b.ErrInvalid
	}
	if e = json.Unmarshal(raw, &env); e != nil {
		return res.StatusCode, env, e
	}
	return res.StatusCode, env, nil
}

// newRoot由案例调用；session取得返回值立刻保存，start没有另起未登记的任务。
func (s *session) newRoot(h b.Hooks) error {
	g, e := newGateway(s.r, h)
	if e != nil {
		return e
	}
	s.root = g
	g.start()
	return nil
}

// assertOrder独立核对业务内容，不能让HTTP200或仅ID正确掩盖用户/金额/状态错误。
func assertOrder(o b.Order, owner, key string, qty int64) error {
	return b.Assert(o.ID == b.OrderID(owner, key) && o.Owner == owner && o.Quantity == qty && o.SKU == b.SKU && o.AmountMinor == qty*299 && o.Currency == "CNY" && o.Status == "ACCEPTED" && o.Version == 1, "订单完整协议 owner=%s qty=%d got_owner=%s got_qty=%d", owner, qty, o.Owner, o.Quantity)
}

// counts直接独立SQL读库存/订单/outbox/receipt，验证的是持久事实而不是业务返回值。
func counts(ctx context.Context, s *session, wantStock, wantOrders, wantEffects int64) error {
	stock, e := s.r.Store.Stock(ctx)
	if e != nil {
		return e
	}
	var orders, outbox, effects int64
	if e = s.r.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+s.r.Store.Table("orders")).Scan(&orders); e != nil {
		return e
	}
	if e = s.r.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+s.r.Store.Table("outbox")).Scan(&outbox); e != nil {
		return e
	}
	if e = s.r.SQL.QueryRowContext(ctx, "SELECT COALESCE(SUM(effect_count),0) FROM "+s.r.Store.Table("receipts")).Scan(&effects); e != nil {
		return e
	}
	fmt.Fprintf(s.out, "SQL_FACT stock=%d orders=%d outbox=%d effects=%d\n", stock, orders, outbox, effects)
	return b.Assert(stock == wantStock && orders == wantOrders && outbox == wantOrders && effects == wantEffects, "SQL固定不变量 want=%d/%d/%d got=%d/%d/%d/%d", wantStock, wantOrders, wantEffects, stock, orders, outbox, effects)
}

// Isolation是最小PR的真实失败/修复回归。candidateBad=true专用命令会失败并清理；
// 默认传false，先明确观察候选漏洞，再用相同两用户/同key验证修复，绝不修改期望。
func Isolation(ctx context.Context, out io.Writer, candidateBad bool) error {
	return withSession(ctx, out, func(s *session) error {
		if e := s.newRoot(b.Hooks{}); e != nil {
			return e
		}
		for _, c := range []struct {
			owner string
			qty   int64
		}{{"alice", 1}, {"bob", 2}} {
			code, env, e := request(ctx, s.root, "POST", "/orders", c.owner, "shared", c.qty)
			if e != nil {
				return e
			}
			if e = b.Assert(code == 201 && env.Outcome == "COMMITTED" && env.Order != nil, "创建完整201"); e != nil {
				return e
			}
			if e = assertOrder(*env.Order, c.owner, "shared", c.qty); e != nil {
				return e
			}
		}
		// 先独立确认授权模型的事实：SQL真的存在两行，不是测试构造器伪造响应。
		for _, owner := range []string{"alice", "bob"} {
			o, found, e := s.r.Store.FindByKey(ctx, owner, "shared")
			if e != nil {
				return e
			}
			if e = b.Assert(found && o.Owner == owner, "独立权威owner/key"); e != nil {
				return e
			}
		}
		code, bad, e := request(ctx, s.root, "GET", "/candidate/status/shared", "bob", "", 0)
		if e != nil {
			return e
		}
		if e = b.Assert(code == 200 && bad.View != nil && bad.View.Owner == "alice", "候选漏洞必须真实出现Bob→Alice"); e != nil {
			return e
		}
		fmt.Fprintf(s.out, "R01_BAD http=%d authenticated=bob returned=%s quantity=%d\n", code, bad.View.Owner, bad.View.Quantity)
		if candidateBad {
			// 这是PR预先冻结的验收不变量，坏候选确实退出非零；finish仍负责收尾。
			return assertOrder(bad.View.Order, "bob", "shared", 2)
		}
		for _, c := range []struct {
			owner string
			qty   int64
		}{{"alice", 1}, {"bob", 2}} {
			for i := 0; i < 2; i++ {
				code, env, e := request(ctx, s.root, "GET", "/status/shared", c.owner, "", 0)
				if e != nil {
					return e
				}
				if e = b.Assert(code == 200 && env.View != nil && env.View.DeliveryStatus == "PENDING", "修复完整READ内容"); e != nil {
					return e
				}
				if e = assertOrder(env.View.Order, c.owner, "shared", c.qty); e != nil {
					return e
				}
				if i == 1 && !env.CacheHit {
					return errors.New("第二次查询应真实命中Redis")
				}
				fmt.Fprintf(s.out, "R01_FIXED owner=%s returned=%s qty=%d cache_hit=%t\n", c.owner, env.View.Owner, env.View.Quantity, env.CacheHit)
			}
		}
		// 同key不同payload必须冲突，不得因本PR查询改动绕过Create事务/幂等边界。
		code, _, e = request(ctx, s.root, "POST", "/orders", "bob", "shared", 1)
		if e != nil {
			return e
		}
		if e = b.Assert(code == 409, "同key载荷冲突真实409 got=%d", code); e != nil {
			return e
		}
		code, _, e = request(ctx, s.root, "GET", "/status/shared", "unknown", "", 0)
		if e != nil {
			return e
		}
		if e = b.Assert(code == 401, "未认证无业务结果"); e != nil {
			return e
		}
		code, _, e = request(ctx, s.root, "GET", "/status/missing", "bob", "", 0)
		if e != nil {
			return e
		}
		if e = b.Assert(code == 404, "认证用户无原key时404 got=%d", code); e != nil {
			return e
		}
		return counts(ctx, s, 7, 2, 0)
	})
}

// shadowTx是独立SQL审查反例，直接借当前stock表；不是M37的生产withTx实现。
// 具名err负责给defer提供结果，而内层if的err是另一个槽，绝无Java异常自动传播。
// bad时第一步扣1，第二步真实1062却return nil，defer看到外层nil而误Commit。
func shadowTx(ctx context.Context, s *session, bad bool) (observed error, err error) {
	tx := s.r.Store.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	raw, ok := tx.Statement.ConnPool.(*sql.Tx)
	if !ok {
		return nil, errors.New("缺真实sql.Tx")
	}
	defer func() {
		// 闭包退出时读取具名err，不是defer注册时复制旧err值。
		if err == nil {
			if e := tx.Commit().Error; e != nil {
				err = errors.Join(b.ErrUnknown, e)
			}
		}
		e := raw.Rollback()
		if !errors.Is(e, sql.ErrTxDone) {
			err = errors.Join(err, b.CleanupCause(e))
		}
	}()
	if err = tx.Exec("UPDATE "+s.r.Store.Table("stock")+" SET quantity=quantity-1 WHERE sku=?", b.SKU).Error; err != nil {
		return nil, err
	}
	sqlText := "INSERT INTO " + s.r.Store.Table("stock") + " (sku,quantity) VALUES (?,10)"
	if bad {
		if err := tx.Exec(sqlText, b.SKU).Error; err != nil { // BAD：if块新err遮蔽结果err。
			observed = err
			return observed, nil // BAD：吞掉实际1062，外层err仍nil。
		}
	} else {
		if err = tx.Exec(sqlText, b.SKU).Error; err != nil {
			return err, err
		} // 修复用=写外层槽并返回。
	}
	return nil, nil
}

// Transactions核对误提交/修复真实副作用，还覆盖M37调用点的error/panic回滚。
func Transactions(ctx context.Context, out io.Writer) error {
	return withSession(ctx, out, func(s *session) error {
		observed, result := shadowTx(ctx, s, true)
		var duplicate *driver.MySQLError
		if e := b.Assert(errors.As(observed, &duplicate) && duplicate.Number == 1062 && result == nil, "真实1062被坏例吞掉"); e != nil {
			return e
		}
		if e := counts(ctx, s, 9, 0, 0); e != nil {
			return e
		}
		fmt.Fprintln(s.out, "R02_BAD inner_error=1062 outer_error=nil stock=9 committed=true")
		_, result = shadowTx(ctx, s, false)
		if e := b.Assert(errors.As(result, &duplicate) && duplicate.Number == 1062, "修复保留1062"); e != nil {
			return e
		}
		if e := counts(ctx, s, 9, 0, 0); e != nil {
			return e
		}
		fault := errors.New("本课真实Tx写后失败")
		for _, isPanic := range []bool{false, true} {
			h := b.Hooks{AfterOrderWrites: func(context.Context, b.Order) error {
				if isPanic {
					panic(fault)
				}
				return fault
			}}
			// recover只包当前同步业务调用；不会吞掉业务cause或把panic改成成功。
			e := func() (err error) {
				defer func() {
					if p := recover(); p != nil {
						err = panicError("事务", p)
					}
				}()
				_, err = s.r.Store.Create(ctx, command("alice", "rollback"), h)
				return err
			}()
			if e = b.Assert(errors.Is(e, fault), "error/panic原cause保留 panic=%t", isPanic); e != nil {
				return e
			}
			if e = counts(ctx, s, 9, 0, 0); e != nil {
				return e
			}
		}
		fmt.Fprintln(s.out, "R02_FIXED stock=9 rollback=true error_and_panic_preserved=true")
		// 用同一真实Tx hook从实际Gin handler再触发panic，验证recovery装配与回滚。
		// Recovery编码500无法撤销事实，因此下方必须独立查SQL，而不能把500当回滚证明。
		if e := s.newRoot(b.Hooks{AfterOrderWrites: func(context.Context, b.Order) error { panic(fault) }}); e != nil {
			return e
		}
		code, env, e := request(ctx, s.root, "POST", "/orders", "alice", "http-panic", 1)
		if e != nil {
			return e
		}
		if e = b.Assert(code == 500 && env.Outcome == "UNKNOWN", "真实Gin recovery返回500/UNKNOWN"); e != nil {
			return e
		}
		if e = counts(ctx, s, 9, 0, 0); e != nil {
			return e
		}
		return nil
	})
}

// command给非HTTPcaller同样受Validate约束的值输入；仅用于本课合成Alice/单SKU/qty1。
func command(owner, key string) b.Command {
	return b.Command{Owner: owner, Key: key, RequestID: "m39-" + key, Input: b.Input{SKU: b.SKU, Quantity: 1}}
}

// Contexts在真实handler返回后观察request ctx取消，比较错误202与明确后台owner。
// 不保存/跨goroutine访问Gin Context；指针池复用是另一个应审查的危险，本实验不制造race。
func Contexts(ctx context.Context, out io.Writer) error {
	return withSession(ctx, out, func(s *session) error {
		g, e := newGateway(s.r, b.Hooks{})
		if e != nil {
			return e
		}
		s.root = g // 构造成功立刻登记，以下配置失败/提前退出也由finish关闭。
		g.badGate = s.makeGate()
		g.goodGate = s.makeGate()
		g.start() // 所有handler借用字段在启动网络以前写完，请求期间不再修改。
		code, env, e := request(ctx, s.root, "POST", "/async/bad", "alice", "async-bad", 1)
		if e != nil {
			return e
		}
		if e = b.Assert(code == 202 && env.Outcome == "PENDING", "坏202已真实返回"); e != nil {
			return e
		}
		if e = await(ctx, s.root.badCanceled, "handler返回后实际request取消"); e != nil {
			return e
		}
		s.root.badGate.open()
		t := s.root.job("bad")
		if t == nil {
			return errors.New("缺bad任务账本")
		}
		e = t.join()
		if e = b.Assert(errors.Is(e, context.Canceled) || status.Code(e) == codes.Canceled, "后台使用已结束requestctx得到Canceled"); e != nil {
			return e
		}
		if e = counts(ctx, s, 10, 0, 0); e != nil {
			return e
		}
		t.consumed = true
		fmt.Fprintln(s.out, "R03_BAD handler_returned=202 request_ctx_observed=Canceled orders=0")
		code, _, e = request(ctx, s.root, "POST", "/async/fixed", "alice", "async-fixed", 1)
		if e != nil {
			return e
		}
		if e = b.Assert(code == 202, "修复返回202"); e != nil {
			return e
		}
		if e = await(ctx, s.root.goodGate.entered, "owner worker进入门"); e != nil {
			return e
		}
		s.root.goodGate.open()
		t = s.root.job("fixed")
		if t == nil {
			return errors.New("缺fixed任务账本")
		}
		if e = t.join(); e != nil {
			return e
		}
		if e = counts(ctx, s, 9, 1, 0); e != nil {
			return e
		}
		t.consumed = true
		fmt.Fprintln(s.out, "R03_FIXED explicit_owner=true command_value_copy=true final_done=true orders=1")
		return nil
	})
}

// Pools持有全部3条真实sql.Conn，再发第4个调用，逼它等database/sql连接许可。
// pool不是无限队列：独立120msctx到期无连接，Stats.WaitCount给实际等待证据。
func Pools(ctx context.Context, out io.Writer) error {
	return withSession(ctx, out, func(s *session) (err error) {
		var held []*sql.Conn
		defer func() {
			for _, c := range held {
				err = errors.Join(err, b.CleanupCause(c.Close()))
			}
		}()
		for i := 0; i < 3; i++ {
			c, e := s.r.SQL.Conn(ctx)
			if e != nil {
				return e
			}
			held = append(held, c)
		}
		before := s.r.SQL.Stats()
		wait, cancel := context.WithTimeout(ctx, 120*time.Millisecond)
		var n int
		e := s.r.SQL.QueryRowContext(wait, "SELECT 1").Scan(&n)
		cancel()
		after := s.r.SQL.Stats()
		if err = b.Assert(errors.Is(e, context.DeadlineExceeded) && after.InUse == 3 && after.WaitCount > before.WaitCount, "真实池等待/取消 inuse=%d waitDelta=%d", after.InUse, after.WaitCount-before.WaitCount); err != nil {
			return err
		}
		fmt.Fprintf(s.out, "R06_BAD max_open=3 in_use=%d wait_delta=%d deadline=true\n", after.InUse, after.WaitCount-before.WaitCount)
		// 正常修复在资源拿到时登记Close，并在借用函数返回归还，不能靠加大池遮掩泄漏。
		for _, c := range held {
			if e = c.Close(); e != nil {
				return e
			}
		}
		held = nil
		if e = s.r.SQL.QueryRowContext(ctx, "SELECT 1").Scan(&n); e != nil {
			return e
		}
		return b.Assert(n == 1 && s.r.SQL.Stats().InUse == 0, "归还连接后恢复无需增池")
	})
}

// typedFault模拟真实接口组装边界；即使动态指针为nil，接口仍带*typedFault类型。
type typedFault struct{ message string }

func (e *typedFault) Error() string {
	if e == nil {
		return "typed nil fault"
	}
	return e.message
}
func nilBad() error   { var concrete *typedFault; return concrete } // BAD：把nil指针装进非nil接口。
func nilFixed() error { return nil }                                // 修复直接返回nil接口，不让nil动态指针越过边界。

// Helpers是辅助纯语言证据，仅证明interface/defer/panic身份，不冒充协议或中间件。
func Helpers(out io.Writer) error {
	bad := nilBad()
	if e := b.Assert(bad != nil && nilFixed() == nil, "typed nil与nil接口不同"); e != nil {
		return e
	}
	// 普通defer实参注册时求值，闭包读取退出时变量；当前例子不涉及DB事务。
	old, late := false, false
	func() {
		err := error(nil)
		defer func(e error) { old = e != nil }(err)
		defer func() { late = err != nil }()
		err = errors.New("later")
	}()
	if e := b.Assert(!old && late, "defer参数旧值与闭包晚读"); e != nil {
		return e
	}
	fmt.Fprintf(out, "R05_HELPER interface_non_nil=%t dynamic_type=%T defer_arg_error=%t defer_closure_error=%t\n", bad != nil, bad, old, late)
	return nil
}
