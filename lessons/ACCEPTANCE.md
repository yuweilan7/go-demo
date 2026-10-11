# 课程主验收记录

2026-10-12（Asia/Shanghai），第 0～3 层共 36 个模块已经由主 agent 阅读实际源码、完整模块 prompt、中文文档与实验记录，并执行必要独立复验。课程由独立 worktree 生成，汇总时逐文件比对验收时的 SHA256；第 0 层395个、第 1 层130个、第 2 层106个、第 3 层97个模块文件全部一致。导航和编排文档另行检查。主 agent 按整层提交，提交 SHA 与详细无秘密验收回执保存在本机 `.local/course-orchestrator/`，也可从 Git 历史查看各层中文提交。

当前真实交付为 **36/39**。后续 M37、M38、M39 尚未验收。MySQL、Redis、RabbitMQ、Kafka 的基础交互，事务、schema演进、Redis租约与SQL拒旧写、隔离/ORM、缓存一致性、outbox 和真实微服务框架已实际验证；综合业务、综合故障恢复和接手审查仍需分别交付。模块 STATUS 是执行者的实际实验记录；它的 READY/PASS 文字本身不是主验收依据。

## 第 0 层实际验收

| 模块 | 主验收的关键事实与独立复验范围 |
| --- | --- |
| [M00](m00_environment_infra/README.md) | 工具链与入口检查；真实 Redis 8.2.8 AUTH/PING/INFO、PG 18.6 只读 SELECT；专属临时 SSH 进程退出且监听为零。越界 Redis host 在客户端构造/协议/网络调用前被拒绝，拦截断言的调用次数全部为零。安装锁只完成脚本/工具核验，首次安装前还须实际竞争与超时验证。 |
| [M01](m01_syntax_packages/README.md) | err 遮蔽真实写入零数量而 caller error 为 nil；修复无写入。真实模块 graph/MVS/replace 消费边界、初始化依赖/显式文件顺序、常量溢出与运行时窄化的编译故障由有界运行器核对。正常 run/test/vet 与依赖内容核验通过。 |
| [M02](m02_values_methods/README.md) | 顶层值复制与内部引用共享、指针重新绑定、方法值捕获与 Clone 双向隔离；4×250 次同步计数为 1000。实际 nil panic、复制锁 vet 诊断保留；run/test/vet/race 通过。 |
| [M03](m03_collections_aliasing/README.md) | slice append/filter/clear 与 map 的真实别名和修复；旧语言版本循环输出 333、现代版本 123；容量裁剪仍保留底层数组。测试、正常入口、旧版本与 nil 故障均实际执行。 |
| [M04](m04_interfaces_nil/README.md) | typed nil 接口非 nil 却打印 nil、方法集/接口复制、embedding 内部调用绑定、any 不可比较值 panic；实际文件读取与错误分支；正常 test/vet 和显式故障通过核对。 |
| [M05](m05_errors_contracts/README.md) | error 链、As/AsType、typed nil、部分 I/O 与主/清理错误；真实 9 字节回执；POST 写入后收到 EOF，仅 GET 对账：stored=1、POST=1、GET=1。run/test/vet/race 通过。 |
| [M06](m06_defer_resources/README.md) | defer 注册值/闭包晚读/结果槽与 LIFO；真实文件预算坏例第三次失败，修复四条记录 peak=1、live=0；Flush/Close 顺序与清理错误；四个真实子进程比较普通返回、Exit、panic、Goexit。 |
| [M07](m07_panic_recovery/README.md) | recover 的 goroutine/栈边界、取消后的独立收尾；真实 HTTP EOF、500、已写前缀 200 与再 panic；六个真实子进程。补上 ctx.Done 路径的独立 join，以 gate/清理超时断言证明不能提前返回。run/test/vet/race 通过。 |
| [M08](m08_json_money_time/README.md) | JSON 输入大小/深度/UTF8/重复字段/trailing，PATCH 缺失/null 语义；整数金额/币种/溢出和独立舍入；deadline 与时间序列化；Go 1.27 正式 json/v2 的实际行为。两种入口及对应 test/vet/race 通过。 |
| [M09](m09_generics_reflection/README.md) | 当前泛型 Map 方法与旧语言版本编译差异、约束/comparable 的动态 panic、真实文件 iter 与提前停止清理、reflect 候选提交边界和实际 backoff。11 项隔离编译故障、正常与兼容命令、test/vet/race 通过。 |
| [M10](m10_testing_debugging/README.md) | 独立金额/业务 expected、Parallel 夹具隔离与 TempDir 删除、真实 HTTP 五类输入；计数 20000 仍检出 DATA RACE，错误提交与共享污染在无 race 时仍 FAIL；worker Fatal 后 owner 继续；真实 Delve 断点/参数/栈。主复验有限 fuzz 244821 次、baseline 93、新 interesting 14，非穷尽证明。 |
| [M11](m11_goroutine_ownership/README.md) | 启动屏障、WaitGroup 加入、任务/事件 owner、请求与服务生命周期、有限队列 admission/drain；四类真实故障子进程。最终工作量与 join 分别检查，run/test/vet/race 通过。 |
| [M12](m12_channels_select/README.md) | channel/select 语义、pipeline 有界队列/来源错误/全部 worker 加入；真实指针竞争诊断；已取消发布任务必须独立等待清理，gate 和 timeout 断言验证。run/test/vet/race 通过。 |
| [M13](m13_context_budgets/README.md) | 真实 HTTP 900ms 整链预算与丢 ctx 后晚副作用；超时未知写、无防重效果 2 vs 稳定 key 效果 1；六个真实并发请求仅一项效果；WithoutCancel/AfterFunc 的 owner 与独立 join。lostcancel 实际 vet 失败；run/test/vet/race 通过。SQL adapter 仅编译/契约。 |
| [M14](m14_sync_memory_races/README.md) | Atomic Load/Store 与 sync.Map 分步操作在 race 通过时仍超卖；Mutex/CAS 同时检查销量和库存守恒；配置完整版本、双向克隆、统一转账锁序；真实持锁 HTTP。故障运行器收到 5 个 runtime fatal、7 个 DATA RACE、3 条复制锁诊断，未以超时替代诊断。 |
| [M17](m17_net_http/README.md) | 14 个真实 HTTP1/TCP/TLS 实验：Body 占池、响应/JSON/业务/未知写、分阶段 timeout、取消后副作用、写超时/流式/空闲回收。修复所有自建请求失败路径，结果与 Finished 分开；实际清理 gate 未开时 owner 不返回，释放后 owned=0。14 项测试、vet、race 通过。 |
| [M31](m31_grpc_protobuf/README.md) | 真实 TCP/HTTP2 unary/bidi、interceptor 顺序、typed status、丢 ctx 后晚写、提交后超时与重复扣减、CloseSend/Recv EOF/Send 背压；protobuf 双版本 unknown/presence/int64 与 reserved 编号实际编译失败。生成文件重复生成 hash 相同，run/test/vet/race 通过。 |
| [M34](m34_observability_diagnosis/README.md) | bad-fast 约 190ms，其中 queue 约 188ms、自己的 dependency 约 1ms；真实栈与 block/mutex 追到 badCheckout 的跨 I/O 锁。fixed 在 holder 仍等待时提交，最终两订单不丢。主 agent 直接解析 exporter 的 42 个 span/18 条 remote edge/6 条新根 Link；CPU/heap、goroutineleak 1/0 盲区与 runtime trace/sync/net profile 均实际解析。所有 driver 的 Done 收尾与清理超时由确定性断言验证；9 项测试/vet/race 通过。 |
| [M35](m35_performance_runtime/README.md) | 32MiB slice 引用保留与 Clone，真实 interface/闭包分配/逃逸/objdump/GC 软预算、Pool 所有权；相同订单键协议只改算法。主 agent 串行 CPU=1 五样本：Fmt 74.85–75.93ns、32B/2alloc，Append 20.79–21.32ns、24B/1alloc；这是固定当前环境样本。正确性、vet、race 通过。 |

