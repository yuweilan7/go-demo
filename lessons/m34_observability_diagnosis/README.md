# M34：从一条慢请求追到等待的源码

这课的业务场景很小：两个独立的合成订单调用本机库存 HTTP 服务。订单 A 的下游暂时不返回，订单 B 也变慢。先用 request ID 找到 B 的日志，再用指标判断有没有积压，用 trace 确认阶段，用 goroutine/block/mutex profile 追到持锁跨网络的代码，最后检查修复是否让 B 在 A 仍等待时完成。额外两组用相同的请求入口诊断重复 hash 的 CPU 工作与凭证 slice 保留的 8 MiB 数组。

课程是独立 module：`go-demo/lessons/m34_observability_diagnosis`，Go 指令为 `go 1.27.0`，OTel API/SDK/stdouttrace 固定 `v1.47.0`。实际运行环境是 Windows/amd64、Go 1.27.0、16 个逻辑 CPU、GOMAXPROCS=16。没有硬前置。M11/M13/M17 只帮助理解 HTTP、context 与并发；不需要先运行它们。

本课创建真实 HTTP listener、client、header 传播与本地 exporter 文件。下游提交事实来自受锁保护的内存 map，服务结束后消失；它不表示数据库持久化或跨进程幂等。本课没有安装云观测平台，也没有复用或恢复已经退役的 Tempo。

## 15 分钟的核心阅读与运行路径

1. 用 2 分钟读下面的命令、五类证据和调用链。
2. 用 4 分钟从 `cmd/demo/main.go` 走到 `contentionTrial → checkout → badCheckout → callDependency → downstream`。先读中文注释，特别是实际等待点与 defer。
3. 运行默认 demo 和解析脚本，用 4 分钟对照 `bad-fast` 日志、等待栈和 `badCheckout` 的两种 profile。
4. 用 3 分钟看 `fixedCheckout`，核对“fast 已提交、holder 尚未提交”的独立断言。
5. 用 2 分钟看结果分类和“不观测不能写成零”。CPU/heap/leak 的完整解析在后半部分，可以分步阅读。

下面这些命令从仓库根目录执行。课程实现所在 worktree 的仓库根目录是 `C:/Users/Lenovo/.codex/worktrees/go-course-m34/go-demo`；主 agent 汇总后可在 `D:/IdeaProject/go-demo` 用同样相对命令复跑。

```powershell
go -C lessons/m34_observability_diagnosis run ./cmd/demo
& .\lessons\m34_observability_diagnosis\scripts\inspect.ps1
go -C lessons/m34_observability_diagnosis test -count=1 -v ./...
go -C lessons/m34_observability_diagnosis vet ./...
go -C lessons/m34_observability_diagnosis test -race ./...
```

最后一条需要能编译 amd64 的 C 工具链。本机默认 `D:/MinGW/bin/gcc.exe` 是 mingw32，首次实际运行报告 `cc1.exe: sorry, unimplemented: 64-bit mode not compiled in`。主 agent 随后统一提供 WinLibs 16.2.0 的 64 位 GCC，本课只在进程内设置 CC/CXX、CGO_ENABLED=1、PATH 后实际复验通过。`CGO_ENABLED=1` 本身不能证明 race 编译链可用。当前机器可运行 `& .\lessons\m34_observability_diagnosis\scripts\verify-race.ps1`，脚本读取被忽略的统一工具链配置并在退出时恢复原环境；其他机器用自己的兼容编译器直接执行 race 命令。

默认 demo 几秒内结束，工作预算为 45 秒；工作预算到期后还要收尾，每个 driver owner 使用独立的 3 秒清理预算，Service 再使用独立的 3 秒清理预算，所以 45 秒不是整个进程退出的硬上限。每个 HTTP client 请求上限 4 秒，泄漏子进程上限 5 秒。CPU 输入固定为 400000 次 4 KiB hash，保留数组固定 8 MiB，每轮最多两个同时进行的业务请求，服务使用三条 `127.0.0.1:0` 动态独占监听。180 ms 是明确采样窗口，不用于猜测 goroutine 是否已经到达 Lock；到达由屏障和真实栈检查。

样本写进本模块被忽略的 `.local/latest/`，每次默认运行覆盖它。要保留另一轮，可从模块目录运行 `go run ./cmd/demo -out .local/another-run`。解析脚本固定解析 `.local/latest`。`evidence/` 是本次实际运行的文本证据，不包含编译产物、profile 二进制或凭据。它不是下一次运行结果。

## 五类证据各自回答什么

