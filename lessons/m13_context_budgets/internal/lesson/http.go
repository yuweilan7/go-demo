package lesson

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// chargeCommand 是 caller 明确传入的业务必需参数，不从 ctx.Value 隐式获取。
// 本课把合成订单的扣款计数当作副作用；IdempotencyKey 为空表示故意关闭防重。
type chargeCommand struct {
	Order          string
	IdempotencyKey string
}

// receipt 是真实 HTTP JSON 响应。Total 是服务端效果计数，Duplicate 表示命中已有回执。
// 收到这个结构才证明客户端读完本次协议响应；只看到 HTTP 200 还不够。
type receipt struct {
	Order     string
	Total     int
	Duplicate bool
}

// ledger 是本次进程中的真实合成业务状态，由 HTTP handler 修改，owner 独立查询。
// 它不是数据库：Mutex 保证本进程内幂等原子性，不保证重启、多实例或持久事务。
type ledger struct {
	mu       sync.Mutex
	counts   map[string]int
	receipts map[string]receipt
}

// apply 仅由 handler 在执行 gate 放行后调用。它持锁完成“检查幂等键 -> 效果 -> 保存回执”，
// 避免两个并发 handler 都看到不存在然后各扣一次。实际生产应把此边界移到持久事务。
func (l *ledger) apply(command chargeCommand) receipt {
	l.mu.Lock()
	defer l.mu.Unlock()
	if command.IdempotencyKey != "" {
		// 把 order 也绑定进键，防止不同业务订单碰巧使用同一文本 token。
		key := command.Order + ":" + command.IdempotencyKey
		if existing, ok := l.receipts[key]; ok {
			existing.Duplicate = true
			return existing
		}
		l.counts[command.Order]++
		result := receipt{Order: command.Order, Total: l.counts[command.Order]}
		l.receipts[key] = result
		return result
	}
	l.counts[command.Order]++
	return receipt{Order: command.Order, Total: l.counts[command.Order]}
}

// count 由实验 owner 或独立测试调用，在锁内读取真实效果，不能用客户端 error 推断。
func (l *ledger) count(order string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.counts[order]
}

// operation 代表一个真实 HTTP 调用的服务端协调状态。channel 是共享同步对象。
// owner 注册并释放 gate；handler 独占关闭 started/observed/effected/exited。
// cooperative 控制危险/修复分支；wireBudget 控制是否按 HTTP header 重建远端预算。
type operation struct {
	id          string
	cooperative bool
	wireBudget  bool
	gate        chan struct{}
	release     func()
	started     chan struct{}
	observed    chan struct{}
	effected    chan struct{}
	exited      chan struct{}
}

// httpLab 是 RunHTTP/RunBudgets 的本地协议资源 owner。
// 每个实例绑定动态 loopback 端口；operations 的注册和 handler 查找共享 Mutex。
type httpLab struct {
	mu         sync.Mutex
	operations map[string]*operation
	state      *ledger
	server     *httptest.Server
	client     *http.Client
	t          *trace
}

// newHTTPLab 由实验入口调用。scope 只被 handler 闭包引用作实验 watchdog，
// 业务取消来自 r.Context，跨进程预算来自显式 header，二者分别解释。
func newHTTPLab(scope context.Context, t *trace) *httpLab {
	lab := &httpLab{
		operations: make(map[string]*operation),
		state:      &ledger{counts: make(map[string]int), receipts: make(map[string]receipt)},
		t:          t,
	}
	// httptest.Server 仍是真实 net/http Server + TCP listener；并非 ResponseRecorder。
	// 自定义 Transport 禁代理、禁 keepalive；此课无需外网，也让 HTTP/1 断连可观察。
	lab.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lab.handle(scope, w, r)
	}))
	lab.client = &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	t.event("HTTP-owner", "启动真实服务 %s（本次随机端口）", lab.server.URL)
	return lab
}

