package lab

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"go-demo/lessons/m19_composition_architecture/internal/api"
	"go-demo/lessons/m19_composition_architecture/internal/config"
	"go-demo/lessons/m19_composition_architecture/internal/memory"
	"go-demo/lessons/m19_composition_architecture/internal/order"
)

// legacyClient 是刻意保留的坏依赖。加锁使访问无 data race，但不能给 App A/B 各自作用域。
// 这是“线程安全≠配置/实例归属正确”的反例，不让 -race 漏报变成隔离正确的证明。
var legacyMu sync.Mutex
var legacyClient *memory.Repository

type globalRepository struct{}

// Commit 由正常 Service 接口调用，却在每次请求时偷偷读取当前全局 client。
// A 的路由仍是 A，A 的 token 仍是 A；其真实写入对象已被 B 的装配替换。
func (globalRepository) Commit(ctx context.Context, req order.Request) (order.Record, error) {
	legacyMu.Lock()
	target := legacyClient
	legacyMu.Unlock()
	if target == nil {
		return order.Record{}, errors.New("legacy client is missing")
	}
	return target.Commit(ctx, req)
}

// LegacyRouter 是显式故障构造入口；root 注册它时改写全局仓储，复现隐式容器的跨实例串线。
// 修复就是 api.NewRouter 接收 root 已构造的 service，业务只保存属于当前 App 的依赖。
func LegacyRouter(repo *memory.Repository) func(*order.Service, config.Config) (http.Handler, error) {
	return func(_ *order.Service, cfg config.Config) (http.Handler, error) {
		legacyMu.Lock()
		legacyClient = repo
		legacyMu.Unlock()
		service, err := order.NewService(globalRepository{})
		if err != nil {
			return nil, err
		}
		return api.NewRouter(service, cfg)
	}
}
func ResetLegacy() { legacyMu.Lock(); legacyClient = nil; legacyMu.Unlock() }
