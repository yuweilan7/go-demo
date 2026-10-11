# Java 工程师转 Go 后端：39 个可独立复制的课程 Prompt

本目录交付 **M00–M35、M37–M39 共 39 份课程生成 prompt**，保留原有模块编号和路径。目标是从“熟悉 Java、刚接触 Go”走到能读复杂 Go 业务调用链，识别事务、资源、并发、缓存和消息的错误，并用真实服务验证修复。每份文件全文就是给下一次对话的用户 prompt，背景、范围、注释标准、实验、路径、验证已包含，可独立复制执行。课程产物统一为中文文档、详细注释的 Go 代码、可运行实验和实际结果记录。

**全部 39 课已完成主验收，按 7 层整层汇总提交。** 已生成的文档、详细注释源码与实验从 [课程入口](../lessons/README.md) 阅读，实际验证范围见 [主验收记录](../lessons/ACCEPTANCE.md)。这些 prompt 仍可独立复制，用于重新生成或继续讲解某一模块。模块编号用于定位，严格整层屏障与恢复规则见 [实施与监测约定](ORCHESTRATION.md)。规划、实施与验收日期：2026-10-12。

## 使用方法

1. 主 agent 根据 [DAG 与并行批次](DAG.md) 按整层屏障委派，每个子 agent 完整执行一份模块 prompt；也可单独打开文件阅读全文。
2. 同层模块可以并行实施。推荐知识前置决定你读课的顺序；当前实施跨层启动的条件是前一层全部验收且完成整层提交。
3. 每个执行者在分配的独立 worktree 中只写 `lessons/mXX_主题/`，包括独立 `go.mod/go.sum`、中文 README、详细注释源码、可运行实验、必要断言和真实 `STATUS.md`。主 agent 汇总验收并整层提交；子 agent 不自行提交，不改根 module 或索引。
4. 中间件安装通过 [基础设施约定](INFRASTRUCTURE.md) 的单一安装者协调；安装完成后的独占命名空间实验可以在容量允许时并行。先完成 Go 原理课不需要等全套中间件安装。
5. 课程生成后，你按文档给出的入口和调用链读注释、运行核心实验、对照完整解析理解输出、反例和修复。讲不清的一段调用链就在该模块继续追问，不需要一次吞下整个项目。
6. 硬前置以对应课程的 `STATUS.md` 和直接契约为准，不能把本目录中的 prompt 文件存在当作完成。

独立子 module 的典型命令是：

```powershell
go -C lessons/m21_transactions_defer_panic run ./cmd/demo
go -C lessons/m21_transactions_defer_panic test ./...
go -C lessons/m21_transactions_defer_panic vet ./...
```

这些路径已按各模块 prompt 创建并独立验收。根 `go test ./...` 只覆盖根 module；复跑某课应使用该课 README 的 `go -C`、实库开关与资源约定，不能用根测试替代嵌套课程实验。无需重建根工程为一个巨型服务。

## 模块目录

“硬前置”是组合生成/验收所需的已交付课；更多知识建议写在各文件内部。每课的“按需/快进”只表示学习投入可以缩短，不允许省略相关错误边界。