// register 由 owner 在启动 client 前调用，把本次操作和 gate 绑定到 op 查询参数。
// opID 是实验协调标识，与业务订单/幂等键分开，不会把重试误认为同一个 handler。
func (lab *httpLab) register(opID string, cooperative, wireBudget bool) *operation {
	op := &operation{
		id: opID, cooperative: cooperative, wireBudget: wireBudget,
		gate: make(chan struct{}), started: make(chan struct{}), observed: make(chan struct{}),
		effected: make(chan struct{}), exited: make(chan struct{}),
	}
	op.release = newOnceGate(op.gate)
	lab.mu.Lock()
	lab.operations[opID] = op
	lab.mu.Unlock()
	return op
}

// handle 由 net/http 每个请求的服务 goroutine 调用，持有 ResponseWriter 到函数返回。
// 故意慢的地方是 gate，不靠 Sleep 猜测客户端是否已经超时。
// 返回只关闭本次 exited；实验 owner 通过 close 等待整个 server 最终停止。
func (lab *httpLab) handle(scope context.Context, w http.ResponseWriter, r *http.Request) {
	lab.mu.Lock()
	op := lab.operations[r.URL.Query().Get("op")]
	lab.mu.Unlock()
	if op == nil {
		http.Error(w, "unknown operation", http.StatusNotFound)
		return
	}
	defer close(op.exited)
	defer lab.t.event(op.id, "handler defer/退出；不再使用 ResponseWriter")
	var command chargeCommand
	// 网络输入有长度上限，业务字段通过 Body 传送，而不是靠请求元数据猜出来。
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&command); err != nil || command.Order == "" {
		http.Error(w, "invalid command", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	_, inboundHasDeadline := ctx.Deadline()
	lab.t.event(op.id, "服务端开始 order=%s HTTP=%s 入站自带deadline=%t trace=%s", command.Order, r.Proto, inboundHasDeadline, r.Header.Get("X-Request-ID"))
	if op.wireBudget {
		remaining, err := strconv.ParseInt(r.Header.Get("X-Budget-Ms"), 10, 64)
		if err != nil || remaining <= 0 || remaining > 2000 {
			http.Error(w, "invalid budget", http.StatusBadRequest)
			return
		}
		// HTTP 不会自动序列化 Context deadline；本实验协议明确传剩余毫秒数。
		// 入站 ctx 仍保留断连取消，服务端上限 2 秒；本函数拥有并 defer cancel。
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(remaining)*time.Millisecond)
		defer cancel()
		lab.t.event(op.id, "按 X-Budget-Ms=%d 派生服务端 deadline（网络传输时间未精确补偿）", remaining)
	}
	close(op.started)
	if op.cooperative {
		select {
		case <-ctx.Done():
			lab.t.event(op.id, "服务端观察 ctx.Done: %v；执行 gate 尚未放行，不写效果", ctx.Err())
			close(op.observed)
			return
		case <-op.gate:
			// gate 和 Done 同时就绪时 select 可选任一分支，所以还检查已知取消。
			// 这个检查不是事务：取消仍可能在检查之后发生，不能保证绝无晚到副作用。
			if ctx.Err() != nil {
				close(op.observed)
				return
			}
		case <-scope.Done():
			return
		}
	} else {
		// 危险分支：它完全不观察请求 ctx，只等业务 gate。
		// scope 只为教学故障兜底，不是生产取消修复；客户端超时不能强杀这里。
		if err := wait(scope, op.gate, "危险 handler gate"); err != nil {
			return
		}
		lab.t.event(op.id, "忽略请求取消后仍执行；此刻入站 Err=%v", ctx.Err())
	}
	result := lab.state.apply(command)
	lab.t.event(op.id, "真实效果 order=%s total=%d duplicate=%t", result.Order, result.Total, result.Duplicate)
	close(op.effected)
	// 即使 Encode 返回 nil，也不证明客户端读到了回执；内核缓冲和连接状态仍有边界。
	// 对已超时客户端，账本效果不能靠 response 写入失败自动回滚。
	if err := json.NewEncoder(w).Encode(result); err != nil {
		lab.t.event(op.id, "响应写入返回 error=%v；效果仍已存在", err)
	}
}

