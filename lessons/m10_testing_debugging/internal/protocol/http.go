// Package protocol 把订单边界放到真实的本机 HTTP 通信上；Store 仍然是内存业务，并非实库替身验收。
package protocol

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"go-demo/lessons/m10_testing_debugging/internal/order"
)

// Response 是客户端能独立核验的协议字段。HTTP 状态只是其中一项，不能取代 code 与业务副作用。
type Response struct {
	Code  string `json:"code"`
	State string `json:"state,omitempty"`
	Count int    `json:"credit_count"`
}

// NewHandler 由 cmd/demo 和 httptest.NewServer 调用，闭包共享同一个 store 指针。
// net/http 每个请求可能在不同 goroutine 调用 handler；Store 自己拥有数据锁，handler 不复制它。
func NewHandler(store *order.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/pay" {
			writeResponse(w, http.StatusNotFound, Response{Code: "NOT_FOUND"})
			return
		}
		// MaxBytesReader 控制真实 HTTP body 读取上限；请求 body 由服务器拥有并关闭。
		// 这里读失败还没进入 Store，所以既不会改变订单，也不重试有副作用的业务调用。
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil {
			writeResponse(w, http.StatusBadRequest, Response{Code: "INVALID_INPUT"})
			return
		}
		payment, err := order.DecodePayment(raw)
		if err != nil {
			writeResponse(w, http.StatusBadRequest, Response{Code: "INVALID_INPUT"})
			return
		}
		value, err := store.ApplyPayment(payment)
		if err != nil {
			code, status := "INTERNAL_ERROR", http.StatusInternalServerError
			switch {
			case errors.Is(err, order.ErrAmountMismatch):
				code, status = "AMOUNT_MISMATCH", http.StatusConflict
			case errors.Is(err, order.ErrNotFound):
				code, status = "ORDER_NOT_FOUND", http.StatusNotFound
			}
			writeResponse(w, status, Response{Code: code})
			return
		}
		writeResponse(w, http.StatusOK, Response{Code: "PAID", State: value.State, Count: value.CreditCount})
	})
}

// writeResponse 只由 handler 调用。先提交 header 再写 JSON；客户端断开导致 Encode 失败也不能撤回 Store 已完成的入账。
// 本例没有“发送失败就再次支付”的重试。业务完成与响应被客户端读到是两个不同事实。
func writeResponse(w http.ResponseWriter, status int, value Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value) // 响应写失败无法再发送第二套 header；协议测试从客户端核对实际读到的内容。
}