| 模块 | 可复制的完整 prompt | 硬前置 | 投入 | 学完能做什么 |
| --- | --- | --- | --- | --- |
| M00 | [工具链、项目地图与共享基础设施入口](M00_environment_infra.md) | 无 | 按需/快进 | 能证明某条运行命令使用了哪个 Go/module，识别现有服务与退役配置，并为后续实库实验准备唯一入口。 |
| M01 | [快速补齐业务代码语法、包与依赖管理](M01_syntax_packages.md) | 无 | 按需/快进 | 面对一页 Go 业务代码能识别声明、赋值、作用域、函数边界、导出和 import，而不用逐个符号询问。 |
| M02 | [值、指针、结构体与方法：谁被复制、谁会改变](M02_values_methods.md) | 无 | 重点 | 能判断函数参数、接收者和 struct 复制会影响哪个对象，识别复制锁、指针别名和 nil 接收者风险。 |
| M03 | [slice、map、string 与闭包：共享内存和循环陷阱](M03_collections_aliasing.md) | 无 | 重点 | 能从业务数据流判断 append、range、缓存对象及闭包捕获会不会改变别人的数据。 |
| M04 | [interface、typed nil、embedding 与可替换依赖](M04_interfaces_nil.md) | 无 | 重点 | 能读懂框架中的小接口与组合，避免 nil error、方法集和伪继承引起的业务错误。 |
| M05 | [error 链、分类和重试边界](M05_errors_contracts.md) | 无 | 重点 | 能保持错误原因并按语义处理，避免文案比较、误重试和日志掩盖。 |
| M06 | [defer、return 与资源释放的精确时间线](M06_defer_resources.md) | 无 | 重点 | 能逐行预测 defer 注册、参数求值、结果赋值和资源清理，识别藏在循环与闭包里的泄漏。 |
| M07 | [panic/recover：恢复边界、goroutine 与进程后果](M07_panic_recovery.md) | 无 | 重点 | 能判断 panic 是否会被捕获、恢复后从哪里继续，以及恢复是否隐瞒了已发生的副作用。 |
| M08 | [JSON、金额、时间与业务数据边界](M08_json_money_time.md) | 无 | 掌握常用 | 能阻止 null/零值/缺字段、浮点金额和时区错误进入数据库与接口契约。 |
| M09 | [读框架必需的泛型、函数选项、迭代器与反射](M09_generics_reflection.md) | 无 | 按需/快进 | 能读懂泛型签名、functional options、序列迭代和反射绑定，不为炫技设计抽象。 |
| M10 | [Go 测试、调试与错误路径的独立证据](M10_testing_debugging.md) | 无 | 掌握常用 | 能把看起来正确改成可重复验证，并区分单测、协议集成、竞态检测与真实故障证据。 |
| M11 | [goroutine 的启动、阻塞、完成与回收](M11_goroutine_ownership.md) | 无 | 重点 | 能为每个 goroutine 指定拥有者、等待对象和退出路径，避免 fire-and-forget 与请求结束后泄漏。 |
| M12 | [channel/select：所有权、背压和结束协议](M12_channels_select.md) | 无 | 重点 | 能判断 send/receive/close/nil 的状态，构造不会丢错误或永久等待的生产消费路径。 |
| M13 | [context、取消、超时与整条调用链预算](M13_context_budgets.md) | 无 | 重点 | 能区别 cancel 通知、函数观察到取消、I/O 返回与业务副作用停止，保持请求总预算。 |
| M14 | [Mutex、atomic、sync.Map 与内存可见性](M14_sync_memory_races.md) | 无 | 重点 | 能区别 data race、逻辑竞态与锁保护范围，识别同步对象复制和并发 map 的错误。 |
| M15 | [组合并发：errgroup、worker pool、限流和部分失败](M15_structured_concurrency.md) | M11、M12、M13、M14 | 重点 | 把独立请求并发化，同时证明并发上限、错误策略、取消传播和所有 goroutine 退出。 |
| M16 | [Timer/Ticker、后台任务与优雅停机](M16_timers_shutdown.md) | M15 | 掌握常用 | 能停止接收工作、处理在途任务并在有界时间内关闭资源，读懂旧计时器代码的版本条件。 |
| M17 | [net/http：请求、响应体、连接复用与取消](M17_net_http.md) | 无 | 掌握常用 | 能写和读真正的 HTTP 客户端/服务端代码，定位 body 泄漏、无超时、响应已写出和重复副作用。 |
| M18 | [Gin 路由、middleware、绑定与请求作用域](M18_gin_middleware.md) | M17 | 掌握常用 | 能沿真实 Gin 调用链读中间件，并识别 Abort/return、Context 池化和取消未传递的坑。 |
| M19 | [构造、配置与复杂仓库的真实业务调用链](M19_composition_architecture.md) | M04、M18 | 掌握常用 | 能从 main 和一条 API 定位依赖装配、业务入口及持久化边界，避免 Java 式全局容器思维。 |
| M20 | [database/sql、真实 MySQL 与连接池生命周期](M20_sql_pools.md) | M00 | 重点 | 能解释 *sql.DB/Conn/Tx 的区别，定位池耗尽、Rows 泄漏和真实 I/O 超时。 |
| M21 | [事务、defer、panic 和 Commit 错误的实库交叉实验](M21_transactions_defer_panic.md) | M20、M05、M06、M07 | 重点 | 能逐行审查事务代码，证明失败不留下部分业务效果，并处理提交结果未知。 |
| M22 | [MySQL 隔离、行锁、死锁与并发库存](M22_mysql_locks_isolation.md) | M21 | 重点 | 能用实际两条会话证明事务隔离和锁行为，修复超卖、死锁和锁等待放大。 |
| M23 | [GORM：SQL、零值更新、作用域、hooks 与事务](M23_gorm_semantics.md) | M21 | 重点 | 能把 GORM 链式代码还原成 SQL 与事务范围，识别零值丢失、条件污染和 hook 副作用。 |
| M24 | [表结构、索引、分页与安全演进](M24_schema_queries_migrations.md) | M20 | 掌握常用 | 能读数据库交互代码背后的契约，定位慢查询、数据截断和迁移不兼容。 |
| M25 | [真实 Redis、go-redis、TTL 与命令原子边界](M25_redis_client.md) | M00 | 重点 | 能正确使用长寿命 Redis client，区分缓存 miss、错误、命令原子性和持久事实。 |
| M26 | [缓存一致性、singleflight、热点与降级](M26_cache_consistency.md) | M25、M15、M21 | 重点 | 能复现缓存失效竞态并声明一致性边界，避免缓存错误放大为流量雪崩。 |
| M27 | [Redis 锁、租约、fencing 与限流](M27_redis_leases_limits.md) | M25、M13、M20 | 重点 | 能识别锁过期后的旧持有者、错误解锁和双执行，正确声明限流/租约保证。 |
| M28 | [RabbitMQ：ack、confirm、重连与毒消息](M28_rabbitmq_delivery.md) | M00 | 按需/快进 | 能区分消息入队、投递、业务提交与 ack，处理重复、丢响应和连接恢复。 |
| M29 | [Kafka：分区、offset、rebalance 与至少一次处理](M29_kafka_offsets.md) | M00 | 重点 | 能读 Kafka Go 消费代码，区分 poll、处理、数据库提交、offset 提交及分区顺序。 |
| M30 | [MySQL + Kafka：outbox、幂等与结果未知](M30_outbox_idempotency.md) | M21、M29 | 重点 | 能处理数据库与消息的双写窗口，证明至少一次投递下业务效果唯一，并对未知结果对账。 |
| M31 | [gRPC/Protobuf：生成代码、deadline、拦截器与流](M31_grpc_protobuf.md) | 无 | 掌握常用 | 能从 .proto 和生成入口读完整 RPC 调用，区分传输成功、业务错误、取消与服务端效果。 |
| M32 | [真实微服务框架：Kratos 主线，Kitex/go-zero 迁移阅读](M32_framework_source_reading.md) | M19、M31 | 掌握常用 | 能运行一套真实框架并沿其源码解释装配、中间件、RPC 与生命周期，迁移到公司框架时有阅读方法。 |
| M33 | [超时、重试、熔断、限流和资源隔离](M33_resilience_retries.md) | M13、M17 | 重点 | 能为依赖调用建立总预算和重试语义，避免请求风暴、写重复和故障扩散。 |
| M34 | [日志、指标、trace、pprof 与慢请求定位](M34_observability_diagnosis.md) | 无 | 掌握常用 | 能沿请求 ID 和时间证据区分排队、I/O、锁和 CPU，诊断泄漏且不把观测缺失说成零。 |
| M35 | [性能与运行时：benchmark、逃逸、GC 和内存预算](M35_performance_runtime.md) | 无 | 按需/快进 | 能基于测量定位 CPU/分配/锁问题，知道哪些运行时细节不应当成业务保证。 |
| M37 | [综合业务：订单、库存、缓存、RPC 与 Kafka](M37_order_backend_capstone.md) | M08、M19、M23、M26、M30、M31 | 重点 | 将已验证机制组成可以读、改、测试的 Go 后端，用真实中间件证明订单状态与副作用一致。 |
| M38 | [故障演练：取消、宕机、重复、乱序与恢复对账](M38_failure_recovery_lab.md) | M37、M33、M34 | 重点 | 在实库/真实 MQ/应用进程上复现 AI 容易漏掉的错误，证明恢复不重复、不丢失已承诺效果。 |
| M39 | [陌生 Go 仓库接手、AI 代码审查与小改动交付](M39_takeover_code_review.md) | M32、M38 | 重点 | 能接手一段未讲过的框架业务代码，发现隐蔽错误并用最小改动与回归证据交付。 |