## 第 1 层实际验收

| 模块 | 主验收的关键事实与独立复验范围 |
| --- | --- |
| [M15](m15_structured_concurrency/README.md) | 真实HTTP库存批处理保存SKU对应、并发上限和部分失败；128项批量结果一致。原协程池的锁内阻塞发送与worker递归发送两种死锁有界复现，修复有独立接纳/结果/Done所有权。pool、errgroup failfast、弃读结果、weighted admission均等待真实收尾。补严响应4097字节边界和HTTP pre-handler连接收尾；正常run、真实race、vet通过。 |
| [M18](m18_gin_middleware/README.md) | Gin1.12.0真实Next顺序、Abort忘return导致401后多JSON与错误写入；ShouldBind、业务校验、body缓存与严格边界。8个同key请求只1订单；Copy浅拷贝/借用生命周期、同goroutine Recovery与两个真实panic子进程、认证/资源归属/代理、取消后晚写与504均直接观察。正常run、14项真实race测试、vet通过，handler/连接/body/job归零且Serve joined。 |
| [M20](m20_sql_pools/README.md) | MySQL8.4.11/driver1.10.1真实参数写读、NULL/空串、DECIMAL分、UTC微秒、ErrNoRows和Scan错误；Rows未关闭/Tx再借DB导致MaxOpen1真实排队deadline。PROCESSLIST先见UPDATE、已借连接WaitCount0，取消回滚并等服务端连接消失后SELECT仍101、池恢复。主审返工拆Result/Finished，确定性清理gate阻止提前join。修复版全run/5项真实integration race/vet通过；每run表DROP后查询remaining0，DB全0。 |
| [M25](m25_redis_client/README.md) | Redis8.2.8/go-redis9.23真实46条语义断言：TTL -1/-2、丢更新与INCR、Pipeline/MULTI运行时错误/Lua不回滚、INCR→EXPIRE窗口与Lua修复、连接池与ctx取消预算差别。独占真实连接延迟响应时INCR已成功而客户端超时，未盲重放。6项真实race测试通过；独立RESP检查23个精确键全部不存在，任务18/18加入、10个socket归零；warmup错误链已修复。 |
| [M28](m28_rabbitmq_delivery/README.md) | Rabbit4.3.6/amqp091-go1.15真实8组：confirm不等于业务完成、mandatory return312仍可publisher ACK、PublishWithContext取消I/O边界、提前ACK丢业务、提交未ACK断连重投与继续新消息、同ID效果1、有限TTL retry/DLQ与x-death、多条ACK误确认、foreign channel真实406。root全run/3项真实race/vet通过，两轮各删24队列/24exchange；独立rabbitmqctl检查队列/exchange为空、课程用户连接0。 |
| [M29](m29_kafka_offsets/README.md) | Kafka4.3.1/franz1.22.1真实key/value原始字节/partition/offset读回、next位置、业务前commit丢处理、业务后未commit重投、乱序commit跳过低任务与watermark修复。broker确认Stable成员1→2→1，旧generation取消且Done后才交接，迟到效果0。root全core/真实race/vet通过，两份实际JSONL均16尝试/15效果；另一个只读admin确认课程topic/group为空。真实独占stop/start后before/after原始ACK一致、next1/2；B协调超时清理及C提前读marker的时间偏差诚实保留，没有把marker当broker实证。 |
| [M33](m33_resilience_retries/README.md) | 真实HTTP总预算/单次预算、次数、Retry-After、Reader重放、三层12→2倍增、gobreaker半开成功/失败、有限排队与健康路径隔离。主审返工：实际已commit后坏JSON/超限/缺或错201回执均UNKNOWN，返回caller原key GET对账，每例rows1/POST1；空body写不能绕过key规则，可信提交前422仍确定失败。root五组run/13项真实race及6个新子例/vet通过；Result后cleanup gate仍未开放时owner不能提前完成。 |

