# M12：channel/select 的所有权、背压和结束协议

这课围绕六个合成订单建立一个可预测的有界校验流程：来源产生订单，三个 worker 校验金额，消费者收集结果。`O-03` 的金额确实为 `0`，应当形成业务拒绝；零值不代表通道结束。正常批次收完六单，来源失败时收完已经交付的前缀，消费者只需要两单时停止并等待所有参与者清理。这里“业务完成”指结果被消费者加入 `Report.Results`，没有数据库或网络副作用。

本课是独立 module `go-demo/lessons/m12_channels_select`，`go.mod` 为 `go 1.27.0`，只用标准库。实测工具链为 `go1.27.0 windows/amd64`。`go mod tidy` 删除了与 `go` 指令相同的冗余 `toolchain go1.27.0`，未升级根项目或其他课程。没有硬前置；推荐先读 M11 的 goroutine owner，但下面包含所需的最小时间线。

现在必须理解：nil/打开/关闭的通信条件，两值 receive，唯一关闭者，发送被阻塞时的取消出口，通知与 join 的差别，值复制与指针别名，select 没有业务优先级。泛型 `send[T any]` 的完整类型推导、runtime 的 channel 等待队列布局、调度器的实现细节可以以后再查。本课注释解释泛型在当前调用里的作用，不要求先学习全部泛型特性。

## 运行顺序与入口

从最终汇总后的仓库根目录 `D:/IdeaProject/go-demo` 运行：

```powershell
go -C lessons/m12_channels_select run ./cmd/demo -experiment pipeline
go -C lessons/m12_channels_select run ./cmd/demo
go -C lessons/m12_channels_select test -v ./...
go -C lessons/m12_channels_select vet ./...
```

从本模块目录运行时，将 `-C lessons/m12_channels_select` 换成 `-C .`。实施时实际工作目录是 `C:/Users/Lenovo/.codex/worktrees/go-course-m12/go-demo/lessons/m12_channels_select`。根 `go test ./...` 不递归覆盖这些嵌套 module，需执行上面的模块命令。

核心课程约 10～20 分钟：先看下面的状态表和 owner 表，读 `cmd/demo/main.go` 的 `pipelines`，顺着 `Start → feeder/worker → send → Collect → Wait` 阅读，执行 `-experiment pipeline`，再对照正常、提前停止、来源失败和饱和四段时间线。程序执行本身只需很短时间，课程时间包含源码阅读与日志解析。

扩展按以下顺序逐段执行；全部原理、错误解析和修复代码都已交付，不要求另做 exercise：

```powershell
go -C lessons/m12_channels_select run ./cmd/demo -experiment states
go -C lessons/m12_channels_select run ./cmd/demo -experiment select
go -C lessons/m12_channels_select run ./cmd/demo -experiment failures
go -C lessons/m12_channels_select run ./cmd/demo -experiment memory
```

每个正常入口段落使用 10 秒兜底 deadline，清理阶段使用独立 3 秒上限。所有顺序都用 channel 屏障或不能通信的稳定状态证明，没有 `time.Sleep` 猜调度。`select` 故障循环固定 100,000 次，双 ready 观察固定 4,096 次；订单最多 128，worker 最多 8，业务缓冲最多 16。

| 文件 | 调用与阅读职责 |
| --- | --- |
| [cmd/demo/main.go](cmd/demo/main.go) | `main` 选段落、建立 deadline；`pipelines` 用同一组订单组织正常/空/停止/错误/饱和 |
| [internal/course/pipeline.go](internal/course/pipeline.go) | `Order/Result/Completion/Report` 定义协议，`Start` 创建参与者，`feeder/worker/send/Collect/Wait` 管理完整生命周期 |
| [internal/course/trace.go](internal/course/trace.go) | `Trace` 序号化日志；`await/cleanupAwait` 有界等待屏障 |
| [internal/course/states.go](internal/course/states.go) | 状态表、零值排空、无缓冲会合、来源返回但 range 不结束的错误与修复 |
| [internal/course/select.go](internal/course/select.go) | default 忙循环、closed 忙循环、nil 禁用、多个 ready 和取消非优先 |
| [internal/course/failures.go](internal/course/failures.go) | `SampleOrders`、稳定满队列、消费者退出后裸发送、接收者误关的真实反例 |
| [internal/course/memory.go](internal/course/memory.go) | `MemoryOwnership → memoryOwnership` 演示值副本、指针回传、close 发布，以及取消后的独立发布清理；私有hooks仅用于断言屏障 |
| [cmd/fault/main.go](cmd/fault/main.go) | 显式 `pointer-race`；真实竞争只在独立进程入口发生 |
| [internal/course/course_test.go](internal/course/course_test.go) | 用订单集合、金额、错误链、终态和关闭后接收验证独立事实，避免固定 worker 顺序 |
| [evidence/demo.txt](evidence/demo.txt)、[evidence/test.txt](evidence/test.txt) | 一次完整真实运行；不同运行的参与者顺序、计数分布、耗时可以变化 |
| [STATUS.md](STATUS.md) | 实际命令、结果、race 工具链及清理边界 |

