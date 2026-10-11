# M04 实际交付与验证记录

记录日期：2026-10-12，Asia/Shanghai。本轮验证期间读取的本机时间为 `2026-10-12 01:12:30 +08:00`。

实现状态：`READY_FOR_REVIEW`。本模块的文档、源码、正常/反例/修复实验与独立断言已经完成；主 agent 的独立验收、整层汇总与提交由主 agent 记录，本文件不宣称已经 ACCEPTED。

## 目录、版本与范围

- 实施 module：`C:/Users/Lenovo/.codex/worktrees/go-course-m04/go-demo/lessons/m04_interfaces_nil/`。
- module 路径：`go-demo/lessons/m04_interfaces_nil`；`go.mod` 为 `go 1.27`，没有 toolchain 指令。
- 实际 `go version`：`go version go1.27.0 windows/amd64`。
- 本模块 `go env GOVERSION GOTOOLCHAIN GOOS GOARCH GOMOD GOWORK`：依次是 `go1.27.0`、`auto`、`windows`、`amd64`、上述本模块 `go.mod` 路径、空 GOWORK。
- 全部依赖来自标准库，无第三方依赖、无 go.sum、无本地 replace、无其他课程硬前置。
- 先读 ORCHESTRATION、TEACHING_STYLE、M04 prompt、根 README/go.mod 和要求定位的四组示例。项目根 AGENTS.md 不存在，已核查祖先可适用文件；现有 `C:/Users/Lenovo/.codex/AGENTS.md` 为空。
- 新材料仅从 `D:/IdeaProject/go-demo/files/` 只读，真实路径为 `基础语法/go语言接口`、`基础语法/go语言error`、`原理剖析/数据结构/interface`。README 已明确纠正材料中的 new、比较、原变量别名和实现布局结论。
- 所有交付修改局限于本模块目录；未修改根 module、共享索引、examples、files 或其他课程。没有执行 git add/commit/push，没有创建子 agent。

## 最终命令与真实结果

以下“仓库根目录”均指 `C:/Users/Lenovo/.codex/worktrees/go-course-m04/go-demo/`；“模块目录”指上述 M04 路径。命令实际通过工具执行，而非从代码静态推断。

| 工作目录 | 命令 | 真实退出码/结果 | 观察点 |
| --- | --- | --- | --- |
| 模块目录 | `gofmt -w cmd/demo internal/domain internal/adapter internal/usecase internal/app internal/lab` | 0，无错误输出 | 全部手写 Go 源码与测试格式化 |
| 仓库根目录 | `go -C lessons/m04_interfaces_nil run ./cmd/demo` | 0，E01–E05 全部完成 | 真实读路径、nil、方法集、any、embedding |
| 仓库根目录 | `go -C lessons/m04_interfaces_nil test -count=1 -v ./...` | 0，9 个测试全部 PASS | 从外部测试包独立断言业务/语言事实，不复用 PASS 文案 |
| 仓库根目录 | `go -C lessons/m04_interfaces_nil test -count=1 ./...` | 0，三个含测试的包全部 ok | 交付阅读后删去暂时无使用者的 Reader 接口，再次复核全部测试 |
| 仓库根目录 | `go -C lessons/m04_interfaces_nil vet ./...` | 0，无诊断 | 静态检查 |
| 仓库根目录 | `go -C lessons/m04_interfaces_nil run ./cmd/demo -scenario core -data testdata/invalid.json` | 1，符合命名故障预期 | 错误 JSON 形状真实失败，上层没有宣称业务成功 |
| 仓库根目录 | `go -C lessons/m04_interfaces_nil run ./cmd/demo -scenario core -data testdata/no-such-file.json` | 1，符合命名故障预期 | 实际文件不存在，原因通过包装返回 |

最终正常入口的关键输出原样节选：

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

empty: type=<nil>, ==nil=true, print=<nil>
bad: type=*domain.DomainError, !=nil=true, print=<nil>
fixed: type=<nil>, ==nil=true, print=<nil>
bad.(*DomainError): ok=true, pointer==nil=true
bad optional repo: type=*adapter.Repository, !=nil=true, assertion-ok=true, pointer==nil=true
isolated repo fault: panicked=true
interface-only nil guard: accepted=true, Read-panicked=true
fixed optional repo: ==nil=true
required dependency rejected at concrete boundary: compose: repository pointer is nil

addressable value.Find: id=ORDER-1001; Repository implements=false, *Repository implements=true
value interface type=adapter.Snapshot status=PAID; pointer interface type=*adapter.Snapshot status=SHIPPED

Order assertion: ok=true, amount_cents=12900
*Order -> Order assertion: ok=false, zero-result=true
isolated single-result assertion: panicked=true
type switch: nil interface
type switch: typed nil *Order
type switch: OrderRepository implementation: *adapter.Repository
slice boxed in any: !=nil=true
isolated interface ==: panicked=true; typed-list comparison=true

