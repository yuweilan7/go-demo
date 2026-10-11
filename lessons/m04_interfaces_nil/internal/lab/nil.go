package lab

import (
	"context"
	"fmt"
	"io"

	"go-demo/lessons/m04_interfaces_nil/internal/adapter"
	"go-demo/lessons/m04_interfaces_nil/internal/app"
	"go-demo/lessons/m04_interfaces_nil/internal/domain"
	"go-demo/lessons/m04_interfaces_nil/internal/usecase"
)

// BadOptionalRepository 由 typed nil 实验调用。enabled 模拟某个文件读取插件是否启用。
// 它故意在禁用时把具体指针置 nil，再直接转为接口返回；调用者会错误地认为依赖存在。
func BadOptionalRepository(enabled bool, repo *adapter.Repository) usecase.OrderRepository {
	if !enabled {
		repo = nil // 只改本函数的指针变量，不改 caller 保存的原指针，也没有销毁原仓储。
	}
	return repo // 错误点：(*adapter.Repository, nil) 仍是非 nil OrderRepository。
}

// FixedOptionalRepository 修复上述返回边界，仍由同一实验使用同一个仓储调用。
// 可选依赖缺失就返回 nil 接口；实际业务服务要求必需依赖，所以 Compose 会明确拒绝缺失。
func FixedOptionalRepository(enabled bool, repo *adapter.Repository) usecase.OrderRepository {
	if !enabled || repo == nil {
		return nil // 在仍然知道具体指针类型的位置归一化，不用反射万能 isNil。
	}
	return repo
}

// RunNil 由 demo 的 nil/all 场景调用，输出错误返回与仓储返回的同一条 typed nil 规律。
// out 属于入口；本函数只写文本，不关闭它。仓储指针仅用于接线观察，不新开资源。
func RunNil(out io.Writer, repo *adapter.Repository) {
	fmt.Fprintln(out, "\n[E02] typed nil：打印、分支与返回边界")
	var empty error
	broken := domain.ValidateBad("ORDER-1001")
	fixed := domain.ValidateFixed("ORDER-1001")
	// %T 显示动态类型，%v 通过 Error 得到显示文本；文本不是 nil 判断的依据。
	fmt.Fprintf(out, "empty: type=%T, ==nil=%t, print=%v\n", empty, empty == nil, empty)
	fmt.Fprintf(out, "bad: type=%T, !=nil=%t, print=%v\n", broken, broken != nil, broken)
	fmt.Fprintf(out, "fixed: type=%T, ==nil=%t, print=%v\n", fixed, fixed == nil, fixed)
	problem, ok := broken.(*domain.DomainError)
	fmt.Fprintf(out, "bad.(*DomainError): ok=%t, pointer==nil=%t\n", ok, problem == nil)
	// ok=true 只证明动态类型匹配，取出的指针值仍可能是 nil。
	if broken != nil {
		fmt.Fprintln(out, "bad caller: 被误判为失败，合法订单读取被阻断")
	}
	fmt.Fprintf(out, "fixed invalid: %v\n", domain.ValidateFixed(""))

	badRepo := BadOptionalRepository(false, repo)
	nilPointer, ok := badRepo.(*adapter.Repository)
	fmt.Fprintf(out, "bad optional repo: type=%T, !=nil=%t, assertion-ok=%t, pointer==nil=%t\n",
		badRepo, badRepo != nil, ok, nilPointer == nil)
	// 这是命名明确的故障边界。Find 的 r.path 解引用 panic，业务路径不会用 recover 代替修复。
	fault := ObservePanic(func() { _, _ = badRepo.Find(context.Background(), "ORDER-1001") })
	fmt.Fprintf(out, "isolated repo fault: panicked=%t\n", fault != "")
	// 即使有 repo == nil 检查的通用构造器，也识别不了这个 typed nil。
	// 明确观察它的局限；实际接线仍必须走 Compose 的具体指针检查。
	unsafeService, constructorErr := usecase.NewOrderService(badRepo, adapter.NewLogger())
	fault = ""
	if constructorErr == nil {
		fault = ObservePanic(func() { _, _ = unsafeService.Read(context.Background(), "ORDER-1001") })
	}
	fmt.Fprintf(out, "interface-only nil guard: accepted=%t, Read-panicked=%t\n", constructorErr == nil, fault != "")
	fmt.Fprintf(out, "fixed optional repo: ==nil=%t\n", FixedOptionalRepository(false, repo) == nil)
	_, err := app.Compose(nil, adapter.NewLogger())
	fmt.Fprintf(out, "required dependency rejected at concrete boundary: %v\n", err)
}
