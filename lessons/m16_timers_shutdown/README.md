# M16：触发源、后台订单与有界停机

这课把三个容易混在一起的事实拆开：**Timer/Ticker 发出触发，Ticket 发布一项结果，服务 Done 证明归属资源已经收尾**。停止定时器不会关闭它的 C，也不会等待已启动的工作；取消 context 只发通知，owner 还要明确 join。我们的业务是一个有唯一 worker、一个队列位的后台订单服务：A=12500 分在真实 HTTP handler 内等待，B=1800 分已排队，停机时应先停止接纳，再把已接纳 A/B 排空，最后才能关闭共享客户端。

本课以本机 Go1.27.0 为准。M15 已在前层交付，实际核对其 STATUS 与公开 `Pool/Ticket/Fetch/Row` 接口，通过本课 go.mod 的 `replace ../m15_structured_concurrency` 直接复用。这里没有复制 M15 的 worker/队列，也不导入其他课的 internal。`Row.Price` 是前课的历史字段名，本课将这个整数分槽位用作订单金额，不把它误认成另一个 SKU 的价格。第三方 `golang.org/x/sync v0.24.0` 是 M15 的间接依赖，已由本课 tidy 锁定 go.sum。

## 先跑一轮，再沿主线读 10～20 分钟

在仓库根目录执行，nested module 不会被根 `go test ./...` 包含：

```powershell
go -C lessons/m16_timers_shutdown run ./cmd/demo
go -C lessons/m16_timers_shutdown test -v -count=1 -timeout=60s ./...
go -C lessons/m16_timers_shutdown vet ./...
```

正常入口有 45 秒总上限，真实请求有 3 秒客户端上限；每个实验的救援/join 都用独立 2 秒清理预算。core 实际执行很短，10～20 分钟说的是读懂、对照、讲清的主线，不是故意把线程睡十分钟。这里没有额外 exercise；错误与修复已经实现，直接运行相应测试即可核验。

| 阅读时间 | 现在必须理解 | 直接定位 |
| --- | --- | --- |
| 0～4 分钟 | timer 是触发，不拥有业务任务；Stop/Reset bool 描述旧状态 | [timers.go](timers.go) `TimerProbe`；输出 E01 |
| 4～7 分钟 | ticker Stop 后接收仍会等待；工作要 select ctx 并 join | `StopOnlyProbe → CancelTickerProbe`；E02 |
| 7～15 分钟 | A 在途/B 排队/C 拒绝，如何 drain 和关闭客户端 | [scenarios.go](scenarios.go) `CoreDrain` → [lifecycle.go](lifecycle.go) `Service.shutdown` → [HTTP adapter](http.go)；E04 |
| 15～20 分钟 | 超时当时 unknown 与最终结果不同，清理预算不能继承已取消 ctx | `ServiceProbe(timeout/canceled-budget)`、`HTTPAbortProbe`；E05/E07 |

E03 的三种策略和 E06 父取消可以在主线后按表逐步展开。现在先知道谁通知、谁等待、谁拥有资源即可。runtime timer 堆、netpoll 实现、signal 的平台细节、跨进程的持久任务恢复、数据库事务/outbox 在读相关生产代码时再深入；本课不给内存账本添加虚假的持久化承诺。

## 当前规则与旧材料纠正