zero embedded *Logger: panicked=true
constructor rejects nil embedded dependency: true
promoted Find: id=ORDER-1001; names resolved explicitly: memory-log | file:testdata/orders.json
binding event[0]=combined.begin
binding event[1]=outer:direct
binding event[2]=inner.run
binding event[3]=outer:explicit.run
```

实际两个文件故障的错误输出：

```text
demo failed: read order "ORDER-1001": decode orders file: json: cannot unmarshal object into Go value of type []domain.Order
exit status 1
```

```text
demo failed: read order "ORDER-1001": read orders file: open testdata/no-such-file.json: The system cannot find the file specified.
exit status 1
```

9 个测试的最终结果：

```text
TestFileFailures                         PASS
TestRealReadPath                         PASS
TestValidateAndCancellationBeforeIO      PASS
TestConcreteDependencyBoundary           PASS
TestTypedNilBoundaries                   PASS
TestMethodSetsAndCopies                  PASS
TestAnyAssertionsAndComparison           PASS
TestEmbeddingBinding                     PASS
TestFaultBoundaryDoesNotResumeAction     PASS
```

## 逐项覆盖与证据边界

| M04 要求 | 完成情况与证据 |
| --- | --- |
| 订单仓储、隐式实现、动态类型/动态值、T/*T、编译期断言 | 完成：usecase 小接口；app/embedding/domain 编译期正向断言；E03 值不匹配、可寻址调用、指针修复；独立测试验证复制/共享和同一服务可替换 |
| nil *DomainError 转 error；nil 仓储接口；返回边界修复 | 完成：E02 实际输出 `!=nil=true print=<nil>`；泛化接口构造器检查不能拦 typed nil；明确 nil 返回及具体构造边界拒绝；不使用反射 isNil |
| 具体类型返回、使用者接口、any、断言、type switch、比较 panic | 完成：构造器返回具体指针；业务层不导入 adapter；E04 单值危险操作与 comma-ok 修复、typed nil type switch、slice 接口比较与元素比较修复 |
| Logger/Repository embedding、方法提升/冲突/零值、实际绑定 | 完成：E05 提升读取真实文件、明确字段化解 Name 冲突；零值嵌入 panic；`inner.run` 无 outer 前缀，显式 LogSink 修复有 outer 前缀；不解释为继承或虚方法覆写 |
| 最小真实读路径与依赖契约隔离 | 完成：JSON 实读、金额/状态/错误链/取消/日志断言；Snapshot 作为另一策略经过同一 usecase；明确注入，不使用全局 service locator |
| 中文教学、调用链、失败/清理、直接完整解析 | 完成：README 按 E01–E05 给出数据、时间线、实际日志和修复；非平凡函数和关键语句就地解释 caller、对象、分支、错误与资源；没有未解析练习 |

方法集不匹配和同名选择器歧义的“不编译语句”只保留为源码注释，正常 module 不包含无法编译的包。方法集反例以实际运行 comma-ok=false 和正向编译断言观察，不宣称额外执行过一个坏编译文件。危险运行操作每次只做有限的同步 action，明确由 lab.ObservePanic 捕获，不靠 sleep 或无界重试。

本课没有并发工作者、数据库、远端中间件、网络端口或共享命名空间；因此没有执行并发 race 或真实中间件实验，不宣称这些证据。取消证明限定为请求开始前已取消与明确 ctx 检查，不证明 ReadFile 支持进行中的取消中断；未做性能/生产容量测量。

## 资源最终状态与查证

所有 go run/go test/go vet 进程都已正常结束或按预期故障退出。课程不启动 goroutine、监听器或代理；仓储每次只调用 ReadFile，在标准库内部注册关闭文件；Logger 仅存放本次实验的内存日志。合成 fixture 只读，没有实验临时数据、生成二进制或需清理的业务资源。

已实际读取官方 Go 1.27 [接口](https://go.dev/ref/spec#Interface_types)、[方法集](https://go.dev/ref/spec#Method_sets)、[提升方法](https://go.dev/ref/spec#Struct_types)、[比较规则](https://go.dev/ref/spec#Comparison_operators) 与 [nil error FAQ](https://go.dev/doc/faq#nil_error)。本机 `go doc os.ReadFile`、`go doc errors.As`、`go doc slices.Equal` 均成功；ReadFile 生命周期另核对本机 `src/os/file.go:871–878` 的 Open/defer Close/readFileContents 路径。

主 agent 待验重点：阅读具体指针 Compose 与通用接口构造器的责任边界；核对方法集反例注释和运行断言；核对 outer.Run 的内部 Log 绑定；复跑上述正常入口/test/vet，确认 STATUS 的预期失败没有被当作默认入口失败。