| 证据 | 本课回答的问题 | 不能直接证明的事实 |
| --- | --- | --- |
| slog JSON 日志 | 哪个请求、哪个阶段、最终分类、单次耗时和定位栈 | 全部请求的总体分布；最终数据库结果 |
| 低基数 metrics | 正在进行/排队的数量，累计成功/拒绝/取消/未知，耗时样本的 bucket/count/sum | 具体订单为何慢；缺失 scrape 不能当零 |
| OTel distributed trace | 真实 HTTP 跨边界 parent、下游时间、异步任务的 Link | 所有业务事实；没采到 span 不证明未执行 |
| goroutine/CPU/heap/block/mutex profile | 当前停在哪里、CPU 在执行什么、哪些分配还活着、哪里发生同步等待与持锁争用 | profile 不是对象引用图，也不是按请求计费的全量账本 |
| runtime execution trace | 本进程 G/P/M、阻塞/唤醒、用户 task/region 的时间关系 | 它不会自动成为跨服务的业务 trace，网络统计也包含正常 Accept 等待 |

现在必须理解的是 owner、等待点、副作用边界、观测样本和修复不变量。Prometheus 的远端存储、Grafana 面板、采样策略设计、Collector 部署和整套运维告警可以遇到相关源码时再查，本课没有展开这些课程。

## 文件与完整调用关系

| 文件 | 职责和先读的符号 |
| --- | --- |
| `cmd/demo/main.go` | 总入口；flag、45 秒工作 ctx、工作与有界收尾错误返回后 Exit；显式隔离 `-leak-child` |
| `internal/lab/experiments.go` | `Run` 拥有实验；`contentionTrial/outcomeTrial/cpuTrial` 编排真实请求、断言和采样；`request` 拥有同步 HTTP 调用 |
| `internal/lab/drivers.go` | `driverOwner.launch/start/Close` 拥有异步 caller；`take` 只等结果，独立 `Done` 证明 worker 收尾完成 |
| `internal/lab/service.go` | `Service/NewService/startServer`；真实 `checkout` 和 `downstream`；错误 `badCheckout` 与修复 `fixedCheckout`；`callDependency/startAudit/Close` |
| `internal/lab/http.go` | 小型固定方法请求构造，最终调用 `http.NewRequestWithContext` |
| `internal/lab/telemetry.go` | `NewTelemetry`、真实 stdouttrace exporter、结束 span 旁路元数据 `captureProcessor` |
| `internal/lab/metrics.go` | 真实 counter/gauge/histogram 与 Prometheus 文本快照，没有订单标签 |
| `internal/lab/profiles.go` | CPU 的 `Digest/digestOnce`，heap 的 `retainBad/retainFixed`，两种有界泄漏子进程 |
| `internal/lab/lab_test.go` | 九个独立业务、传播、引用、listener 和 driver 清理断言；工作取消/清理阻塞用 gate 确定顺序，没有精确耗时断言 |
| `scripts/inspect.ps1` | 实际解析 pprof，追到源码，解析 runtime trace 并转换 sync/net profile |

同步调用部分是：

```text
main → Run → contentionTrial/outcomeTrial/cpuTrial
  → request → http.NewRequestWithContext + client.Do
  → [真实前端 HTTP] checkout
       → badCheckout → orderMu.Lock → callDependency → client.Do
       → fixedCheckout ─────────────→ callDependency → client.Do
           → [真实下游 HTTP] downstream
               → 等待独立 gate 或观察 request ctx
               → commit → storeMu 下写 store[id]=true
               → JSON 响应 → Body 读取与关闭 → 上层 Reply/error
       → startAudit（另一个由 Service 拥有的 goroutine）
       → 写 HTTP 响应 → 退出 defer：metrics/日志/span.End/finished
  → 验证 HTTP+Reply，并另外查 Committed(id)
  → driverOwner.Close → 取消全部 child、释放 gate、新预算逐个 await Done
  → Service.Close → 取消服务、Shutdown、Serve done、audit Wait
```

`request` 返回 Response/Reply/error 到实验 caller；`callDependency` 返回业务分类、测得时间和相应 error 到 `checkout`。这里“相应 error”有明确范围：构造请求与读取 Body 分支用 `%w` 保留原错误；`Do` 错误分支在 `ctx.Err()!=nil` 时保留该取消/截止原因，供 `errors.Is` 判断；`ctx.Err()==nil` 时替换为固定的分类 cause，避免把完整 URL 带入日志，未保留原 `*url.Error` 与底层网络错误链，不能再用 `errors.As` 从返回值找它们。固定 cause 会损失网络细节，本课不声称完整保留每种原始错误。

