# M05：沿 error 链找原因，在业务边界决定下一步

本课用同一笔合成订单 `ORDER-05` 讲清 `(value, error)`、错误包装、类型匹配、typed nil、输出清理和重试边界。面向熟练 Java 后端工程师、Go 初学者；所有反例和修复都已给出，不需要另外补练习答案。

核心实验是 `-case core`。建议先花 10～20 分钟运行一次并沿注释阅读 `CreateOrder → persistOrder → Classify`，再分步看 I/O、清理和真实 HTTP 扩展。默认入口会串行执行全部实验，预期故障被断言验证后正常结束。

## 入口、版本和阅读顺序

本模块独立 `go.mod` 为 `module go-demo/lessons/m05_errors_contracts`，`go 1.27.0`，没有 `toolchain` 指令、第三方依赖或本地 replace。硬前置为空；M01/M04 只是推荐阅读。仓库根 `go test ./...` 不会递归测试这个嵌套 module，要显式使用 `go -C`。

主 agent 汇总到主仓库后，从 `D:\IdeaProject\go-demo` 执行：

```powershell
go -C lessons/m05_errors_contracts run ./cmd/demo -case core
go -C lessons/m05_errors_contracts run ./cmd/demo
go -C lessons/m05_errors_contracts test -count=1 -timeout=20s -v ./...
go -C lessons/m05_errors_contracts vet ./...
go -C lessons/m05_errors_contracts test -race -count=1 -timeout=40s ./...
```

本次实际开发和验证的独立 worktree 路径是：

```powershell
$lesson = 'C:\Users\Lenovo\.codex\worktrees\go-course-m05\go-demo\lessons\m05_errors_contracts'
go -C $lesson run ./cmd/demo
go -C $lesson run ./cmd/demo -case io
go -C $lesson run ./cmd/demo -case cleanup
go -C $lesson run ./cmd/demo -case unknown
go -C $lesson run ./cmd/demo -case bad
go -C $lesson test -count=1 -timeout=20s -v ./...
go -C $lesson vet ./...
```

Windows 上 `-race` 需要能构建 amd64 runtime/cgo 的 C 编译器。本机最初的 PATH 只有 mingw32 GCC，已由主 agent 准备 portable amd64 WinLibs 并补验通过。复用本机配置时，只在当前 PowerShell 进程设置（不写用户/系统环境）：

```powershell
$m05Toolchain = Get-Content 'D:\IdeaProject\go-demo\.local\course-orchestrator\toolchain.json' -Raw | ConvertFrom-Json
$env:CC = $m05Toolchain.cc
$env:CXX = $m05Toolchain.cxx
$env:CGO_ENABLED = '1'
$env:PATH = "$($m05Toolchain.bin_dir);$env:PATH"
go -C $lesson test -race -count=1 -timeout=40s ./...
```

`toolchain.json` 是本机已忽略的配置，不是课程公开依赖。其他环境按自己的 amd64 C 编译器配置执行标准 race 命令即可；本课未更改全局 PATH，也未升级 Go。

`-case bad` 是明确命名的错误入口：只复现 Close 覆盖 Commit 的返回错误，立即结束；不 panic、不盲重试。core/io 中的错误对照也只观察普通返回值，不会使正常入口崩溃。参数错误或任何不变量不满足时，main 输出失败并退出 1。

| 文件 | 阅读符号和职责 |
| --- | --- |
| `cmd/demo/main.go` | `main`：参数、5 秒总上下文、最终错误与进程退出 |
| `internal/course/labs.go` | `Run`、`runCore`、`runIO`、`runCleanup`、`runUnknown`、`runBad`：实验输入、日志与可观察断言 |
| `internal/course/errors.go` | `CreateOrder`、`persistOrder`、`OperationError`、`DatabaseError`：调用链和结构化原因；`BadValidate/Validate`：typed nil |
| `internal/course/policy.go` | `Classify`、`Decision`：HTTP/RPC/消息边界和下一步策略 |
| `internal/course/io.go` | `TailReader.Read`、`BadReadPayload/ReadPayload`：同次返回数据与错误 |
| `internal/course/export.go` | `ExportReceipt/exportBody`、`BadExportReceipt`、`FileSink/FaultSink`：真实文件和阶段故障 |
| `internal/course/unknown.go` | `RunUnknownExperiment`、`localLedger.serveHTTP`、`queryOrder`：真实本地 HTTP 响应丢失和结果核实 |
| `internal/course/course_test.go` | 独立 caller 包的语义、真实内容、关闭句柄、POST 次数断言 |
| `STATUS.md` | 当前工具链、实际命令、观察结果、资源结局与未验证范围 |