## 阅读路线与取舍

先用 M01 的文档与实验补齐语法，再读 M02–M08 的值/接口/错误/资源/数据边界；这些课可以同时生成，但你学习时应把具体执行时间线读懂。M09 中泛型和反射以能读签名、配置和绑定为限，复杂约束、unsafe、内部布局不作必修。

M11–M14 各自建立 goroutine、channel、context 与同步模型，M15 将它们组合，M16 才讨论完整停机。取消通知、观察取消、任务退出、资源关闭是不同事件；并发课必须画出每个任务的 owner 和真正阻塞点。

M20–M24 是数据库主线。M21 专门把 error/defer/panic/Tx 交叉起来，M22 看真实锁竞争，M23 看 ORM 隐式行为。M25–M27 是 Redis 主线，分别处理命令、缓存一致性和租约/限流。M28/M29 是两种不同 MQ 语义；主综合项目采用 Kafka，RabbitMQ 为可独立学习的任务队列分支，不要求为了完成综合项目同时运行两种 MQ。

M17/M18 → M19 是 HTTP/框架装配路径；M31 是独立 RPC 路径，M32 把装配和 RPC 迁到一个真实微服务框架。默认 Kratos 深跑一条链，Kitex/go-zero 做迁移阅读；未来你提供公司实际框架后直接替换主线，不据此猜测 Shopee 的内部技术栈。

