package lab

import (
	"context"
	"net/http"
)

// httpRequest/httpGet 只集中构造方法固定、无 body 的课程请求。返回的 request 属于
// caller；NewRequestWithContext 将 ctx 绑定到网络请求，取消时 Transport 才会停止等待。
func httpRequest(ctx context.Context, url string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
}
func httpGet(ctx context.Context, url string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
}