现在必须理解：error 是接口值；成功返回真正的 nil；每个 API 自己定义其他返回值在错误时是否有效；`%w/Unwrap/Is/As` 保留结构；错误类别不等于重试许可；请求失败不等于业务未发生；Close 失败不能覆盖主失败。

可以以后遇到源码再查：自定义 `Is(error) bool`/`As(any) bool` 的兼容适配、堆栈采集库、数据库具体错误码、HTTP Transport 的自动重试实现、完整幂等重试和消息 ack 协议。这些没有被本课的最小映射替代，完整请求重试属于 M33。

## 最小规则：error 是值，返回值的有效范围由 API 决定

Go 的内置 `error` 接口只有 `Error() string`。`return result, err` 是正常的控制流返回，caller 必须主动选择分支；它不是 Java 的 throw，没有自动栈展开或 checked exception 声明。普通返回时仍会执行该函数已登记的 defer。

`CreateOrder` 的明确契约是：成功返回 `Receipt{OrderID: ...}, nil`；失败返回零值 `Receipt{}, err`。因此这一个 API 在 `err != nil` 时不能继续使用订单回执。**不能把这一约定扩展到所有 Go API。** `io.Reader.Read` 则允许同次返回 `n > 0, err != nil`，调用者必须先消费 `p[:n]`，再处理 error。[Go 1.27 io.Reader 契约](https://pkg.go.dev/io@go1.27.0#Reader)

`err == nil` 也只表示这个操作按其契约成功。写到缓冲成功不代表 Flush 成功，Flush 成功不代表 Commit 成功，HTTP 返回不直接代表业务内容正确。每一个阶段都有自己的确认边界。

## CORE01：同文案不等于同一业务错误

入口 `runCore` 创建两次 `errors.New("conflict")`。它们可以执行 `==` 比较，但结果为 false；两个 `Error()` 文本则相同。复用包变量 `ErrConflict` 的两个接口副本仍引用同一个哨兵，`errors.Is` 能匹配它。每次 `errors.New` 返回不同错误值是标准库契约。[errors.New](https://pkg.go.dev/errors@go1.27.0#New)

实际日志：

```text
same_text=true same_error=false sentinel_match=true
```

实际业务影响：订单键冲突、版本冲突、别人的内部错误都可能恰好写着 `conflict`。按字符串归类会把修复方式不同的故障混在一起。文本还可能被包装、翻译或版本更新。测试 `TestCancellationAndUnknownPriority` 还会把另一个 `errors.New("order conflict")` 判为 internal，而不是包哨兵对应的 conflict。

纠正资料 `files/基础语法/go语言error` 的两点：地址可以比较；同文案不同实例不是“不能比较”。比较 `.Error()` 只能比显示文本，不能替代语义判断。error 的动态值若是 slice 等不可比较类型，两个接口直接 `==` 还可能 panic；正常业务先用 `Is/As`，不要把“所有 error 都能安全互比”作为规则。本课只比较已知可比较的哨兵或指针。

## CORE02：数据库原因怎样从 repository 到 caller

输入是 `CreateOrder(ctx, "ORDER-05", BeforeWrite)`，完整同步调用链为：

```text
main
  → Run → runCore
    → CreateOrder：Validate 成功，继续调用 repository
      → persistOrder：ctx.Err()==nil；选择 before-write
      ← *DatabaseError{Code:"40001", Phase:"before-write", Cause:ErrDependency}
    ← *OperationError{Operation:"create", OrderID:"ORDER-05", Cause:databaseErr}
  → errors.Is / errors.As / errors.AsType / Classify
  → reporter 在有订单上下文的实验边界输出
```

`DatabaseError` 是课程定义的合成驱动类型，用于解释错误对象关系，没有真实 SQL、事务或数据库服务。`40001` 在这里由 fixture 明确约定为“写入前短暂失败”，实际驱动必须另外核对代码、阶段和数据库契约。不能把单测替身称为实库验收。

`OperationError.Error` 用 `%v` 显示 Cause 的文本，但它的 `Unwrap() error` 已经建立了真实对象关系，因此仍保留链。单独 `fmt.Errorf("legacy: %v", wrapped)` 则只新建带文案的 error，没有 Unwrap，链真正丢失。修复写法是 `fmt.Errorf("legacy: %w", wrapped)`，或像本课这样使用实现了 Unwrap 的业务 wrapper。[错误包装与 API 契约](https://go.dev/blog/go1.13-errors)

实际日志：

```text
boundary_error=create order=ORDER-05: database code=40001 phase=before-write: order dependency failed
direct_assert=false As=true AsType=true same_pointer=true
good_dependency=true lost_dependency=false Unwrap_type=*course.DatabaseError
```

读日志时逐段对应源码：最外层是 `*OperationError`，所以 `wrapped.(*DatabaseError)` 返回 `ok=false`，不会自动走链；`errors.Unwrap(wrapped)` 只走一层，得到原 `*DatabaseError`；`Is` 找到了下方哨兵；`As/AsType` 找到同一个原始数据库指针，可以读取 Code/Phase，而非解析文案。

旧 API 在 Go 1.13 起可用：

```go
var cause *DatabaseError
if errors.As(err, &cause) && cause != nil {
    // &cause 是 **DatabaseError，As 给 cause 这个变量写入找到的指针。
}
```

当前 API 在 Go 1.26 起可用，本模块实际编译运行：

```go
if cause, ok := errors.AsType[*DatabaseError](err); ok && cause != nil {
    // [*DatabaseError] 是泛型类型参数，不是 slice，也不是调用时传入的对象。
}
```

`As` 的 target 必须是合法的非 nil 指针，否则会 panic；不能把 `cause` 代替 `&cause`。`AsType` 在找不到时返回目标类型的零值与 false。二者都遍历单原因链和多原因树，并匹配可赋值类型；当前 Go 1.27 的本地 `go doc` 和[版本化 errors 文档](https://pkg.go.dev/errors@go1.27.0#AsType)已核对，`TestErrorChainAndBothAPIs` 直接编译执行两种 API。较老公司仓库应继续按实际 Go 版本用 As，不能抄入 AsType 后声称没有升级要求。

包装会向调用方公开可匹配的下层原因，因而也是 API 契约。真实项目可在 repository 有意转换驱动原因为稳定领域错误，避免业务 API 依赖换库后消失的私有类型。本课为教学故意保留 `DatabaseError`，同时用 `ErrDependency` 给稳定语义。

## CORE03：T 和 *T 都满足接口，也不是同一个类型

`ValueCodeError.Error` 是值接收者方法，所以 `ValueCodeError` 与 `*ValueCodeError` 都能放进 error。wrapper 内实际保存 `*ValueCodeError` 时，目标 `ValueCodeError` 匹配不到；保存值时，指针目标也匹配不到。`As` 并不隐式解引用或取地址。

实际日志：

```text
actual=*ValueCodeError search_T=false search_pointer=true code=V05
actual=ValueCodeError search_T=true search_pointer=false
```

修复有两个层次：读取旧类型时选对原 API 的动态形态；新设计统一构造与返回形态。本课真实业务类型 `OperationError`、`DatabaseError`、`RuleError` 均使用指针接收者，值类型自身不满足 error，编译期 `var _ error = (*DatabaseError)(nil)` 检查接口实现。这一行仅做编译检查，不是业务“无错”返回。

本次还实际遇到 Go 1.27 的 vet 新检查：

```text
%w wants operand of error type ValueCodeError, not pointer type *ValueCodeError (defeats errors.Is)
```

原因是 `%w` 的操作数用了指针，然而值本身已实现 error。语法能编译不代表用法符合工具建议。[Go 1.27 Vet 变更](https://go.dev/doc/go1.27#vet)与本地 printf analyzer 源码已核对。最终实验用显式 `OperationError.Cause` 分别装入值与指针，完整观察这两个合法动态类型；生产形态则统一为指针接收者。没有关闭 vet，也没有把这条诊断当作语言编译错误。

## CORE04：typed nil 的两部分接口值

`BadValidate` 内的 `var err *RuleError` 是一个 nil 指针。返回类型却是 error，于是返回时发生接口装箱：动态类型为 `*RuleError`，动态值为 nil 指针。接口与 nil 比较必须两部分都为空，所以 `bad != nil`。

| 返回写法 | error 动态类型 | 动态值 | `err==nil` |
| --- | --- | --- | --- |
| `return nil` | 无 | 无 | true |
| `return (*RuleError)(nil)` | `*RuleError` | nil 指针 | false |
| `return &RuleError{...}` | `*RuleError` | 非 nil 指针 | false |

实际日志：

```text
bad_type=*course.RuleError bad_equal_nil=false AsType_ok=true matched_pointer_nil=true fixed_equal_nil=true
```

注意 `AsType` 的 bool 为 true 却返回 nil 指针：类型确实找到了，对象不存在。直接读 `matched.Field` 会 panic。修复成功返回应直接 `return nil`；nil-safe 的 `Error()` 只防打印解引用，不能修复 caller 的无错判断。与 Java 比较：Java 的 null 引用没有 Go 接口中这个已装入的具体动态类型部分，不能照搬 `null == null` 的直觉。

## CORE05：错误分类、协议结果与重试许可

入口用相同订单键切换 Scenario，`CreateOrder` 包装后交给 `Classify`。业务层不逐层 `log-and-return`；实验边界已知 orderID/请求阶段，输出必要的结构化结果一次。客户端响应应使用稳定业务码和适当文案，不泄露数据库路径、SQL 或内部 cause 文本。本课 HTTP/RPC/消息表是最小策略演示，没有启动 gRPC 或 broker。

| 已知事实 | kind | HTTP / RPC | 消息处理示例 | 下一步 |
| --- | --- | --- | --- | --- |
| 正常得到订单回执 | ok | 201 / OK | ack | not-needed |
| 下单引用的资源缺失 | not-found | 404 / NOT_FOUND | 记录业务拒绝后 ack | do-not-retry |
| 订单键冲突 | conflict | 409 / ALREADY_EXISTS | 记录业务拒绝后 ack | do-not-retry |
| 40001 且 fixture 证明写入前失败 | dependency | 503 / UNAVAILABLE | 幂等且预算允许才有界重投 | may-retry |
| SCHEMA 稳定配置错误 | dependency | 503 / UNAVAILABLE | 停放，等待修复 | do-not-retry |
| Commit 确认丢失，不能确认写入结果 | outcome-unknown | 503 / UNAVAILABLE | 核实结果再决定 ack | reconcile |
| 在 preflight 观察到取消 | canceled | 499 / CANCELLED | shutdown 时保持未确认 | do-not-retry |
| 在 preflight 观察到截止时间 | deadline | 504 / DEADLINE_EXCEEDED | 依具体协议决定后续确认 | do-not-retry |

499 是本课的常见网关约定，非标准 HTTP 状态；连接已断开时根本无法发出响应。消息的 `ack-with-business-rejection` 也依赖当前业务契约：本课说的是拒绝结果本身已被处理，不能据此承诺所有消费者都应 ack。`may-retry` 是有条件许可，实际代码没有发起重复业务请求。

Canceled/DeadlineExceeded 仍用 `%w` 保留，`errors.Is(err, context.Canceled)` 在外层可用。取消通知本身不会倒退已经发生的副作用。本课 preflight 在写入前检查 ctx，故那个具体分支已知未写；不能泛化成所有超时都未写。[context 的取消与 Err 契约](https://pkg.go.dev/context@go1.27.0#Context)

独立断言 `TestBoundaryMappingAndReplayClasses` 分别固定 may-retry、do-not-retry、reconcile 三类；`TestCancellationAndUnknownPriority` 把未知结果、超时、Close 失败 Join 到一起，仍要求 reconcile。这个优先级由业务边界制定，errors.Join 本身没有“主错误优先”属性。支付、下单尤其不能因为文案包含 `timeout` 或 `connection reset` 就重放整笔业务。

error 与 `%w` 不自动保存调用栈。操作名、订单键和阶段能从本课链里恢复；Java Throwable 常见的创建栈不会自动出现。若需要栈，要在特定错误创建处保存 `runtime.Callers` 或使用约定的错误类型；边界当场采集的栈是记录点的当前栈，不能冒称原始故障发生栈。`%+v` 对普通标准库 error 也不会凭空产生栈。

## IO01：先消费有效字节，再处理错误

输入 `ORDER-05\n`。`TailReader` 是纯函数辅助 fixture，每次最多给 `ReadPayload` 的 4 字节缓冲填数据，最后同次返回剩余字节与 io.EOF。它体现 Reader 契约，不冒充网络故障。

| 步骤 | caller → 当前函数 | 状态变化 | 结果 |
| --- | --- | --- | --- |
| 1 | runIO → BadReadPayload → Read | 大缓冲得到 9 字节与 EOF | 错误版本先检查 err，丢掉 9 字节 |
| 2 | runIO → ReadPayload → Read #1 | `ORDE`，n=4、err=nil | bytes.Buffer 复制这 4 字节 |
| 3 | ReadPayload → Read #2 | `R-05`，n=4、err=nil | 累计 8 字节 |
| 4 | ReadPayload → Read #3 | `\n`，n=1、err=EOF | 先复制，再把正常 EOF 转成成功 |
| 5 | 另一 fixture 输入 `ORDER-` | 最后返回 io.ErrUnexpectedEOF | 已读文本与被包装的错误一同交回 caller |

实际输出：

```text
bad="" bad_EOF=true fixed="ORDER-05\n" fixed_error=<nil>
partial="ORDER-" preserved_unexpected_EOF=true; 部分文本不等于完整业务成功
```

非 EOF 错误时，caller 有权使用部分字节作诊断，但不能把截断的订单当成完整业务成功。切片 `buffer[:n]` 与下次 Read 共用底层数组，所以本课马上复制到 bytes.Buffer，不保存可变缓冲别名。bytes.Buffer.Write 的返回 error 总为 nil，代码忽略它有明确契约；对文件/网络 Writer 不能照搬。

本实验最多 4 次 Read，0,nil 被本课策略报告为 ErrNoProgress，预算不足会返回已有字节与 ErrReadBudget。循环次数只能限制调用次数，不能强行打断某个陌生 Reader 自身的阻塞。网络版仍需要 deadline 或能观察 context 的 I/O API。

## CLEAN01～03 / BAD01：主失败、清理失败和实际文件

入口 `runCleanup → NewFileSink → ExportReceipt → exportBody`。实际文件临时目录在本模块 `.local/m05-receipt-*`；打开成功以后关闭责任移交 ExportReceipt。打开失败没有可关闭资源。读取内容使用另一次真实 os.ReadFile。

| 步骤 | 当前函数 | 状态和拥有者 | 失败怎样返回 |
| --- | --- | --- | --- |
| 1 | NewFileSink | 打开真实文件，得到 fd | 返回打开错误，未转移资源 |
| 2 | ExportReceipt | 登记 defer，取得 sink.Close 责任 | 所有后续返回都会尝试 Close |
| 3 | exportBody → WriteString | 小文本进入本次 64 字节缓冲 | 若已触及底层且写失败，返回 write wrapper |
| 4 | writer.Flush | 缓冲字节交给实际 sink.Write | 返回 flush wrapper，不能继续 Commit |
| 5 | sink.Commit | FileSink 调用 fd.Sync | 返回 commit wrapper，成功也不能跳过 Close |
| 6 | ExportReceipt defer | Close 实际句柄 | 有错误则 Join 到具名结果 err |
| 7 | runCleanup | 新读文件得到 9 字节 | 内容断言后删除自己创建的专属目录 |

真实正常日志 `file_content="ORDER-05\n" bytes=9`，测试还在返回后尝试对旧句柄 Write，要求 `errors.Is(err, os.ErrClosed)`。Sync 的验证范围是当前文件系统调用成功与文件内容一致，没有模拟断电，也不是数据库事务 Commit 证明。

故障替身 `FaultSink` 独立观察 Accepted、Committed、Closed 与调用次数：

```text
stage=write  cause_preserved=true commit_calls=0 close_calls=1 committed=false
stage=flush  cause_preserved=true commit_calls=0 close_calls=1 committed=false
stage=commit cause_preserved=true commit_calls=1 close_calls=1 committed=false
stage=close  cause_preserved=true commit_calls=1 close_calls=1 committed=true
```

write 用 128 字节输入，让 WriteString 阶段触及底层；flush 用 9 字节输入，让 WriteString 先成功、真正 Flush 才失败。commit 用明确已知失败的替身；close 在主操作已经成功后失败，所以 `committed=true` 也必须返回 error，不能声称整条流程毫无错误。FaultSink 的 Closed 表示“清理调用已尝试”，不是外部资源一定释放；真实关闭状态由 FileSink 的独立断言证明。

`ExportReceipt` 的 `err` 是具名结果。`return exportBody(...)` 先把结果赋给 err，然后运行 defer，再把最终 err 给 caller。defer 里的闭包在退出时读 err，`closeErr` 是新的 if 局部变量；不用 `err := ...` 遮蔽具名结果。`BadExportReceipt` 把 err 直接赋为 Close 原因，丢掉主 Commit 失败；修复版 `errors.Join(err, closeWrapper)` 同时保留两者。

```text
bad_commit=false bad_close=true fixed_commit=true fixed_close=true
commit_preserved=true close_preserved=true tree=true errors.Unwrap(join)==nil=true
```

Join 形成 `Unwrap() []error` 多分支树；Is/As/AsType 可以遍历，`errors.Unwrap` 仅识别 `Unwrap() error`，所以对 Join 返回 nil。把这个 nil 理解为“没有原因”是另一个错误。[Join](https://pkg.go.dev/errors@go1.27.0#Join)、[Unwrap](https://pkg.go.dev/errors@go1.27.0#Unwrap)。全部输入都为 nil 时 Join 返回 nil；typed nil 接口不等于 nil，不会自动被丢弃。

## UNKNOWN01：真实 HTTP 失败时，业务可能已经完成

运行 `-case unknown`。本实验是实际 loopback TCP/HTTP，不是模拟 client 返回 EOF。`httptest.NewServer` 监听系统分配的本地端口，服务端 ledger 是本次进程的内存 map。它证明响应丢失窗口与查询行为；不证明数据库持久性、支付网关语义或分布式 exactly-once。

完整调用链：

```text
main → Run → runUnknown → RunUnknownExperiment
  client.Do(POST /orders?request_id=REQ-M05-001)
    → net/http handler goroutine → localLedger.serveHTTP
    → map 保存 ORDER-05 → Hijack 连接 → conn.Close，未写响应
  ← client.Do 返回 *url.Error，原因 EOF
  → Join(ErrOutcomeUnknown, postErr) → Classify 返回 reconcile
  → queryOrder → client.Do(GET 同一 request_id)
    → handler 读取 map → JSON 返回请求键、订单键、数量
  → Decode + 内容断言 → 等待两个 handler 完成通知 → owner 清理 → 返回 report
```

| 顺序 | 参与者 / 实际等待点 | 状态变化 | 可确认事实 |
| --- | --- | --- | --- |
| 1 | 入口在 client.Do 等 POST 响应 | handler 获得请求并写 map | 服务端订单 1 笔；客户端此时仍未确认 |
| 2 | handler 关闭被 Hijack 的 conn | 原连接无响应可读 | POST client 返回 EOF；不能反推未写入 |
| 3 | 入口分类 OriginalErr | next=reconcile | 没有发第二次 POST |
| 4 | 入口在 GET client.Do / Decode 等 | 第二个 handler 查同一键，返回 JSON | HTTP 200 与字段内容都满足断言才确认 |
| 5 | 入口 select 等 completed | 接收 2 次 handler defer 的工作段结束通知 | 最后阶段错误已可读，handler 不再修改业务状态 |
| 6 | 子函数退出执行 defer | transport 清闲连接、server.Close、cancel | server 和连接 owner 的清理已完成 |

入口 owner 保存 3 秒子 ctx 与 cancel，client.Timeout 为 2 秒；main 总上下文为 5 秒。服务端请求处理 goroutine 由 net/http 创建，锁只保护短内存操作，从不带锁等待网络。`completed` 由入口创建，容量 2；handler 在 defer 中发送，入口接收，没人关闭这个 channel。这个通知证明业务和连接清理段结束、最后诊断已记录；不承诺发送的瞬间 net/http goroutine 已经退出。正常请求由 server.Close 等待完成；Hijack 连接已关闭，通知后没有后续 I/O 等待。通知、handler 返回和 owner 收到通知属于不同动作，不能把 channel 当自动取消。这里用显式通知与 client 请求结果保证需要的先后关系，没有 sleep 猜调度。

Hijack 后连接不再由 server 管理，所以 handler 必须 Close；`server.Close()` 不能代替这个责任。获得查询 Response 后，queryOrder 拥有 Body，defer 保留 Body.Close 的失败。Transport 只为本实验直接连 loopback；不读取系统代理设置，不改全局网络配置。没有启用 HTTP/2、可触发幂等 Transport 重放的请求头或 keep-alive 连接复用，POST 次数还有服务端独立断言。

实际结果：

```text
server: POST #1 stored ORDER-05 before response
client: POST failed; outcome unknown; no POST replay
server: GET request_id found ORDER-05
first_error_has_url_cause=true first_error_has_EOF=true before_query_next=reconcile
after_query confirmed=true order=ORDER-05 stored=1 POST=1 GET=1
```

OriginalErr 在 report 中保留第一次真实通信错误；后来的 Confirmed=true 表示新的证据查到了结果，不能把历史 POST 改写成“当时就成功返回”。如果查询也失败，本函数把原未知结果与查询错误 Join 返回；不能凭“查不到响应”判断订单不存在。真实系统还要核对查询一致性、幂等键范围、结果存储及恢复协议，不能只套本课内存 map。

## 独立断言与真实范围

`course_test` 是调用者包：

| 断言 | 独立检查的事实 |
| --- | --- |
| `TestErrorChainAndBothAPIs` | 原数据库对象身份、字段、%v 丢链、As/AsType 同时运行 |
| `TestSameTextDoesNotDefineBusinessMeaning` | 文案相同、实例不同、稳定哨兵可匹配 |
| `TestValueAndPointerTargets` | T/*T 两个方向、旧/新 API 的准确动态形态 |
| `TestTypedNilAndValidationContract` | typed nil 非空接口、成功真正 nil、失败有实际对象 |
| `TestBoundaryMappingAndReplayClasses` | 可考虑重试、不可重试、先核实三类及业务零回执 |
| `TestCancellationAndUnknownPriority` | 取消/截止时间保留、未知结果优先、文案不冒充类别 |
| `TestReaderPartialResults/NoProgressBound` | 同次字节与 EOF、截断内容、预算和无进展立即返回 |
| `TestExportStagesAndCleanupTree` | 四阶段原因、关闭一次、Commit 进度、Join 与覆盖对照 |
| `TestActualFileAndClosedHandle` | 真文件内容与旧句柄已关闭 |
| `TestRealHTTPLostResponseAndReconciliation` | 真 *url.Error/EOF、同请求键内容、恰好 1 POST/1 GET |
| `TestBoundaryOutputErrorReturned` | 边界输出失败没有被成功文案掩盖 |

实际验证情况见 STATUS：普通 run/test/vet 已通过；`-race` 已在当前进程使用 portable amd64 编译器实际通过，包含真实 HTTP 共享 ledger 的检查。初始 mingw32 不匹配已解决并保留历史错误；不会把编译器失败冒称代码竞态。数据库错误和清理故障是注明范围的替身，HTTP 响应丢失与文件内容/关闭是实际协议和系统调用证明。竞态检测通过证明本次执行未发现 race，不替代业务不变量或所有可能调度的形式证明。
