# M33：依赖故障时，谁在重试、谁承担副作用、谁负责结束

这课围绕合成库存查询和创建订单运行真实本机 HTTP/1.1。你会看到 503 后恢复、总预算在退避里耗尽、request body 第二次变空、写成功却丢响应或收到损坏回执、三层重试把两个逻辑调用放大到十二次、断路器只放一个半开探针，以及慢依赖占满队列时健康路径仍能完成。所有正常、危险和修复代码都已给出，没有需要你补写的 exercise。

默认实际环境为 **Go 1.27.0，Windows/amd64**，依赖锁定 `github.com/sony/gobreaker/v2 v2.4.0`。每次启动使用 `127.0.0.1:0` 动态端口，新账本、新 gate、新 client，不需要 Redis/MySQL，也不访问其他课程或生产服务。内存账本是真实 handler 的合成副作用；它的证明范围是当前进程，不能外推为数据库持久化、多实例幂等或 exactly-once。

## 从核心入口读起

在仓库根目录执行：

```powershell
go -C lessons/m33_resilience_retries run ./cmd/demo -scenario core
go -C lessons/m33_resilience_retries run ./cmd/demo
go -C lessons/m33_resilience_retries test -v -count=1 -timeout 60s ./...
go -C lessons/m33_resilience_retries vet ./...
```

`core` 是 10～20 分钟的核心讲解路线：读取 `Retry`，运行 GET 的故障/恢复，再沿订单的 UNKNOWN → 查询 → 幂等回执看业务事实，并运行四种不可信201响应和空body写的边界。运行命令本身通常几秒，讲解时间用来对照源码和日志。`all` 额外运行 `amplify`、`breaker`、`isolation`、`limits`；每组也可用 `-scenario` 单独执行。

当前唯一实施目录为 `C:/Users/Lenovo/.codex/worktrees/go-course-m33/go-demo/lessons/m33_resilience_retries`；主 agent 汇总后同一课程路径位于 `D:/IdeaProject/go-demo/lessons/m33_resilience_retries`。进入本课目录可直接执行 `go run ./cmd/demo`。根 `go test ./...` 不会进入嵌套 module，所以验收要显式 `go -C` 本课。

| 阅读顺序 | 文件和符号 | 要掌握的调用关系 |
| --- | --- | --- |
| 1 | `cmd/demo/main.go:main` → `experiments.go:Run/core` | 入口总 watchdog；组安全预算；实验同步返回后才选退出码 |
| 2 | `retry.go:Spec/Policy/Retry/oneAttempt` | 业务协议、总预算、次数和每个响应体作用域 |
| 3 | `fixture.go:handle/Ledger.commit/lookup` | 实际 handler 次数、读取原文、真实 append 副作用和查询 |
| 4 | `experiments.go:retryCore/orders/untrustedWrites/cancellation` | 正常、危险和修复的屏障、返回、业务断言 |
| 5 | `lifecycle.go:Task/startTask/StopAndJoin` | result、取消通知、清理等待和 Done 的分别观测 |
| 6 | `governance.go` → `amplify/breakerExperiment/isolation/limits` | 三层调用、库状态、独立队列/池、开始速率 |
| 7 | `lab_test.go` → `STATUS.md` → `evidence/` | 独立副作用/协议测试，实际版本、命令和清理结果 |

现在必须理解的是：一个 attempt 是一次实际 HTTP 请求；`error` 是返回值；ctx 是传递预算和通知的接口；复制接口、指针或 slice 头不会复制它们引用的共享对象；每个阻塞点和每个资源都必须有 owner。Kitex 的各种策略、滚动桶细节、HTTP/2、跨进程 deadline 协议和分布式限流可以在对应代码中再展开。

## 五种机制分别放在哪一层