`Result.Elapsed` 从 driver span Start 之后的 `started` 到前端 `client.Do` 返回响应头/错误为止，包含构造、header 传播和等待响应头；不包含之后的 Body JSON 解析、Body.Close 与 span.End，构造失败的早退分支则没有该计时样本、值为默认零。它与 handler 日志的 duration、整个 request 函数生命周期、CPU profile 采集窗口各有不同范围。变量 `ctx` 经 `:=` 在同一函数块更新为包含新 span/task 的 context；它不表示原 context 被原地修改。`s *Service` 接收者指向各 handler 共用的服务对象；每次请求的 `outcome/queue/dependency` 是独立局部值。

`checkout` 的退出 defer 是闭包，退出时读最终 outcome；如果写成 `defer metrics.finish(outcome, time.Since(started))`，两个实参在注册时已经求值，就会记录错误 outcome/时长。这里先在 closure 内 recover，再收集最终数据。Go 普通 error 不含 Java 异常那样默认保存的创建栈；本课在上述指定分支用 `%w` 保留对应 cause，panic 故障通过同 goroutine 的直接 defer/recover 和 `debug.Stack` 保存定位栈。`AddSource` 只是 log 调用点，不等于 error 的原始发生栈。

## 核心实验 E01：锁跨 I/O 让无关订单排队

两个请求有不同 ID，没有冲突的共享订单不变量。坏版本把整个下游 HTTP 等待包在 `orderMu` 全局锁内。Java 的 `synchronized`/`ReentrantLock` 类比可帮助理解锁范围；Go 的 `Mutex.Lock` 没有可传入 context 的取消 API，取消等待者不会自动让 Lock 返回。

| 时间步骤 | 参与者与当前函数/等待点 | 状态变化与证明 |
| --- | --- | --- |
| 1 | 实验 owner `contentionTrial` 创建 Service，`drivers.launch` 请求 A | 立即登记 A 的独立 child cancel、ResultReady、Done 和 gate release；caller span 注入 frontend header |
| 2 | 前端 A `checkout → badCheckout` | queueBegin → 真正 Lock 成功；queueEnd；注册 defer Unlock |
| 3 | A `callDependency → client.Do`；下游 A `downstream` 的 select | 真正发出 HTTP；下游发 `down_started`，阻塞在自己的 response gate |
| 4 | owner 等到 A 的 down_started，才 launch B | 保证 A 已经持锁并进入下游阶段，没有用 Sleep 猜 |
| 5 | 前端 B `badCheckout` | 发 queue_entered 只表示将尝试 Lock；`waitingStack` 再确认同一栈含 badCheckout 和 lockSlow |
| 6 | owner 从真实 debug listener 收集栈/metrics | in-flight=2、queue-waiters=1；dependency_count=0，因为 A 还没返回，B 还未派发 |
| 7 | owner 观察 180 ms，再检查 B 没完成且没提交 | B 的 ResultReady 未关闭；Committed(B)=false；这次 select 不消费结果 |
| 8 | owner Release(A)，关闭 gate；下游 A 观察它 | commit(A) 后返回真实 HTTP；通知与观察是两个动作 |
| 9 | A client.Do 返回；badCheckout defer Unlock | B 才能 Lock 成功、派发下游并提交；mutex profile 记持锁者 Unlock，block profile 记等待者 Lock |
| 10 | owner take 两个业务结果，再 Await finished/audit_finished | 结果可读与 caller Done、handler 退出 defer 分开核对；最终提交 2，in-flight/waiters 回零 |
| 11 | 无论成功或提前 return，defer driverOwner.Close | 先 cancel 全部 child、Release 所属 gate，再用新的 Background 3 秒预算逐个等不可消费的 Done |
| 12 | defer Service.Close，再合并两类错误 | 停止 listeners，等待 Serve/handler/audit，关闭 idle connections；原失败和清理失败经 errors.Join 返回；全部成功才输出本轮 PASS |

所有 goroutine 的 owner 与停止关系：

| goroutine | owner | 实际等待与停止条件 | 谁等待结束 |
| --- | --- | --- | --- |
| `drivers.launch` caller | contentionTrial / outcomeTrial / 测试 Cleanup | 独立 child 的 frontend HTTP；请求取消/timeout 后还要执行 worker defer | `take` 只读结果；driverOwner.Close 另用新预算等每个 Done |
| 三个 Serve | Service | listener Accept；Shutdown/Close 结束监听 | Close 读取各 `runningServer.done` |
| 前端 HTTP handler | net/http server/Service | bad B 的 Lock；A 的下游 RoundTrip；取消实验的 select | Shutdown 等 handler 结束；实验额外 Await finished |
| 下游 HTTP handler | net/http server/Service | request 的 gate 或 request ctx.Done | Shutdown；unknown 额外等 down_canceled/down_finished |
| Transport 内部 read/write goroutine | 本课 http.Transport | 网络/内部队列 | Body 关闭、请求取消、CloseIdleConnections 和 server 关闭协调清理 |
| `audit.async` | Service | 本课任务很短；Service ctx 或 1 秒 deadline | Close 在 handler 退出后 `audits.Wait` |
| 泄漏反例 | 有界 child process | unreachable channel 无法被外部唤醒 | 父进程 CommandContext 的 Wait；进程退出回收 |