// close 由实验 owner 的 defer 调用；先释放所有实验 gate，再等待已开始的 handler，
// 最后关闭自己创建的 Transport/listener，避免 httptest.Close 被慢 handler 永久拖住。
func (lab *httpLab) close() error {
	lab.mu.Lock()
	ops := make([]*operation, 0, len(lab.operations))
	for _, op := range lab.operations {
		ops = append(ops, op)
	}
	lab.mu.Unlock()
	for _, op := range ops {
		op.release()
	}
	var result error
	for _, op := range ops {
		if closed(op.started) {
			result = errors.Join(result, join(op.exited, op.id+" handler"))
		}
	}
	lab.client.CloseIdleConnections()
	lab.server.Close()
	lab.t.event("HTTP-owner", "handler 已收尾；Transport 空闲连接与 listener 已关闭")
	return result
}

// charge 是 usecase 调用的 client adapter。ctx 必须放首参数，command 是必需业务参数。
// 它在 client.Do 等连接/请求/响应头，再在 Decode 等响应体；任一步都可能产生 error。
// HTTP error 包装用 %w 保留底层原因，使 caller 能 errors.Is 判断 DeadlineExceeded。
func (lab *httpLab) charge(ctx context.Context, op *operation, command chargeCommand) (receipt, error) {
	encoded, err := json.Marshal(command)
	if err != nil {
		return receipt{}, err
	}
	requestURL := lab.server.URL + "/charge?" + url.Values{"op": {op.id}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(string(encoded)))
	if err != nil {
		return receipt{}, fmt.Errorf("构造 HTTP 请求: %w", err)
	}
	if requestID, ok := RequestID(ctx); ok {
		req.Header.Set("X-Request-ID", requestID)
	}
	if op.wireBudget {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 {
			return receipt{}, fmt.Errorf("HTTP 发送前预算已耗尽: %w", context.DeadlineExceeded)
		}
		req.Header.Set("X-Budget-Ms", strconv.FormatInt(time.Until(deadline).Milliseconds(), 10))
	}
	response, err := lab.client.Do(req)
	if err != nil {
		return receipt{}, fmt.Errorf("client.Do: %w", err)
	}
	// 只有 Do 成功返回 response 才有 Body 可清理。defer 在此注册，所有后续分支都关闭。
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return receipt{}, fmt.Errorf("HTTP 状态 %s", response.Status)
	}
	var result receipt
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result); err != nil {
		return receipt{}, fmt.Errorf("读取回执: %w", err)
	}
	if result.Order != command.Order || result.Total < 1 {
		return receipt{}, errors.New("回执业务内容不合法")
	}
	return result, nil
}

// clientResult 把一次调用的 (receipt, error) 成对交给 owner。
// task.done 表示 goroutine 已退出，与 response/read error 是两个独立事实。
type clientResult struct {
	receipt receipt
	err     error
}

type clientTask struct {
	result chan clientResult
	done   chan struct{}
}

// startCharge 由实验 owner 调用，创建且登记一个 HTTP client goroutine。
// result 缓冲为 1，owner 即使在错误路径暂未读取，worker 也不会卡在发送结果上。
// 请求 ctx 必须有界，owner 在收尾时等 done；函数返回本身不是任务已经结束。
func (lab *httpLab) startCharge(ctx context.Context, op *operation, command chargeCommand) *clientTask {
	task := &clientTask{result: make(chan clientResult, 1), done: make(chan struct{})}
	go func() {
		defer close(task.done)
		result, err := lab.charge(ctx, op, command)
		lab.t.event(op.id+"/client", "I/O 函数返回 err=%v receipt=%+v", err, result)
		task.result <- clientResult{receipt: result, err: err}
	}()
	return task
}

// awaitClient 由 owner 同步收集结果，并另外等待 client goroutine 完成。
// 如果 caller scope 先结束，交回取消原因；上层 defer 仍负责 cancel/join。
func awaitClient(scope context.Context, task *clientTask) (clientResult, error) {
	select {
	case result := <-task.result:
		return result, join(task.done, "client goroutine")
	case <-scope.Done():
		return clientResult{}, scope.Err()
	}
}