| 机制 | 它回答的问题 | 本课位置/真实等待 | 它不能代替什么 |
| --- | --- | --- | --- |
| timeout/deadline | 这次工作最多允许等到什么时候？ | `Retry` 总 ctx；`oneAttempt` 收紧单次预算；队列 `acquire` 沿 caller ctx | 不杀 goroutine，不撤销已提交订单 |
| retry | 哪些暂时失败允许再次派发、最多几次？ | 应用/SDK/server 的显式调用循环；`pause` 等退避 timer | 不能修复非法输入、业务拒绝或未定义的写幂等 |
| rate limit | 一段时间里允许开始多少次？ | `SpacingLimiter.allow`，超额立即拒绝，不发网络 | 不限制已在执行的慢任务数量 |
| concurrency limit | 同时允许多少个业务调用、等待多少个？ | `Bulkhead.slots` 容量1，队列上限1，`select` 等槽/ctx | 不是请求开始速率，不是 socket 数量 |
| circuit breaker | 最近依赖故障是否应暂时停止派发？ | `gobreaker.Execute` 在闭包调用前准入，半开探针 | 不抢回已经占用的槽、连接、数据库锁 |

总预算从业务入口开始就要包括**队列等待、连接池等待、拨号、发送、服务端执行、读取响应和退避**。`Retry` 自己的 Total 从进入 Retry 才开始，所以 `Bulkhead.call` 排队阶段沿用更外层的 ctx：即使排队耗时，内层 `WithTimeout(parent, 2s)` 也不能延长 parent 的期限。工作预算到期后还需要新的独立有限**清理预算**，用于确认工作确实结束，不能把清理时间重新用于业务重试。

`net.Dialer.Timeout` 限制建连阶段；`ResponseHeaderTimeout` 限制写出请求后的等头阶段；读取 body 仍需要 request ctx。连接池 `MaxConnsPerHost` 限制指定 host 的拨号/活动/空闲连接总数；HTTP/2 一条 socket 还能承载多个 stream，因此不能把它当业务并发上限。扩池可能把更多请求送入已经过载的下游，并不能修复慢 SQL、锁等待或故障依赖。

## 完整真实调用链

GET 主线没有另起业务 goroutine，`Retry` 同步执行到读取结果或错误返回：

```text
main -> Run -> core -> withFixture -> retryCore
  -> Retry(parent, Spec, Policy)
     -> WithTimeout(parent, Total) [只建立一次总预算]
     -> oneAttempt(totalCtx, Attempt)
        -> child ctx -> NewRequestWithContext -> Client.Do
           -> countedTransport.RoundTrip -> http.Transport.RoundTrip
           -- loopback TCP / HTTP/1.1 --> Server -> Fixture.handle
              -> ReadAll(MaxBytesReader) -> counts/body原文
              -> 503 / OK / 等gate / Ledger.commit
        <- headers -> 有界ReadAll -> HTTP/JSON/业务分类
        -> defer Body.Close + defer cancel -> Reply/Attempt/error
     -> 分类 -> equal jitter/Retry-After -> pause(totalCtx,timer)
     -> 下一次oneAttempt（仍继承同一个绝对deadline）
  <- Stats/Reply/error -> 实际handler次数/账本/参数断言
  -> client.Close -> Fixture.close -> Shutdown + Serve Done + conns/active归零
```

并发实验加了一层 `startTask`：owner 获得 child cancel、Result 和 Done；任务内仍同步调用同一个 Retry。`Spec` 参数是 struct 值副本，Body 的 slice 头被复制但底层字节共享；本课只读这些字节，修改参数时用新的 `[]byte` 替换。`*Client/*Fixture/*Trace` 是共享指针别名，分别由 Transport、自建锁/atomic、日志锁承担并发访问规则。

`http.NewRequestWithContext` 创建的是客户端 Request；服务端收到的是网络解析后的另一份 Request，客户端 Context 对象和 deadline 不自动序列化。`/gateway` 在入站 `r.Context()` 上另加服务端上限，断连仍可取消它；本课没有实现跨机器精确传播总期限，不声称所有主机共享同一个时间点。

## 核心 A：什么错误能重试，预算什么时候用完

`MaxAttempts=3` 包含首次请求。GET暂时 HTTP 状态只选择 429/502/503/504；明确库存拒绝、幂等参数冲突、其他4xx、坏JSON和业务code均不盲重试。实际日志中 `/flaky` 的 handler 三次为 503、503、200；`/always503` 达到三次即返回 error；`/reject` 只调用一次，业务 code 为 `OUT_OF_STOCK`。