### 从旧清理缺口看取消与加入的区别

主 agent 独立审查发现，原先 `launch` 只返回一个容量 1 的 Result channel，README 把 `take` 读结果描述成 join。这有两个缺口：`take(workCtx, result)` 可以因为工作 deadline 先返回，此时 worker 还没完成；某个 select 已读走一次结果后，该 channel 也不能再次作为退出证明。`contentionTrial` 的 Await/采样/栈定位失败路径和测试 `t.Fatal` 路径因此没有可靠等待自己启动的 driver。关闭 HTTP 服务只等待它拥有的 server/handler/audit，并不等于加入调用 HTTP 的外层 goroutine。旧版本未经这项独立验收，已经在原模块返工。

现在 `ResultReady` 只关闭一次，结果写入后不再修改，`take` 先等关闭再读 Result；多次观察都不会拿走信号。worker 最后才关闭独立 `Done`，顺序是 `request` 完成 Body.Close/span.End → 发布结果 → worker cleanup → child cancel → close(Done)。测试故意加入一个阻塞 cleanup gate，展示结果已经可读但 Done 尚未关闭的情况。每个 owner 提前退出时，都先取消所有 child、释放所有所属 gate，再用全新的清理预算等每个 Done。清理预算不继承已取消的 workCtx；仍未退出时返回带任务名的 `cleanup incomplete` 错误，并和原失败 `errors.Join`，不会把 timeout 记成完成。

`TestDriverOwnerWaitsAfterWorkDeadline` 先让真实工作 deadline 到期，确认 `take(workCtx)` 在结果门未开放时返回 DeadlineExceeded；owner 的 gate release 才允许 worker 发布结果。测试用明确的 select 求值屏障确认 owner 已进入 join，再确认结果可重复读取、worker 的 cleanup gate 仍关着时 owner 没返回。放行 cleanup 后，真实 Done 关闭，owner 才携带原失败返回。`TestDriverCleanupTimeoutReportsIncomplete` 故意不放行，确认原失败与清理超时同时保留且 Done 尚未关闭，最后测试自己放行并再次 join。测试的 Cleanup 同样负责每个已经启动的 driver，包括 `t.Fatal` 路径；没有用 Sleep 或 NumGoroutine 猜退出。

### 从日志选择 profile，而不是把工具全开当答案

本次真实文本样本保存在 `evidence/requests.jsonl`。以该样本的 `bad-fast` 为例，request duration 约 186.4 ms，queue 约 185.4 ms，dependency 约 1.0 ms。三者不必相加恰好等于总耗时：JSON、同步 exporter、业务与 handler 退出等还有开销。请求有 trace ID，但 metrics 没有该 ID 标签。

这条请求慢在 queue，而不是自己的下游。于是先读 `goroutine-bad.txt`：B 的栈包含 `internal/sync.(*Mutex).lockSlow → sync.(*Mutex).Lock → Service.badCheckout`；A 的栈经 `callDependency` 等待真实 HTTP；下游 A 则在 `downstream` 的 select 等 gate。栈快照没有“B 后来一定会解锁”的含义，只给出此刻状态。

释放 A 后，读 `block-bad-source.txt` 与 `mutex-bad-top.txt`：block 的 badCheckout Lock 等待约 185.8 ms，mutex 的同一 badCheckout 持锁者调用栈约 185.8 ms；Unlock 总项另含其他锁争用，约 187.15 ms。精确归因应跟完整调用栈；内联、return/defer 和采样位置可能让 `-list` 将累计时间落在相邻调用行，不能按某一行有最大数字就认定它本身最慢。

在模块目录可以直接复查：

```powershell
go tool pprof -top .local/latest/block-bad.prof
go tool pprof -list=badCheckout .local/latest/block-bad.prof
go tool pprof -top .local/latest/mutex-bad.prof
go tool pprof -top -diff_base .local/latest/mutex-bad.prof .local/latest/mutex-fixed.prof
```

`block/mutex` 快照包含到该时刻的累计历史，不是“采完后自动清零”。所以 fixed 快照仍可能包含 bad 的旧栈；用同一进程的 bad 基线比较，不能看到历史栈还存在就说修复无效。`mutex-fixed-diff.txt` 在本次新增等待中没有 badCheckout 的新增争用；其小量 OTel/运行时锁开销仍是真实观察。

