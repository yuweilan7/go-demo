# M32：沿真实 Kratos 源码读库存请求、错误与停止

接手一个微服务仓库时，先找到一条库存扣减请求：它怎么从网络进入业务，ctx 在哪里派生，库存在哪一行改变，error 如何编码，谁最后关闭资源。本课实际运行 **Kratos v3.0.0 的 HTTP 与 gRPC 服务和客户端**，沿同一份库存业务观察正常请求、业务错误、panic、取消、timeout 和 Stop。Kratos 是本课程教学默认选择；没有关于 Shopee 使用此框架的事实依据。

网络使用本机真实 TCP，所有 listener 的创建参数都是 `127.0.0.1:0`。库存是真实进程内对象的副作用，初始20；没有 SQL、Redis、MQ、持久化或跨实例事务证明。这样可以把阅读精力放在框架边界，接入数据库时仍须业务自己设计事务和幂等。资料 `files/开发框架` 只列出阅读方向，不能据此宣称框架性能排名、自动事务或自动发现已生效。

## 10～20 分钟核心入口

从仓库根目录执行；`-C` 将 Go 命令切进这个独立 module，根 `go test ./...` 不会跨嵌套 module。

```powershell
go -C lessons/m32_framework_source_reading run ./cmd/demo
go -C lessons/m32_framework_source_reading test -v -count=1 -timeout 90s ./...
go -C lessons/m32_framework_source_reading vet ./...
go -C lessons/m32_framework_source_reading mod verify
```

先用2分钟读“最小规则”；接着运行入口，看 E01 的6条事件并沿调用地图查符号；再看 E03/E04 的坏例与修复；最后对照 E07 的两条 OBSERVE 和最后 ASSERT。所有原理、反例与修复在本课文档和源码内，不需要额外 exercise。

本次最终业务摘要是：

```text
PASS stock=10 lost_ctx_writes=1 propagated_writes=0 committed_and_retry_writes=2 stream_messages=2 handlers_joined=true connections_closed=true
```

完整真实输出见 [demo.txt](evidence/demo.txt)，测试及 race 见 [test.txt](evidence/test.txt)、[race.txt](evidence/race.txt)，实际范围见 [STATUS.md](STATUS.md)。日志里的端口、日期、线程执行交错属于本次观察。库存数、请求写入次数、协议错误、屏障的先后和资源关闭由实际断言检查。

## 先掌握这些 Go 规则

`app.New`、`service.New`、`biz.New` 和 `data.New` 都是普通构造函数，没有自动扫描与 Bean 生命周期。Root 持有一份 `*data.Memory`；把它赋给 `biz.Repository` 只复制接口值，动态类型是 `*data.Memory`，动态值仍是同一个指针。并发 HTTP/gRPC 请求会借用同一仓储，框架不会自动串行，库存临界区必须由仓储的 `sync.Mutex` 保护。这里没有用每层一个空接口来模拟 Spring 层数。

`Config` 以值传递，字符串和 Duration 是值快照，`Tail` 是故障 fixture 共享指针。`biz.Request` 只有字符串/int32，复制后不共享可变数组或 map；`*pb.ReserveRequest` 是一个请求的解码对象，service 转为业务值，业务层不保存 HTTP wrapper/stream/连接。未来若添加 slice/map，必须重新检查别名；“struct 是值”不等于深复制所有成员。

`func(*Server)` 是可调用的函数类型；`khttp.Timeout(d)` 返回捕获 d 的闭包，`NewServer(opts ...ServerOption)` 同步执行这些闭包修改具体 Server。`...` 表示可变参数，`middlewares...` 把 slice 元素作为参数展开。options 本身没有创建任务，也不证明 timeout、重试、缓存或发现已经产生实际行为。实际行为要继续追到使用字段的代码，再用实验观察。

