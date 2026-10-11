package lockcourse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// slowService 是真实loopback HTTP服务，不用sleep冒充RPC。handler在owner的门上
// 等待；entered只表示收到请求。有限两个路径的接纳记录在mu内登记，close先
// 封住接纳再快照，避免等待没启动的/fixed，也不能漏掉已接纳的/bad尾部defer。
type slowService struct {
	server    *http.Server
	worker    *task
	client    *http.Client
	transport *http.Transport
	url       string
	entered   [2]chan int64
	mu        sync.Mutex
	accepting bool
	records   [2]*handlerRecord
	gates     [2]*gate
	tail      func(int) error // 仅测试注入受控尾部清理；正常fixture为nil。
}

// handlerRecord是一个实际获准进入fixture业务的handler账本项。done只由它
// 最后一项defer关闭；cleanup只在done同步后读，TCP关闭/Serve返回不能代替它。
type handlerRecord struct {
	done    chan struct{}
	cleanup error
}

var ErrHandlerJoin = errors.New("fixture接纳handler尚未最终退出")

// startSlow 由externalCase调用，caller拥有返回服务的close。listener属于server；
// Serve是独立task，Close/Shutdown解除Accept等待，随后caller真正join。
func startSlow(ctx context.Context, gates [2]*gate) (*slowService, error) {
	return startSlowWithTail(ctx, gates, nil)
}

// startSlowWithTail由startSlow/真实HTTP故障测试调用。tail在启动前确定且只读，
// 用于把handler最后清理停在测试门上；这不是扩大为通用生产HTTP框架。
func startSlowWithTail(ctx context.Context, gates [2]*gate, tail func(int) error) (*slowService, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &slowService{url: "http://" + l.Addr().String(), transport: &http.Transport{MaxIdleConns: 2}, accepting: true, gates: gates, tail: tail}
	s.client = &http.Client{Transport: s.transport, Timeout: 10 * time.Second}
	for i := range s.entered {
		s.entered[i] = make(chan int64, 1)
	}
	s.server = &http.Server{ReadHeaderTimeout: 3 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i, rec := s.admit(r.URL.Path)
		if rec == nil {
			http.Error(w, "fixture不再接纳或路径已使用", http.StatusServiceUnavailable)
			return
		}
		defer close(rec.done) // 最先登记，最后执行；tail清理必须早于最终done。
		if s.tail != nil {
			defer func() { rec.cleanup = s.tail(i) }()
		}
		s.entered[i] <- int64(i)
		if e := gates[i].wait(r.Context()); e != nil {
			return
		}
		fmt.Fprint(w, "external preflight ready")
	})}
	s.worker = start(ctx, func(context.Context) taskResult {
		e := s.server.Serve(l)
		if errors.Is(e, http.ErrServerClosed) {
			e = nil
		}
		return taskResult{Err: e}
	}, nil)
	return s, nil
}

// admit由HTTP handler入口调用，close也使用同一mu封住接纳。登记早于entered/
// 业务门等待；后续close的快照因此一定包含它。每个固定路径最多一次，不创建
// 无限任务；拒绝分支不进入fixture资源区，标准库内部goroutine不在本账本承诺内。
func (s *slowService) admit(path string) (int, *handlerRecord) {
	i := 0
	if path == "/fixed" {
		i = 1
	} else if path != "/bad" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accepting || s.records[i] != nil {
		return i, nil
	}
	r := &handlerRecord{done: make(chan struct{})}
	s.records[i] = r
	return i, r
}

// seal由close无条件执行，把实际已接纳项复制给唯一close owner；锁内先禁止新
// fixture业务进入，因此快照之后不会新增资源项。nil路径从未接纳，不能等它。
func (s *slowService) seal() []*handlerRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accepting = false
	var records []*handlerRecord
	for _, r := range s.records {
		if r != nil {
			records = append(records, r)
		}
	}
	return records
}

// request 由业务worker调用，拥有Response.Body；Do返回不等于body已经读完，必须
// 读完整且Close才释放HTTP连接。ctx控制I/O，非2xx/内容错误仍返回真实失败。
func (s *slowService) request(ctx context.Context, path string) (err error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+path, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(r)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || string(body) != "external preflight ready" {
		return errors.New("外部HTTP语义结果不符")
	}
	return nil
}

// close由externalCase无条件defer：正常/提前错误都封接纳、解除handler门、关闭
// server并真正join Serve/实际接纳handler。三阶段各自新Background预算，不能
// 在Shutdown耗尽后复用已取消ctx；Close不等于handler尾部已执行完。
func (s *slowService) close(err *error) {
	s.closeWith(err, 8*time.Second, cleanupContext)
}

// closeWith由close/故障断言调用；shutdownBudget只限制Shutdown，newHandlerBudget
// 必须在前两阶段之后创建全新Background预算。测试给可观察等待点的ctx，仍走
// 同一实际关闭/账本join代码，保留原失败、Shutdown失败、Serve/handler清理失败。
func (s *slowService) closeWith(err *error, shutdownBudget time.Duration, newHandlerBudget func() (context.Context, context.CancelFunc)) {
	records := s.seal()
	for _, g := range s.gates {
		g.open()
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownBudget)
	shutdownErr := s.server.Shutdown(shutdownCtx)
	cancelShutdown()
	*err = errors.Join(*err, shutdownErr, s.server.Close())
	s.transport.CloseIdleConnections()
	s.worker.cancel()
	serveCtx, cancelServe := cleanupContext() // Shutdown超时也不能耗掉Serve的join预算。
	r, e := s.worker.join(serveCtx)
	cancelServe()
	*err = errors.Join(*err, e, r.Err, r.Cleanup)
	handlerCtx, cancelHandlers := newHandlerBudget() // 尾部handler另有全新预算，不继承业务ctx。
	defer cancelHandlers()
	for _, rec := range records {
		select {
		case <-rec.done:
			*err = errors.Join(*err, rec.cleanup)
		case <-handlerCtx.Done():
			*err = errors.Join(*err, ErrHandlerJoin, handlerCtx.Err())
		}
	}
}

