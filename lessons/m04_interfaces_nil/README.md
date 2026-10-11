# M04：interface、typed nil、embedding 与可替换依赖

本课从“读取一个已支付订单”讲起。调用者只想拿到订单，文件仓储负责读取 JSON，日志记录器负责记录读路径。接线位置显式创建依赖，业务层只描述自己要用的两个动作。你会在同一套 `ORDER-1001` 数据上看到正常读取、错误返回、取消、typed nil、方法集不匹配、断言失败、接口比较 panic 和伪覆写失效，随后直接读修复代码。

这是独立 module：`go-demo/lessons/m04_interfaces_nil`，`go.mod` 的语言版本为 `go 1.27`，没有 `toolchain` 指令和第三方依赖。本次实际使用 `go1.27.0 windows/amd64`，`GOTOOLCHAIN=auto`，未启用 `go.work`。所有源码、文档与合成数据都在本模块；没有硬前置，不需要 M02 的代码。

## 先跑，再沿符号阅读（核心约 15 分钟）

从仓库根目录运行：

```powershell
go -C lessons/m04_interfaces_nil run ./cmd/demo
go -C lessons/m04_interfaces_nil test -count=1 -v ./...
go -C lessons/m04_interfaces_nil vet ./...
```

也可以只读一段实验：

```powershell
go -C lessons/m04_interfaces_nil run ./cmd/demo -scenario core
go -C lessons/m04_interfaces_nil run ./cmd/demo -scenario nil
go -C lessons/m04_interfaces_nil run ./cmd/demo -scenario methods
go -C lessons/m04_interfaces_nil run ./cmd/demo -scenario any
go -C lessons/m04_interfaces_nil run ./cmd/demo -scenario embedding
```

`go -C` 会先切到模块目录，随后进程按该目录解析 `testdata/orders.json`。本次实施位置为 `C:/Users/Lenovo/.codex/worktrees/go-course-m04/go-demo/lessons/m04_interfaces_nil/`。在这个目录复跑时，把上面的 `-C lessons/m04_interfaces_nil` 换成 `-C .` 即可。根 module 的 `go test ./...` 不递归包含这个嵌套 module，必须用本课命令验收。

建议一次阅读路径：

1. 约 3 分钟：运行全部实验，读 `cmd/demo/main.go` 的 `main → run`，看到构造与场景分发。
2. 约 4 分钟：读 E01 的 `usecase.OrderRepository → app.Compose → OrderService.Read → Repository.Find`，对照真实文件与日志。
3. 约 4 分钟：读 E02 的 `ValidateBad/ValidateFixed`，再读 E03 的 `RunMethods`，把接口的类型、值、方法集区分开。
4. 约 4 分钟：读 E04/E05 的显式故障与修复，对照外部测试断言。完整解析就在后面，不需要自行做题补完。