`middleware.Handler` 是 `func(context.Context, any) (any, error)`，`Middleware` 是 `func(Handler) Handler`。`Chain(a,b,c)(handler)` 是 a 包 b 包 c 包 handler，同一个请求 goroutine 同步进入，再逐层返回。`any` 就是 `interface{}` 的别名；生成代码的 `req.(*ReserveRequest)` 是动态类型断言，依赖该 route/RPC 解码的消息契约，而不是在所有地方随意断言都安全。详细机制见 [Go 接口与函数规范](https://go.dev/ref/spec#Interface_types) 和 [锁定版 middleware.Chain](https://github.com/go-kratos/kratos/blob/v3.0.0/middleware/middleware.go)。

`ctx` 传值只复制接口，底层取消/deadline/Value 关系仍共享。`context.WithValue(ctx, tenantKey{}, "m32")` 增加本次可信身份，继续沿父 ctx 的取消范围；`Background()` 重新起根则丢失这条关系。`cancel()` 是通知，没有强行停止 goroutine、自动等待或者撤销库存。等待通知、观察通知、业务退出、transport 退出、owner 收尾完成是不同事实。

`:=` 在当前作用域声明变量；`if err := ...` 的 err 只属于该 if。本课会将普通变量和具名返回变量明确区分，例如 `Run(...)(summary Summary, err error)` 的 return 先给 err 赋值，随后 defer 才把 Close 错误 Join 进去。`defer close(Done)` 最先登记，因 LIFO 最后执行；只有最后 Done 才能发布完整收尾结果。给容量1的 Result 发值只是结果已进缓冲，不证明 defer 已执行。[CancelFunc 的契约](https://pkg.go.dev/context#CancelFunc)明确区分取消通知与等待工作结束。

现在必须理解：生成注册、函数 options、middleware 顺序、ctx 传播、库存写点、显式协议错误、recovery 作用域、Stop 与 handler owner。可以延后：Wire/Fx provider graph、配置热更新、完整 selector 算法、HTTP2 窗口、生产认证/TLS、远端 registry、SQL 事务。本课保留 M31 的小流实验，M31 已有的 schema 兼容性/背压实验不再复制。

## 前置契约与版本

已读 M19 的 [STATUS](../m19_composition_architecture/STATUS.md)、[业务小接口](../m19_composition_architecture/internal/order/service.go)以及 README 的 root/Close 调用图；复用“入口显式装配、使用者定义小接口、root 拥有资源、独立 Background 收尾、观察真实 handler”。M19 的订单 owner/key 幂等不是本课库存 ID 的语义，不能把两种业务混为同一个接口。

已读 M31 的 [STATUS](../m31_grpc_protobuf/STATUS.md)、[proto](../m31_grpc_protobuf/proto/inventory/v1/inventory.proto)、[库存实现](../m31_grpc_protobuf/internal/lesson/service.go)。保留 `course.inventory.v1.Inventory` 的 Reserve/Inspect/Watch 方法和原字段编号；`001-SKU` 初始20；quantity 为正；SKU 不存在→NotFound；库存不足→FailedPrecondition 和 typed Conflict；request_id 只追踪，无去重；提交前 panic/error 不写；丢ctx可以晚写；正确传播取消不写；响应超时不能撤销已写；半关闭发送后仍能接收结果。增加 HTTP annotation 和 `PANIC_AFTER_COMMIT=5` 是本课的清晰增量。

前置业务在 `internal` 包，不能直接跨 module 导入。复用方式是契约与本课最小库存适配，不复制整个前置项目，不修改它们，不建 go.work/replace 或循环导入，不重跑它们全部实验。本 worktree 基线已由主 agent 集成前31课，最终验收记录仍由主 agent 持有。

| 工具/库 | 实际固定版本 | 用途/核对方式 |
| --- | --- | --- |
| Go | go1.27.0 windows/amd64 | module `go 1.27.0`；无额外 toolchain directive |
| Kratos runtime | `github.com/go-kratos/kratos/v3 v3.0.0`，最低Go1.25.0 | 实际 `go list -m` 和 module cache 源码；不是main分支混版API |
| gRPC-Go | v1.84.0 | 真正的TCP gRPC、标准 status、泛型双向流 |
| Protobuf-Go / protoc-gen-go | v1.36.12 | schema runtime 与生成插件 |
| protoc-gen-go-grpc | v1.6.2 | 生成 `InventoryClient/Server/ServiceDesc` |
| 官方 HTTP 插件 | `protoc-gen-go-http/v3@v3.0.0-20260626125723-668db92c2c00` | 二进制固定提交；生成头自报 v3.0.0，二者含义不同 |
| protoc | release v36.2，`libprotoc 36.2`，生成头 v7.36.2 | 官方win64归档；插件和protoc都落本模块 `.local/` |
| Kitex / go-zero | v0.16.4 / v1.10.3 | 只下载并读锁定版官方源码，没有加入本课runtime或运行服务 |

真实版本/sum见 [versions.json](evidence/versions.json)，源码文件/symbol/哈希见 [source-symbols.txt](evidence/source-symbols.txt)。本次在线查询 v2 最新为 v2.9.2，v3 最新为 v3.0.0；明确选择已实际运行的 v3，旧公司项目应按其 go.mod 阅读，不能自动升级。[Kratos v3 官方仓库入口](https://github.com/go-kratos/kratos/tree/v3.0.0)列出了此版本的transport和生成布局。

## 文件和阅读顺序

| 文件 | 真实职责与阅读符号 |
| --- | --- |
| [cmd/demo/main.go](cmd/demo/main.go) | `main → run → lab.Run`；总预算15秒，run defer完成后main才可能退出 |
| [proto/inventory/v1/inventory.proto](proto/inventory/v1/inventory.proto) | IDL、字段编号、HTTP annotation；不是运行时注册器 |
| [gen/inventoryv1/inventory.pb.go](gen/inventoryv1/inventory.pb.go) | protobuf消息、descriptor；不手改生成代码 |
| [gen/inventoryv1/inventory_grpc.pb.go](gen/inventoryv1/inventory_grpc.pb.go) | `NewInventoryClient`、`RegisterInventoryServer`、`_Inventory_Reserve_Handler`、`Inventory_ServiceDesc` |
| [gen/inventoryv1/inventory_http.pb.go](gen/inventoryv1/inventory_http.pb.go) | `RegisterInventoryHTTPServer`、`_Inventory_Reserve0_HTTP_Handler`、`InventoryHTTPClientImpl.Reserve` |
| [internal/app/app.go](internal/app/app.go) | `DefaultConfig/Validate/New/Start/Close`；真实框架、中间件、handler账本的接线 |
| [internal/app/ownership.go](internal/app/ownership.go) | `activeOwner`、`observedTransport`、`Task`、`StartOwnedTask/Join`；结果与收尾分开 |
| [internal/service/inventory.go](internal/service/inventory.go) | `Reserve/Inspect/Watch/protocolError`；生成消息与业务输入、error的适配 |
| [internal/biz/inventory.go](internal/biz/inventory.go) | `Repository`、`Usecase.Reserve`、`Plan/wait`；语义、ctx、明确故障屏障 |
| [internal/data/memory.go](internal/data/memory.go) | `Memory.Commit/Inspect/Snapshot/Close`；唯一库存副作用与Mutex |
| [internal/lab/scenarios.go](internal/lab/scenarios.go) | E01～E07的有界驱动、实际网络client、屏障与独立事实断言 |
| [internal/lab/client.go](internal/lab/client.go) | `newClients/clientMiddleware/close`；Ready观察、header、连接与Transport owner |
| [internal/lab/scenarios_test.go](internal/lab/scenarios_test.go) | 5项独立测试；全链、并发不超卖、已取消owner、未Start关闭、最终错误链 |

import 方向是 `app → service/biz/data`、`service → biz/gen`、`data → biz`、`biz → trace/stdlib`；lab驱动它们，没有业务反向依赖lab。Repository接口调用具体Memory不需要biz反向import data。Root是构造能力集合，业务接口是借用能力集合，两者职责不同。

## 一条真实 HTTP 请求的完整地图

以下文件/符号全部在实际锁定源码中重新核对；第三方目录通过 `go list -m -f '{{.Dir}}' github.com/go-kratos/kratos/v3` 找到，而不是把本机缓存路径硬写成人人相同。

| 顺序 | caller → callee | ctx/对象/返回与证明 |
| --- | --- | --- |
| 装配1 | `app.New → Config.Validate → data.New → biz.New → service.New` | 配置先验，repo具体指针创建后才转小接口；单一共享仓储 |
| 装配2 | `net.Listen → khttp.NewServer/kgrpc.NewServer` | 每个listener真实绑定动态loopback；root持有外部句柄，未Start也必须关闭 |
| 注册 | `RegisterInventoryHTTPServer → Server.Route → Router.Handle` | annotation生成POST与GET；handler闭包保存service接口动态指针 |
| 启动 | `Root.Start → Task → App.Run → observedTransport.Start → http.Server.Start → net/http.Server.Serve` | App errgroup拥有Start/Stop与signal waiter；root另有最后RunOwner.Done |
| 客户端1 | `InventoryHTTPClientImpl.Reserve → http.Client.Invoke → invoke` | protobuf作为args，固定Content-Type/Accept/Operation；caller ctx进入实际HTTP request |
| 客户端2 | `clientMiddleware → transport.FromClientContext → next → http.Client.Do` | 写课程header，等待网络；共享独占Transport属于clients |
| 服务端1 | `net/http → httpOwnerFilter → gorilla/mux → Server.filter` | outer filter登记active；框架filter从req.Context派生200ms预算、构造transport、defer cancel |
| 服务端2 | `Router.Handle → wrapper.Reset → generated _Inventory_Reserve0_HTTP_Handler` | 分配当前wrapper，Bind protobuf；生成代码SetOperation，再取Middleware |
| 中间件 | `wrapper.Middleware → middleware.Chain → serverMiddleware → recovery.Recovery → authenticate` | 同goroutine嵌套；transport header变可信tenant ctx；Recovery明确安装，覆盖其内层panic |
| 业务 | `service.Reserve → Usecase.Reserve → Repository.Commit` | 解码指针转业务值；ctx贯穿；接口动态分派到真实Memory，普通同步等待 |
| 副作用 | `Memory.Commit → mu.Lock → available-=qty; writes[ID]++` | 锁内ctx检查/库存检查/写次数；这一行改变库存，HTTP编码前已经发生 |
| 返回 | `Commit → Usecase → service → middleware → generated ctx.Result(200,reply)` | 成功完整reply；error走 `protocolError`，不会伪装为success |
| HTTP错误 | `Router.Handle` 读handler error `→ DefaultErrorEncoder → errors.FromError → 编码/WriteHeader` | 库存不足412；ErrorInfo提取reason/metadata，HTTP不保留任意typed Any详情 |
| 客户端完成 | `DefaultResponseDecoder/DefaultErrorDecoder → invoke defer res.Body.Close → clientMiddleware返回` | Body归Invoke；clients.Close在所有调用join后关闭resolver和独占Transport idle池 |
| 最终收尾 | `Root.Close → StopNotification → JoinFramework → active.wait → Memory.Close` | gate全部释放；停止独立预算；实际handler完全退出后再关仓储 |

[HTTP server.go](https://github.com/go-kratos/kratos/blob/v3.0.0/transport/http/server.go) 的 `filter`/`Start`/`Stop`，以及 [HTTP client.go](https://github.com/go-kratos/kratos/blob/v3.0.0/transport/http/client.go) 的 `Invoke/invoke/Close` 是这条链的主要第三方定位点。本机对应行号分别在source-symbols记录；读者换版本后应重新定位符号。

gRPC前半段改为：`pb.inventoryClient.Reserve → ClientConn.Invoke → Kratos unaryClientInterceptor → middleware client header → grpc invoker → TCP/HTTP2 → _Inventory_Reserve_Handler → Kratos unaryServerInterceptor → root.grpcOwnerInterceptor → generated handler closure → service.Reserve`。后半段仍是同一Usecase/Memory。客户端 `WithNoProxy` 只作用该loopback连接，`direct:///` 由Kratos direct resolver解析，避免把本地课堂请求交给系统HTTP代理。

gRPC `unaryServerInterceptor` 位于 [interceptor.go](https://github.com/go-kratos/kratos/blob/v3.0.0/transport/grpc/interceptor.go)：先 Merge(网络请求ctx, baseCtx)，再加入Transport和200ms预算，然后匹配同一middleware链。生成 `_Inventory_Reserve_Handler` 会先decoder填消息，再把FullMethod/handler交给interceptor。最终gRPC读取返回error的GRPCStatus，protobuf reply与status分别编码；业务typed Conflict由status详情承载。它不是“返回error就等于Java异常自动捕获”。

## ctx 的来源、等待与释放

| ctx | 谁创建、谁取消 | 本课实际使用与边界 |
| --- | --- | --- |
| 总实验ctx | cmd.run，15s，defer cancel | 限制所有观察/请求；失败收尾不沿用它 |
| base业务根 | app.New `WithCancel(Background)`，BeforeStop显式cancel | 传入Kratos opts.ctx，HTTP BaseContext与gRPC baseCtx借用它 |
| App内部ctx | Kratos.New从opts.ctx派生，App.Stop调用a.cancel | 驱动errgroup Stop watchers；框架Start收到octx（opts.ctx），不等于a.ctx |
| HTTP请求ctx | net/http从BaseContext派生；Server.filter再200ms WithTimeout | 客户断开/服务端预算/业务根取消可被业务观察；它不强制打断忽略ctx的函数 |
| gRPC请求ctx | gRPC网络ctx，Kratos Merge与baseCtx，再200ms timeout | Merge的内部wait监听两父ctx或cancelCh；interceptor defer cancel发通知 |
| client请求ctx | 实验或client options的2s预算；坏/提交丢响应课例100ms | 更早的deadline获胜；响应超时不能反推服务端未提交 |
| 丢失ctx保险 | Usecase坏例重新Background再2s预算 | 丢tenant/deadline/cancel；仅为有限坏例，不能恢复正确业务归属 |
| 最终清理ctx | 各owner从Background新建3/4s | Join客户端最后Done、业务Done、真实handler、框架Start与Run，不被旧Canceled截断 |

Merge源于 [internal/context/context.go](https://github.com/go-kratos/kratos/blob/v3.0.0/internal/context/context.go)：取更早deadline，Value先查网络ctx再查baseCtx，必要时创建内部wait goroutine。它的cancel关闭cancelCh；源码没有暴露内部goroutine的专属最终Join API，因此本课不会用“总goroutine数回到某数”声称观测了所有库内部指令。我们按公开生命周期调用，实际join自己创建的Task与业务handler，有限CLI最后进程退出。App.Run的signal waiter由errgroup等待结束，但此版本源码的signal.Notify注册没有显式signal.Stop；这也不该被本课包装成长期多App生产资源保证。

## E01/E02：成功、业务错误和panic的日志解析

E01-grpc的日志001～006是 `client.enter → server.enter → usecase.enter tenant=m32 → repository.commit → server.exit code=200 → client.exit error=false`。HTTP接着出现相同六步。第1次qty2使20→18，第2次qty1使18→17；reply.remaining、request_id、后续真实HTTP Inspect及仓储快照同时检查。`server.exit code=200` 是Kratos错误映射的成功类别，不是声称gRPC底层使用HTTP200表达业务成功。

E02-stock-grpc请求99，日志没有repository.commit，server.exit为412/STOCK_INSUFFICIENT。网络gRPC是FailedPrecondition，typed `Conflict{requested:99,available:17}` 与HTTP412/reason/metadata都被检查。这个映射来自 [HTTP/GRPC status转换](https://github.com/go-kratos/kratos/blob/v3.0.0/transport/http/status/status.go)，不能把409/Aborted和412/FailedPrecondition随意混用。`protocolError`明确把普通业务错误映射到可预期协议；包装的ctx错误由`status.FromContextError`保留Canceled/DeadlineExceeded。

quantity=0→InvalidArgument、未知SKU→NotFound、缺课程身份→Unauthenticated；每个实际执行后都逐ID检查writes=0。SKU校验/库存检查由业务与仓储完成，框架只传递/编码结果，不知道“库存不足”是什么。

panic-before的日志先usecase.enter、后server.exit500/UNKNOWN，没有commit。Root明确安装官方Recovery，采用默认handler返回UNKNOWN；本课程传入io.Discard的Recovery日志writer以避免反复打印合成堆栈，恢复行为与默认错误handler仍来自官方代码。[Recovery 源码](https://github.com/go-kratos/kratos/blob/v3.0.0/middleware/recovery/recovery.go)在同goroutine defer里recover；HTTP Filter自身panic、另一个手写goroutine的panic不属于这个作用域。

panic-after则在日志058发生repository.commit，再返回500/UNKNOWN；fresh Inspect确认17→16，该ID的writes=1。Recovery保护服务继续处理后续请求，但不会撤销库存。Java的异常事务拦截器经验在此失效：Go的panic不是自动rollback信号，Kratos Recovery也没有业务事务范围。修复业务正确性需要在事务层安排提交/回滚或幂等，不是给Recovery改一条文案。

## E03/E04：坏ctx与修复的时间线

| 步骤 | 参与者/等待点 | 实际状态、下一步与证明 |
| --- | --- | --- |
| 1 | owner先 `Usecase.Install`，再启动client Task | Started/Gate/Committed/Done都未关闭；handler不会先于计划安装 |
| 2 | gRPC handler `Usecase.Reserve` | 关闭Started；坏例wait.lost-context，tenant变空；owner观察Started后才推导请求进入 |
| 3 | client在真实RPC等100msdeadline | client.exit error=true，Task最后Done；网络结果为DeadlineExceeded |
| 4 | 服务端仍等待p.Gate/错误新根ctx | owner非阻塞检查p.Done未关闭，不把客户端Done当服务端Done |
| 5 | owner Release(p.Gate) | 坏例Commit忽略旧请求取消，repository.commit；16→14、writes=1 |
| 6 | Usecase取消保险timer、记录cleanup、最后close(p.Done) | owner等业务Done；middleware随后返回成功，已来不及恢复客户端响应 |
| 7 | fresh HTTP Inspect + Snapshot | 两者都确认14；不是以旧deadline结果推断“没扣库存” |

对应本次日志：066～070先出现client错误，071才commit，072 cleanup、073 server.exit200。073和074 ASSERT之间的顺序不是库保证：p.Done只表示Usecase结束，外层middleware还可能在运行；最终Root handler账本覆盖外层退出。

修复是同一业务wait使用传来的ctx，Commit在锁内再检查ctx.Err。E04-cancel先观察Started后主动cancel：client Canceled，服务端cancel.before-commit，writes=0；gRPC与HTTP server timeout课例不设100msclientdeadline，真实200ms框架预算到达后wait观察ctx.Done，分别返回DeadlineExceeded/HTTP504，writes=0。日志082是等待点，084是观察取消，不是cancel函数强行切断那行代码。服务端取消观察与客户端error谁先打印可以变化。

坏例在两处明确标注：`Usecase.Reserve`的新Background以及`Memory.Commit(honorContext=false)`。只修wait、不修写点会在门与取消同时ready的select窗口继续错误写入。好版本锁内检查限制“已经观察到取消后的新提交”，不能承诺取消一定先于一个同时进行的提交，更不能撤销已提交事实。

## E05/E06：响应丢失、显式重试与流

E05先等真实Committed屏障，证明qty2已经把14→12，再等100msclientdeadline。原请求取消后Usecase已退出，owner移除旧计划，用同request_id发送第二次NORMAL。第二次真实扣2，库存12→10，writes=2。这是实验明确写出的应用重试，client未安装retry middleware，gRPC还显式WithDisableRetry；没有拿连接重建、配置字段或trace ID冒充自动业务重试生效。错误结果的状态未知需要查询事实，生产库存扣减应有明确幂等键与事务/唯一约束策略。

E06只保留M31接口的2条小流验证：Send一个WatchRequest，CloseSend，Recv得到sequence1/2与available10，再读EOF。流server先Recv、同步两次Inspect/Send，最后Recv看到客户端半关闭。CloseSend不关闭接收半边，Send返回也不证明客户端处理完成。小流ctx限定2秒，defer cancel，Root Close最后等待真实库存stream interceptor退出。流认证限定Inventory/Watch；框架health.Watch由health lifecycle负责。

## E07：Stop结果、TCP停止与handler真正结束

此实验另建一套动态HTTP/gRPC fixture，库存重新20，和前面独立。`/owned-tail`标准HTTP handler写Content-Length=4、body="tail"并Flush；客户端可以读完Body，但outer filter的defer等Tail.Gate。它是显式放大的退出窗口，业务defer仍未完成，不能用HTTPBody读完推断handler已结束。

| 时间 | owner/当前函数 | 状态/证明 |
| --- | --- | --- |
| 1 | rawclient Task `Do → ReadAll → Body.Close → 最后Done` | HTTP200+完整body被验证；filter已进入Returning屏障，active=1 |
| 2 | Stop caller Task调用 `App.Stop` | BeforeStop封账、取消业务根；Stop返回nil发送Result，caller自己的tail defer仍等另一道门 |
| 3 | owner读Result，检查StopTask.Done未关闭 | 明确 `close-caller.Done=false`；不会把缓冲结果当caller清理完成 |
| 4 | App内部Stop watchers运行HTTP Shutdown与gRPC GracefulStop | HTTP handler仍尾部等Gate，80ms stop预算结束；HTTP Stop强制Close TCP |
| 5 | owner JoinFramework | App.Run.Done和两个Start.Done已到；实际尝试连接这两个自有TCP地址都失败，但handler.Done仍false/active1/repo未关 |
| 6 | owner释放Stop caller门、Join其Done，再Root.Close释放handler门 | filter记录defer.released→Tail.Done→active.leave；root等idle后才Repository.Close |
| 7 | 最后ASSERT | caller.Done、handler.Done、active0、repo.closed、所有本实验手写Task均Join |

实际日志中的关键证据：

```text
OBSERVE App.Stop.result=nil close-caller.Done=false handler.active=1
WARN [HTTP] server couldn't stop gracefully in time, doing force stop
OBSERVE App.Run.Done=true transport.Start.Done=true TCP.closed=true handler.Done=false repo.closed=false
handler.defer.released
ASSERT caller.Done=true handler.Done=true active=0 repository.closed=true all-owned-tasks-joined=true
```

`App.Stop`在 [app.go](https://github.com/go-kratos/kratos/blob/v3.0.0/app.go) 执行hooks/注销/取消，不等所有transport。`App.Run`的errgroup最后等待各Start/Stop；Stop watcher用WithoutCancel(octx)建立独立stop预算。HTTP Stop用Shutdown，超时后Close并返回Close结果，因此返回nil可能已经走了强制关闭；本课直接捕获warn与handler状态，不把nil当“优雅结束”。[net/http Shutdown/Close契约](https://pkg.go.dev/net/http#Server.Shutdown)与[本次标准库文档](evidence/stdlib-contracts.txt)说明连接与handler等待边界。

gRPC Stop关闭admin/health，创建一个等GracefulStop的内部goroutine；stop预算到达后调用Server.Stop，源码这个分支没有再次显式等内部done。本课正常gRPC请求/流都已结束，观察了正常Stop；没有声称实测了被阻塞业务的gRPC强制Stop分支。未来该分支同样要观察自己的业务handler owner，不能把连接Stop当副作用已经停止。

`activeOwner`的关键修复是封账+idle：请求进入时count从0变1创建新idle，真正退出时count回0关闭idle；Stop先seal，之后请求拒绝，才能安全用idle等待。仓储只能在idle后Close。只用Server.Serve返回、TCP不通或者p.Done都少了一层。若一开始就用已Canceled的owner ctx执行Join，会立刻返回而遗漏业务尾部，所以所有正常/提前失败路径另建Background收尾预算。

## 每种资源与goroutine的owner

| 对象/任务 | 创建者、等待点 | 停止与最终join |
| --- | --- | --- |
| Root的两个listener/Memory | app.New，Start前已经持有真实句柄 | Root.Close；未Start也精确关listener，忽略已关的net.ErrClosed，其他close错误Join |
| App.Run Task | Root.Start；内部errgroup等待transport/signal waiter | App.Stop通知；Root.JoinFramework等Task最后Done，不读Result当完成 |
| observedTransport.Start | Kratos errgroup；实际HTTP Serve/gRPC Serve | 官方Stop；装饰器最后defer发布Done，仅观察、不重写transport |
| HTTP/gRPC库存请求/流 | 官方网络server；业务wait、锁、stream Recv/Send | 请求ctx/根ctx取消或Gate释放；outer账本leave，Root等idle；框架正常Stop还等待自己的请求生命周期 |
| E03/E04/E05客户端Task | 每个scenario；真实generated client call | 该scenario的cancel与独立Join；早退也Release Gate/等business.Done |
| E07 rawclient Task | StopExperiment；Do/ReadAll | requestCancel、Body.Close、最后Task.Done；独占Transport在全部任务join后CloseIdleConnections |
| E07 Stop caller Task | StopExperiment；Result以后tail defer等Gate | 正常/早退均释放callerTail、独立Join；实验先断言它未完成再释放 |
| Plan信号 | 实验Install；业务关Started/Committed/最后Done | Gate只由实验Release/Root.ReleaseAll关；Remove不删除已写入事实 |
| Task收尾错误 | StartOwnedTask的defer cleanup | Result只有work错误；解除tail后cleanup错误Join入final，最后Done后Join才能取完整链 |
| gRPC ClientConn / HTTP Client+Transport | newClients，Ready/真实请求 | clients.close一次；gRPC观测SHUTDOWN；HTTP Invoke自己Close每个Body、Close resolver之外另关独占idle池 |
| 生成工具子进程 | generate.ps1 Invoke-Bounded | 有限WaitForExit、输出管道异步读取、超时Kill子树并Wait、finally Dispose；GOBIN保存存在性与精确原值，finally恢复三态 |

框架自己的内部goroutine根据公开生命周期和源码责任分析，未通过外部不可访问的Done做逐条动态观测；本课不会宣称进程中所有runtime/信号/库goroutine精确为零。手写Task的唯一Done、业务handler账本和自有TCP关闭则都有实际断言。

## 两次真实建连失败怎样定位与修复

首次记录在 [demo-initial-failure.txt](evidence/demo-initial-failure.txt)：listener日志存在，但Ready观察15秒到期，业务请求未发。增加明确direct地址和此连接WithNoProxy后仍失败，第二次记录见 [demo-health-auth-failure.txt](evidence/demo-health-auth-failure.txt)。因此不能把第一次失败直接归因于代理；listener启动也不能当业务成功。

沿client.go看默认service config包含healthCheck；沿server注册看health service；再沿stream interceptor看所有流共用入口。最初课程认证也要求health.Watch携带课程tenant，health client不会带此业务header，健康状态无法Ready。修复在 `streamOwnerInterceptor` 用生成的 `Inventory_Watch_FullMethodName` 限定业务认证/账本，其他框架流交给默认health lifecycle。修复后同一NewClient→WaitForStateChange真实Ready→实际Inventory调用→完整库存断言全部通过。

这是框架源码阅读应有的顺序：连通状态→默认治理链→真正生成/注册方法→业务中间件作用范围，而不是看见TIMEOUT就无限重试或取消健康检查来隐藏原因。本课没有把这个坏认证配置再次开放到常驻服务，只保留实际失败日志与修复代码。

## Kitex / go-zero：迁移时读哪个签名与生命周期

这里只读官方锁定源码，不运行另外两个框架，不做排行榜。源文件与哈希都在source-symbols；以下签名是读过的真实API，不能把上层生成代码的名字凭空当成底层函数。

| 角度 | Kratos v3.0.0（本课实跑） | Kitex v0.16.4（源码阅读） | go-zero v1.10.3（源码阅读） |
| --- | --- | --- | --- |
| 最小client签名 | `grpc.NewClient(ctx, ...ClientOption) (*grpc.ClientConn,error)`，再生成client.Reserve | `client.NewClient(*serviceinfo.ServiceInfo,...Option)(Client,error)`；`Client.Call(ctx,method,request,response) error` | `zrpc.NewClient(RpcClientConf,...ClientOption)(Client,error)`；`Conn()`给生成gRPC client |
| 最小server签名 | `NewServer(...ServerOption) *Server`，生成RegisterInventoryServer/HTTPServer | `server.NewServer(...Option) Server`；生成NewServer用ServiceInfo+handler注册 | `zrpc.NewServer(RpcServerConf,RegisterFn)(*RpcServer,error)`；RegisterFn实际是 `func(*grpc.Server)` |
| handler链 | generated handler→Kratos统一middleware→service | ServiceInfo MethodInfo→`buildInvokeChain/unaryOrStreamEndpoint/invokeHandleEndpoint`→IDL handler | register callback→标准gRPC ServiceDesc；`setupUnaryInterceptors`按配置装trace/recover/breaker/timeout |
| ctx/重试定位 | `unaryClientInterceptor/unaryServerInterceptor`、Merge；本课应用显式重试，未安装retry/registry | `kClient.Call`在RetryContainer nil时单次eps，否则 `WithRetryIfNeeded`；server `EnableContextTimeout`条件装serverTimeoutMW | `UnaryTimeoutInterceptor`派生ctx并另开handler goroutine，select done/panic/ctx.Done；timeout返回不证明内部handler已退出 |
| 生命周期 | App.Run errgroup；App.Stop通知；transport.Start/Stop | `server.Run → remotesvr.NewServer/Start → waitExit → Stop`；Stop Once执行hooks/注销/remote Stop→transport Shutdown；client.Close有close callbacks | `RpcServer.Start → internal.rpcServer.Start → net.Listen/Register/Serve`；proc.AddShutdownListener调用GracefulStop；公开RpcServer.Stop在此版本只 `logx.Close()` |
| 迁移要保留的业务责任 | 事务/幂等/取消后事实查询/实际handler join | 核对错误分类、RPCInfo生命周期、retry是否重新派发以及副作用幂等；没有实跑它的治理策略 | 核对配置是否实际开启、timeout worker是否还在工作以及proc关闭路径；不能照搬Kratos Stop语义 |

Kitex签名来自 [client/client.go](https://github.com/cloudwego/kitex/blob/v0.16.4/client/client.go) 与 [server/server.go](https://github.com/cloudwego/kitex/blob/v0.16.4/server/server.go)，治理options来自 [client/option.go](https://github.com/cloudwego/kitex/blob/v0.16.4/client/option.go)。生成层还会给每个IDL方法加typed request/response与callopt；本次只读到仓库中的官方旧生成fixture，其头为Kitex v0.9.1，不能把它冒充v0.16.4生成实证。此课未装/运行Kitex generator。

go-zero签名/启用路径来自 [zrpc/server.go](https://github.com/zeromicro/go-zero/blob/v1.10.3/zrpc/server.go) 和 [zrpc/client.go](https://github.com/zeromicro/go-zero/blob/v1.10.3/zrpc/client.go)，实际停服入口在 [internal/rpcserver.go](https://github.com/zeromicro/go-zero/blob/v1.10.3/zrpc/internal/rpcserver.go) 与 [core/proc/shutdown.go](https://github.com/zeromicro/go-zero/blob/v1.10.3/core/proc/shutdown.go)。尤其公开Stop函数名不能证明它执行transport关闭；要沿实现找到proc listener。`UnaryTimeoutInterceptor` 的独立handler goroutine与Kratos同步middleware不同，见 [timeoutinterceptor.go](https://github.com/zeromicro/go-zero/blob/v1.10.3/zrpc/internal/serverinterceptors/timeoutinterceptor.go)。这些是源码推导，没有宣称其实际运行效果。

## 从陌生框架定位ctx、retry与SQL

先用一个真实方法名/FullMethod做锚点：本课从 `Inventory_Reserve_FullMethodName` 找生成client/handler、Register、app.New，再读实际版本transport。ctx路径逐跳看：谁WithTimeout/WithCancel，参数是不是原ctx，哪里改成Background，等待ctx.Done还是只等channel，哪个defer取消，谁最后Join。不要在中间一跳只见 `ctx` 名字相同就认定传播正确；E03同名变量就丢了父关系。

retry从实际client Call/Invoke继续读中间件与invoker循环，核对启用条件、attempt数、err分类、总预算和已经派发后的副作用。Kitex的RetryContainer条件分支就是示范：配置对象存在不证明某方法走了重试；必须看实际调用条件和请求次数。本课只有E05显式第二次调用，断言writes2。

SQL从Usecase持有的Repository动态实现继续跳到实际写点。在本module执行下面搜索，现有数据实现只有Memory.Commit，没有SQL/Tx字段，因此不能宣称框架给了事务：

```powershell
rg -n 'Reserve|RegisterInventory|Commit|WithTimeout|Background|WithDisableRetry' lessons/m32_framework_source_reading
rg -n 'ExecContext|QueryContext|BeginTx|sql\.DB|sql\.Tx' lessons/m32_framework_source_reading/internal
```

接公司数据库实现时，继续确认哪个`*sql.DB`创建/Close，BeginTx返回哪个`*sql.Tx`，后续ExecContext究竟借DB还是Tx，提交/回滚和Rows.Close由谁拥有，ctx是否同一请求，以及响应丢失时怎样查询已提交事实。框架transport/middleware不是事务proxy；M20/M22已有数据库契约应按真实实现复用，而不是为“biz/data结构”在此新增无意义SQL空壳。

## 生成与验证复跑

模块包含三个官方插件生成文件；直接run/test不需要protoc。想重新生成时，首次执行 `-Install`，工具只落本模块忽略的 `.local/`。脚本只临时改变当前进程GOBIN，并在finally按存在性与精确原值恢复；不写用户/机器环境变量。protoc annotation输入来自实际锁定Kratos的third_party；插件参数的module前缀保证输出落 `gen/`。

```powershell
pwsh -File lessons/m32_framework_source_reading/scripts/generate.ps1 -Install
pwsh -File lessons/m32_framework_source_reading/scripts/generate.ps1 -Check
pwsh -File lessons/m32_framework_source_reading/scripts/source-map.ps1
pwsh -File lessons/m32_framework_source_reading/scripts/test-race.ps1
pwsh -File lessons/m32_framework_source_reading/scripts/check-environment-restore.ps1
```

`-Check`写到模块私有regenerated目录，再逐SHA256比对pb/grpc/http，结果 `GENERATION_IDENTICAL`。工具进程都有确定的Wait/Dispose，网络下载有120秒上限、编译/生成工具180秒，超时终止自有进程子树并收尾。归档哈希是本课固定下载内容的实际SHA256，不声称独立官方签名验证。

race脚本只读取主agent的toolchain.json，临时设置当前进程CC/CXX/CGO_ENABLED/PATH，并在finally逐项恢复存在性与精确原值；实际WinLibs GCC16.2.0、x86_64-w64-mingw32，避免旧32位MinGW默认compiler。没有go env -w、全局工具安装或系统代理修改。

主审发现旧脚本只保存值，再把保存的`$null`赋回Env。在Windows PowerShell 7.6.5中，原本不存在的变量可能因此变成“存在但为空串”；旧文档笼统写“finally恢复”不足以证明caller环境没有变化。本次修复同时记录`Exists`和`Value`：原不存在时`Remove-Item Env:`，原存在时`Set-Item`恢复原值，包括空串。

[check-environment-restore.ps1](scripts/check-environment-restore.ps1)在一个PowerShell进程中用`&`调用两个实际脚本，不通过新shell掩盖caller状态。它先构造不存在、空串、非空三态：GOBIN分别执行真实`generate -Check`，CC/CXX/CGO_ENABLED/PATH分别执行真实race，每次同时检查存在性相等与序数精确值相等。PATH为空或不存在时，检查函数只在自身作用域设一个指向入口已解析真实Go exe的临时alias，仍然运行实际Go、GCC与全部5项测试；函数退出alias消失。另为每种状态注入非法`GOTOOLCHAIN=m32-invalid`：生成脚本在设置临时GOBIN后的实际`go install`失败，race在设置四个临时变量后的实际`go test`失败，验证两个finally。检查脚本自己也在finally恢复包括GOTOOLCHAIN在内的全部实验变量。

2026-10-12实际结果见[environment-restore-r1.txt](evidence/environment-restore-r1.txt)：3次生成检查均`GENERATION_IDENTICAL`，3次race均5项PASS，无race诊断；6次预期Go失败均保留退出1错误且恢复三态。每条`ENV_RESTORED`同时有`presence_equal=True`与`value_equal=True`，没有只凭值比较宣布恢复。16个Go/IDL/依赖文件SHA256逐项保持一致，因此原demo/vet/mod verify及业务实证可复用。这是脚本修复与收尾证据，预期配置失败没有冒充课程业务测试失败或扩大原运行覆盖面。

本课默认总预算15秒，请求client2秒/server200ms，故障client100ms，丢ctx坏例保险2秒，小流2秒，Stop80ms，独立收尾3/4秒；没有Sleep调度猜测。库存只属于进程内合成对象，端口由OS动态分配；不访问共享中间件、DeepHelp业务数据或固定端口。未验证边界包括生产认证/TLS、外部registry、自动retry/cache、SQL事务/持久幂等、跨进程崩溃、多实例一致性、Kitex/go-zero运行与生成。实验通过只覆盖上文实际断言。
