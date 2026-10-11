# 课程主验收记录

2026-10-12（Asia/Shanghai），第 0 层的 19 个模块已经由主 agent 阅读实际源码、完整模块 prompt、中文文档与实验记录，并执行必要独立复验。课程由独立 worktree 生成，汇总时逐文件比对验收时的 SHA256；本层 395 个模块文件全部一致。导航和编排文档另行检查。主 agent 按整层提交，提交 SHA 与详细无秘密验收回执保存在本机 `.local/course-orchestrator/`，也可从 Git 历史查看本层中文提交。

当前真实交付为 **19/39**。后续 20 个模块尚未验收，MySQL/RabbitMQ/Kafka 的课程实验尚未在本层完成。模块 STATUS 是执行者的实际实验记录；它的 READY/PASS 文字本身不是主验收依据。

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

## 验证和资源边界

每课 README 的命令都以独立 module 为工作目录。并发课实际使用兼容 Windows/amd64 的便携 GCC 16.2.0 进行 race；CC/CXX/CGO/PATH 只设置在命令进程，没有改变系统设置。默认 mingw32 的构建失败保留为历史诊断，不算检测器已执行。必要故障入口用 build tag、显式命令或有界子进程隔离；正常入口和正向断言保持可运行。

汇总时仅修整了少量文档/公开文本输出的行尾空白和额外结尾空行，使 Git diff 检查通过；原字节记录保留在忽略的验收目录。没有改写诊断、数字或命令结果，修整后的文件同步到原 worktree 并更新验收哈希。

取消通知、结果可读、handler 退出、清理完成和 owner join 分别核对。M07/M12/M17/M34 的失败路径曾有收尾缺口，已经返工或主修正并独立复验；没有用自然结束、自报 PASS、HTTP 200、编译成功或全进程 goroutine 数量替代验收。正常验证进程、故障子进程、课程独占 listener、HTTP/RPC Body/连接、任务和临时隧道已收尾。性能 profile/调试产物与原始无秘密诊断在忽略目录保留以便追查，不随课程源文件提交。

本层的业务账本/库存多数为独占内存或实际临时文件。真实 HTTP/gRPC/Redis/PG 证据不能外推到尚未执行的 MySQL 事务、持久化幂等、消息交付、云观测后端、生产 SLA 或长期稳定性。第 1 层之后将另做真实数据库/消息队列实验，仍使用独占数据、真实驱动与业务结果断言。

## 后续层

第 1 层为 M15/M18/M20/M25/M28/M29/M33；第 2 层为 M16/M19/M21/M24/M27；第 3 层为 M22/M23/M26/M30/M32；随后依次 M37、M38、M39。只有上一层全部主验收通过并整层提交后才派发下一层。总体终点是 39 课全部验收、层提交与实验资源收尾，而不是当前第 0 层完成。