## 最小 Go 规则：channel 是共享通信对象

`var ch chan Order` 得到 nil 句柄；`ch := make(chan Order)` 创建无缓冲对象；`make(chan Order, 1)` 创建最多容纳一个尚未接收值的缓冲对象。`:=` 是当前词法作用域的短变量声明，容量写在 `make` 内。将 channel 变量传给函数复制的是句柄，各参与者操作同一个通道；方向收窄也不会产生新队列。

`chan<- Order` 允许发送和 `close`，`<-chan Order` 只允许接收，`chan Order` 允许双向通信。方向在编译时限制能力；只发送参数仍可编译 `close`，所以“哪个发送者拥有 close”还需要协议明确。`Pipeline.Results` 对外为 `<-chan Result`，消费者连 `close(p.Results)` 都无法编译；内部 worker 虽然能发送，仍约定不关闭 results。

下表来自 `ChannelStates` 的独立工厂。收/发/close 三列各用一个新 channel，避免第一列取走元素改变第二列的状态。空/满一律假设没有另一个可会合参与者；有并发参与者时即使无缓冲也可以通信。

| 当前状态 | 接收 `<-ch` | 发送 `ch <- 7` | `close(ch)` | 应怎样解析 |
| --- | --- | --- | --- | --- |
| nil | 永久等待 | 永久等待 | panic：`close of nil channel` | 不是“空队列”；在 select 内该 case 不可能 ready |
| 打开的无缓冲，当前无参与者 | 等待发送者 | 等待接收者 | 成功 | 两边必须会合；不是先把值存入隐藏缓冲 |
| 打开的有缓冲、空，cap=1 | 等待值 | 成功入缓冲 | 成功 | send 返回不证明有消费者运行过 |
| 打开的有缓冲、内有真实 `0`，cap=2 | `0,true` | 还有空位，成功 | 成功 | 数据零值仍是实际发送的值 |
| 打开的有缓冲、内有真实 `0`，cap=1 | `0,true` | 等待空位 | 成功 | buffer 是有限待处理量，满时形成背压 |
| 已关闭、缓冲仍有真实 `0` | `0,true` | panic：`send on closed channel` | panic：`close of closed channel` | close 保留已有数据，不能提前把 ok 判为 false |
| 已关闭且排空 | 立刻 `0,false`，以后每次都相同 | panic：`send on closed channel` | 重复关闭 panic | 结束与真实数据需看 ok，closed 接收永久 ready |