### 修复与业务影响

`fixedCheckout` 不再持有前端全局锁跨 I/O。权威 map 的共享写入仍由下游 `storeMu` 在极短临界区保护；两个订单没有需要前端 orderMu 保持的共同不变量。修复实验先收到 B 成功响应并确认 Committed(B)=true、Committed(A)=false，之后才 Release(A)。这是机制保证的先后关系，独立于本机某次快了多少毫秒。

最终两轮都有两个成功提交、两个下游样本、in-flight=0、queue-waiters=0。修复没有以丢失订单换速度。真实库存、数据库 Tx、幂等或跨订单一致性需要其权威存储契约；本课的理由不能被泛化为“并发问题都删锁”。

## E02：成功、已知失败、取消与未知结果

| 样本/触发 | 客户端观察 | 服务端 outcome | 下游权威状态与指标 |
| --- | --- | --- | --- |
| normal/hold 释放 | HTTP 200、Reply success | success | 两个核心订单都实际写入 map |
| business | HTTP 409、Reply business_error | business_error | 未提交；依赖可用，但业务拒绝 |
| dependency | 真实下游 HTTP 503，前端 503 | dependency_error | 未提交；基础设施错误单列 |
| admission-cancel | caller 得到 context.Canceled | canceled | 派发前仍在 select；无提交，dependency_count 不增加 |
| response-lost | caller 同样得到 context.Canceled | unknown | 下游先 commit，后阻止响应；取消后查询 Committed=true |
| panic-stack | 前端 500 | internal_error | 未派发；真实 panic 栈保存在日志 |

unknown 的时间线是 `downstream.commit → committed 信号 → owner cancel → caller 停止等 → 前端 client.Do 返回取消原因 → outcome unknown → 下游观察 ctx.Done → down_finished → 查权威 map`。取消不能反向抹去已发生的写入。这里实验能在随后权威查询时确认已提交，但当时响应丢失的请求分类仍是 unknown；观察工具也不能帮客户端倒推不存在的响应。

取消 sample 只证明一个特定边界：本次还没有派发下游，因此可确认无副作用。不能把任何网络请求的 context.Canceled 都分类成“未执行”。本课不会自动重试未知写入，也不把完整 URL/body/header 记入日志。

`metrics-outcomes.prom` 的独立计数是：business_error/dependency_error/canceled/unknown/internal_error 各 1，known errors_total=3、cancellations_total=1、unknown_total=1、dependency histogram count=3、request histogram count=5。错误总数口径在源码里定义为已知业务/依赖/内部失败；未知结果不冒充已知失败。

### histogram 的样本、累计和窗口

本课记录 request、queue_wait、dependency 三个 histogram，bucket 上界为 1 ms、10 ms、50 ms、250 ms、1 s 和 +Inf，单位为秒。依赖计时在 `client.Do` 返回响应头/错误时记录，未覆盖后续 Body 读取；本课 Body 很小且同步读取关闭，真实流式响应要另计 body 阶段。queue 时间包含 queueBegin/signal/Lock 等入场路径开销，并不是内核精确排队时间。

一个 5 ms 样本会让 le=0.01、0.05、0.25、1、+Inf 全部加一，不是产生五个请求。`_count` 是样本数，`_sum/_count` 可算有样本时的均值。count=0 时 sum=0 没有平均延迟含义；bad 等待时 dependency_count=0 也不是“下游延迟为零”，因为那次等待尚未结束。日志用 `queue_observed/dependency_observed` 和 `null` 明示没进入该阶段，避免把缺失改写成零；真实测到的极短阶段可以是 0，仍有 observed=true。

本课未连接 Prometheus。下面是标准查询口径的说明，未声称已在云上执行：

```promql
sum(rate(m34_requests_total[5m]))
sum(rate(m34_requests_total{outcome="dependency_error"}[5m]))
histogram_quantile(0.95, sum by (le, variant) (rate(m34_request_seconds_bucket[5m])))
```

本地窗口可以把同一 Service 两次 scrape 的 count/sum/bucket 相减，看这段时间新增的已完成样本。counter 重启会重置；没有 scrape、没有注册某阶段、只有一个样本和 bucket 边界很宽，都是精度边界。P95 不是某条订单的原始耗时，极少样本不宜解释成稳定分位数。request_id/order_id/user_id/trace_id 不当 metrics label，否则每个订单会扩张时间序列数量；它们放在有界日志或 trace 属性的合适位置。

## E03：真实 trace context 与异步 span 的归属