现在必须理解：动态类型/动态值、nil 接口与 typed nil、T/*T 方法集、comma-ok、embedding 的实际接收者，以及在创建/返回位置保证依赖可用。遇到相关框架源码再查：泛型约束接口、反射、方法表达式和编译器优化。本课不要求背 `itab`/`eface` 的字段或内存布局。

## 文件与依赖方向

| 文件 | 责任与重点符号 |
| --- | --- |
| [cmd/demo/main.go](cmd/demo/main.go) | CLI 参数、具体对象创建、场景分发、失败退出；`main`、`run` |
| [internal/domain/order.go](internal/domain/order.go) | 订单数据、nil-safe 错误文本、坏/好返回边界；`Order`、`DomainError`、`ValidateBad`、`ValidateFixed` |
| [internal/usecase/service.go](internal/usecase/service.go) | 使用者定义小接口及业务读路径；`OrderRepository`、`LogSink`、`OrderService.Read` |
| [internal/adapter/repository.go](internal/adapter/repository.go) | 真实文件读取与另一种值读取策略；`Repository.Find`、`Snapshot.Find` |
| [internal/adapter/logger.go](internal/adapter/logger.go) | 本次实验的内存日志；`Log`、`Entries`、`Name` |
| [internal/app/composition.go](internal/app/composition.go) | 具体指针检查、转换为接口、编译期契约断言；`Compose` |
| [internal/lab/core.go](internal/lab/core.go) | E01 正常/未找到/取消完整业务实验 |
| [internal/lab/nil.go](internal/lab/nil.go) | E02 错误与仓储的 typed nil 返回边界 |
| [internal/lab/methods.go](internal/lab/methods.go) | E03 值/指针方法集、值复制、指针共享、可替换依赖 |
| [internal/lab/any.go](internal/lab/any.go) | E04 comma-ok、type switch、不可比较接口值与修复 |
| [internal/lab/embedding.go](internal/lab/embedding.go) | E05 提升、冲突、零值嵌入和实际绑定；`Combined`、`BaseStep`、`Outer`、`RunWith` |
| [internal/lab/faults.go](internal/lab/faults.go) | 明确的同步故障边界；`ObservePanic` |
| [testdata/orders.json](testdata/orders.json) | 两条可核对的合成订单，金额单位为分 |
| [testdata/invalid.json](testdata/invalid.json) | 合法 JSON 对象，但不是仓储要求的订单数组；用于真实解码失败 |
| `internal/{app,adapter,lab}/*_test.go` | 从公开调用位置独立验证业务结果、错误原因和语言边界 |

依赖方向是 `cmd/demo → lab → app → usecase`，`app` 同时知道 `adapter`；`usecase` 只导入 `domain`，完全不导入 `adapter`。适配器也只依赖数据层，不依赖使用者定义的接口。组合层同时看见双方，所以接口断言放在这里。这是可查证的实际边界，不只是给类型换一个名字。

```text
run: NewRepository(文件路径) 返回 *adapter.Repository
  → RunCore: NewLogger() 返回 *adapter.Logger
  → app.Compose(具体指针，具体指针)：先检查 nil，再转换为接口
  → usecase.NewOrderService(OrderRepository, LogSink) 返回 *OrderService
  → service.Read(ctx, "ORDER-1001")
      → ValidateFixed：请求输入校验
      → logger.Log("order.read.begin ...")：记录当前请求开始
      → repo.Find(ctx, id)：接口分派到 *adapter.Repository.Find
          → ctx.Err() → os.ReadFile(path) → ctx.Err()
          → json.Unmarshal(data, &orders) → 按 ID 搜索
          → 返回 Order 值和 nil，或包装后的文件/解码/业务错误
      → 记录 success/failed → 返回给 RunCore → 打印结果
```

没有全局 `GetBean`、全局 map 注册仓储或隐藏初始化顺序。读 `Compose` 的两个参数就能追踪依赖是谁创建的。`OrderService` 使用具名字段，读取服务没有把底层仓储的所有方法提升到自己身上。

## E01：订单读路径与真实业务契约

`OrderRepository` 定义在使用者 `usecase` 内，只需要 `Find(context.Context, string) (domain.Order, error)`。签名说明允许做什么；注释另行规定成功、未找到、取消的含义。实现无需声明 `implements`，名称与完整签名匹配就隐式满足接口。参数/返回类型也必须匹配，不能把 Java 协变返回的直觉直接套上去。基础接口规则查证于 [Go 1.27 规范：接口](https://go.dev/ref/spec#Interface_types)。

`NewRepository` 返回具体 `*Repository`，`NewLogger` 返回具体 `*Logger`，`NewOrderService` 返回具体 `*OrderService`。只有使用它们的地方需要抽象时才转换到小接口。接口不是每个结构体都必须提前配备的一层；例如 `Order` 是清晰的数据值，不需要 `IOrder`。当前 CLI 直接使用具体服务；本课没有第二种服务实现或额外服务边界，因此没有再给 `Read` 添加一个暂时用不到的接口。

真实读取的是 JSON 文件中的 `ORDER-1001`，不是硬编码在 `Find` 内返回一个成功对象。文件适配器每次读取都会实际打开、读取并解码文件。日志是当前 Logger 内部切片的真实副作用；本课没有数据库、HTTP 或消息队列，因此不把这些结果称作实库或远端生产证明。

正常路径和失败路径都由同一个 goroutine 串行执行：

| 步骤 | 当前参与者/函数 | 数据或资源变化 | 接下来发生什么 |
| --- | --- | --- | --- |
| 1 | `run → NewRepository` | 保存文件路径，还未打开文件 | 把具体指针传到组合层 |
| 2 | `RunCore → Compose` | 拒绝 nil 具体依赖，再生成业务接口值 | 构造业务服务，没有资源启动 |
| 3 | `Read → ValidateFixed` | 合法 ID 返回 nil 接口 | 记录 begin；空 ID 直接返回，连 begin 都没有 |
| 4 | `Read → Repository.Find` | 先观察 ctx；`ReadFile` 真正读取文件 | I/O 返回后再检查 ctx，随后解码 |
| 5 | `Find → Read` | 找到时返回 Order 值副本和 nil | 记录 success，结果交回入口 |
| 6 | 再次 `Read("ORDER-404")` | 同样读取文件，但没有匹配 ID | 返回 `*DomainError`，业务用 `%w` 包装、记录 failed |
| 7 | `RunCore` 创建并提前取消 ctx | cancel 发出取消状态；未启动 goroutine | Find 的第一次检查返回 `context.Canceled`，没有开始文件读取 |
| 8 | `RunCore` 返回 | defer 再调用 cancel；ReadFile 已尝试关闭自己的文件 | 入口正常完成，无后台任务或持久连接 |

`os.ReadFile` 是同步 API；当前 goroutine 在该调用返回前不继续。它不接收 context，所以调用进行中收到 cancel 不能打断该次文件读取。本课不做长时间 I/O 或并发调度实验。取消只在两个明确检查点被观察，不能据此承诺“取消发生之后绝不会再执行任何代码”。

本机 Go 1.27 标准库源码 `src/os/file.go:871` 的 `ReadFile` 使用 `Open`，成功后注册 `defer f.Close()`，再返回读取结果。本课不拿到这个 `File`，也无需重复关闭。这里调用 Close 的错误不会由 ReadFile 单独返回；文件读取返回也没有证明任何写入持久化。本课日志没有外部句柄，进程退出后即消失。

实际关键输出：

```text
success: id=ORDER-1001 status=PAID amount_cents=12900
missing: code=ORDER_NOT_FOUND wrapped_error=read order "ORDER-404": ORDER_NOT_FOUND: order="ORDER-404"
canceled: errors.Is(context.Canceled)=true
business event[0]=order.read.begin id=ORDER-1001
business event[1]=order.read.success id=ORDER-1001
business event[2]=order.read.begin id=ORDER-404
business event[3]=order.read.failed id=ORDER-404
business event[4]=order.read.begin id=ORDER-1001
business event[5]=order.read.failed id=ORDER-1001
```

这六条事件有保证的顺序来自同步代码的调用顺序，不来自 sleep 或偶然调度。最后两条 begin/failed 属于已取消请求，`begin` 只说明业务开始，不能当成文件已读取。`errors.As(err, &problem)` 沿 `%w` 保留的错误链写入 `problem`；先检查成功且指针非 nil 后再读 Code。`errors.Is` 识别取消原因，不比较错误文本。本课采用仍受支持的 `errors.As` 形式，便于读现有代码；Go 1.27 还支持直接返回匹配结果的 `errors.AsType`，不需要为了本课引入泛型写法。

## E02：typed nil 为什么会骗过打印和构造器

接口值需要分别追踪“静态接口类型”“动态具体类型”“动态值”。下面的二元组是帮助预测语义的记法，不是承诺内存布局：

| 变量 | 静态类型 | 动态类型/值 | `== nil` |
| --- | --- | --- | --- |
| `var empty error` | error | 没有动态类型/值 | true |
| `var problem *DomainError` | *DomainError | 这是具体 nil 指针，尚未成为接口 | true |
| `broken := ValidateBad(合法ID)` | error | *DomainError / nil 指针 | false |
| `fixed := ValidateFixed(合法ID)` | error | 没有动态类型/值 | true |
| `badRepo := BadOptionalRepository(false, repo)` | OrderRepository | *Repository / nil 指针 | false |

`ValidateBad` 的 return 位置发生转换：`*DomainError` 的 nil 不是 error 的 nil。`DomainError.Error` 特意处理 nil 接收者并返回 `<nil>`；所以打印文本像没有错误，但 `if broken != nil` 真会进入错误分支，合法订单请求被阻断。nil-safe 的 Error 只是避免格式化时再次出错，不能修复返回契约。这个规则及推荐的显式 nil 返回查证于 [Go FAQ：nil error](https://go.dev/doc/faq#nil_error)。

修复并不复杂：`ValidateFixed` 先检查具体指针。确有错误时转为 error 返回；成功直接 `return nil`。不要在 caller 到处判断 Error 字符串，也不要反射遍历任意接口寻找“内部是不是 nil”。caller 有权依赖公共返回约定，制造错误接口的边界负责保证这个约定。

```text
empty: type=<nil>, ==nil=true, print=<nil>
bad: type=*domain.DomainError, !=nil=true, print=<nil>
fixed: type=<nil>, ==nil=true, print=<nil>
bad.(*DomainError): ok=true, pointer==nil=true
bad caller: 被误判为失败，合法订单读取被阻断
fixed invalid: ORDER_ID_REQUIRED: order=""
```

断言 `broken.(*DomainError)` 成功只是“动态类型匹配”。`ok=true` 和 `problem==nil` 可以同时成立。它没有为 nil 对象分配实例，也没有做解引用。

相同问题可以出现在仓储依赖：`BadOptionalRepository` 返回 nil *Repository，转换后接口仍非 nil。Find 接收到 nil 指针后，在 `r.path` 解引用处 panic。通用 `NewOrderService` 中的 `repo == nil` 检查也识别不了它，因为接口不是 nil。本课显式运行这一限制，避免读者把“构造器里有 nil 检查”当作充分保证：

```text
bad optional repo: type=*adapter.Repository, !=nil=true, assertion-ok=true, pointer==nil=true
isolated repo fault: panicked=true
interface-only nil guard: accepted=true, Read-panicked=true
fixed optional repo: ==nil=true
required dependency rejected at concrete boundary: compose: repository pointer is nil
```

两种修复对应不同业务契约：可选依赖的创建/返回函数在具体类型处归一化为 nil 接口；必需依赖的 `Compose` 在具体指针处明确拒绝缺失，让失败停在启动接线阶段。`NewOrderService` 的参数前置契约仍然是“可用实现、禁止 typed nil”。面向新实现时，由该实现的创建者维持契约；仅靠方法集无法证明内部对象、连接或配置可用。nil 接收者的方法也可以自己安全处理 nil，本课的 Find/Log 选择把它视作接线错误。

Java 的 `error` 不能类比为异常自动传播：它只是一个方法接口和普通返回值，caller 不检查就可能继续执行。Java 一个接口引用的 null 判断可用于熟悉“依赖缺失”，但 Go 还要问接口里有没有一个带具体类型的 nil 值；这正是类比失效之处。

## E03：T 与 *T，值复制与指针共享

对没有 embedding 的具体类型，先掌握这张表：

| 本课类型 | 值方法集 | 指针方法集 | 是否实现 OrderRepository |
| --- | --- | --- | --- |
| `Repository` | `Name` | — | 否，缺少 Find |
| `*Repository` | — | `Name`、`Find` | 是 |
| `Snapshot` | `Find` | — | 是 |
| `*Snapshot` | — | `Find` | 是 |

值接收者方法在 T 与 *T 的方法集中；指针接收者方法只在 *T 的方法集中。规则查证于 [Go 1.27 规范：方法集](https://go.dev/ref/spec#Method_sets)。

`RunMethods` 中 `value := *repo` 取出一个 Repository 值，它是可寻址的局部变量。所以 `value.Find(...)` 合法，编译器便利地按 `(&value).Find(...)` 调用。但 `var r OrderRepository = value` 仍会编译失败：接口赋值看类型的方法集，不自动替你取地址。不能用“我刚才明明调用成功”证明这个值实现接口。

反例与修复就标在 `RunMethods` 的实际调用旁，故意不编译的赋值只留在注释里，避免破坏正常入口。运行时以 `any(value).(OrderRepository)` 的 comma-ok 形式独立检查方法集，结果 false；`any(&value)` 的结果 true。组合层的 `var _ OrderRepository = (*Repository)(nil)` 是正向编译期断言：改坏签名会直接编译失败，不需要等到线上调用。

```text
addressable value.Find: id=ORDER-1001; Repository implements=false, *Repository implements=true
value interface type=adapter.Snapshot status=PAID; pointer interface type=*adapter.Snapshot status=SHIPPED
```

第二行来自一次有意修改：

1. 读取真实订单，构造 `snapshot := Snapshot{Order: order}`。
2. `copied = snapshot`：接口动态类型是 Snapshot，保存当时的结构体值副本。
3. `shared = &snapshot`：接口动态类型是 *Snapshot，保存指向原对象的指针副本。
4. 修改 `snapshot.Order.Status = "SHIPPED"`。
5. copied 读到 PAID，shared 读到 SHIPPED。

接口不是自动“指向原对象”的盒子。接口赋值遵循被赋入值的语义。这里 Order 的 string/int 字段可以独立复制；若结构体有 slice、map 或指针字段，结构体副本仍可能通过那些字段共享别的对象。Java 对象引用赋给接口的经验接近这里的指针分支，不能解释结构体值分支。

`Snapshot.Find` 以值接收者读取一份订单，所以它和 *Snapshot 都能替换仓储。`ReadSnapshot`/独立断言确实把 Snapshot 传给同一个 `NewOrderService → Read`，不改业务逻辑，也不在业务层断言具体类型。它是一种可执行的本地策略，只能证明接口隔离/替换；E01 的真实文件读取证据仍来自 Repository。

## E04：any 让值进入边界，断言把信息取回来

`any` 是 `interface{}` 的别名；不是动态语言变量，也不会自动把接口转换回原来的具体类型。`any(order)` 是类型转换语法。对业务核心的参数用 `Order` 或 `OrderRepository` 更容易检查契约；只有输入的类型集合确实不固定时才考虑 any。

`AssertOrder(value any)` 使用 `order, ok := value.(domain.Order)`。目标是具体类型时，要求动态类型就是 Order；*Order 不会自动解引用为 Order。失败时得到 Order 零值与 false；caller 必须先看 ok。`MustOrder` 用单返回值断言，同样输入会 panic；本课把它放进显式故障操作，随后继续正常实验。

目标是接口时，断言检查动态类型是否满足该接口，不是要求动态类型名字等于接口名字。`Describe` 的 `case OrderRepository` 可以匹配 *Repository；它只打印类型，避免把“方法集匹配”误当作“动态值可用”。`case nil` 只匹配 nil 接口，typed nil *Order 走 *Order 分支，在具体类型上检查 nil 后再读 ID。

```text
Order assertion: ok=true, amount_cents=12900
*Order -> Order assertion: ok=false, zero-result=true
isolated single-result assertion: panicked=true
type switch: nil interface
type switch: Order value: ORDER-1001
type switch: *Order pointer: ORDER-1001
type switch: typed nil *Order
type switch: OrderRepository implementation: *adapter.Repository
type switch: unsupported: int
```

接口静态类型允许 `==`，但不承诺任意动态值都能比较。本课把两个动态类型相同的 `[]string` 包进 any：`left == right` 会 panic，因为 slice 不能做元素相等比较。相同动态类型是这个反例的关键条件，不能绝对化成“接口里只要有 slice，所有比较都 panic”。例如这里的 `left != nil` 是安全的，因为另一侧是没有动态类型的 nil 接口。规则查证于 [Go 1.27 规范：比较运算](https://go.dev/ref/spec#Comparison_operators)。

```text
slice boxed in any: !=nil=true
isolated interface ==: panicked=true; typed-list comparison=true
```

修复 `EqualOrderIDs([]string, []string)` 用 `slices.Equal` 按元素比较。输入留住具体业务类型，相等含义也变得明确：相同长度且每个订单 ID 相同；nil 和空列表都代表零个订单，因此视为相等。若要订单去重，可比较 ID 这种明确的业务键。不给 any 配一个万能相等函数，也不靠 recover 来做常规业务判断。

Java `Object.equals` 的直觉在这里也不能直接用：Go 的 `==` 是语言规则，不会自动调用自定义 `Equal` 或 `Error` 方法。两个错误文案一样只是显示相同，不足以证明业务错误类别相同。

## E05：embedding 提升选择器，没有虚方法覆写

`Combined` 嵌入 `*Logger` 和 `*Repository`。所谓匿名字段并非没有名字，它们的字段名就是 Logger 与 Repository。`combined.Log(...)` 可简写为 `combined.Logger.Log(...)`，`combined.Find(...)` 可简写为 `combined.Repository.Find(...)`。实际接收者仍是对应的内层指针，Combined 没有变成仓储的子类。

没有冲突时，嵌入 T 会把 T 的值接收者方法提升到 S/*S，T 的指针方法提升到 *S；嵌入 *T 则让 S/*S 都能包含该指针字段的值/指针方法。提升受选择器是否唯一的约束，查证于 [Go 1.27 规范：结构体与提升方法](https://go.dev/ref/spec#Struct_types)。

本课 Logger.Name 与 Repository.Name 在相同嵌入深度冲突：`combined.Name()` 会编译失败，不能按字段声明顺序猜一个赢家。坏调用留在源码注释中，实际修复用 `combined.Logger.Name()` 和 `combined.Repository.Name()` 显式选择，两个含义都得到保留。外层自己的同名方法会遮蔽提升方法，仍可通过明确字段路径访问内层。

零值也要按实际对象看：`var logger Logger` 的 nil entries 切片可以 append，所以零值 Logger 可用；`var zero Combined` 的两个嵌入指针却都是 nil，没有自动分配 Logger 对象。`zero.Log` 找到了合法方法，但执行 `l.entries` 解引用时 panic。`NewCombined` 先拒绝具体 nil 指针，成功时保存同一对依赖引用。

```text
zero embedded *Logger: panicked=true
constructor rejects nil embedded dependency: true
promoted Find: id=ORDER-1001; names resolved explicitly: memory-log | file:testdata/orders.json
```

`BaseStep`/`Outer` 再现维护时更危险的“像 Java override”直觉：

```text
outer.Log("direct")
  → Outer.Log 的接收者是 *Outer
  → o.BaseStep.Logger.Log("outer:direct")

outer.Run()
  → 通过提升选择器调用 BaseStep.Run，接收者是 outer.BaseStep
  → b.Log("inner.run")
  → BaseStep 结构中提升的 Logger.Log

RunWith(outer)
  → 参数 sink 静态类型 LogSink，动态类型 *Outer
  → sink.Log("explicit.run") 接口分派到 Outer.Log
  → Logger.Log("outer:explicit.run")
```

实际日志：

```text
binding event[0]=combined.begin
binding event[1]=outer:direct
binding event[2]=inner.run
binding event[3]=outer:explicit.run
```

索引 2 的 `inner.run` 没有 outer 前缀，证明外层同名 Log 没有自动接管内层方法内部调用。BaseStep.Run 里的 b 是 *BaseStep，它没有隐藏的外层 this；不是先回到 Outer 再寻找所谓 override。修复把变化行为放进 `RunWith(sink LogSink)` 的明确参数，选择 *Outer 后才发生接口动态分派。

Java interface 可以帮助理解调用契约，Java Spring 构造注入可以帮助理解 Compose 的显式接线。但 struct embedding 不建立父子类型关系：不能把 Combined 当作 Repository 直接传给需要具体 Repository 的函数；满足仓储接口则靠提升后的方法集。Go 也不会像 Java 类继承那样自动把内层调用送回外层的覆写方法。

## 故障边界、修复证据与失败命令

正常入口的故障都由命名明确的 `ObservePanic(func(){...})` 隔离：nil 仓储、零值嵌入、失败单值断言、不可比较动态值。每次 action 只执行一个同步有限操作，没有 goroutine、网络等待或循环。panic 展开 action 调用栈，到 ObservePanic 的 defer 被直接 recover；具名 message 被写入后，ObservePanic 返回 caller。它不会从 action 的故障语句后面继续执行。独立测试 `TestFaultBoundaryDoesNotResumeAction` 断言这条返回边界。

`OrderService.Read` 没有用 recover 当作业务错误处理：正确方案是验证依赖创建/返回位置、使用 comma-ok、保留具体业务比较规则、明确传递行为依赖。这个区别可以在实际 imports 和调用位置检查。

实际文件/解码失败不会被隔离成成功：

```powershell
go -C lessons/m04_interfaces_nil run ./cmd/demo -scenario core -data testdata/invalid.json
go -C lessons/m04_interfaces_nil run ./cmd/demo -scenario core -data testdata/no-such-file.json
```

两个命令都应该返回非零退出码。第一个把对象解成订单数组失败；第二个保留文件不存在原因。它们不是“预期失败但忘了修”，而是检查业务失败被真实上报的命名输入。默认正确 fixture 的 demo 与所有测试必须成功。

| 独立测试 | 证明的实际事实 |
| --- | --- |
| `TestRealReadPath` | 真实文件订单 ID/状态/金额正确，返回值修改不改变文件；未找到原因保留；日志快照不暴露内部切片 |
| `TestValidateAndCancellationBeforeIO` | 空 ID 不到 read.begin；提前取消从仓储返回取消原因，而非不存在文件错误；无 success 事件 |
| `TestConcreteDependencyBoundary` | nil 具体仓储/日志在接线位置被拒绝 |
| `TestFileFailures` | 实际文件与 JSON 解码原因可从 `%w` 错误链分类，不检查文案假装成功 |
| `TestTypedNilBoundaries` | 坏 error 文本 `<nil>` 但分支失败；坏仓储仍 panic；修复缺失返回 nil，启用时仍能读真实订单 |
| `TestMethodSetsAndCopies` | Repository 值不满足读取接口，指针满足；直接可寻址调用可行；接口值复制与指针共享；Snapshot 可接同一服务 |
| `TestAnyAssertionsAndComparison` | 失败断言零值/false；单值断言与接口 slice 比较确实 panic；修复保留列表相等含义 |
| `TestEmbeddingBinding` | 三条 Log 实际绑定次序、零值嵌入失败、构造拒绝缺失、同名显式选择与提升读取 |
| `TestFaultBoundaryDoesNotResumeAction` | recover 后 action 的剩余语句没有继续执行 |

这些测试是外部 `*_test` 包从公开接口调用，业务路径使用真实只读 fixture，没有 Mockito 式仓储替身冒充 I/O。编译期断言证明方法签名，运行断言证明业务含义；两者各有范围。未做数据库、远端系统或性能测量，本课也没有并发路径需要 race 实验。

## 对原材料的纠正与本课查证范围

本次先读了 worktree 的 `examples/basic-syntax/{interfaces,interfaceassertions,methods-embedding,errors}`，再从主目录只读新材料。prompt 拼在一起的资料路径不是一个真实路径，实际位置是 `files/基础语法/go语言接口`、`files/基础语法/go语言error` 和 `files/原理剖析/数据结构/interface`。

- “new 的参数必须是接口的实现”不准确。`new(T)` 分配 T 的零值；接口实现检查发生在赋值/传参位置。根示例已经纠正，本课继续采用具体构造再注入。
- “接口 data 总指向原变量 a”不能作为语言保证。E03 已用 PAID/SHIPPED 的独立断言证明值副本和指针共享的区别，不用不稳定的地址实现细节替代语义。
- “errors.New 返回的地址不能比较”不准确。指针动态值可比较，不同 New 调用通常得到不同错误值；同文本不等于同业务错误。任意 error 接口的动态值也可能不可比较，本课 E04 解释这种真正的 panic 边界。
- “error 不携带堆栈”应限定为接口本身只要求 Error 方法；具体错误类型可以另外保存堆栈。本课错误只携带 Code/OrderID，不声称标准接口自动带栈。
- 原材料的 `runtime2.go`、`_type`、`itab` 等片段来自特定实现版本，不能直接当成当前语言规则或公司旧版本源码。本课查证当前 Go 1.27 规范与实际标准库 API，不复制或要求背诵旧内存布局。

本课的语言规则由官方规范/FAQ核对，ReadFile/As/slices.Equal 的 API 在实际 Go 1.27.0 下用 `go doc` 复核，关键现象由真实运行和独立断言核对。完整命令、日期、结果和未验证范围见 [STATUS.md](STATUS.md)。
