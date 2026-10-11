# M10：Go 测试、调试与错误路径的独立证据

这课把一笔 `order-42` 订单从“看起来能跑”变成可以重复验证的业务事实：应收 `12.99` 元（`1299` 分），错误支付 `13.00` 必须被拒绝且订单仍待支付；正确支付和重放只能入账一次。金额、订单、后台清理和 HTTP 都使用同一套合成数据。所有原理、错误反例和修复都已给出，无需另外完成练习。

现在必须理解：`_test.go` 与测试函数的组织、独立的 expected、`t.Fatal` 的 goroutine 边界、测试夹具所有权、通知与退出的区别、`-race` 与业务断言的证明范围。首次阅读可以延后 fuzz 内部覆盖引导算法、Delve 完整命令集和 benchmark；正式性能证据属于 M35，本课不把一次耗时当成性能结论。

## 版本、材料和复跑范围

实际开发环境为 Go **1.27.0 windows/amd64**，本模块 `go.mod` 的路径是 `go-demo/lessons/m10_testing_debugging`，`go` 指令是 `1.27.0`，没有 `toolchain` 指令、第三方依赖或 `go.sum`。`GOTOOLCHAIN=auto` 允许 Go 根据模块要求选工具链，本次实际用的就是安装的 1.27.0。根模块也是 `go 1.27`，但根目录 `go test ./...` 不递归测试本嵌套模块。

首先读过主目录 `files/工程管理/Go单元测试` 和 worktree 的 `examples/advanced-syntax/goroutine-pool/pool_test.go`。前者的 GoConvey 排名、推荐猴子补丁和 mock 组合是作者建议，不能当语言或库契约；本课标准库已经能表达所需事实，不引入全局函数补丁。后者的池容量断言能验证特定业务事实，但 `time.After(50ms)` 观察“没有返回”仍是受调度影响的有限窗口；本课主要用显式屏障证明状态。资料中“测试文件必须和源码同名”也不成立，只需所属包和 `_test.go` 命名等规则正确。