M33–M35 分别解决依赖治理、应用诊断和性能。M35 只在能测量时深入，先不要用旧 runtime 源码背调度常数。M37 组装订单业务，M38 对实库/MQ/应用进程做故障实验，M39 直接示范陌生代码接手、审查、最小修复和回归验证。M16 保留开发中必须理解的任务停止与资源清理；需要在服务器运行实验时，按基础设施约定准备实际环境。

39 个模块没有固定天数和篇幅。基础可以结合现有认识与实验结果快速阅读，关键机制可以分多轮。每课的 10～20 分钟核心实验不包括首次依赖安装和服务搭建，也不限制总学习时长。

## 为什么这套 prompt 会比知识点目录更具体

每课要求真实代码和中文讲解，围绕“哪个值/状态被谁改变、在哪一行阻塞、错误何时传播、资源何时释放”组织内容。实验直接提供预期时间线、危险反例、修复对照、完整解析和独立证据，让文档、源码和实际结果能相互核对。

所有手写教学源码采用 [中文教学与源码注释规范](TEACHING_STYLE.md)：函数职责、caller、参数来源、结果去向，值副本/共享对象、分支理由、阻塞点、错误传播和清理都要在关键代码旁讲透。Go 初学者尚不熟悉的语法和 API 也就地解释。README 另给阅读顺序、带符号的调用链和运行日志的逐段解析，不能把“详细”简化成多写几句泛泛注释。

这一标准参照 Python 项目第 21 章取消/超时和第 24 章异步生成器的实际源码：解释资源与任务的拥有者、等待点、通知和完成的区别，以及正常/失败路径的时间顺序。每份 prompt 已内置核心要求，另有仓库规范供执行者核对，不依赖安装全局 skill。

重点的跨机制坑已有单独验收：typed nil、err 遮蔽、defer 过早捕获、panic 时意外提交、用外层 DB 绕开 Tx、context 超时后副作用仍成功、worker 关闭等待、缓存旧读回填、锁过期旧 owner、MQ commit/ack 时机和 outbox 重发。

默认使用真实 MySQL/Redis/Kafka、真实本机 HTTP/gRPC。单测中的可控替身只辅助定位；实库 rollback、broker redelivery、rebalance 和应用退出/恢复都需要相应真实证据。小型单机实验不自动代表公司规模、集群高可用或长期 SLA。

每课的完成条件是文档与注释充分、实验可以复跑、关键行为有实际证据。测试和断言服务于验证实验的正确性；STATUS 记录实际交付、运行结果和缺口。

## 依据、版本与文件职责

本机核验为 `go1.27.0 windows/amd64`，根 module 声明 `go 1.27`。官方发布记录截至规划日包含 1.27.2；本次不调整工具链，执行每课时重新核对补丁、语言版本和目标公司版本。[Go 发布记录](https://go.dev/doc/devel/release)

课程按现有 53 个 Go 源文件、47 个教程文件的目录/符号盘点与关键语义抽查，以及 Python 学习项目现有路线/prompt 组织方式规划；本轮修订另读取了 Python 第 21、24 章的实际源码注释。来源中的正确内容保留，读取范围、风险与缺口见 [材料评估](SOURCE_REVIEW.md)。

- 本 README：路线、使用和模块入口。
- [ORCHESTRATION.md](ORCHESTRATION.md)：整层屏障、子 agent 隔离、主 agent 验收、整层提交、五分钟监测与结束规则。
- [TEACHING_STYLE.md](TEACHING_STYLE.md)：所有模块采用的中文文档、源码注释、调用时间线与实验解析要求，附具体注释示例。
- [DAG.md](DAG.md)：硬依赖、有向无环图与并行批次。
- [modules.json](modules.json)：可机器读取的节点、依赖、路径和拓扑分层，作为当前主 agent 的拓扑调度输入，文件本身不会自动开聊天或安装服务。
- [INFRASTRUCTURE.md](INFRASTRUCTURE.md)：真实中间件、首次安装、并行隔离与验收约定。
- [SOURCE_REVIEW.md](SOURCE_REVIEW.md)：已有材料覆盖、旧版风险与当前已核实范围。

原 examples、files 和 Python/DeepHelp 项目保持各自用途。每个课程有自己的 STATUS；实际完成状态须由代码、运行结果和主 agent 验收共同确认。
