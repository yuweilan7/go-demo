package protocol_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-demo/lessons/m10_testing_debugging/internal/order"
	"go-demo/lessons/m10_testing_debugging/internal/protocol"
)

// TestRealHTTPPaymentBoundary 用真实 loopback TCP/http client/handler 测试协议和内存业务副作用。
// 每个 parallel 子测试都有独占 store、动态端口及连接池；普通单测不访问云端。
func TestRealHTTPPaymentBoundary(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantState  string
		wantCents  int64
		wantCount  int
	}{
		{"mismatch", `{"id":"order-42","amount":"13.00"}`, 409, "AMOUNT_MISMATCH", "pending", 0, 0},
		{"valid", `{"id":"order-42","amount":"12.99"}`, 200, "PAID", "paid", 1299, 1},
		{"invalid_type", `{"id":"order-42","amount":12.99}`, 400, "INVALID_INPUT", "pending", 0, 0},
		{"unknown_order", `{"id":"other","amount":"12.99"}`, 404, "ORDER_NOT_FOUND", "pending", 0, 0},
		{"oversize", strings.Repeat(" ", 4097), 400, "INVALID_INPUT", "pending", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := order.NewStore("order-42", 1299)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(protocol.NewHandler(store)) // 端口由操作系统分配，不先探测再绑定固定端口。
			t.Cleanup(server.Close)                                  // 子测试结束后关闭 listener、等待 server 请求结束；不是每次请求立即关闭服务器。
			client := server.Client()
			client.Timeout = 2 * time.Second // 限制真实网络等待，不是测试运行器 -timeout，也不是 synctest 虚拟时间。
			t.Cleanup(client.CloseIdleConnections)
			status, response := post(t, client, server.URL, tc.body)
			if status != tc.wantStatus || response.Code != tc.wantCode {
				t.Fatalf("HTTP status=%d response=%+v; want status=%d code=%s", status, response, tc.wantStatus, tc.wantCode)
			}
			if tc.wantCode == "PAID" {
				// 再次通过真实 HTTP 请求重放，不仅在单测里直接调用 Store。
				status, response = post(t, client, server.URL, tc.body)
				if status != 200 || response.State != "paid" || response.Count != 1 {
					t.Fatalf("replay status=%d body=%+v; want paid once", status, response)
				}
			}
			got, err := store.View("order-42")
			if err != nil {
				t.Fatal(err)
			}
			// HTTP 200 不足够：独立快照验证拒绝不改状态，成功恰入账 1299 分且只一次。
			if got.State != tc.wantState || got.CreditedCents != tc.wantCents || got.CreditCount != tc.wantCount {
				t.Fatalf("actual business=%+v; want state=%s cents=%d count=%d", got, tc.wantState, tc.wantCents, tc.wantCount)
			}
			t.Logf("dynamic endpoint=%s HTTP=%d code=%s state=%s credited=%d count=%d", server.URL, status, response.Code, got.State, got.CreditedCents, got.CreditCount)
		})
	}
}

// post 只由当前测试 goroutine 调用，因此 Helper/Fatal 使用位置符合 testing 契约。
// 每次取得响应便安排关闭 Body；读取到 EOF 后连接才有机会被 Transport 重用，CloseIdleConnections 是额外池清理。
func post(t *testing.T, client *http.Client, baseURL, body string) (int, protocol.Response) {
	t.Helper()
	response, err := client.Post(baseURL+"/pay", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("real HTTP request: %v", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close response body: %v", err)
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		t.Fatalf("read real response: %v", err)
	}
	if got := response.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type=%q; want application/json", got)
	}
	var decoded protocol.Response
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode response %q: %v", raw, err)
	}
	return response.StatusCode, decoded
}