本课使用真实 OTel `TracerProvider`、`AlwaysSample`、`WithSyncer(stdouttrace exporter)`。导出位置是 `.local/latest/otel-spans.jsonl`，不是远程 OTLP/Tempo。读取 capture/exporter 前，每个 trial 已先加入 driver 的独立 Done，确认 driver span.End 和 worker 收尾完成，再由 Service.Close 加入 HTTP/audit；同步 CPU caller 则已返回并关闭该 Service。随后读取并解析完整 JSON 流，再和结束记录数对照，因此只声称这次真实本地导出成功。旁路 `captureProcessor` 只拷贝元数据核验关系，没有代替 exporter。

```text
driver.checkout (client)
  └─ checkout.server (server，remote parent)
       └─ reserve.client (client)
            └─ reserve.server (server，remote parent)

audit.async (另一个 trace 的 root)
  Link → 原 checkout.server 的 trace ID / span ID
```

`request` Inject 到前端请求；`checkout` Extract 后 Start server span；`callDependency` Start client span 再 Inject；`downstream` Extract 再 Start server span。四个 span 都由当前调用的函数拥有，结束 defer 与调用返回相连。context 不是网络共享对象，HTTP header 中传的是 W3C traceparent 的值。

`startAudit` 是请求之后的独立任务。它使用 Service ctx 的 1 秒子范围；`WithNewRoot + WithLinks` 让另一个 trace 的 root 关联原请求。owner Service 负责等待它，audit goroutine 自己 End；直接用 `r.Context()` 启动返回后任务会继承请求结束取消。Link 不会自动做消息可靠性或任务投递，任何任务持久化仍需业务自己的协议。

本次样本导出 42 个 span：11 条 driver→frontend remote 边和 7 条 frontend client→downstream remote 边，共 18 条真实 HTTP parent 边；6 个 async new root Link。`span-relationships.json` 保留这些 ID 的实际对应关系，测试也检查同一个 trace ID、精确 Parent ID、RemoteParent=true 和 audit 的不同 trace/new-root Link，不能用相似 span 名称替代此验证。

源码定位已经核对 v1.47.0：`propagation/trace_context.go` 的 `TraceContext.Inject/Extract`；`sdk/trace/tracer.go:Start → newSpan → ContextWithSpan → processor.OnStart`；`sdk/trace/span.go:recordingSpan.End → processor.OnEnd`；`simple_span_processor.go:OnEnd → exporter.ExportSpans`；`exporters/stdout/stdouttrace/trace.go:Exporter.ExportSpans → JSON Encoder.Encode`。WithSyncer 适合演示，结束时同步导出会影响本次耗时；生产是否使用 batch/采样需要单独核对。

还实际读到 v1.47.0 SDK 的 `recordingSpan.runtimeTrace`：只有 runtime tracing 开启时，它会为 span 创建 runtime task，结束 span 时也结束该 task。所以本次 runtime 样本还出现 driver.checkout/checkout.server/reserve.client 等 task；这属于当前 SDK 实现。本课自己的 m34.checkout/request_id/queue/dependency region 则明确写在业务源码里。OTel trace 和 runtime trace 仍然回答不同层的问题。

## E04：CPU、heap、goroutineleak 与 runtime trace

### CPU：重复做不变输入的 hash

`checkout?mode=cpu_bad` 调用 `Digest(false, 400000)`，每轮都把同一份 4 KiB payload 送给 `digestOnce → sha256.Sum256`。fixed 同样累计 checksum，却只 hash 一次复用值。读 CPU 的 flat/cum：flat 是函数自身采样，cum 含子调用，不能把累计项相加当总时间。

本次 bad 的真实 CPU 样本共 690 ms，其中 `digestOnce` 的 cum 为 690 ms，SHA256 指令实现 flat 为 680 ms，能追到 `sha256.Sum256(payload)`。前端响应头等待样本 `Result.Elapsed` 约 704.6 ms → 0.5 ms，未包含之后 Body 解析/关闭与 span.End；report 的 `cpu_timing_scope` 也记录该范围。CPU profile 在同步 request 前开始、request（含上述收尾）返回后停止，采集窗口与这个 elapsed 不同。CPU 样本累计多个线程的执行，可能超过墙钟时长，本轮 total 是其 Duration 的约 97.93%，不能当单核使用率。fixed 窗口实际没有业务 CPU 样本；这不表示绝对零 CPU。独立测试按参考 SHA256 公式算 checksum，验证 bad/fixed 相等；源码执行调用数从 400000 到 1，证明去掉了重复工作。

```powershell
go tool pprof -top .local/latest/cpu_bad.prof
go tool pprof -list=digestOnce .local/latest/cpu_bad.prof
go tool pprof -top .local/latest/cpu_fixed.prof
```