课程新MySQL/RabbitMQ/Kafka均是独立容器与新named volume，安装者已实际写入、独占重启、读回并精确删除合成对象；Redis保持共享服务运行，故障使用课程独占连接。运行安装锁的竞争/超时/后续取得与稳定inode已有实证。固定镜像digest、资源限额、真实协议和失败修复记录在忽略的安装报告中；公开使用规则见[INFRASTRUCTURE](../learning_prompts/INFRASTRUCTURE.md)。保护的17个DeepHelp容器实际复核名称/镜像tag/State/原健康状态及StartedAt早于安装基线通过，退役MySQL仍退出。当前学习volume无硬磁盘quota，不把retention/日志轮转冒充quota。

## 第 2 层实际验收

| 模块 | 主验收的关键事实与独立复验范围 |
| --- | --- |
| [M16](m16_timers_shutdown/README.md) | 实际Timer Stop/Reset/AfterFunc与已启动callback独立join，真实慢Ticker与synctest时间戳，overlap/serial/skip、8次有限触发。直接复用已验收M15 Pool/Ticket，关闭入站→producer join→drain/abort→worker/ticket→HTTP client关闭。正常HTTP账本A12500/B1800，总14300；deadline返回未知A/B且caller成功0，服务器却已存A12500，实际连接/handler全收尾。15项真实race测试与12子项、vet通过；removed asynctimerchan旧开关实际退出1。直接mod verify遇本地replace的缺ziphash错误，原失败保留；M15源码/模块/文档哈希与验收成果一致，5份历史日志仅CRLF/LF差异，下载依赖sum对应同一已实际校验缓存，不把原工具失败写成成功。 |
| [M19](m19_composition_architecture/README.md) | 真Gin handler→service→repository显式构造，9次同key请求1订单、独立请求状态、冲突/输入/鉴权无额外写；global client实测A/B写入0/1，显式修复1/0。typed nil、未Start、部分资源+error与构造panic清理、实际init早于main的子进程均证明。主审R1补独立Close caller.Done：预览Result后尾部清理仍被门挡住，owner正常/提前失败均真实join，三项原因保留。真实handler强制Close后两次预算耗尽仍active1/repo ready，放门后active0再关仓储。修复后root run/race/vet/依赖校验通过。 |
| [M21](m21_transactions_defer_panic/README.md) | 真实MySQL同Tx库存/订单、正常提交、第二步1062、业务冲突、panic回滚；具名err panic误Commit、遮蔽/旧err、忘Rollback、DB脱离Tx五组真实副作用与修复。已取消Begin、idle事务取消后的新FOR UPDATE、实际带标记SQL锁等待取消均查服务端事实。两次独占COMMIT通信截断都返回invalid connection/UNKNOWN，却分别stock5/orders0与stock4/orders1；原key查账、payload核验，不重放下单。root完整run、五辅助与真实集成race、vet/依赖通过；两精确表不存在、DB全0、代理两方向Done和总Finished均加入。 |
| [M24](m24_schema_queries_migrations/README.md) | 真实NULL/default/空串、DECIMAL分/BIGINT/UTC微秒/emoji/大小写唯一键，FLOAT/DOUBLE/INT损失和严格模式scale警告。1203行实际联合索引/排序/函数/转换/覆盖计划，有界只读ANALYZE实际执行，N+1=11/Join=1结果一致。offset插删重漏、时间tie与可变keyset失败，短同Tx RR修复与事务外当前数据核对。expand/双写/fallback/回填/旧writer门禁/contract，结构在账本缺恢复与重复执行；成功/失败ALTER后Rollback nil仍读20/30，PROCESSLIST实际metadata锁等待后1205。root run、三辅助与两真实集成race、vet/依赖通过；7表与取消路径3表精确不存在、DB全0。 |
| [M27](m27_redis_leases_limits/README.md) | 真实Redis+MySQL 28项：GET后暂停旧A覆写effects2；持久权威行锁串行发号1/2，下游原子条件写拒A并保留B-new/effects1/last_token2/issued2。raw DEL误删B，对照比较DEL/renew0和重复release1/0；续租丢失取消后Result≠Finished且已接受SQL效果仍在。独占真实SET回复门导致UNKNOWN，独立GET证明已执行、不派发业务/重放。12并发allow3/deny9、拒绝不续TTL、身份接口隔离、跨窗口5次样本、ErrClosed/真实AUTH故障策略。两合法租约仍双效果，SQL唯一订单+库存Tx修复1效果。root run/四辅助与真实集成race/vet/依赖通过，17/17任务、6/6socket、13键/2表清理。 |