POST采用更保守的订单写协议：只有合法JSON且状态/code符合 `definitiveOrderRejection` 的提交前拒绝契约，才返回确定业务失败，具体为400/BAD_BODY、405/METHOD、409/KEY_PAYLOAD_CONFLICT、422/BAD_ORDER或OUT_OF_STOCK。派发后的网络错误、未约定的非2xx、超限响应、坏JSON或无效成功回执返回UNKNOWN。GET协议错误与POST未知写都停止自动重试，但含义不同：UNKNOWN仍可能已经产生订单，必须继续由caller对账，不能当普通“下单失败”。HTTP状态本身不能说明提交发生在哪个阶段。

`Retry` 入口只调用一次 `WithTimeout`。第一组各 `Attempt.Deadline` 与 `Stats.Deadline` **相等**；慢请求组的 Attempt=70ms 收紧单次等待，而任何 child deadline 都不晚于总190ms。先前成功收到503花掉的时间、真实网络耗时和退避都不退还。150ms总预算、最多10次的实验通常只到第二次，正在等下一段退避时总 ctx 结束；断言依据相同deadline、DeadlineExceeded和真实调用数，实测151ms只是一轮观察。

本次核心日志的因果顺序如下（端口/精确毫秒可变）：

| 顺序 | 参与者/当前函数与等待点 | 当前状态 | 下一步为何发生 |
| --- | --- | --- | --- |
| 1 | caller `Retry` → attempt1 `Do/ReadAll` | 服务端真实503，body被完整读取并Close | 暂时失败、GET幂等、次数还有余额 |
| 2 | caller `pause` | timer退避约9.28ms；总ctx继续走 | timer可读并且ctx尚未取消 |
| 3 | caller attempt2 | 再次真实503，总remaining变小 | 同一deadline，指数上限增大 |
| 4 | caller `pause` | timer退避约13.80ms | 退避完成才派发attempt3 |
| 5 | caller attempt3 | `/flaky` 200/OK；handler总数3 | 同步返回成功，响应body owner归零 |
| 6 | 150ms预算实验caller | 真实503两次，下一wait约110ms但只剩约74ms | ctx到期先结束等待，不发attempt3 |
| 7 | 慢请求handler/客户端 | handler真实等Release；单次70ms期限结束 | I/O失败；server观察断连取消；最终总190ms耗尽 |

equal jitter 的上限 `min(Cap, Base*2^(failed-1))`，等待在上限的半值和完整上限之间随机选择。它缓解多个请求同一时间再打下游。教材每次调用用固定 PCG seed，方便解释日志；**生产需要各请求独立的随机种子/随机源，不能照搬同一seed导致实例重新同步**。重试预算还应结合总体流量、错误比例和依赖容量，不能只设很大的 MaxAttempts。

危险的“每次失败后用 Background 新建完整Total”会切断 parent，退避和前面请求已耗时不再计入后一次预算。M13 的已验收 `budgetCase/budgetUsecase` 已经用真实HTTP与gate复现了这个错误；本课不复制其整套项目，也不重跑前置全部课程。这里用相等deadline与跨attempt调用数进一步验收修复后的Retry。

## 核心 B：body 可以重新读，不代表业务可以重复做

`unsafeBodyReplay` 故意让两份 Request 引用同一个 `*bytes.Buffer`。第一次 Transport 消耗它的读游标，第二次再建 Request 也不能把游标自动倒回。服务端保存的真实请求 body 是 `['stock-query', '']`；两次HTTP都可以200，仍无法证明调用内容一致。

修复 `oneAttempt` 每次从不变的 Body 字节建立新的 `bytes.Reader`。服务端 `/replay` 首次503、第二次200，保存的两段完整 JSON 字节完全相同。`Replayable=false` 模拟一次性流/不能重新读取的来源；第二次派发前返回 `ErrBodyNotReplayable`，handler只新增一次。不是拿同一客户端参数比较两遍，而是读取 server 实际解码前保存的字节。

