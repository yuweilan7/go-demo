package lab

import (
	"context"
	"fmt"
	"io"

	"go-demo/lessons/m04_interfaces_nil/internal/adapter"
	"go-demo/lessons/m04_interfaces_nil/internal/domain"
	"go-demo/lessons/m04_interfaces_nil/internal/usecase"
)

// RunMethods 由 demo 的 methods/all 场景调用，观察编译规则与实际复制/共享关系。
// repo 来自入口的具体文件仓储；原订单读一次后作为 Snapshot 的合成输入。
func RunMethods(out io.Writer, repo *adapter.Repository) error {
	fmt.Fprintln(out, "\n[E03] T/*T 方法集：调用便利写法不改变接口实现")
	value := *repo // 解引用取出 Repository 值副本，只有 path 字符串，不复制任何文件句柄。
	// value 是可寻址的局部变量，因此 value.Find 自动改写为 (&value).Find。
	// 但是 var _ usecase.OrderRepository = value 编译失败：Find has pointer receiver。
	// 修复是交给接口 &value 或原来的 repo，不能要求编译器在接口赋值时自动取地址。
	order, err := value.Find(context.Background(), "ORDER-1001")
	if err != nil {
		return err
	}
	_, valueImplements := any(value).(usecase.OrderRepository)
	_, pointerImplements := any(&value).(usecase.OrderRepository)
	fmt.Fprintf(out, "addressable value.Find: id=%s; Repository implements=%t, *Repository implements=%t\n",
		order.ID, valueImplements, pointerImplements)

	snapshot := adapter.Snapshot{Order: order}
	var copied usecase.OrderRepository = snapshot  // 动态类型 Snapshot，动态值是当时的结构体副本。
	var shared usecase.OrderRepository = &snapshot // 动态类型 *Snapshot，指针副本指向同一个 snapshot。
	snapshot.Order.Status = "SHIPPED"
	oldOrder, err := copied.Find(context.Background(), order.ID)
	if err != nil {
		return err
	}
	newOrder, err := shared.Find(context.Background(), order.ID)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "value interface type=%T status=%s; pointer interface type=%T status=%s\n",
		copied, oldOrder.Status, shared, newOrder.Status)
	// Snapshot.Find 是值接收者，所以两个接口都满足相同契约。复制与共享由赋入的值决定，
	// 不由“它们都是接口”决定；结构体有 map/slice/指针字段时，还必须看那些字段的共享关系。
	return nil
}

// ReadSnapshot 由独立测试调用，证明替换为 Snapshot 后业务无需类型断言/修改服务代码。
// 输入 order 是已知的订单值；结果仍经过同一个业务 Read，而不是直接打印仓储字段。
func ReadSnapshot(order domain.Order) (domain.Order, error) {
	service, err := usecase.NewOrderService(adapter.Snapshot{Order: order}, adapter.NewLogger())
	if err != nil {
		return domain.Order{}, err
	}
	return service.Read(context.Background(), order.ID)
}