已核对安装工具链的 `go doc`、`go help test/testflag`，以及版本化的 [testing 文档](https://pkg.go.dev/testing@go1.27.0)、[synctest 文档](https://pkg.go.dev/testing/synctest@go1.27.0)。本课使用 Go 1.27 新增的 `synctest.Sleep`；公司旧版本没有这个 API 时用它实际支持的 API，不能照搬本课后擅自升级。fuzz 操作参照 [Go 官方 fuzz 文档](https://go.dev/doc/fuzz)，检测范围参照 [官方 race detector 文档](https://go.dev/doc/articles/race_detector)。

默认从课程仓库根目录执行：

```powershell
go -C lessons/m10_testing_debugging run ./cmd/demo
go -C lessons/m10_testing_debugging test -v -count=1 -shuffle=1729 -timeout=30s ./...
go -C lessons/m10_testing_debugging vet ./...
```

所有记录都来自模块工作目录 `C:/Users/Lenovo/.codex/worktrees/go-course-m10/go-demo/lessons/m10_testing_debugging/`，见 [STATUS.md](STATUS.md) 和 [evidence/](evidence/)。端口、临时路径、goroutine ID、运行耗时和 fuzz 数量是本次观察，下一次无需相同。

## 10～20 分钟核心实验

1. 读 [store.go](internal/order/store.go) 的 `ApplyPaymentWrong → ApplyPayment`，运行正常入口，对照 E02 的两行快照。两者都返回金额不符错误，但只有修复版保持 `pending, credited=0, count=0`。
2. 运行下面的错误契约测试。它**应退出 1**，真实失败行直接指出拒绝后状态已经 paid；不能把这条命令用于“所有命令必须成功”的正常 CI。
3. 运行对应修复测试，核对订单事实，再读 [worker.go](internal/lifecycle/worker.go) 和 E06 的四个 channel。停止通知之后并未退出，放行清理之后 owner 才加入 Done。
4. 看 E07 的真实 HTTP 409、200、重放 200 和实际入账记录，明白“响应码”和“业务副作用”必须一起核对。

```powershell
go -C lessons/m10_testing_debugging test -tags=faults -run='^TestWrongCommit' -count=1 -timeout=30s ./faults
go -C lessons/m10_testing_debugging test -v -run='^TestRejectedPaymentLeavesBusinessUnchanged$' -count=1 ./internal/order
go -C lessons/m10_testing_debugging test -v -run='^TestStopNotificationIsNotExit$' -count=1 -timeout=30s ./internal/lifecycle
```

正常入口能成功，因为它明确验证“坏版本已复现、修复版本满足业务”，不是将坏版本误判为正确。`faults/` 使用 `//go:build faults` 或 `fatalfault` 隔离预期失败；没有显式 tag 时这些文件不属于普通构建。

## 文件职责与完整调用关系

| 文件 | 从入口到事实的职责 |
| --- | --- |
| [cmd/demo/main.go](cmd/demo/main.go) | `main → run → runLogic/runWorker/runHTTP/runStack`；每次实验拥有自己的资源，返回前释放和等待 |
| [internal/order/money.go](internal/order/money.go) | `DecodePayment → ParseCents`；限制输入、校验 JSON/编号/金额，把外部文本变成整数分 |
| [internal/order/store.go](internal/order/store.go) | `NewStore → ApplyPayment/ApplyPaymentWrong → View`；锁保护内存对象，View 返回业务快照副本 |
| [internal/order/money_test.go](internal/order/money_test.go) | 表驱动子测试和 `FuzzParseCents`，用人工金额事实与独立 `math/big` oracle 核验 |
| [internal/order/store_test.go](internal/order/store_test.go) | 拒绝不变、重放一次、缺失订单、独占夹具/临时文件、Cleanup 顺序、环境恢复 |
| [internal/lifecycle/worker.go](internal/lifecycle/worker.go) | `Start` 创建 worker；`Stop` 通知、`ReleaseCleanup` 放行、`Await` 等事实；`RunJob` 回传后台错误 |
| [internal/lifecycle/worker_test.go](internal/lifecycle/worker_test.go) | 真实屏障、虚拟截止、后台错误回传、原子计数器修复，各自断言真实状态 |
| [internal/protocol/http.go](internal/protocol/http.go) | 实际请求 `net/http → NewHandler → DecodePayment → ApplyPayment → writeResponse` |
| [internal/protocol/http_test.go](internal/protocol/http_test.go) | loopback TCP 的 POST、JSON 响应和 Store 快照；每个用例独占动态端口 |
| [faults/logic_test.go](faults/logic_test.go) | 错误提交与共享夹具污染的预期 FAIL，后者有确定的跨用例屏障 |
| [faults/race_test.go](faults/race_test.go) | 两个 worker 对同一计数器无同步写入，显式 race 反例 |
| [faults/fatal_test.go](faults/fatal_test.go) | 后台调用 Fatal 的预期失败，以及 vet 能发现的错误调用位置 |
| [scripts/verify.ps1](scripts/verify.ps1) | 执行有限验证，保存工作目录/命令/输出/退出码；预期失败也核对具体失败原因 |
| [scripts/delve.commands](scripts/delve.commands) | 在修复版支付入口断点，打印真实 Payment、调用栈，再继续并退出 |

`package order_test` 使用独立外部测试包，通过公开 API 观察业务，不依靠直接访问 Store 私有 map。`:=`、指针接收者、map 取值副本、channel 方向、defer 捕获时机及资源所有权均在相应源码旁解释。这里只复制必要事实，不复制整套前置课程；没有硬前置。

## E01：表驱动、子测试、金额与 JSON 边界

`TestParseCents` 将每个名字、输入、人工期望放到 struct 表，`t.Run` 将失败定位到 `TestParseCents/one_cent` 等名字。`1299` 来源于批准的订单金额，不能写 `want := ParseCents(input)`；相同错误实现再算一次 expected 只会共同出错。`t.Errorf` 标记失败并允许当前函数继续检查，`t.Fatalf` 用于后续断言无法成立的前置失败，例如 HTTP 调用根本没有返回响应。失败信息包含输入、got、want 和错误，而不是只有 `assert failed`。

业务金额的规则是 ASCII 非负十进制、无多余前导零、恰好两位小数、最多 `1000000.00`。`ParseCents("0.00")` 合法，但 `DecodePayment` 在支付边界拒绝零。`"12.99" → 1299` 不经过 float64，因此不会让浮点近似值成为资金事实。JSON number、未知字段、连续两份 JSON、无效编号和过大请求均被拒绝。`%w` 保留顶层分类错误和底层原因，`errors.Is` 沿错误链找哨兵，不靠文案相同判断错误。标准 `encoding/json` 对重复键采用后值覆盖前值，本课没有另行实现重复键拒绝，不应把此边界包装成签名支付报文的完整安全校验。

在 go 指令 >=1.22 的现代版本，`for _, tc := range cases` 每轮声明自己的迭代变量，捕获当前 tc 的闭包不再沿用旧版循环变量陷阱；如果循环外声明一个变量再用 `=` 赋值，仍可能共享它。表字段里若包含 map/slice/指针，迭代变量独立也不会深拷贝被指向的数据。本课金额表只有值和不可变 string，因此能 `t.Parallel`。

## E02：没有 data race 的错误提交

| 步骤 | 参与者/函数 | 错误版本的状态 | 修复版本的状态 |
| --- | --- | --- | --- |
| 1 | owner/NewStore | pending，credit=0，count=0 | 相同独立种子 |
| 2 | 调用者/支付方法 | 取得 mu，读取订单副本 | 取得 mu，读取订单副本 |
| 3 | 方法内部 | 先写 `State=paid` 回 map | 先检查 1300 != 1299 |
| 4 | 方法返回 | 返回 ErrAmountMismatch；已写状态不会自动撤销 | 返回 ErrAmountMismatch；尚无写入 |
| 5 | owner/View | paid，credit=0，count=0 | pending，credit=0，count=0 |

真实输出在 [fault-logic.txt](evidence/fault-logic.txt)。坏版也用 Mutex，因此没有不受保护的 map 访问；问题是业务写入位置错误。Java `synchronized` 也不会在业务方法返回错误后自动恢复字段，Go 的 `(value, error)` 更不会像想象中的事务异常那样撤销内存副作用。修复把检查移到全部修改之前，并在已 paid 时返回现有结果，使重复支付不再次计数。

`TestRejectedPaymentLeavesBusinessUnchanged` 不只断言 error，而是独立检查三个事实。`TestPaymentCreditsExactlyOnce` 重放后核验 `paid,1299,1`。在错误提交中即使 expected 仅写“错误应等于金额不符”，测试也会 PASS，却漏掉已 paid 的订单。这就是非镜像断言的价值。

## E03：隔离、Parallel、Cleanup、环境与缓存

共享夹具反例使用同一 Store：`pay` 子测试修改状态后关闭 `changed`，`expect_pending` 子测试接收后再读。锁和 channel 都正确，读到 paid 完全符合同步顺序，但第二个测试本来希望独立 pending 订单，因此真实 FAIL。该错误是测试共享业务数据，不是 data race。修复版 `TestParallelIsolatedFixtures` 为每个子测试调用 `newStore`，并使用 `t.TempDir` 的独占目录；即使订单 ID 和 `receipt.json` 名字相同也互不覆盖。测试还真正读取落盘文件并核验状态与入账字段。

`t.Parallel()` 暂停当前子测试，父测试函数返回之后再按并行上限运行；父函数不能在返回前等一个只有 parallel 子测试才会关闭的 channel。本课故障命令明确 `-parallel=2`，避免一个等待另一个却占满唯一并行槽位。子测试最后完成前父测试不会整体完成。`t.Cleanup` 在测试及其全部子测试完成时执行，后注册先执行；`TestCleanupLIFOAndTempDirRemoval` 实际看到 `[last first]` 并检查临时目录已不存在。Cleanup 是测试生命周期回调，不能自动取消任意 goroutine，必须写出其 Stop/释放/加入动作。

环境变量、工作目录和全局函数补丁属于进程级资源，动态端口或 TempDir 不能隔离它们。`TestEnvironmentRestored` 用串行 child 的 `t.Setenv`，child 返回后检查原值和存在性恢复；`t.Setenv` 禁止在 parallel 测试或 parallel 祖先下使用，因为自动恢复也无法使并发读者隔离。不要先手工 `os.Setenv` 再认为 `t.Parallel` 安全。本课 HTTP 各自创建 `httptest.NewServer`，由操作系统分配空闲端口，避免“先找一个端口再关闭探测 socket”的抢占窗口。

`go test ./...` 是包列表模式，成功结果可被缓存；[cache-second.txt](evidence/cache-second.txt) 的 `(cached)` 表示复用旧结果，不能声称本次又发生了网络请求或故障。`-count=1` 禁用测试结果缓存，不禁用编译缓存；`-timeout=30s` 限制每个测试二进制总运行时间，不是业务请求超时。`-shuffle=1729` 改变顶层测试与子测试顺序并可按种子复跑，不能代替隔离或证明所有调度；本课普通测试已经在该种子下真实跑过。

## E04/E05：race、vet 与后台 Fatal 的证明边界

race 反例的 owner 创建 `start`，两个 worker 接收 start 后各 `counter++` 10,000 次。共同开始没有让两个 worker 彼此同步，`++` 的读改写会冲突。owner 用 WaitGroup 加入所有 worker 后读最终值；本次最终值**恰好为 20,000**，仍有两段真实 `WARNING: DATA RACE`，见 [race-fault.txt](evidence/race-fault.txt)。正确数量不能证明并发访问安全。修复版 `TestAtomicCounterFix` 用 `atomic.Int64.Add`，既检查数量 20,000，也让检测器检查实际执行路径。

Windows 首次 `-race` 实际因默认 `gcc` 为 32 位 MinGW 而构建失败，原输出见 [race-toolchain-initial.txt](evidence/race-toolchain-initial.txt)，不是业务测试 FAIL，也不是 race 检测成功。即使业务源码是纯 Go，平台的 race 运行时构建仍需要对应 C 工具链；amd64 race 构建需要所需 mingw-w64 runtime，官方给出 `--print-file-name libsynchronization.a` 核对办法。课程主 agent 已准备私有 GCC **16.2.0 x86_64-ucrt-posix-seh**，实际返回了该库的完整路径，见 [race-compiler.txt](evidence/race-compiler.txt)、[race-runtime-library.txt](evidence/race-runtime-library.txt)。脚本只在当前进程传 CC/CXX、前置其 bin 目录并启用 CGO，退出时恢复，不修改系统 PATH 或全局 Go 配置。

以下直接 Go 命令需要当前进程已选用该 amd64 工具链；本机可用本文最后的脚本从课程私有 JSON 配置读取工具链，完成同一组命令。

```powershell
go -C lessons/m10_testing_debugging test -race -count=1 -timeout=60s ./...
go -C lessons/m10_testing_debugging test -race -tags=faults -run='^TestRaceSharedCounter$' -v -count=1 -timeout=30s ./faults
go -C lessons/m10_testing_debugging vet -tags=faults ./faults
```

第一条修复集实际 PASS，见 [race-safe.txt](evidence/race-safe.txt)；第二条实际因 `WARNING: DATA RACE` 退出 1。错误提交和共享夹具也在 race 下重跑，分别仍因业务不变量和污染 FAIL，输出没有 DATA RACE，见 [race-logic.txt](evidence/race-logic.txt)、[race-shared.txt](evidence/race-shared.txt)。`go vet -tags=faults` 实际 PASS：它不替我们证明资金状态，也不模拟所有并发读写。race 检测只覆盖这次真正执行过的路径，无法证明没有未触发的数据竞争，更无法证明幂等、状态正确或数据库事务。

`TestFatalInWorker` 是实际错误调用：worker 中 `t.Fatal` 标记整个测试失败，但 `runtime.Goexit` 只终止当前 worker 并执行其 defer；owner 仍继续。证据 [fault-fatal.txt](evidence/fault-fatal.txt) 同时包含“worker Fatal”“owner continued”“owner reached its final line”。[vet-fatal.txt](evidence/vet-fatal.txt) 准确指出 `call to (*testing.T).Fatal from a non-test goroutine`。为观察运行后果，这个孤立命令才关闭 vet，正常验证从不关闭 vet。

```powershell
go -C lessons/m10_testing_debugging vet -tags=fatalfault ./faults
go -C lessons/m10_testing_debugging test -vet=off -tags=fatalfault -run='^TestFatalInWorker$' -v -count=1 -timeout=30s ./faults
```

修复 `RunJob` 使用一个容量 1 的 error channel。job 先执行自己的 defer，再返回 error，wrapper 才发送 error；owner 接收并检查原因、检查 cleaned 信号，然后加入 wrapper 的关闭。owner 是调用 Fatal 的正确位置。这里 job 是有界的合成函数，channel 缓冲只避免无人接收时发送阻塞，不会替任意卡死 job 提供取消。

## E06：停止通知与真正退出的可重复时间线

| 对象 | 谁创建/写入 | 谁等待/读取 | 表示的事实 |
| --- | --- | --- | --- |
| child ctx/Done | Start 创建；owner Stop 或 parent 截止取消 | worker 在 `<-ctx.Done()` 等待 | 停止通知已发出 |
| Started | Start 创建；worker 关闭 | owner/Await | worker 已进入自己的函数 |
| Observed | Start 创建；worker 收到 ctx.Done 后关闭 | owner/Await | worker 已观察通知，并将走到清理屏障 |
| release | Start 创建；owner/ReleaseCleanup 关闭，Once 保证只关闭一次 | worker 在 `<-w.release` 等待 | 清理可以继续，尚不等于已完成 |
| cleaned | worker 原子 Store(true) | owner/Cleaned 原子读取 | 本合成清理标志已完成 |
| Done | Start 创建；worker 的 defer 关闭 | owner/Await | worker 清理并走到退出，owner 可以加入 |

真实顺序为：owner 等 Started → Stop 返回 → owner 等 Observed → 此时 release 未关闭、Done 必须仍开放、cleaned=false → owner 放行 release → worker 设置 cleaned 并 defer 关闭 Done → owner 等 Done、核验 cleaned=true。默认 `Await` 的 context 只给观察者一个诊断上限，观察超时不能自动释放 worker。测试 Cleanup 用独立的清理 context，不能复用已过期业务 context，然后 Stop、ReleaseCleanup、等 Done。没有用调大 sleep 假装后台退出。

`TestVirtualDeadline` 在 `synctest.Test` bubble 内创建 ctx/channel/worker，用 `synctest.Sleep(10s-1ns)` 检查仍未到期，再 Sleep(1ns) 后检查 DeadlineExceeded 和已观察通知。Sleep 在 Go 1.27 等价于推进 bubble 时钟的 `time.Sleep` 后再 Wait；Wait 等其他 bubble goroutine 达到 durably blocked，防止恰好截止时读到尚未执行的状态。本次输出 `virtual elapsed=10s`，外部实际测试无需等现实十秒。

fake clock 是测试环境契约，不是生产网络时间：真实 socket I/O、系统调用和 Mutex 等不属于可确定阻塞。不要把真实 `httptest.NewServer` 放进此 bubble 之后，宣称已模拟数据库超时；E07 真实网络实验在 bubble 外运行。bubble 的 T 不能调用 Run、Parallel 或 Deadline，内部对象也不允许外部 goroutine 任意操作。synctest 会等待 bubble goroutine 退出；它是额外的可控测试，不替代资源 owner 的停止与加入协议。

## E07：协议集成的实际副作用和清理

入口 `runHTTP → client.Post → 真实 loopback TCP → handler → DecodePayment → ApplyPayment → JSON response → client.ReadAll/Close → View`。handler 的请求 body 由服务器拥有；客户端响应 body 由调用者拥有，读到 EOF 后立即 Close，再由 client.CloseIdleConnections 清理空闲连接，server.Close 关闭 listener 并等待请求结束。client.Timeout=2s 是真实 HTTP 等待上限，和 test -timeout、virtual deadline 不同。

本次日志是：错误金额 `409/AMOUNT_MISMATCH → pending,0,0`；正确金额 `200/PAID → paid,1299,1`；重放 `200/PAID → paid,1299,1`。协议测试另覆盖 JSON number、未知订单和过大 HTTP body，所有用例读实际响应并核验 Store。这里确实使用了网络与 HTTP 实现；业务存储仍是内存，没有 MySQL/RabbitMQ/Kafka。

即使业务入账完成，写 JSON 失败或客户端断开也不会自动撤销状态。因此“请求错误就重试”的安全性需要业务幂等协议，不能由 mock 的 `Ack().Return(nil)` 或 `Commit().Return(nil)` 推断。mock PASS 只证明调用方符合替身预设：它没有证明 broker 接收到 ack、重投窗口、offset、数据库连接绑定、真实提交/回滚或断线后的未知结果。这些应在相应模块用真实服务、独占数据和故障窗口验收。本课没有声称此内存支付实现是实库事务。

## E08：真实栈和 Delve，不用猜测解释阻塞

`runStack` 创建 gate/entered/done，worker 进入 `waitForApproval` 后关闭 entered，再在 `<-gate` 等待。owner 取得 entered 后用 `runtime/pprof.Lookup("goroutine").WriteTo(...,2)` 采样真实栈；本次 [demo.txt](evidence/demo.txt) 确实看到：

```text
goroutine ... [chan receive]:
main.waitForApproval(...)
    .../cmd/demo/main.go:183
created by main.runStack in goroutine 1
```

读栈时先找业务符号和阻塞类型，再回源码找到 gate 的 owner。entered 只是进入标志，它关闭时下一条 receive 不一定已经运行；本次采样的 `[chan receive]` 才是当前阻塞证据。栈是瞬时快照，goroutine 号和精确行号随代码变化，不能当语言保证。owner 最后关闭 gate 并等待 done，正常演示没有留下卡死任务。

Delve 使用 `go build -gcflags='all=-N -l'` 关闭优化/内联，再在真实 `ApplyPayment` 入口断点，打印当前参数 `Payment{ID: "order-42", Cents: 1300}` 和调用栈。这能定位本次函数调用、参数与 caller，不等于自动发现业务错误；最后仍用独立业务快照确认修复。详见 [delve.txt](evidence/delve.txt)，重跑可以运行验证脚本的 `-IncludeDelve`。编译二进制放被忽略的 `.local/`，没有提交 debugger 进程或端口；脚本命令文件最后 continue、exit。

## E09：有限 fuzz 与一次完整验证

`FuzzParseCents` 的 seed 覆盖零、一分、正常金额、上限、超限、前导零、指数、空串、Unicode 和 NUL。`decimalOracle` 用正则描述业务批准语法、用 `math/big.Rat` 精确乘 100 再比较整数分；它不使用被测 ParseCents 循环计算 expected。格式化 round-trip 是辅助性质，不能单独证明金额数值：如果 parser 和 formatter 共享同一种错误，两者仍可能互相抵消。

```powershell
go -C lessons/m10_testing_debugging test ./internal/order -run='^$' -fuzz='^FuzzParseCents$' -fuzztime=3s -parallel=2 -timeout=30s
```

最终保存的有限运行检查了 244,821 个输入、两个 worker、约 3 秒变异，未找到失败，详见 [fuzz-amount.txt](evidence/fuzz-amount.txt)。最初 10 个 seed 之外，前次运行的覆盖 cache 使这次 baseline 为 93，随后新增 14 个 interesting 输入。数量、覆盖 cache 和耗时可能变化；这不代表穷尽了 string 或所有金额业务。普通 go test 只运行 seed，`-fuzz` 才进入变异。若找到反例，Go 会把失败输入写到包的 `testdata/fuzz/FuzzParseCents/` 用于回归，应保留而非删掉让测试重新 PASS。

从模块目录执行下面的脚本，记录全部正常与预期失败证据。`-RaceOnly` 只跑新增竞态验收，不重复已经有效的 fuzz/Delve；`-IncludeRace` 可以在完整验证中一起跑竞态。JSON 由主 agent 管理，本模块只读 cc/cxx/bin_dir，不写共享配置。脚本只设当前进程环境，退出时恢复，不运行全局安装，也不把业务凭据写入报告。

```powershell
pwsh -NoProfile -File ./scripts/verify.ps1 -IncludeDelve
# 本机实际完成的竞态补验：
$compiler = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' -Raw | ConvertFrom-Json
pwsh -NoProfile -File ./scripts/verify.ps1 -RaceOnly -CCompiler $compiler.cc -CxxCompiler $compiler.cxx -CompilerBinDir $compiler.bin_dir
```

课堂正常验证应 PASS，故障入口应因指定原因 FAIL。验收只认实际日志中对应的业务事实与协议边界，详细完成情况和未验范围保存在 STATUS，不用 mock、缓存命中或编译成功代替真实故障证据。