`ChannelStates` 用 `select/default` 只观察“当前不能立即进行”，不会真的把 nil send/receive 挂死。单独且稳定的 nil 状态才允许据此说明直接操作会永久等。向 closed 发送的 case 仍能被 select 选中并 panic；增加 ctx case 不会自动保护它。规范依据是 [channel 类型](https://go.dev/ref/spec#Channel_types)、[发送](https://go.dev/ref/spec#Send_statements)、[接收](https://go.dev/ref/spec#Receive_operator) 与 [close](https://go.dev/ref/spec#Close)。

`DrainClosed` 先发 `0,7` 再 close，真实日志依次为：

```text
receive read=1 value=0 ok=true
receive read=2 value=7 ok=true
receive read=3 value=0 ok=false
receive read=4 value=0 ok=false
```

第 1 条是合法零值；第 3 条才是通道结束。对 channel 的 `for value := range ch` 隐含反复 receive，只有关闭且排空才自然退出，生产者函数返回不会把 channel 变成 closed；也可以自行 break，但那时接收者须履行提前停止协议。无需为“释放 channel 资源”把每一个通道都关闭：close 的意义是无后续值的协议声明，引用不可达后存储由 GC 回收；阻塞 goroutine 若仍持有引用就不会因 caller 返回自动消失。

## 核心业务协议：值、错误、结束、提前停止

输入为 `SampleOrders()`：`O-01..O-06`，金额为 `100,200,0,400,500,600` 分。结果是 `Result{Order, Worker, Err}`。`Worker` 仅是观察信息，金额非法产生包装 `ErrInvalidOrder` 的逐单错误，剩余订单仍继续。成功批次应出现六个互不重复的 ID，其中五单合法、一单业务拒绝。

批次来源故障由 `FailAt` 控制：`-1` 关闭故障，`3` 在发送第 4 单之前产生包装 `ErrSource` 的错误，`len(input)` 在全部订单交付之后才报故障。错误不塞进可能满且无人读取的 results；feeder 在 `sourceDone` 留一次 `sourceCompletion`，协调者拿到它再构造 `Completion`。`sourceDone` 容量为 1 是基于“一个 feeder、恰好一次终态”的确定上限；没有扩大业务 buffer 来隐藏阻塞。

结束事实由唯一关闭者的 `close(results)` 表达，但结束不代表成功。消费者排空结果后必须 `Wait` 获取终态：`Completion.SourceErr` 仍可能说明来源失败，`CancelErr` 保留 `Canceled`/`DeadlineExceeded` 类型。`Report.Err` 表示整批结局；`errors.Is` 沿包装/Join 链区分 `ErrEarlyStop`、`ErrSource`、外部取消。若消费者提前停止与来源故障同处一批，`errors.Join` 保留两者，独立测试专门证明这点。

提前停止由 `StopAfter=2` 表达：消费者 append 第 2 个结果后调用 `Cancel`，停止读取 results，使用新的有界清理 ctx 等待终态。这个批次只承诺返回已经消费的子集。输入队列中剩余订单、worker 已校验但未交付的 Result、results 里可能残留的值均不被解释成业务成功；本课不支持断点重放、顺序承诺或持久化。若业务必须保证每单完成，需要另行设计确认、持久队列、幂等与重试。

## 谁拥有谁，具体在哪等待

`main → pipelines → Run → Start → Collect → Pipeline.Wait` 是同步 owner 调用链。`Start` 中的 `go feeder`、`go worker` 和协调者 goroutine 是异步分支；`Start` 返回并不等待它们。Go 没有自动绑定 parent/child goroutine 的语言级生命周期，这里的 owner 是我们用代码明确承担的责任。

| 参与者 | 创建者 / owner | 当前函数与等待点 | 退出条件 | 谁关闭什么 / 谁等待 |
| --- | --- | --- | --- | --- |
| 入口/消费者 | `pipelines` 调 `Run` | `Collect` 的 select 等 `Results` 或外部 ctx；之后 `Wait` 等 `Done` | 排空、StopAfter、外部取消 | consumer 发 `Cancel`，等待终态，不关闭业务 channel |
| feeder | `Start` / Run owner | `feeder → send`；jobs 满或无接收者时等 `jobs <- order` / `ctx.Done` | 输入完成、FailAt、取消 | feeder 唯一关闭 jobs，在 sourceDone 留一次终态 |
| worker-0..N-1 | `Start` / Run owner | `worker` 等 jobs/ctx；`send` 等 results/ctx | jobs 关闭排空、取消发送或接收 | 每个 worker 在 defer 记录次数、`WaitGroup.Done`；都不 close results |
| 结果关闭协调者 | `Start` / Run owner | `WaitGroup.Wait` 等所有 worker，再等 sourceDone | 所有发送者完成且 feeder 终态可取 | 唯一 close(results)，发布一次 Completion，close(Done) |
| Trace | 入口 | `Log` 临时等 mutex，只为了整行日志与编号 | 单次 Log 返回 | 不负责业务取消，也不决定 worker 顺序 |

`WaitGroup.Add(N)` 在所有 worker 启动和协调者等待之前完成。各 worker 只写自己的 `counts[id]`，协调者在 Wait 返回后才读，避免多个 worker 对同一共享计数递增。`Done` 的容量 1 让协调者无需消费者先结束当前处理即可发布；`Wait` 收一次终态后还等待 Done 关闭，证明协调者完成最后发布动作。每个 Pipeline 只有一个 owner `Wait` 一次。

`send` 的第一个带 default 的 select 只用于当前快速发送和观察暂时不能发送；不能发送时记录 `send_wait`，随后第二个 select 同时等待发送与取消。`Blocked/Produced` 是可丢通知的实验观察 channel，满时不阻塞业务；业务可靠性不依赖它。饱和夹具为这两个观察 channel 各分配容量8，足够保存冻结期间有限的失败/成功通知。`ctx.Err()` 的入口检查减少取消后领新工作，但检查与发送之间不原子化，不能承诺“cancel 调用之后绝无任何额外交付”。

## 正常与空输入：逐段对照实际日志

完整运行保存在 [demo.txt](evidence/demo.txt)。下面用 `code` 定位机制，不依赖每次都会相同的序号或 worker ID。`Trace` 的 mutex 只序列化记录顺序：send 已与 receive 会合后，接收者可能先打印 `business_done`，发送者才打印 `result_accepted`；这并不代表接收发生在发送之前。

| 步骤 | 参与者 → 函数 / 等待点 | 状态变化与对应日志 | 下一步为何发生 |
| --- | --- | --- | --- |
| 1 | owner → `Start` | 创建 jobs(cap=2)、results(cap=1)、3 个 worker、feeder、协调者；`scenario_start normal` | owner 立即进入 Collect，不由 Start 自动等结束 |
| 2 | feeder → `send(jobs)` | `job_accepted` 只证明交给了 worker 或进入队列；偶尔 `send_wait jobs` | worker 接收释放有限容量，来源才能继续 |
| 3 | worker → `validate → send(results)` | O-03 的 Result.Err 为业务拒绝；`result_accepted` 表示已交付或已排入缓冲 | consumer 真正接收后 append 才是本课业务完成 |
| 4 | consumer → `Collect` | 六条 `business_done`，ID 集合 O-01..O-06 | 持续读取为上游释放背压，无顺序保证 |
| 5 | feeder → deferred finalizer | `jobs_closed produced=6 err=<nil>` | 空队列后 worker 收到 ok=false |
| 6 | worker → defer | 各 `jobs_drained → worker_exit`；本次有人可能处理 0 单 | worker 数不是均分承诺，Done 才是退出证明 |
| 7 | closer → Wait/sourceDone | `results_closed → all_joined`，所有发送已停止 | close 允许 consumer 的下一次排空 receive 返回 false |
| 8 | consumer/owner → Wait | `results_drained → wait_returned`，`scenario_result normal consumed=6 produced=6 validated=6 workersExited=3 err=<nil>` | owner 返回，清理已完成 |

`results_closed` 日志可能出现在 `results_drained` 日志后面，因为真正的 `close(results)` 执行后才打印；同步的是 close 和接收，两个后续日志没有固定顺序。`all_joined` 的生产/校验计数、订单集合和最终业务错误才是验收点。

空输入仍会启动约定数量的 worker：feeder 不发值直接 close jobs，worker 都收 `ok=false`，协调者关闭 results，consumer 零次 append 后正常完成。实测结局为 `empty consumed=0 produced=0 validated=0 workersExited=3 err=<nil>`。不需要偷偷发送一条“特殊空订单”当结束标记，更不能靠 ID/金额零值断定 EOF。

## 消费者提前退出：错误与修复的两条时间线

`BlockedConsumer` 的错误版本采用无缓冲 results 和两个真实订单，生产者裸写两次。主 consumer 只取 O-01，然后离开；此时再没有正常接收者。

| 步骤 | 参与者 / 等待 | 错误状态与日志 | 夹具为何能继续 |
| --- | --- | --- | --- |
| 1 | bad-producer 第一次裸 send | 与 consumer 接收 O-01 会合；`consumer_left` | 生产者继续执行 |
| 2 | bad-producer close(secondAttempt) 后第二次裸 send | owner 收到发送前屏障；没有接收者，所以发送语义保证不能完成 | 屏障不冒充 runtime 栈上的挂起观测 |
| 3 | owner 非阻塞检查 producerDone | `send_cannot_finish`；完成通知不可能 ready | 若没有额外接收，程序会一直等待 |
| 4 | deferred rescuer 真正接收 O-02 | `rescue_receive`，第二次裸发送才有完成机会 | 这是有界教学救援，危险代码本身没有被修好 |
| 5 | producer defer close(results)/producerDone | `second_send_returned → bad_example_cleaned producerExited=true` | owner 确认没有留下发送 goroutine |

把缓冲改大只会允许更多无人消费的结果入队；只要输入超过容量，问题重新出现。真实修复已经在 `pipeline.send`：每个可能阻塞的发送都同时等待 ctx.Done，且 owner 提前退出时发 Cancel、再 Wait。只“给 consumer 加 cancel”却保留上游裸 send 仍然无效。

修复版使用 3 个 worker、jobs(cap=1)、results(cap=0)，StopAfter=2。实测保存的结局是 `early-stop consumed=2 produced=5 validated=4 workersExited=3 err=消费者主动提前停止`；5/4 是这次上游跑到的位置，其他运行可出现不同计数或结果子集，只断言 consumed=2 和 3 名 worker 均退出。

| 步骤 | 参与者 → 函数 / 当前等待 | 修复状态与日志 | 后续同步边界 |
| --- | --- | --- | --- |
| 1 | consumer → Collect | append 第 2 单，`cancel_requested` | 只是发出通知；它还没有证明任何 worker 已停 |
| 2 | worker → send(results) 或 worker 的接收 select | `send_canceled` 或 `cancel_observed` | 当前 select 观察通知才可返回；若另一 ready 分支被选，也可能先多做一次动作 |
| 3 | worker → send 返回错误 / defer | `result_abandoned` 表示已校验但没有交给结果通道；随后 `worker_exit` | Done 通知计数，不偷偷宣称未交付结果已业务完成 |
| 4 | feeder → send(jobs) / defer | `jobs_closed err=context canceled`；也可能来源早已正常完成 | jobs 唯一 owner 关闭，无接收者也不会永久发送 |
| 5 | closer → Wait/sourceDone | 全部 worker 退出后 `results_closed → all_joined canceled=true` | 不等待 consumer 再来读 results |
| 6 | owner → Wait(独立清理 ctx) | `wait_returned consumed=2 workersExited=3` | 通知 → 观察 → 函数退出/defer → 所有 sender 完成 → 关闭/终态 → owner 等完 |

主业务 ctx 已取消后，若拿它调用 Wait，它的 Done 已 ready，可能立即返回“取消”并跳过退出证据。所以清理等待使用独立的 3 秒 deadline；超时返回明确错误，并不冒充已清理。独立 `TestEarlyConsumerStop` 使用无缓冲结果，Collect 返回后实际 receive 得到 ok=false，验证全部发送者结束与 results 关闭；不能靠增加 buffer 让这个测试偶然通过。

## 来源错误：结束信号不能吞错误

FailAt=3 使用同样的输入：feeder 成功交付 O-01/O-02/O-03，下一轮在 index=3 产生 `fmt.Errorf(... %w, ErrSource)`，停止生产，defer 关闭 jobs。worker 排空先前值，三个结果仍包括 O-03 的业务拒绝；consumer 不会因一个订单非法或来源错误丢弃已交付的前缀。

日志对应 `source_error → jobs_closed`，worker 各自退出，closer 在 `sourceDone` 读到错误，`all_joined sourceErr=source index=3: 合成订单来源解析失败`，最后 `source-error consumed=3 produced=3 validated=3 workersExited=3 err=source index=3: 合成订单来源解析失败`。

这里 `results_drained` 只证明通道关闭排空；`Collect` 还必须看 Completion。独立测试覆盖 failAt=0、3、6：0 条也可失败，3 条前缀失败，6 条结果全部出来以后整批仍可能失败。`TestEarlyStopDoesNotLoseSourceError` 让 producer 交付一单后一定生成 ErrSource，consumer 同时 StopAfter=1，最后 `Report.Err` 的链里同时能 `errors.Is(ErrEarlyStop)` 和 `errors.Is(ErrSource)`，避免只返回先看到的错误。

## 饱和与恢复：有限容量表示在途工作

`Saturation` 固定一个 worker、jobs(cap=1)、results(cap=1)，owner 最开始不读结果。一个结果已在 results，一个 worker 拿到下一单但不能再发送，一个后续订单在 jobs；feeder 的后续发送也不能越过满队列。这是有限背压，不是程序错误。

| 步骤 | 参与者 / 等待点 | 可观察状态 | 下一步 |
| --- | --- | --- | --- |
| 1 | feeder/worker → send | 各自曾记录 jobs/results `send_wait` 并发送通知；feeder 每次成功 jobs 发送后也发 Produced 累计进度 | 失败通知只证明那次快速探测不能发送，不冒充精确的 runtime 栈状态 |
| 2 | owner 同时等两种 Blocked 通知和 Produced=3 | O-01 已占 results、O-02 已被 worker 拿到但发不出去、O-03 已真实送进 jobs | 只等两条旧失败通知不够；第三次成功交付把 jobs 尚未填满的窗口排除 |
| 3 | owner → Queues | `queues_saturated jobs=1/1 results=1/1 produced=3` | 原因是唯一 worker 不能推进且 consumer 被冻结，不能把普通 len 快照当同步协议 |
| 4 | owner → Collect | 开始接收释放一个结果位置，worker 可继续，jobs 随后出现空位 | 背压逐级解除，不需要改容量 |
| 5 | feeder/worker/closer/owner | 六单完整，`saturation consumed=6 produced=6 validated=6 workersExited=1 err=<nil>` | 正常终止并 join |

日志与真实“执行到发送前”“已经在 select 等待队列里挂起”要区分：通知是代码插入的边界，不读取 runtime 栈。本课证明的是该操作在无人改变状态时不能完成；先后由屏障和稳定条件给出。`len(ch)` 在活跃生产消费中只是瞬时快照，不能写成 `if len(ch)<cap(ch) { ch<-value }` 来保证不阻塞。

早期版本仅等两种失败通知，独立复验实际捕获 `jobs=0 results=1`。旧 jobs 通知可能在 worker 取走订单前发出；没有成功交付第三单的屏障，观察状态还不稳定。修复后补上 Produced=3，20 次重复普通测试通过，诊断保存在 [saturation-barrier-diagnostic.txt](evidence/saturation-barrier-diagnostic.txt)。这也是为什么本课不把“日志里曾经等待过”当成“此刻已处于该等待状态”。

`TestSaturationCancelAtSend` 在同样冻结状态下不恢复接收，直接 Cancel 并 Wait，证明取消出口可以独立解开发送等待。results 中先前的一单仍然存在，Wait 后真实 range 排空得到 O-01；取消不会自动删除缓冲数据。业务在途量不只有两个缓冲，还包括每个 worker 当前握有的订单和 feeder 正准备发送的值；当前状态的有界上限由容量和 worker 数一起决定。

## 多个生产者：只让知道“全部发完”的参与者关闭

results 有 N 个发送者。错误写法是每个 worker 都 `defer close(results)`：第一个退出的 worker 会让其他 worker 的后续发送 panic，多个退出者还会重复 close。接收者拿到第一条就 close 更早破坏协议。

`ReceiverCloses` 真正复现后一种错误：sender 发 O-01 后等待 gate，consumer 收到第一单就关闭 out，再 close gate，sender 执行第二次 send，稳定得到 `send on closed channel`。故障发生在 sender goroutine 内，它自己的 defer recover 记录事实并交回 owner；main 的 recover 无法跨 goroutine 捕获。吞 panic 不能使丢失的 O-02 变成业务成功。

修复版是 `Start` 的协调者：登记所有 worker → 每个 worker 的最后一次发送结束 → worker defer Done → WaitGroup.Wait 返回 → sourceDone 可取 → 唯一 close(results)。worker 的“发送能力”和“关闭所有权”不是一回事。`TestNormalAndEmptyMultiProducer` 对 1/3/8 个 worker、0/1 缓冲、正常/空输入组合共 12 次真实运行，逐单核对集合并在 owner 返回后检查通道已关闭。没有断言均分、固定 worker 编号、固定结果顺序。

## select：就绪规则与危险循环

`select` 在当前 goroutine 里等待 channel 通信，不会自动创建 goroutine；每次只选一个能继续的 case。没有 ready case 时，有 default 就直接执行 default，没有 default 就等待。进入 select 时 channel 操作数和发送右值按源码顺序各求值一次，即使最后没选该分支，求值副作用也已经发生。两个 case 同时 ready 时按规范均匀伪随机选择一项，源码排列不是优先级，也没有轮流执行或某个有限时间内必选一次的业务保证。依据 [select 规范](https://go.dev/ref/spec#Select_statements)。

| 实验 / 日志 | 真实观察 | 直接修复与含义 |
| --- | --- | --- |
| `default_spin` | 空 channel 完全没有发送者，仍完成 100,000 次 default，没有任何业务进展 | 删除 default，阻塞 select 同时等数据/取消；当前 goroutine 的挂起不等于整个程序不能运行 |
| `blocking_receive` | 接收 goroutine 先公布 started，owner 才发 9；真实通信后完成 | 屏障不是 Sleep；没有 default 的接收不会在无数据时反复空跑 |
| `closed_spin` | closed/drained 无需 default 就立即返回 100,000 次 `ok=false` | !ok 后从循环退出；多路合并则把局部 channel 变量设 nil，禁用该 case |
| `merge_value → disable_left/right` | 两个 closed 的缓冲值 11/22 都被收到，各自排空后赋 nil | 最后循环条件 `left!=nil || right!=nil` 为 false；若全 nil 还 select 且无 default 会永久等 |
| `both_ready` | 保存运行 left=2018/right=2078，总数 4096 | 不把比例、某轮顺序或轮流当契约；case 不表达业务优先级 |
| `cancel_not_priority` | 每轮 ctx.Done 在 select 前就 ready，空 buffer 的 send 也 ready；保存运行仍选择发送 2016 次、取消 2080 次 | cancel case 不是优先中断；两种值只属当前运行，断言仅检验总次数 |

业务若要求严格“停止点以后禁止新写入”，必须让单个 owner 串行决定是否接受业务操作，并在它的状态机里定义停止点；只加一行 `select { case <-ctx.Done(): ...; case out<-v: ... }` 或前置 `ctx.Err()` 不能形成原子禁令。若有外部持久化副作用，还必须分别定义已发请求、服务器执行与业务确认的事实。本课订单校验没有外部请求。

空 `select {}` 会让当前 goroutine 永久等待；只有整个程序满足 runtime 的死锁检测条件才可能报 `all goroutines are asleep`。有网络 poll/timer/其他工作时并不能指望它一定立即替你发现业务泄漏。课程用有界 owner 和断言验收，无需运行无恢复的空 select。

## channel 同步了什么，指针没有自动变安全

以下是语言内存模型保证；本课既给正确实现，也提供真实 race 入口，而不是从“channel 内部有锁”推导对象安全。依据 [Go 内存模型的 channel communication](https://go.dev/ref/mem#Channel_communication)。

| 保证 | 当前代码里的对应关系 | 不能外推的结论 |
| --- | --- | --- |
| send 在对应 receive 完成之前同步 | `Order` 初始化 → send → worker receive → validate，worker 可以看见发送前的字段 | send 后发送者继续写指针指向对象，不受这一条保护 |
| 无缓冲 receive 在对应 send 完成之前同步 | `Rendezvous` owner 在接收前写 consumerState=77；sender 在 send 返回后读到 77 | buffered send 可在无接收者时返回，不能套用该反向保证 |
| close 在因关闭而返回零值的 receive 前同步 | `finalCount=6 → close(finished) → await(finished) → 读6` | goroutine 自然返回本身没有发布其共享写入的同步保证 |
| 容量 C 的第 k 次 receive 在第 k+C 次 send 完成前同步 | 缓冲满后只有 receive 释放有限位置，下一发送才能继续 | 不是每次 buffered send 都必须等对应业务消费完成 |

`MemoryOwnership` 的值实验先发送 `Order{AmountCents:100}`，再把原 struct 改成 9，收到的独立值仍是100。这里只含 string/int；若 struct 含指针、slice 或 map，struct 的值复制仍然会共享它们所指向的可变存储，需要更深复制或所有权协议。

指针修复实验使用同一个 `*Order`：owner 初始化100，通过 toWorker 发送指针后停止访问；worker 独占修改成200，经 returnPointer 回传；owner 收回以后才再次读取。worker 的修改 → 回传发送 → owner 接收 → owner 读，建立明确同步链，最后等待 worker 的 done。

最终的 `finalCount=6 → close(finished)` 是另一个独立工作段，前面的指针 worker `done` 不能证明它已经完成。`memoryOwnership` 在启动这个任务之前就注册 deferred `cleanupAwait(finished, "最终值发布任务清理")`。业务 `await(ctx, finished)` 正常返回时，`close_visibility` 记录字段可见；如果业务 ctx 先取消，函数进入 defer 后仍用新的3秒清理预算等 finished，不会拿已取消的业务ctx立即结束清理。收到 finished 后才把已经确认的 FinalCount 放进报告，`errors.Join` 保留原始业务取消和任何清理/发布错误。正常值结果100/9/200/6保持相同，多一条 `final_publish_joined` 说明独立确认完成。

`TestMemoryFinalPublisherCancellation` 对公开入口使用的同一个实际函数加入私有测试屏障。正常入口的hooks为nil，不增加正常路径等待；测试中的发布gate自身也有独立3秒上限，失败退出会放行gate并等待owner，避免夹具泄漏。详细输出保存在 [test.txt](evidence/test.txt)。时间线如下：

| 步骤 | 测试驱动 / 工作段 / owner | 实际同步与状态 | 独立证明点 |
| --- | --- | --- | --- |
| 1 | owner 已完成前面指针回传，启动最后 publisher | publisher 关闭 finalStarted，等待 releaseFinal；finished仍未关闭 | 确认测试命中了最后工作段，没有在入口提前取消而绕过它 |
| 2 | 测试驱动收到 finalStarted 后调用 cancelBusiness | owner 的业务 await 返回包装的context.Canceled，进入最终defer并关闭 cleanupStarted | 取消通知已被owner观察，和发布完成仍是不同状态 |
| 3 | 驱动收到 cleanupStarted，暂不关闭 releaseFinal | publisher不能写6/关闭finished，独立cleanupAwait不能成功完成；返回channel尚无结果 | 没有sleep/NumGoroutine；不把发送前通知或记录日志冒充runtime栈观测 |
| 4 | 驱动关闭 releaseFinal | publisher写6，然后deferred close(finished)公布无后续写入 | 发布值与完成通知发生在同一任务，并建立channel同步 |
| 5 | owner 独立cleanupAwait收到finished，返回到驱动 | 返回完整报告100/9/200/6，errors.Is(err, context.Canceled)=true | 最后的工作段已被确认，业务取消原因没有被清理覆盖 |

这些完成通知证明已公布的工作状态与相应清理动作；课程不据此声称Go runtime此刻已经回收goroutine栈。收到发布任务finished不能替代前面指针worker的done，反过来也一样；owner必须分别承担两项收尾。

显式危险入口与它使用同一种订单：先 buffered channel 发送地址，reader 在接收后与 writer 共用 ready 屏障开始固定 20,000 次循环。reader 读 AmountCents，writer 在 send 完成后继续写 AmountCents；ready 只同步循环开始，不把每一对读写排成先后，WaitGroup 只等待最后退出，不能追溯修复此前的数据竞争。`observed` 的具体总数不作为断言。

```powershell
# 必须按 STATUS 的已验证 64 位 CC 配置执行；这是预期失败的显式故障入口。
go -C lessons/m12_channels_select run -race ./cmd/fault pointer-race
# 正常路径的同步与清理验证，应退出 0。
go -C lessons/m12_channels_select test -race -count=3 ./...
go -C lessons/m12_channels_select run -race ./cmd/demo
```

`cmd/fault` 固定工作量、独立进程，不被默认 demo 或正常单元测试调用。预期结果是 race detector 报告 AmountCents 的冲突并使 go run 非零退出；普通代码的 race 通过和坏例子的 race 失败是两个分开的验收事实。实际正常 `test -race -count=3` 和完整 `demo -race` 均退出0，没有竞争报告；显式故障实际报告一处数据竞争，`go run` 退出1并显示 `exit status 66`。冲突定位到 [fault/main.go](cmd/fault/main.go) 的 `owned.AmountCents` 读取（第39行）与 `pointer.AmountCents` 写入（第47行），原始证据为 [pointer-race.txt](evidence/pointer-race.txt)。编译器设置和完整结果见 [STATUS](STATUS.md)。

## Java 经验怎样帮助，以及哪里会失效

有缓冲 channel 的有限入队等待可类比有界 BlockingQueue，但 channel 还有无缓冲会合、语言级 select、关闭后排空及永久零值接收这些独立契约。Java 队列里放 poison pill 是应用数据，本课 close 是通道状态，金额0不能用来扮演 poison pill。

context 取消可类比协作停止标记：它发布通知，代码在 select/Err 或支持 context 的 I/O 才能观察；它不等于 Java Thread.interrupt，更不会像强制杀线程那样自动让所有裸发送返回。WaitGroup 可借用 CountDownLatch 的等人直觉，但它不传播 error、不帮忙取消、不决定关闭谁；sourceDone/Completion 明确补上了这些责任。

channel 建立的先行关系有助于从 Java JMM 的 happens-before 经验理解，但 Go 的规则必须按具体通信来画。对象地址通过 channel 后仍有多个别名，和引用通过安全队列后继续并发修改同一个对象一样需要另外的协议。Go 的“用通信交接”是可采用的设计方式，不是 channel 对所有对象自动加锁的语言魔法。

## 原材料的纠正与本课边界

已只读核对现有 `examples/advanced-syntax/{channels,select}` 和主目录 `files/语法进阶/{Channel,Select}`，没有修改或复制整批材料。需要纠正的具体说法如下：

| 材料表述 | 本课采用的准确解释 / 运行证明 |
| --- | --- |
| `var ch [size]chan int` 声明有容量的 channel | 这是 channel 数组，各元素初始 nil；容量只写在 make 内 |
| channel 用完必须 close，关闭后接收到的永远是零值 | close 是结束协议；先保留缓冲值，排空后才是零值/false；GC 回收与业务结束是不同问题 |
| close 后 ok=false | 必须关闭且排空；closed buffered zero 的真实 receive 仍是0,true |
| producer 返回，或 close 之后 range 立即结束 | producer 返回不关闭；close 后还要消费缓冲。RangeNeedsClose 用实际等待窗口证明 |
| select 起一个 goroutine 监听 | select 由执行它的当前 goroutine 进行；go 语句才创建 goroutine |
| select case 顺序/ctx case 代表优先级 | 多 ready 按规范伪随机选；已经取消的 ctx 旁边仍实际选到 send |
| 内部一定有队列/一把锁，故整个对象并发安全 | 内部结构属于版本相关的 runtime 实现；本课只依赖语言契约，不给传递的可变指针对象附加保护 |
| 所有接收者都会收到关闭消息 | 正在等或之后接收可因 close 继续；原缓冲值每次只交给一个接收者，不是向所有 consumer 复制每条数据 |
| `time.Sleep(2)` 等所有 goroutine 做完 | 2 是2纳秒；任意 Sleep 时长也不是 join。显式完成屏障和 WaitGroup 才能证明收尾 |

本课没有生产服务、数据库、MQ、持久队列或真实支付。语义验收来自真实 Go channel/goroutine/context/race，单元测试直接执行同样的并发协议，不用 mock 模拟阻塞或取消。正常运行不改外部配置、不占端口、不启动服务，坏程序只有显式 pointer-race 入口。完整命令证据和尚未通过的项目在 STATUS 中如实列出。

优先阅读来源：[Go 1.27 channel/select 规范](https://go.dev/ref/spec)、[Go pipelines 与 cancellation](https://go.dev/blog/pipelines)、[Go 内存模型](https://go.dev/ref/mem)。核对时间为2026-10-12；示范的是当前本模块版本，阅读公司旧代码时应按它的 go/toolchain 与库契约重新核对。
