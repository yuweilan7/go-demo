package environment

import (
	"context"
	"net"
	"testing"
)

// TestTCPDoesNotProveAuthentication 用真实 loopback listener 独立检查证据边界。
// 这个 listener 甚至没有读取一个请求：握手成功不能据此推断鉴权或业务完成。
// t 拥有 listener，Cleanup 在测试结束关闭它，不依赖 sleep 猜服务是否已就绪。
func TestTCPDoesNotProveAuthentication(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	evidence := ProbeTCP(context.Background(), listener.Addr().String())
	if !evidence.Connected || evidence.Authenticated || evidence.Business {
		t.Fatalf("TCP 层不应升级为鉴权或业务证据: %+v", evidence)
	}
}

// TestCanceledDial 调用者先取消 ctx，验证拨号失败会保留在连接层。
// cancel 发出通知，DialContext 观察它并返回；不存在由本测试接管的共享服务。
func TestCanceledDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	evidence := ProbeTCP(ctx, "127.0.0.1:1")
	if evidence.Connected || evidence.Authenticated || evidence.Business {
		t.Fatalf("已取消的拨号不应产生通过证据: %+v", evidence)
	}
}

// TestIndexRejectsScopeExpansion 验证退休 MySQL 或公网地址不能通过 M00 入口。
// table 中每项是一份独立配置；测试断言的是权限边界，而不是复制 JSON 内容。
func TestIndexRejectsScopeExpansion(t *testing.T) {
	cases := []Endpoint{
		{ID: "mysql-retired", Address: "127.0.0.1:13306", ReadOnlyTCP: true},
		{ID: "redis", Address: "192.0.2.10:6379", ReadOnlyTCP: true},
	}
	for _, endpoint := range cases {
		if err := ValidateEndpoints([]Endpoint{endpoint}); err == nil {
			t.Fatalf("应拒绝越界配置: %+v", endpoint)
		}
	}
}