主 agent 另用独立只读连接核对本层正常/真实race日志中的25张精确M21/M24/M27表全部不存在，并用独立Redis Client查询M27真实race登记的13个精确键不存在。该核对只查指定名字，没有扫描/删除其他数据；它证明实验后的目标收尾，不代替实验运行中库存、订单、迁移或拒旧写的实际断言。

## 第 3 层实际验收

| 模块 | 主验收的关键事实与独立复验范围 |
| --- | --- |
| [M22](m22_mysql_locks_isolation/README.md) | 真 MySQL 超卖、原子条件更新/行锁/CAS守恒，RC/RR快照与当前读，实际索引计划及范围/扫描锁、1213整事务重试、1205仅语句失败后的显式回滚。HTTP内外事务等待的实库后果均核对。主审返工后 root 全run、8项enabled race、vet/依赖通过；真实CREATE后应用确认错误仍登记并清理，占用名字受保护；强制TCP Close与Serve完成后 handler尾部仍挡住owner，放门才收尾，原失败/Shutdown/尾部原因均保留。两轮各4表与DDL专属表精确不存在、DB全0。 |
| [M23](m23_gorm_semantics/README.md) | GORM1.31.2/mysql1.6.0实库零值更新、链式条件污染、用户作用域、callback/manual Tx、吞1062与recover误Commit、外层DB逃离Tx。Preload2与N+1=4、安全查询3及soft-delete持久行核对；hook已confirm的真Rabbit消息不随SQL Rollback撤回，同Tx outbox修复边界明确。root run/6辅助及完整enabled race/vet/依赖通过；阻塞SQL取消后原服务端任务已结束，5表/独占队列不存在、DB全0，AMQP绝对deadline修复有真实pipe辅助证明。 |
| [M26](m26_cache_consistency/README.md) | 真SQL+Redis 44项：错误用户/租户key、旧SELECT回填覆盖新提交、TTL真实到期、版本key与Lua代际拒旧、post-Commit窗口、singleflight键/ctx/共享结果、负缓存/抖动TTL/大DTO/热点/真实AUTH降级及提交后失效失败。root返工复跑run/7辅助及enabled race/vet/依赖通过；16次确定的permit交接使Peak保持2，旧顺序mutation实际失败Peak3。body与drainer最终Done分别加入；32精确键/1表不存在，SQL与Redis关闭通过。 |
| [M30](m30_outbox_idempotency/README.md) | 真SQL+Kafka双写坏顺序、独占Produce ACK截断、原key恢复、outbox与持久inbox、重复事件、offset提前丢效果及精确修复group、v2/v1拒绝和受控对账。root两轮各6次真实独立进程强杀/9次恢复，最终订单/qty11、outbox9全部sent、inbox11、stock/projection99、effects11、broker12。返工补实际1062恢复出口的cleanup/UNKNOWN优先级，8辅助子项与两轮各2次真实1062通过；root明确enabled的全量race123.304s通过，默认SKIP不算实库。另以独立只读入口复查两份登记各5表/11topic/12group及offset为空，30个登记child PID不存在。 |
| [M32](m32_framework_source_reading/README.md) | 真Kratos3.0.0 HTTP/gRPC生成链、具体仓储副作用，typed业务错误、commit前后panic、lost ctx晚写、正确ctx零写、已commit后同ID显式重试双写、真实双向流。forced HTTP Close/App.Run及两个Start完成仍有active handler1，尾部退出/idle后才关仓储；Stop.Result与最终Done分开。root run/5项race/vet/依赖通过；返工后独立执行生成/race成功及真实Go失败的环境三态恢复，共78项断言、15个测试PASS、6次预期配置失败、16运行文件hash不变。Kitex/go-zero只读锁定源码，未宣称运行。 |