// externalCase 由Run调用：坏路径先锁SKU50再调用真实HTTP，handler由gate固定
// 等待，另一会话UPDATE必得1205；修复路径先做只读外部preflight再开Tx，SKU51
// 的竞争者在外部等待期间实际完成。最终状态由DB查询，不能由HTTP200代替。
func (s *store) externalCase(ctx context.Context) (err error) {
	for _, id := range []int{50, 51} {
		if err = s.seedStock(ctx, id, 5); err != nil {
			return err
		}
	}
	var own scope
	gates := [2]*gate{own.gate(), own.gate()}
	external, e := startSlow(ctx, gates)
	if e != nil {
		return e
	}
	defer external.close(&err)
	defer own.close(&err) // LIFO：先解除门/join client，再等待HTTP server/handler退出。
	bad := own.start(ctx, func(ctx context.Context) (r taskResult) {
		defer captureTaskPanic(&r)
		c, e := acquire(ctx, s.db, "READ-COMMITTED")
		if e != nil {
			r.Err = e
			return
		}
		defer func() { r.Cleanup = errors.Join(r.Cleanup, c.release()) }()
		r.Err = transact(ctx, c.conn, func(tx *sql.Tx) error {
			var stock int
			if e := tx.QueryRowContext(ctx, "SELECT stock FROM `"+s.stock+"` WHERE id=50 FOR UPDATE").Scan(&stock); e != nil {
				return e
			}
			// 错误边界：这里事务已持锁，HTTP handler未放行前，锁不能释放。
			// 应用mutex即使包在外面也不保护另一进程/另一MySQL会话。
			if e := external.request(ctx, "/bad"); e != nil {
				return e
			}
			return s.decrement(ctx, tx, 50, "")
		})
		return
	})
	if _, e = receive(ctx, external.entered[0]); e != nil {
		return e
	}
	ready := make(chan int64, 1)
	marker := s.prefix + "_external_wait"
	waiter := own.start(ctx, func(ctx context.Context) (r taskResult) {
		defer captureTaskPanic(&r)
		c, e := acquire(ctx, s.db, "READ-COMMITTED")
		if e != nil {
			r.Err = e
			return
		}
		defer func() { r.Cleanup = errors.Join(r.Cleanup, c.release()) }()
		ready <- c.id
		_, r.Err = c.conn.ExecContext(ctx, "/* "+marker+" */ UPDATE `"+s.stock+"` SET stock=stock-1 WHERE id=50")
		return
	})
	id, e := receive(ctx, ready)
	if e != nil {
		return e
	}
	if err = s.waitSQL(ctx, id, marker); err != nil {
		return err
	}
	r, e := waiter.join(ctx)
	if e != nil {
		return e
	}
	if r.Cleanup != nil {
		return r.Cleanup
	}
	if code(r.Err) != 1205 {
		return fmt.Errorf("持锁RPC应使竞争者1205，实际%w", r.Err)
	}
	fmt.Fprintf(s.out, "[external.bad] HTTP handler仍等门，竞争UPDATE真实%v；现在放行HTTP\n", r.Err)
	gates[0].open()
	r, e = bad.join(ctx)
	if e != nil {
		return e
	}
	if err = errors.Join(r.Err, r.Cleanup); err != nil {
		return err
	}
	if err = s.audit(ctx, "external.bad", 50, 4, 0, 0); err != nil {
		return err
	}
	fixed := own.start(ctx, func(ctx context.Context) (r taskResult) {
		defer captureTaskPanic(&r)
		// 修复先做只读校验/准备，不持Conn/Tx；不能把不可幂等发货简单搬到前面。
		if e := external.request(ctx, "/fixed"); e != nil {
			r.Err = e
			return
		}
		c, e := acquire(ctx, s.db, "READ-COMMITTED")
		if e != nil {
			r.Err = e
			return
		}
		defer func() { r.Cleanup = errors.Join(r.Cleanup, c.release()) }()
		r.Err = transact(ctx, c.conn, func(tx *sql.Tx) error { return s.decrement(ctx, tx, 51, "") })
		return
	})
	if _, e = receive(ctx, external.entered[1]); e != nil {
		return e
	}
	if inUse := s.db.Stats().InUse; inUse != 0 {
		return fmt.Errorf("外部preflight期间应不持数据库连接，InUse=%d", inUse)
	}
	result, e := s.db.ExecContext(ctx, "UPDATE `"+s.stock+"` SET stock=stock-1 WHERE id=51")
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return errors.New("preflight期间竞争UPDATE应成功1行")
	}
	fmt.Fprintln(s.out, "[external.fixed] HTTP handler仍等门，DB InUse=0，竞争UPDATE已完成1行；现在放行preflight")
	gates[1].open()
	r, e = fixed.join(ctx)
	if e != nil {
		return e
	}
	if err = errors.Join(r.Err, r.Cleanup); err != nil {
		return err
	}
	if err = s.audit(ctx, "external.fixed", 51, 3, 0, 0); err != nil {
		return err
	}
	// 不把最终handler等待仅放在正常末尾：上面的任意return都会进入无条件close。
	// close按接纳账本等待最后defer；client结果/TCP关闭/Serve返回均不是替代证据。
	fmt.Fprintln(s.out, "[external.handlers] 正常业务完成；无条件close将join实际接纳handler最后defer与Serve")
	return nil
}