这是一次受并行课程构建、机器调度、CPU 指令实现、profile 与同步 exporter 开销影响的样本，不是吞吐率基准或生产优化倍数。若需要性能结论，必须在其他课程构建结束后固定输入/工具链/并发/机器状态串行复验，重复采样并报告分布；本课机制和业务不变量已独立验证，不需要用精确速度阈值判成功。

### heap：小 slice 仍引用完整大数组

`retainBad` 分配 8 MiB 数组，再返回 `data[:16]`；Retainer 看似只保留 16 字节，却仍把大数组带活。`heap-bad.prof` 在一次完成 GC 后的 inuse_space 报告 `retainBad` 的 8192 KiB。profile 定位的是分配调用栈；真正的保留引用还要读 `Retainer.Data` 和 slice 的底层数组关系，pprof 不是 JVM heap dump 的引用图。

`retainFixed` 把 16 字节复制到新数组，owner 替换 Data，再 GC。header 内容保持相同、cap 变为 16，fixed heap 样本不再保留 retainBad 的 8 MiB。独立测试修改旧数组并证明新 header 不变。`data[:16:16]` 只是限制 cap，仍引用旧数组，不能修复保留；GC 后 RSS 何时归还操作系统也不是本课证明的事实。

```powershell
go tool pprof -inuse_space -top .local/latest/heap-bad.prof
go tool pprof -list=retainBad .local/latest/heap-bad.prof
go tool pprof -inuse_space -top .local/latest/heap-fixed.prof
```

### Go 1.27 goroutineleak：检出与盲区都实际运行

`unreachableWaiter` 创建只在自身永远阻塞的栈上保留的 channel；任何可运行 owner 无法发送/关闭，Go 1.27 的 GC 可达性检测实际报告 total=1。它属于坏例子，隔离在最多 5 秒子进程；子进程退出后没有父进程持有的泄漏任务。

`reachableWaiter` 的门仍由 `reachableGate` 全局引用，程序暂时忘记发停止通知。goroutine 栈明确仍在 chan receive，但 goroutineleak debug=1 实际 total=0。运行时无法把“业务永远不会再关闭”推理出来，所以空报告不能证明无业务泄漏。修复由 owner close(gate)，再等 done，确认 worker 的 defer 已完成。

本课首轮故障还揭示一个具体陷阱：本机 Go 1.27 的 `runtime/pprof.writeGoroutineLeak` 先做 leak GC，但 `debug>=2` 随后调用 `writeGoroutineStacks`，包含未泄漏 goroutine。仅看到 reachableWaiter 函数名不能说它被检出；debug=2 中要区分 `(leaked)` 标记。课程用 debug=1 的专用泄漏记录和 Count 核对，首次错误判断及修复记录在 STATUS。

```powershell
# 默认 demo 已经运行过这两种有界子进程；以下是单独的明确故障入口。
go run ./cmd/demo -leak-child unreachable
go run ./cmd/demo -leak-child reachable
```

这不是 Go 1.26 的实验开关用法。Go 1.27 已将 goroutineleak 设为一般可用，旧 GOEXPERIMENT 开关已移除。本课不把 netpoll 等正常长期等待或可达同步对象上的业务停滞一概视为此检测器能识别的泄漏。

### runtime trace：同一请求上的 region 和阻塞/唤醒

`Run` 只在两轮核心实验期间 Start/Stop runtime trace，避免把后续 CPU/heap 实验混在同一个时间窗口。`checkout` 创建 task 并 Log request_id，queue.global_mutex/dependency.http/downstream.response_gate 是明确阶段 region。region 必须在同一 goroutine 内开始/结束；task 通过 context 关联逻辑工作。

解析结果 `runtime-trace-events.txt` 可以按 request_id 找到 bad-fast 的 task，再看到 queue.global_mutex Begin，直到 holder 的 downstream gate End 和 reserve.client End 之后才出现 bad-fast 的 RegionEnd。不是把文件中所有事件行的顺序当作整个程序的严格因果；屏障、锁和 channel 同步机制定义的先后才能断言。

```powershell
go tool trace -d=parsed .local/latest/runtime-trace.out
go tool trace -http=127.0.0.1:0 .local/latest/runtime-trace.out
# inspect.ps1 已实际生成 trace-sync.prof/trace-net.prof 并用 pprof 验证。
go tool pprof -top .local/latest/trace-sync.prof
go tool pprof -top .local/latest/trace-net.prof
```

第二条会启动受控本机查看器，需要使用者看完停止；本课验收没有留下该服务器。sync profile 显示 Lock/chan/select 阻塞；net profile 包含 FD.Read/Accept 等网络等待，这些总数会累计多个 goroutine 的等待，可以大于整个 trace 的墙钟长度。健康 listener 在 Accept 上等待不是业务慢的根因，要回到 request task、HTTP span 和源码关联。