本层各模块均保留原失败与返工事实，普通/实库测试的SKIP界限、环境变量缺失/空串/非空三态、提交未知和清理失败不被成功归一化。有限应用owner的退出证据不扩为所有库内部goroutine已回收。M30只做单relay/单分区与SQL副作用；M32业务账本仍是本课进程内库存。已验机制仍需在M37真实综合链和M38故障矩阵中重新证明组合行为。

## 验证和资源边界

每课 README 的命令都以独立 module 为工作目录。并发课实际使用兼容 Windows/amd64 的便携 GCC 16.2.0 进行 race；CC/CXX/CGO/PATH 只设置在命令进程，没有改变系统设置。默认 mingw32 的构建失败保留为历史诊断，不算检测器已执行。必要故障入口用 build tag、显式命令或有界子进程隔离；正常入口和正向断言保持可运行。

汇总时仅修整了少量文档/公开文本输出的行尾空白和额外结尾空行，使 Git diff 检查通过；原字节记录保留在忽略的验收目录。没有改写诊断、数字或命令结果，修整后的文件同步到原 worktree 并更新验收哈希。

取消通知、结果可读、handler 退出、清理完成和 owner join 分别核对。M07/M12/M17/M34 的失败路径曾有收尾缺口，已经返工或主修正并独立复验；没有用自然结束、自报 PASS、HTTP 200、编译成功或全进程 goroutine 数量替代验收。正常验证进程、故障子进程、课程独占 listener、HTTP/RPC Body/连接、任务和临时隧道已收尾。性能 profile/调试产物与原始无秘密诊断在忽略目录保留以便追查，不随课程源文件提交。

基础语言/HTTP/RPC课的账本多数为独占内存或实际文件；M20/M25使用真实SQL/Redis，M28/M29使用真实broker，M21/M24/M27新增真实事务、表结构与Redis/SQL组合，M22/M23/M26/M30运行真实隔离/ORM/缓存/outbox链路。M19的进程内幂等不等于跨进程数据库唯一约束；M27的SQL唯一性实验仅固定载荷、同订单顺序重试，M30下游幂等只覆盖其同Tx SQL效果。前四层的有界协议与失败窗口证据不能外推到后续综合业务、跨服务幂等、云观测、生产SLA或长期稳定性。Kafka单副本确认、同进程文件Sync和当前SQL读回也不证明多节点容灾。

## 后续层

第 0～3 层均已验收。随后依次 M37、M38、M39。只有上一层全部主验收通过并整层提交后才派发下一层。总体终点是39课全部验收、层提交与实验资源收尾。