Go 1.27 的 NewRequest 对 `*bytes.Buffer/*bytes.Reader/*strings.Reader` 可生成 `GetBody`，用来重新取一个body；其他 Reader 不自动拥有这种能力。请求体读取/关闭由 Transport 管理，响应体由调用者关闭。本课为了明确归因，使用新连接并去掉 `GetBody`，由显式Retry重新建Request。生产复用池时须考虑 Transport 对已用过连接上的特定网络错误可能自动重发；可重放 body、幂等方法/幂等 header 和具体失败路径共同决定，不是所有error都自动重试。核对本机 `D:/Go/src/net/http/transport.go:shouldRetryRequest` 与 `request.go:isReplayable`，以及 [net/http 文档](https://pkg.go.dev/net/http)。

GET携带body只用于本机自定义读取实验；通用HTTP GET的body没有普遍定义的业务语义，线上应该按接口协议用query或明确的查询POST。我们没有借此暗示所有代理或外部GET服务都支持body。本课 `Spec` 的所有POST统一代表订单写，没有通过URL或空body猜测查询POST；需要查询POST的系统应显式区分协议种类及无副作用契约。

`Retry-After` 根据 [RFC 9110 10.2.3](https://www.rfc-editor.org/rfc/rfc9110.html#name-retry-after) 可为非负整数秒或HTTP-date。真实 `/retry-after` 首次503携带1秒，显式Retry取 `max(jitter,RetryAfter)`，第二次真实尝试开始至少在一秒后。`/retry-after-long` 同样建议1秒但总预算只有80ms：handler仅1次，退避中DeadlineExceeded，不能把等待截短后提前再发。日期、负数和非法文本由辅助解析测试验证；HTTP日期受时钟偏差影响，生产协议要说明处理策略。

## 核心 C：写已经完成，客户端为什么还收到 UNKNOWN

`/lost-order` 的 handler 在 `Ledger.commit` 锁内先 `append(rows)`，随后 `Hijack` 接管本条真实TCP连接并关闭，不发响应头。此时客户端收到EOF，但账本已经增加。HTTP状态或transport错误都不能证明“没下单”。`Unknown` 是普通error包装器，`Unwrap`保留底层EOF、Canceled、DeadlineExceeded、JSON语法错误或协议原因；不是Go自动替你定义的业务状态。

| 步骤 | caller / handler | 业务事实与观测 |
| --- | --- | --- |
| 1 | 无key POST `/lost-order` | handler1，rows从0到1，然后真实断TCP；客户端UNKNOWN |
| 2 | 正常Retry检查业务协议 | 无幂等协议，返回UNKNOWN + ErrUnsafeRetry，不发第二attempt |
| 3 | 显式危险反例人为再POST `/orders` 无key | 收到ID2/201，rows=2，原本一次订单被重复创建 |
| 4 | 修复输入用固定key `order-m33-stable` POST `/lost-order` | rows=3，客户端仍UNKNOWN，key并不能保证响应不会丢 |
| 5 | 对账owner限时GET `/status?key=...` | 网络返回ID3、SKU/qty，与独立账本行数3一致 |
| 6 | 同key、相同参数POST `/orders` | Duplicate=true、ID仍3、rows仍3，没有再次append |
| 7 | 同key但qty改为2 | 409 KEY_PAYLOAD_CONFLICT，只一次请求，rows仍3 |

对账是有明确owner和有限预算的后续工作。本课在组ctx内创建单独一秒查询；线上如果原请求已经结束，应由独立恢复任务/操作状态机拥有查询预算，不偷偷把原写的Total重置后继续执行。查询“暂未查到”也不自动证明原操作永远不会到达；生产需要协议定义完成/进行中/失败/未知、读一致性、重试范围、幂等记录保留期和权限边界。

安全重试写接口至少需要稳定幂等键或业务唯一约束、相同请求参数、原子副作用与回执记录，以及未知结果的查询/对账协议。只设置 `Idempotency-Key` header 不会替服务端实现这些条件。内存 Mutex 的原子性仅限本进程：八个并发真实POST的独立测试验证handler8次、账本1行、Duplicate7个，但进程重启或多实例需要持久事务/唯一约束，属于后续实库课程。

本课自动 `Retry` 一遇到UNKNOWN就返回给业务caller，即使key稳定、body可重放且MaxAttempts尚有余额，也先查询原操作。上表第6步是caller确认原订单后的独立幂等协议实验；它不表示Retry自己在未知写后自动再发POST。缺少安全协议且配置了额外attempt时，返回链还包含 `ErrUnsafeRetry`，便于通过 `errors.Is` 识别。所有订单POST检查key的规则都与 `len(Body)` 无关：空body可以由服务端默认参数产生真实效果。

`untrustedWrites` 使用另一份新账本，四种真实handler都在commit之后返回201。`oneAttempt`确认订单成功至少要求合法JSON、code=OK、Receipt.ID>0，以及回执SKU/qty与请求一致；读到2xx或code=OK都还不够。

| 实际路径 | commit之后发送的响应 | client返回原因 | 原key查询后的独立事实 |
| --- | --- | --- | --- |
| `/corrupt-order` | 不完整JSON，仍为201 | UNKNOWN + ErrInvalidOrderResponse + 原json.SyntaxError | ID1、真实SKU/qty，rows1，POST仅1次 |
| `/large-order` | 合法JSON加6000字节padding，仍为201 | UNKNOWN + ErrResponseTooLarge，保护4096字节读取上限 | ID2、真实SKU/qty，rows2，POST仅1次 |
| `/no-receipt-order` | 只有code=OK，仍为201 | UNKNOWN + ErrInvalidReceipt | ID3、真实SKU/qty，rows3，POST仅1次 |
| `/mismatched-order` | 回执ID存在但qty被改坏，仍为201 | UNKNOWN + ErrInvalidReceipt | ID4、账本中的原qty，rows4，POST仅1次 |

每一项的次序都是：handler锁内真实append → 发送不可信响应 → Retry返回UNKNOWN且attempts1 → caller带**同一个key**发GET `/status` → 对照账本行数、原参数和写handler计数。查询确认以后没有额外POST，不能拿客户端生成的回执或两个相同断言代替服务端副作用证据。独立测试每个子例使用新fixture，所以各自为rows1/ID1；demo在同一组账本里逐项得到ID1～4。

紧接着 `/empty-lost-order` 没收到body仍创建服务端默认订单，再断TCP。即使输入错误地标记Idempotent=true，缺key也只派发1次，返回UNKNOWN + ErrUnsafeRetry，账本从4到5。独立测试还运行带稳定key的空body写：同样停一次，GET原key查到server-default-m33/qty1，不借空body绕过协议或盲重放。已知的BAD_ORDER/OUT_OF_STOCK拒绝则在commit前返回有效422，另一个独立测试要求它们保持确定失败且rows0。

## 核心 D：取消的是等待，owner还要确认结束

`cancellation` 等真实 `/always503` 的handler返回，`OnBackoff`关闭专属通知，再调用Task的child cancel。任务可能正在timer的select；Done变为可读后pause返回Canceled，Retry停止，handler仍1次。owner接收Result之后还调用StopAndJoin；最终Task.Done已关闭、owned_tasks=0。

独立 `TestResultIsNotDone` 更精确地制造了真实HTTP返回之后的清理窗口：

| 顺序 | 参与者/真正等待点 | 有机制保证的事实 |
| --- | --- | --- |
| 1 | worker真实GET `/healthy` → Do/ReadAll/Close | 网络结果成功，向容量1的Result发送结果 |
| 2 | worker关闭cleanup.Entered，等cleanup.Release | result已可读，附加清理尚未完成，Task.Done未关 |
| 3 | 测试owner消费Result，cancel工作ctx | 工作取消了，但清理有独立有限预算且gate未开，owned_tasks=1 |
| 4 | 新的join owner任务调用StopAndJoin(nil) | JoinWaiting只表示它到达等待；两个Task.Done都未关闭 |
| 5 | 控制者Open cleanup gate | worker得到合成cleanup error，再减计数/cancel/close Done |
| 6 | join owner接收worker.Done，返回errors.Join | 保留主错误与清理错误，测试再join owner.Done，owned_tasks=0 |

这里没有sleep猜调度、NumGoroutine猜泄漏或“调用cancel即完成”的断言。早退测试在真实slow.Entered之后直接执行收尾，不先读取Result，仍确认Task.Done和handler.Done。`t.Cleanup`后注册先运行，Task清理先于fixture关闭；任何`t.Fatal`也执行相同cancel/unblock/独立join路径。清理预算耗尽会返回明确error，不会打印已完成。

Go `defer` 在当前函数返回或panic展开本goroutine调用栈时执行，LIFO；`startTask`最后才close Done，因此收到Done证明业务函数和附加清理/计数归还/child cancel都走完。Result发送较早，二者不能互换。error是显式返回；panic不是可重试error。本课没有把任意panic转成503；库Execute会记录失败后重新panic，在它当前goroutine内继续展开，入口recover不能跨goroutine接住它。参见 [gobreaker固定版本API](https://pkg.go.dev/github.com/sony/gobreaker/v2@v2.4.0)。

## 扩展一：三层重试怎样放大真实请求

运行 `-scenario amplify`。应用层两次逻辑调用，每次SDK最多3次 `/gateway`；每个gateway的handler实际再次HTTP调用 `/always503` 两次。服务端和客户端不是直接相乘后打印一个数字，而是读取路径handler的独立计数。

```text
application (2)
  -> SDK Retry /gateway (3 per application)
     --真实HTTP--> gateway handler
       -> server Retry /always503 (2 per gateway)
          --真实HTTP--> leaf handler 503
```

本次 `fixed=false app=2 sdk=3 server=2 gateway=6 leaf=12`。修复让一个层级负责重试：application2、SDK1、server1，仍使用相同gateway网络链，得到gateway2、leaf2。因为这个合成依赖每次都503且预算够用，观察到乘法上限；取消、成功、预算不足或breaker拒绝都可能更早停止，不能说所有请求永远恰好十二次。jitter改变重发时间，不能消除层数乘法。

Kitex只作迁移阅读，没有冒充本课RPC实现。实际查得并下载核对的当前版本是 **v0.16.4**，官方 [retry入口](https://www.cloudwego.io/docs/kitex/tutorials/service-governance/retry/) 与固定版本 [`failure.go`](https://github.com/cloudwego/kitex/blob/v0.16.4/pkg/retry/failure.go)、[`failure_retryer.go`](https://github.com/cloudwego/kitex/blob/v0.16.4/pkg/retry/failure_retryer.go)、[`util.go`](https://github.com/cloudwego/kitex/blob/v0.16.4/pkg/retry/util.go) 显示：`MaxRetryTimes`不含首次；Do循环`i<=retryTimes`；MaxDuration检查是否允许后续重试，不能仅靠名字推断它一定打断当前RPC；DDLStop需要注册回调；chainStop依赖远端重试标记。阅读公司框架应把这些契约与当前调用链/ctx实际传播对应，本课MaxAttempts与它并非同名同义。Kitex未加入本课go.mod、未运行RPC/backup request。

## 扩展二：closed → open → half-open 的真实探针

运行 `-scenario breaker`。锁定的gobreaker v2.4.0入口链为 `NewCircuitBreaker[Reply](Settings)` → `Execute` → `beforeRequest/currentState` → 业务闭包 → `afterRequest/onFailure/onSuccess`。本课固定窗口Interval=1s、连续2次依赖故障打开、Timeout=100ms、半开MaxRequests=1。固定窗口清计数与半开失败再open由独立测试实际运行；BucketPeriod的滚动窗口可在阅读库时延后展开。

真实两次503之后第三次被ErrOpenState拒绝，handler503仍2次。明确库存拒绝与主动cancel通过IsExcluded排除，不算成依赖可用性失败。open并不是库启动timer goroutine自动恢复；状态查询/下一次准入经currentState观察真实time.Now后进入half-open。owner在已确认open之后等待超过100ms，这是状态预算实验，不是调度猜测。

第一半开probe真实进入 `/probe` handler等待Release；owner读Entered并检查half-open。第二probe得到ErrTooManyRequests，server probe仍1次。放行第一个后真实200/OK，库恢复closed，再次真实 `/healthy` 成功。本课用probe未完成的gate稳定制造并发窗口。

`OnStateChange`在库自己的锁中被调用，不能在回调里再调用同一breaker.State/Counts形成重入死锁；我们只记录日志/关通知channel。库计数针对Execute闭包，不自动识别HTTP状态，必须把503转换成error。breaker套在单次attempt还是整轮Retry外面，会改变失败统计与半开放大；本课throughBreaker只包一次网络调用，保持可解释的计数。

fallback返回 `INVENTORY_UNAVAILABLE` 和原error，订单ID零值明确未确认，不能伪造库存充足、支付已成功或把未知写当成功。允许用过期只读数据时也应明确陈旧度/业务边界；本课没有实际缓存，所以不假造缓存回执。

## 扩展三：慢依赖不能吃光整个服务的等待容量

运行 `-scenario isolation`。故障依赖自己的 `Bulkhead(active=1,queue=1)` 与 `badClient(MaxConnsPerHost=1)`，健康路径另用client。初次slow真实entered后占槽，第二任务关queued通知，第三被ErrQueueFull拒绝；拒绝时server slow仍1次。

slow.Release仍关着且第一Task.Done未关闭，健康 `/healthy` 已完成200/OK/handler1。这是具体健康业务路径的资源隔离证据，不只是一条跟踪指标或另一个虚构返回值。然后取消第二排队任务：从ctx.Done返回并归还队列人数，不发HTTP。取消第一请求：网络I/O返回，handler退出，owner各自join，最终active=0/queue=0/body=0。

这证明当前两个依赖路径的槽、排队和连接资源隔离；它不证明CPU、内存、进程、数据库池或同一个共享Redis被隔离。生产要按真实热点选择隔离维度、等待上限、总预算和拒绝策略，避免一个全局队列把健康依赖也堵住。

运行 `-scenario limits` 看速率区别：前一个HTTP已完成仍在100ms开始间隔内，第二次入口拒绝，无第二handler；窗口到期后第三次真实成功，handler总2次。这个透明SpacingLimiter是本机容量1的最小模型，不是Redis分布式限流。数据库锁由持锁事务/连接管理；breaker只能减少新调用，不能解锁已运行事务。应沿真实事务owner检查Commit/Rollback、lock/statement timeout、访问顺序和索引，本课没有宣称进行了实库锁验收。

## goroutine、资源、停止和完成的owner表

| 对象 | owner / 使用者 | 当前真正阻塞点 | 所有退出路径的收尾 |
| --- | --- | --- | --- |
| main/组ctx | main / Run | 同步调用各实验 | Run返回后cancel；组结束cancel；不依赖cancel当完成信号 |
| Task worker | startTask caller | Retry的HTTP/退避、acquire、可选附加cleanup gate | 立即登记defer或t.Cleanup；cancel、Open所需gate、独立3s等Done，errors.Join |
| join owner测试任务 | TestResultIsNotDone | StopAndJoin等worker.Done | cleanup.Open，再cancel/join owner自己的Done |
| Server.Serve | Fixture | TCP listener accept | Fixture.cancel/Open → Shutdown/必要Close → 独立等Serve Done |
| net/http handler | Server/Fixture | body网络读取、slow/probe Release、gateway下游HTTP | r.Context/fixture.stop解除等待；Shutdown等handler，active与conns归零 |
| Transport I/O | Client/Transport | dial、网络读写、池等待 | request ctx取消；oneAttempt Close响应body；任务join；owner CloseIdleConnections |
| hijacked TCP | lost-order handler | 当前没有额外等待 | commit后Close；错误并入fixture.sideErr；不再由Server.Shutdown管理 |
| response Body | oneAttempt/unsafeBodyAttempt | 有界读取真实body | 拿到response即defer Close，主/清理error聚合，owner计数0 |
| ledger/map/slice | Fixture.Ledger | Mutex临界区 | 只保护进程内原子副作用；不用ctx自动解锁/回滚 |
| slots/waiters | 依赖Bulkhead | channel发送 / ctx.Done | 获槽后defer归还；排队退出defer减人数；不用close slots驱赶sender |
| timer/cancel | pause/Retry/oneAttempt/join | timer.C / ctx.Done | 由创建者Stop/cancel；清理ctx从Background派生且有限 |

Go的goroutine不是固定Java线程，ctx不是ThreadLocal或Thread.stop；可以用显式cancellation token + deadline理解通知，但必须查函数在哪个await等价的阻塞点观察它。Result类似结果交付，但没有Future在所有路径自动管理收尾的保证。channel close是广播“事实已发生”，不是向每个goroutine发一条消息。select多个分支同时ready可任意选择；关键分支额外检查ctx只是覆盖已知取消窗口，不能让ctx检查与远端commit原子化。

## 验证范围、版本来源与复跑race

`go.mod`锁定Go1.27.0与gobreaker v2.4.0，无toolchain指令，无跨模块import、replace或共享go.work。已读M13/M17已提交的STATUS、README和预算/HTTP/owned request核心源码，采用文档约定复用其有效前置证据。第三方测试依赖出现在go.sum是go mod tidy校验依赖测试图的结果，本课运行依赖仍只有gobreaker。

本次官方查证入口为 [context Go1.27.0](https://pkg.go.dev/context@go1.27.0)、[net/http](https://pkg.go.dev/net/http)、[gobreaker v2.4.0](https://pkg.go.dev/github.com/sony/gobreaker/v2@v2.4.0)、上述RFC与Kitex固定源码。Go1.27固定net/http网页当次工具返回Internal Error，随后打开官方当前文档并以本机Go1.27源码/go doc核对版本语义；不能把网页失败写成验证成功。源码位置和版本记录在STATUS/evidence/environment.txt。

race需要支持windows/amd64的C compiler。当前统一工具链索引由主agent提供，只在本次PowerShell进程临时设置并finally恢复：

```powershell
$m33Compiler = Get-Content -Raw -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' | ConvertFrom-Json
$m33SavedCC, $m33SavedCXX, $m33SavedCGO, $m33SavedPath = $env:CC, $env:CXX, $env:CGO_ENABLED, $env:PATH
try {
    $env:CC = $m33Compiler.cc
    $env:CXX = $m33Compiler.cxx
    $env:CGO_ENABLED = '1'
    $env:PATH = $m33Compiler.bin_dir + ';' + $env:PATH
    go -C lessons/m33_resilience_retries test -race -v -count=1 -timeout 60s ./...
    $m33RaceExit = $LASTEXITCODE
} finally {
    $env:CC, $env:CXX, $env:CGO_ENABLED, $env:PATH = $m33SavedCC, $m33SavedCXX, $m33SavedCGO, $m33SavedPath
}
if ($m33RaceExit -ne 0) { throw "M33 race failed exit=$m33RaceExit" }
```

上述脚本从仓库根执行；如果当前目录已经是本课，去掉 `-C lessons/m33_resilience_retries`。另一台机器使用它已验证的amd64 compiler，私有索引不是module依赖。不修改全局PATH/go env，不重复安装工具链。

13项顶层独立测试覆盖总预算/次数、真实取消早退、Result不是Done且join owner被gate挡住、UNKNOWN查账、四种commit后不可信201响应与原key查询、空body有副作用写、有效POST业务拒绝、八个并发订单原子防重、body原文、Retry-After、固定窗口/半开失败、已取消不派发和业务拒绝不污染breaker统计。完整demo还直接断言三层放大、半开成功/并发probe拒绝、健康隔离、队列/速率拒绝。实际命令与结果见[STATUS.md](STATUS.md)，无秘密日志在[evidence/demo.txt](evidence/demo.txt)、[evidence/test.txt](evidence/test.txt)、[evidence/race.txt](evidence/race.txt)。

本课验收只包含当前Go1.27 Windows进程、真实loopback HTTP/1.1、当前内存账本及自有任务/listener/连接/body清理。没有公网故障、HTTP/2、生产压力、持久数据库事务、跨实例/重启幂等、Redis限流或Kitex RPC实跑；不把这些范围写成已完成。