## 采样开销、资源与证明范围

| 类型 | 意义/时机 | 本课开销与边界 |
| --- | --- | --- |
| goroutine debug=2 | 当前所有栈；仍在等待也可看到 | 遍历与栈文本有开销；快照不证明未来/历史 |
| CPU | 采样正在执行的代码 | 只在 CPU 请求窗口启动；短 fixed 窗口可能没样本；Stop 等写完 |
| heap | 最近完成 GC 的活对象分配样本，或累计 alloc_space | 本课启动阶段 MemProfileRate=1，仅有界教学；GC/全采样会影响环境；不证明 RSS |
| block | channel/Mutex/WaitGroup 等同步等待累计时间 | SetBlockProfileRate(1) 收集全部事件，开销高；网络 I/O 需搭配 runtime trace |
| mutex | 争用持锁者释放处，约累计等待者延迟 | SetMutexProfileFraction(1)；多个 waiter 的延迟总和可大于持锁墙钟时长 |
| runtime trace | 创建/调度/阻塞/GC/task/region | 只包两轮 core，Stop 后才关文件；不应长期开启全量当业务账本 |
| goroutineleak | 基于 GC 可达性的永久同步阻塞子集 | 收集触发 leak GC；全局仍可达对象可能漏报；debug=2 含非泄漏栈 |

所有 pprof listener 显式 loopback，业务与 diagnostics mux 分离，没有暴露 cmdline。提供的 CPU HTTP endpoint 固定 1 秒，trace 固定 0.25 秒；客户端不能要求无限采样。课程没有真实凭据，日志也不打印任意请求头/body/完整下游 URL。若未来通过隧道访问，仍应检查隧道端点归属和暴露面，不能直接换成 0.0.0.0。

`Service.Close` 使用 sync.Once，第二次调用等待/复用第一次服务清理结果，不会重复读已经消费的 Serve done channel。它先取消本次服务范围，解除 handler gate/select，再 Shutdown 所有 server，读取 Serve done，最后等待 audit。构造中途失败的 fail helper 也 `errors.Join` 原构造错误与 Close 错误。独立测试在 Close 后真实 Dial 三个地址失败，验证监听已经结束。外层 driver 由 driverOwner 单独管理；它的 Close 可重复观察不可消费 Done，首次超时不会被缓存为完成，放行后可再次真实加入。request result、driver Done、handler finished、audit finished 与 Service 全部清理是不同的证明点。

## 版本与资料核对

2026-10-12 实际核对 `go version/go env/go.mod`、本机 `D:/Go/src` 和 OTel 模块缓存源码，同时读取这些官方入口：

- [slog 文档](https://pkg.go.dev/log/slog)：结构化字段、Handler、context 与 AddSource。
- [runtime/pprof 文档](https://pkg.go.dev/runtime/pprof)：profile 的含义、采样与 WriteTo；本课行为以实际 Go 1.27.0 源码/运行为准。
- [net/http/pprof 文档](https://pkg.go.dev/net/http/pprof)：GET handler、seconds/delta 与受控暴露。
- [runtime/trace 文档](https://pkg.go.dev/runtime/trace)：task/region/Log 与执行事件。
- [Go 1.27 发布说明](https://go.dev/doc/go1.27)：goroutineleak 正式支持、可达性盲区、trace 查看器的 loopback 行为。
- [OTel Go instrumentation](https://opentelemetry.io/docs/languages/go/instrumentation/) 和 [v1.47.0 源码](https://github.com/open-telemetry/opentelemetry-go/tree/v1.47.0)：本课明确 Inject/Extract、SDK Span End 与 stdout exporter。

仓库新材料 `files/开发框架` 只提供框架学习方向，没有可复用的本模块 profile/OTel 实现。现有 `examples/advanced-syntax/context/main.go` 的屏障、Done 广播和 caller 等待做了只读核对。本课没有假定旧材料里的框架版本/API 与当前一致，也不从旧 DeepHelp 观测记录推断云入口现在可用。

语言规则、库契约与运行时实现要分开：channel 关闭广播、defer 参数/闭包读取、slice 别名和锁同步是语言/库层面的规则；HTTP Body 所有权、context 取消以及 SDK Inject/Extract/End 是库契约；SHA256 指令路径、profile 采样位置、Go1.27 leak GC 与当前 OTel 自动 runtime task 是当前实现。遇到公司旧 Go/OTel，先查实际版本，再沿它的源码复核。

全部实际命令、输出、失败修复历史、race 工具链修复与交付边界在 [STATUS.md](STATUS.md)。