核验了锁定版 [Go1.27 time](https://pkg.go.dev/time@go1.27.0)、[Go1.27 发行说明](https://go.dev/doc/go1.27)、[GODEBUG 的 Go1.27 历史](https://go.dev/doc/godebug#go-127)，以及本机 `D:/Go/src/time/sleep.go`、`tick.go`、`internal/godebugs/table.go`。网页默认版本有可能比本机更新；本课的实现解释依照本机源码，公共契约依照锁定版文档。

| API | 必须按当前代码理解的含义 |
| --- | --- |
| `NewTimer(d)` | 至少 d 后让一次接收获得时间值，不自动运行你的业务函数；d≤0 可立即就绪 |
| `Timer.Stop() bool` | true 是本次停止了一轮尚可停止的触发；false 也包含“此前已停止”。不是“false 就一定有值该 drain” |
| channel timer `Reset(d) bool` | 从现在重新计时。bool 表示重置前是否 active；false 不表示新计时失败。当前 Stop/Reset 返回后不会读到旧设置的 stale 值 |
| `AfterFunc(d,f)` | 在它自己的 goroutine 调用 f；返回的 Timer.C 为 nil，接收 nil channel 会一直阻塞 |
| callback timer `Stop/Reset` | 已开始 f 后 Stop=false，Stop 不等待 f。Reset=false 会再次安排 f，不等上一轮，也可能重叠；必须自己设 started/done 和 join |
| `NewTicker(d)` | d 必须正；周期源会调整/丢 tick 以适应慢接收者，它不保存每个业务任务 |
| `Ticker.Stop/Reset` | 没有 bool 返回。Stop 不关闭 C，不取消当前业务；Reset 要求正周期并重建周期 |

语言规则保证 channel/锁的同步关系、defer 的作用域和 goroutine 中 recover 的边界。上述 timer 行为是标准库契约。runtime 当前如何实现同步 timer channel、lazy delivery，以及具体 tick 时间戳间隔属于当前实现/本次观察；不要拿真实机器的 40ms 日志当实时调度保证。

`files/语法进阶/定时器` 的 `Timer { C; r runtimeTimer }` 是旧实现结构，不能当当前私有字段契约。材料把 AfterFunc 说成在当前 goroutine 等待后执行也不准确：它直接返回，回调在另一 goroutine。原 time.After 示例的无缓冲 ch 在等待者超时离开后可能把发送者留在 `ch <- "test"`，等待超时也不取消那项任务。原 Watch 的 send 再 close 不提供 join；若接收方已退出，send 本身可能阻塞，若有多个关闭者还会 panic。

Go≤1.22 的 channel timer 需要结合唯一接收者/调用者条件看 stop-drain-reset。Go1.23～1.26 的旧兼容行为还受主 module 的 go 指令与 asynctimerchan 设置影响。**Go1.27 已永久移除 asynctimerchan=1/2 的旧行为**，本机 cap(timer.C)=cap(ticker.C)=0；不能复制旧 drain 代码宣称每个 Go 版本都必须如此。老写法 `if !t.Stop() { <-t.C }` 在一个已经 Stop 的当前 timer 上第二次 Stop=false，随后接收却没有值，反而会卡住。Go1.27 channel timer 直接 Reset，bool 照实解释即可，多个竞争接收者仍要另建应用所有权协议。

隔离反例有自己的 go.mod，正常 `test ./...` 不遍历进去：

```powershell
go -C lessons/m16_timers_shutdown/labs/removed_switch run .
```

真实失败为 `go.mod:5: removed GODEBUG "asynctimerchan" set to old value "1"`，完整诊断见 [removed-switch.txt](evidence/removed-switch.txt)。这证明显式旧值在本机被拒绝，不是测试成功。环境变量里未识别的设置可能被忽略，不能因设置后没报错就声称旧 timer 行为恢复了。公司旧代码应按它的实际 toolchain/module/设置解释，本课没有代它升级。

## 完整调用链与每个归属者

入口是 [cmd/demo/main.go](cmd/demo/main.go) `main → runMain → run`。`runMain` 创建观察预算，返回前执行 cancel；最外层才 `os.Exit(code)`，避免让它跳过登记在 runMain 的 defer。

```text
run → CoreDrain
  → NewOrderBackend → net.Listen(127.0.0.1:0) → Server.Serve
  → NewOrderClient → 一个专属 http.Transport
  → NewService → M15.NewPool → 固定 worker + pool joiner
      → producerWG.Go → NewTicker → select(producerCtx.Done / ticker.C)
  → Service.Submit(A/B) → mu 内 M15.TrySubmit → 成功 Ticket 登记
      → M15.worker → invoke → OrderClient.Fetch → Client.Do
          → 真 TCP → OrderBackend.handle → A门 → 锁内登记 committed → JSON响应
      → Body读完/关闭 → active归还 → M15写Row/close(Ticket.done)
  → signal.NotifyContext.stop → lifecycle通知
  → Service.Shutdown → 唯一shutdown coordinator
      → producerStop → producerDone + producerWG.Wait
      → M15.Close → worker range排空 → M15.Wait + 内部joiner.Wait
      → Ticket.Result收集终态 → OrderClient.Close → close(Service.done)
      → 写report → close(finished) → shutdownWG.Wait → 返回
  → CoreDrain.defer → OrderBackend.Close
      → Release → Server.Close → Serve.Wait → connections.Wait → handlers.Wait
```

| 对象/参与者 | owner、具体等待点、完成证明 |
| --- | --- |
| `lifeCtx` | CoreDrain 创建 NotifyContext；stop/系统信号/父取消能关闭 Done。它只是停止通知，工作 budget 没挂在它下面 |
| `workCtx / Pool.ctx` | Service 与 M15 创建；任务借用。Abort/父取消通知正在运行函数；队列关闭与 worker join 是别的操作 |
| 周期生产者 | Service 拥有；select 等 tick 或 producerCtx.Done；producerDone 后再 producerWG.Wait；返回时才 defer ticker.Stop |
| M15 worker | Pool 拥有；空队列等 `range jobs`，有任务就同步调用 Fetch；只关闭自己的 Ticket.done。Close 排空，Abort 给未开始项发布取消终态 |
| `Ticket.done / Row` | 领取该项的 worker 唯一发布；Result 的 ctx 只限制 caller 等多久，取消等待不撤回接纳；它不等全部 worker |
| 服务 coordinator | 第一次 Shutdown 唯一创建；使用第一次 drainCtx，之后调用复用结果。finished 表示 coordinator 返回；done 只表示依赖全部释放 |
| HTTP handler | backend/net/http 归属；A 等 release 或请求取消。真实账本写入与 HTTP 结果接收是两个时间点 |
| client/Body/Transport | client 由 Service owner 持有，worker 借用；Fetch 的 defer 先关 Body 再归还 active；所有 worker Wait 后才能 client.Close |
| backend fixture | CoreDrain/HTTPAbortProbe 拥有；Serve 已 join 后不再登记 StateNew，连接终态后不会再启动 handler，最后才 handlers.Wait |

goroutine 不是简单的 Java 线程替身；服务 owner 创建它后，函数返回不会替它等待。context 也不是 `Thread.interrupt` 或强制 Future.cancel；它通知协作代码，到底在哪个 select/I/O 上观察、何时返回、什么副作用已发生，都要沿这条调用链确认。Wait 类似 join 的责任，但一个 Ticket 类似单项结果，不等价于整个 Executor 已终止。

## E01：普通 timer 与两个真实 AfterFunc 回调

`TimerProbe` 先建立 hour timer，Stop=true；再 Stop=false；Reset(0)=false 但能收到新触发。已消费一轮后 Reset(hour)=false，再对 active timer Reset(hour)=true。这组结果说明 bool 讲旧状态，不能当成功码。

随后建立两个 callback。第一轮 close(started[0]) 后停在 release；owner 在收到 started 后 Stop=false，又 Reset(0)=false。第二轮 close(started[1]) 时第一轮还没放门，所以**这两轮确实同时在途**。owner 放门后 callbacks.Wait，最后再 join 发通知者。日志 `callbacks=2 Stop不join=true` 来自实际屏障，主函数中的一个 recover 不能捕获回调 goroutine 的 panic。

最后真实 ticker 周期5ms，owner用一个40ms timer故意延迟读取。日志本次收到的两个 tick 时间戳跨度约40ms，不是累积八条可靠任务。`TestRealTickerDropsTicks` 用 Go1.27 官方 synctest 的虚拟时钟推进真实 `time.NewTicker`：暂停100ms、周期10ms，两次时间戳间隔100ms。测试没有伪造 tick，也不声称证明 Windows 实时时钟精度。

`time.After` 做一次有限 select 等待并不错误；Go1.23 后 GC 能回收未引用 timer，不能继续宣称“用了 After 就永久泄漏”。但在高频循环里每轮创建 timer有创建成本，持续有其他输入时还可能每轮重新开始“闲置超时”；若要一个整轮预算用 context/deadline，若要重设闲置期限就明确复用 timer/Reset。本课没有跑性能基准，不宣称一定更快。`select { default: }` 反复循环则可能忙转，我们只用一次立即状态检查，以及有界 queue 的 TrySubmit，随后回到阻塞 select，不做无等待的轮询循环。

## E02：Stop-only worker 的故障与修复

坏代码实际在 `StopOnlyProbe`：worker 只做 `<-ticker.C`，owner Stop 后 C 没关闭，唯一 return 还要求它先读一个 tick，因此没有退出路径。立即检查 done 未关闭是对协议不可能退出的观察，不是靠 Sleep 猜它正在哪个 runtime 栈上。

故障实验的救援是 owner 把 atomic rescue=true，再 Reset 注入最后一个 tick，worker 这才返回，owner 真实 Wait。**Reset 救援属于 fixture，不能包装成 Stop 提供了取消**；生产里不应让工作依赖下一个 tick 才读取消。

修复 `CancelTickerProbe` 的 loop 同时 select `workCtx.Done()` 和 `ticker.C`；owner cancel 后再接收 done/Wait，worker 返回时 defer Stop。这解释了最常见的 `for range ticker.C` 错误：Ticker.Stop 不会把 range 结束，必须有独立停止协议。取消与 tick 同时就绪时 select 没有 ctx 优先级，已开始那轮也需要把 ctx 继续传给其 I/O；生产系统要依靠入口接纳边界限制停机后新增任务，不能赌分支顺序。

## E03：允许重叠、同步串行、在途跳过

`RunSchedule` 接收触发，最多八次、并发最多两轮。工作通过 safeRound 在自身调用栈 recover，error写到有界结果通道，owner最终汇总。`PolicyProbe` 用两次辅助触发和 started/release 门精确控制在途状态，这个输入 channel能暂存测试输入，**不是**实际 ticker 丢 tick 的证据；真实 tick 证据是 E01/synctest。

| 策略 | 两个触发与工作 gate 的时间线 | 实际 E03 |
| --- | --- | --- |
| overlap | 第一轮在途时 loop 读第二次触发，再启动第二轮；最多两轮同时存在 | received=2 / started=2 / skipped=0 |
| serial | 第一轮同步堵住 loop；放门后第一轮返回，loop 才处理第二次；真实 ticker 期间会调整/丢 tick | 2 / 2 / 0（辅助源保存了第二次输入） |
| skip | 第一轮在途，loop 仍能读第二触发，active=1故直接记录skip | 2 / 1 / 1 |

`TestOverlapHasBoundedCapacity` 把八次触发送到 loop，并把前两轮冻结；剩余六次必须 skip，证明不会每 tick 无界 go。`TestScheduledPanic` 第一轮 panic/第二轮成功，owner得到恰好一个失败，不会“后台任务消失却全部成功”。本课采用逐轮隔离策略，真正的不可恢复故障可改为取消整组并交给服务 owner，但也要 join。`TestRealPeriodicProducer` 则让 Service 的真实1ms ticker至少进入一轮扫描，收到 started 才 stop/放门/join，最终全部接纳的 periodic Ticket都有终态；core 设一小时周期是为了保持 A/B 业务数据稳定，并非假装测试周期工作。

## E04：核心正常 drain 的完整时间线

真实 backend 仅接收 A/B 的 POST `/orders?id=...`，在内存账本按 ID 幂等登记金额并回 JSON。adapter 不仅检查200，还限制4096字节、校验单个 JSON/ID/正金额；三个200坏响应独立测试会拒绝。Body关闭、worker退出、连接关闭分别核对。

| 步骤 | 参与者 / 当前函数与等待点 | 状态变化与下一步证明 |
| --- | --- | --- |
| 1 | owner → NewService，等 Ready | 生产者已创建真实 ticker；一位 M15 worker等jobs |
| 2 | owner → Submit(A)，worker → Client.Fetch → handler | A进入handler后close(entered)，尚未登记金额；owner等Entered才继续 |
| 3 | owner → Submit(B)、Submit(C) | B占唯一队列位，A占唯一worker；C返回ErrQueueFull，没有Ticket/所有权转移 |
| 4 | owner取消 Result 的观察ctx | A Result等待报Canceled，但A仍在service归属；client.Close被active=1保护拒绝 |
| 5 | owner调用NotifyContext.stop | lifeCtx.Done关闭，只是生命周期通知；不是取消A的workCtx |
| 6 | caller → Shutdown，owner等 EntranceStopped | 同步锁内 accepting=false，D返回ErrClosed；随后通知生产者退出并真正Wait |
| 7 | coordinator → Pool.Close → Pool.Wait | jobs关闭但保留B；worker仍在A的HTTP请求；client仍保持可用 |
| 8 | owner → backend.Release | A登记12500并响应；Body读完关闭，Ticket A发布；同一worker再处理B1800并发布 |
| 9 | worker range终止 → M15.Wait完成 | worker及M15的joiner都已退出。coordinator收集Ticket，再client.Close，close(Service.done) |
| 10 | caller join → fixture Close | Server.Close/Serve join，已接纳连接终态后handler join；最终账本A/B=14300，accepted/closed=2/2，activeHandlers=0 |

实际日志顺序是 `入口 stopped → 生产者 cancel 通知 → 生产者 joined → 队列 closed，选择 drain → worker joined → worker之后client closed`。关闭入口的线性化点是 accepting=false受mu保护；持锁期间的TrySubmit立即完成，不能持这把锁等满队列。与停止竞争的接纳按获得锁的先后决定，成功返回Ticket才算已接纳；不能只看ctx当前Canceled就凭空判断归属。

本课选择在途订单 drain：已接纳的每项必须有终态，正常停机不能先关闭依赖客户端。如果使用真实DB，也是等仍会使用DB的worker之后再DB.Close；一个 HTTP server.Shutdown 只等 HTTP 请求连接，不会自动等你从 handler 派出去的后台goroutine。因此生产的完整顺序通常是业务入口停止接纳 → 关闭监听/请求收尾 → 生产者停止 → 已接纳任务drain或明确放弃 → worker join → 底层DB/客户端关闭。HTTP的 [Shutdown 公共契约](https://pkg.go.dev/net/http@go1.27.0#Server.Shutdown) 也不覆盖 hijack/WebSocket，外借资源要有独立归属。

## E05/E06：正常、错误、取消和清理各自返回什么

`ServiceProbe` 使用同样金额的内存函数辅助确定错误顺序，不能冒充真实协议。每条路径有源码、直接默认入口和 TestShutdownPaths 独立断言。

| 输入 | 完整结果解释 |
| --- | --- |
| idle | 没接纳订单；生产者join、关闭空jobs、worker从range退出、client关闭，0个结果 |
| running / full | A在途B已排队C被拒绝；停止入口后释放门，A/B合计14300，未丢Ticket。full强调有界队列拒绝，不表示持锁阻塞提交 |
| timeout | A故意不看ctx而等待门，B排队；25ms drain预算结束时unknown=[A B]、active=1、queued=1。先Abort/取消/放门，再Background新2秒预算Wait，最终两个终态、所有资源Done，但report.Err仍是DeadlineExceeded |
| canceled-budget | 调用时预算已经Canceled；正常等待生产者/worker失败，仍先放门再独立清理。最终Done可关闭，Err保留Canceled，绝不能从已取消ctx派生新的“2秒”清理ctx |
| panic | 业务函数panic由M15.invoke当前worker调用栈恢复；两个Ticket都是PanicError。failFast=false所以服务收尾可成功，shutdownErr=nil不代表两项业务成功，必须看FinalRows.Err |
| repeated / concurrent | 后续调用等待同一个coordinator、复用同一份结果；16个并发caller独立测试中client.Close只调用一次 |

timeout 首轮断言曾误要求运行中的 A 也返回DeadlineExceeded，实际发现 **A的业务函数返回workCtx.Err()=Canceled，未开始B由M15读取context.Cause()=drain的DeadlineExceeded**。已据实际契约修正断言：取消类型与触发原因不同，report.Err的超时主因并未丢失。不能通过把每个error字符串都写成“超时”解决这个语义差别。

E06 `ParentCancelProbe` 进一步核验M15：A观察父取消并发布Canceled Ticket，Ticket.Result已经返回；pool.Joined此刻仍开着，因为空且未关闭jobs的worker会接着等待。owner.Close后range结束，再Wait才join。父ctx、任务Result、最终Done分别拥有各自的通知/完成含义，不能拿其中一个替换另两个。

`ShutdownReport.Err` 表示正常停机/资源清理错误，`FinalRows[i].Err` 表示单项任务业务结果。errors.Join保留多条原因，调用者用errors.Is/As检查。`TestCleanupErrorsArePreserved` 让正常budget先取消、client.Close再报明确错误：worker/producer仍真实join，但ClientClosed=false、Done不关闭，两条错误都保留。该Close是无实际socket的辅助故障函数，没有遗留网络资源。

`TestIndependentJoinError` 使用未关闭channel且**没有创建goroutine**，真实等完独立2秒清理上限：stop已先调用、真正wait未调用、error同时保留原业务错与DeadlineExceeded。它证明清理失败会被报告，不为了展示超时而留下不可回收线程。本课工作门都能被owner解除；生产里若依赖永不返回且不可取消，Go没有安全强杀goroutine的API，你只能报告未joined/保留资源并交给进程监督策略，不能承诺有ctx就总能优雅退出。

## E07：真实 HTTP 超预算，取消不是撤销

`HTTPAbortProbe` 和E04使用同样真实端点/账本，但A在handler门前一直等到25ms drain超时。超时瞬间A/B没有Ticket终态，报告unknown=[A B]；这说的是当时的任务结果，不是断言远端没有副作用。

fallback先Pool.Abort/通知客户端取消，再放远端门，客户端取消传播和handler提交并不原子。A的最终Ticket可能报告取消/网络错误，远端账本也可能已登记A；因此owner最后先真正join HTTP连接/handler，再查询快照。独立测试允许两种合法事实：账本无A，或A=12500最多一次。B一直未调用HTTP，必须不存在B账本，Ticket保留DeadlineExceeded取消原因。A若成功响应，则金额必须等于账本12500；A若报错，不能断言账本无A。

默认输出明确分成 `unknownAtDeadline`、`finalTicket成功合计`、`最终查询ledger`。本次具体日志见 [demo.txt](evidence/demo.txt) 与 [demo-race.txt](evidence/demo-race.txt)；不同运行取消传播先后可变化，README不把一个观察固定成语言保证。这里可查到的是当前实验进程的内存事实，实际订单系统还需要持久幂等键、可查状态、重试/恢复策略。

## 退出与资源责任的边界

[NotifyContext](https://pkg.go.dev/os/signal@go1.27.0#NotifyContext) 的stop既注销信号资源也关闭它的ctx；只有协调代码继续运行，才有机会执行Shutdown。本课用stop可控模拟，不声称实测了Windows console信号或Linux SIGTERM。调用stop恢复默认行为后再次收到某些信号可能直接结束进程，这也不能代替join。

defer是当前函数正常返回或可展开panic时的收尾，外部kill/进程崩溃不会保证它执行；[os.Exit](https://pkg.go.dev/os@go1.27.0#Exit) 也直接结束且不运行defer。即使defer执行了，只有Stop/cancel仍不代表任务已加入。不能用“退出后操作系统回收连接”证明本次订单drain或DB提交安全。本课没有向真实进程发送kill；错误worker靠救援门加入，所有正常实验资源靠明确协议释放。

完整日志与命令/版本/失败修复/未验范围见 [STATUS.md](STATUS.md)。全部源码与辅助测试都在本module，Go代码逐段解释caller、等待对象、错误返回与资源归属；最终主agent验收不由本课SELF VERIFIED字样代替。
