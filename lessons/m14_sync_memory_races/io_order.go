package lesson

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// IOResult 表示在真实本地 HTTP 请求尚未得到响应时，其他请求是否能取得业务锁。
type IOResult struct {
	HeldDuringIO     bool
	ObserverAcquired bool
	Response         string
}

// ObserveIO 由 E08/test 调用。hold=true 模拟在业务锁里等待 HTTP，false 先复制状态再解锁。
func ObserveIO(ctx context.Context, hold bool) (IOResult, error) {
	return observeIO(ctx, hold, false)
}

// ObserveCanceledIO 由 E08/test 注入真实取消：只有 server 确认收到请求后，owner 才取消客户端。
// handler 此时仍等 responseGate，因此不是“请求根本没发出”的替身；底层错误保留给调用者判断。
func ObserveCanceledIO(ctx context.Context, hold bool) (IOResult, error) {
	return observeIO(ctx, hold, true)
}

// observeIO 是两种实验的共同实现。owner 拥有派生请求 ctx、服务器、门和完成信号。
// 返回前等待请求 goroutine 实际退出并关闭服务器；ctx 通知与完成等待是两件事。
func observeIO(ctx context.Context, hold, cancelAtGate bool) (IOResult, error) {
	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	var mu sync.Mutex
	price := "sku-1:100"
	requestStarted := make(chan struct{})
	responseGate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(responseGate) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// handler 是 HTTP server 拥有的 goroutine。真实收到请求后才通知 owner，随后等响应门。
		close(requestStarted)
		select {
		case <-responseGate:
			_, _ = io.WriteString(w, "priced")
		case <-r.Context().Done():
			// 客户端超时/断开也允许 handler 退出。通知并不等于 handler 已退出，Close 再等收尾。
		}
	}))
	defer server.Close()
	defer release() // 后登记的 defer 先执行：出错时先释放 handler，再让 Close 等它结束。
	client := server.Client()
	defer client.CloseIdleConnections()
	type response struct {
		text string
		err  error
	}
	done := make(chan response, 1)
	go func() {
		mu.Lock()
		localPrice := price // string 值副本可在锁外使用；若是 slice/map，还要按共享层次复制。
		if !hold {
			mu.Unlock()
		}
		// 此请求沿当前 ctx 执行；若 hold=true，则别的请求在整个 I/O 等待期都拿不到 mu。
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, server.URL+"?price="+localPrice, nil)
		if err != nil {
			if hold {
				mu.Unlock()
			}
			done <- response{err: err}
			return
		}
		resp, err := client.Do(req)
		var text string
		if err == nil {
			// Body 属于发请求的 goroutine；读完和错误路径都要 Close，才能归还连接资源。
			body, readErr := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			text = string(body)
			if readErr != nil {
				err = readErr
			} else if closeErr != nil {
				err = closeErr
			}
		}
		if hold {
			mu.Unlock()
		}
		done <- response{text: text, err: err} // 缓冲 1 保证 owner 在取消分支也不会把发送者卡住。
	}()
	select {
	case <-requestStarted:
		// 响应门尚未打开，I/O 不可能结束。TryLock 的成功/失败直接观察此刻的持锁状态。
	case <-ctx.Done():
		release()
		<-done // owner 等实际退出，然后 defer 清理服务器/连接；不会只发取消就返回。
		return IOResult{}, ctx.Err()
	}
	acquired := mu.TryLock()
	if acquired {
		mu.Unlock()
	}
	if cancelAtGate {
		cancelRequest()
		// 先等客户端真正返回，再释放响应门：不让成功响应与取消竞争，明确实验错误来源。
		finished := <-done
		release()
		result := IOResult{hold, acquired, finished.text}
		if !errors.Is(finished.err, context.Canceled) {
			return result, fmt.Errorf("取消实验未收到 context.Canceled: %v", finished.err)
		}
		return result, fmt.Errorf("本地定价 HTTP: %w", finished.err)
	}
	release()
	finished := <-done
	if finished.err != nil {
		return IOResult{}, fmt.Errorf("本地定价 HTTP: %w", finished.err)
	}
	return IOResult{hold, acquired, finished.text}, nil
}

// Account 使用稳定 id 决定锁顺序，避免两个方向的转账各自“先锁自己”。
// account 对象创建后不能复制，也不允许在调用过程中改变 id。
type Account struct {
	id      int
	mu      sync.Mutex
	balance int64
}

// NewAccount 由 owner 创建合成余额对象。所有调用者共享返回的指针。
func NewAccount(id int, balance int64) *Account { return &Account{id: id, balance: balance} }

// Transfer 由 worker 调用，把两账户余额更新放在同一临界区内，并统一按 id 小→大取锁。
// 余额不够返回 false；参数错误返回 error；defer 以大→小释放，错误/早返回也不会漏锁。
func Transfer(from, to *Account, amount int64) (bool, error) {
	if from == to || from.id == to.id || amount <= 0 {
		return false, fmt.Errorf("账户必须不同、id 唯一、金额为正")
	}
	first, second := from, to // 指针副本仍指向原账户；没有复制 Mutex。
	if first.id > second.id {
		first, second = second, first
	}
	first.mu.Lock()
	defer first.mu.Unlock()
	second.mu.Lock()
	defer second.mu.Unlock()
	if from.balance < amount {
		return false, nil
	}
	from.balance -= amount
	to.balance += amount
	return true, nil
}

// Balance 由 owner 在汇总时读取。不同账户两个 Balance 不是并发一致总账快照。
// 本课先等待全部转账退出，再读取总额，保证求和不会夹在转账中间。
func (a *Account) Balance() int64 { a.mu.Lock(); defer a.mu.Unlock(); return a.balance }
