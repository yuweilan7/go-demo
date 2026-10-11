# M31：从 .proto 追到库存副作用的真实 gRPC

你要维护一个库存服务：客户端发起“预留 2 件”，服务端需要扣库存，订单方还需要知道缺货、超时和取消分别代表什么。关键问题不是能否调用一个长得像 Java 接口的方法，而是 **客户端拿到的结果与服务端已经发生的库存变化能否对应**。

本课用 gRPC-Go 和真正生成的 Protobuf 代码，在同一进程的独立 server/client 之间建立 loopback TCP、HTTP/2 和 Protobuf RPC。初始合成库存是 `001-SKU=20`。故障屏障只控制真实 handler 的等待位置，客户端调用全部经过生成 stub 和 TCP；没有 fake RPC、bufconn 或替身驱动。业务状态是本进程内存 map，课程不声称验证了数据库持久化。

## 1. 先运行，再按符号读代码

复制课程到主仓库后的工作目录是 `D:/IdeaProject/go-demo`，实际交付执行目录和每个命令结果见 [STATUS.md](STATUS.md)。已提交生成源码，普通运行不需要先安装 protoc。

```powershell
go -C lessons/m31_grpc_protobuf run ./cmd/demo
go -C lessons/m31_grpc_protobuf test ./...
go -C lessons/m31_grpc_protobuf vet ./...
```

核心实验本身约数秒；用 10～20 分钟走完：先看 `E01` 的调用链和 `E02` 业务冲突，再对照 `E03/E04/E05` 的库存，最后看 `E06` 流和 `E99` 资源结束。`E07` 编码实验先执行，所以它会在日志开头出现。不同运行的动态端口、早退流额外发出的条数和背压序号可能不同；代码断言业务事实和必要先后关系。

| 阅读文件与符号 | 这一段解决什么问题 |
| --- | --- |
| [proto/inventory/v1/inventory.proto](proto/inventory/v1/inventory.proto) | 消息字段、方法名、unary/bidi 方向，客户端和服务端共同契约 |
| [gen/inventoryv1/inventory_grpc.pb.go](gen/inventoryv1/inventory_grpc.pb.go)：`NewInventoryClient`、`Reserve`、`RegisterInventoryServer`、`_Inventory_Reserve_Handler` | 生成 stub 怎么调用框架、生成注册怎么找到业务实现 |
| [cmd/demo/main.go](cmd/demo/main.go)：`main` | 正常入口如何运行实验、接收错误、决定退出码 |
| [internal/lesson/scenarios.go](internal/lesson/scenarios.go)：`Run`、`runNormalAndConflict`、`controlledWrite` | 谁创建资源/请求/goroutine，谁观察业务结果和故障屏障 |
| [internal/lesson/harness.go](internal/lesson/harness.go)：`startHarness`、`waitReady`、`close` | TCP listener、Server、共享 ClientConn 的创建、就绪与最后释放 |
| [internal/lesson/interceptors.go](internal/lesson/interceptors.go)：`clientTrace`、`serverTrace`、`identityUnary`、`recoverUnary` | metadata 传输、请求归属、链顺序和 panic 的实际边界 |
| [internal/lesson/service.go](internal/lesson/service.go)：`Reserve`、`waitGate`、`commit`、`Watch` | 请求怎样等待，库存唯一写点在哪里，取消怎样返回 |
| [internal/lesson/compat.go](internal/lesson/compat.go)：`runCompatibility`、`validatePrice` | 编码兼容与业务规则兼容为什么是两件事 |
| [internal/lesson/lesson_test.go](internal/lesson/lesson_test.go) | 独立核对库存、流、资源和旧读者保留/丢弃未知字段的后果 |

现在必须读懂：`ctx`、`ClientConn`、生成接口/注册、status/details、metadata、Send/Recv/EOF、资源 owner。内部帧格式、压缩器、HTTP/2 调度器、resolver/balancer 策略先按相关源码再查，不需要背整个生成 marshal。

## 2. 版本和可复现生成

本课实际锁定如下版本；它们通过生成、编译和真实运行确认兼容，不把“最新”当 API 契约。

| 部件 | 固定版本/作用 |
| --- | --- |
| Go / go.mod | Go 1.27.0，`go 1.27.0`；没有本课额外 toolchain directive |
| gRPC-Go | `google.golang.org/grpc v1.84.0`，实际 module 声明最低 Go 1.25.0 |
| Protobuf-Go runtime 与 protoc-gen-go | `google.golang.org/protobuf v1.36.12`，runtime 最低 Go 1.23 |
| protoc-gen-go-grpc | `v1.6.2`，插件最低 Go 1.25.0 |
| protoc | 官方 release `v36.2` 的 win64 包；命令打印 `libprotoc 36.2`，生成头记录 `protoc v7.36.2`，是其版本标记方式 |

`go.sum` 锁定下载校验和；间接依赖保留 `go mod tidy` 的实际结果。独立 module 是 `go-demo/lessons/m31_grpc_protobuf`，根仓库 `go test ./...` 不会递归覆盖这个嵌套 module。

在模块目录执行以下生成命令：

```powershell
# 首次：工具仅安装到本模块 .local/bin 与 .local/protoc-36.2。
./scripts/generate.ps1 -Install
# 以后只用同一套固定工具重新生成：
./scripts/generate.ps1
# 明确的编译反例入口：在 .local 写错误 proto，确认复用 reserved 编号被拒绝。
./scripts/check-reserved.ps1
```

脚本实际执行的 protoc 参数（脚本会填充模块绝对路径）是：

```text
protoc --proto_path=<模块>/proto --go_out=<模块>
  --go_opt=module=go-demo/lessons/m31_grpc_protobuf
  --go-grpc_out=<模块>
  --go-grpc_opt=module=go-demo/lessons/m31_grpc_protobuf
  inventory/v1/inventory.proto compat/v1/record.proto compat/v2/record.proto
```

`proto package course.inventory.v1` 决定 protobuf 全名和 RPC method `/course.inventory.v1.Inventory/Reserve`。`option go_package = "go-demo/lessons/m31_grpc_protobuf/gen/inventoryv1"` 决定 Go import 路径，二者不是同一种名字。`module=` 去掉输出路径中的模块前缀，文件落在 `gen/inventoryv1`。`pb` 是手写 Go import 的本文件别名，不会改变协议名字。

`.pb.go` 定义消息类型、字段访问、descriptor 和 protobuf runtime 接口；`_grpc.pb.go` 定义服务/client 接口、stub、注册和方法桥接。生成头的 `grpc.SupportPackageIsVersion9` 是编译期兼容检查，当前模板要求 gRPC-Go 至少 v1.64.0，本课使用 v1.84.0。不要手改生成文件修业务问题，应改 proto 或手写 handler 后重新生成。

## 3. 字段编号、presence 和金额是业务协议

字段编码围绕 **编号和 wire 类型**，不是 Go 字段在 struct 里的位置。`sku=1`、`quantity=2` 应在已有协议中保持原含义；改字段名不等于自动破坏 binary wire，但可能影响 JSON/源码调用者。编号不能重复或复用旧编号。删除 `legacy_note=3` 后，V2 显式写 `reserved 3; reserved "legacy_note";`，让以后复用该编号/名字变成编译错误；它不会自动删除旧消息里的字节。

本课的三种“零”需要分开读：

| 声明与 Go 入口 | 缺失与显式零/空值 | 本课实际断言 |
| --- | --- | --- |
| 普通 proto3 `int32 quantity=2`，`GetQuantity()` | 隐式 presence；缺失和设置 0 均得到 0，默认编码相同 | `Record{}` 与 `Record{Quantity:0}` marshal 字节相同 |
| `optional int32 discount_basis_points=4`，`*int32` 字段 | `nil` 代表缺失，`proto.Int32(0)` 代表显式设置 0；Get 都返回 0 | 指针 presence 不同，V2 round trip 保留显式 0 |
| `oneof owner`，`Owner` 接口动态值 | `nil` 未选分支；`&Record_Email{Email:""}` 已选 email 分支且值为空 | `Owner==nil` 与空 email 分支不同；测试另验证 warehouse_id 分支 |

`oneof` 的生成字段存放一种分支 wrapper；给 `Owner` 赋 warehouse wrapper 会替换原 email 分支，不是在同一消息上同时维护两个独立字段。wire 遇到多个 oneof 成员时的选择规则应按语言指南检查，不能把它当业务权限验证。`GetEmail()==""` 也不能区分未选 email、空 email 或选择其他分支，要检查 `Owner` 的动态类型/presence。

`GetX` 可对 nil message 取默认值，减少空指针样板；它不会替你验证请求。`Reserve` 必须明确拒绝 quantity=0/负数、空 sku/id；普通 scalar 无法表达“用户根本没传 quantity”，确实需要这个区分时应改为 optional 或单独业务类型。

V2 定义 `int64 price_minor=7` 与 `string currency=8`。本课 CNY 的最小单位是分，`12345` 表示 123.45 元；不能把所有币种都当两位小数。数量、金额上限、币种和正负数是否合法还要业务校验。ID/SKU 是 string，`0009` 的前导零保留，不转 float。独立测试用 `9007199254740993` 的 int64 金额 round trip，证明没有经 float64 丢失整数精度；这不意味着所有 JSON/JavaScript 消费方也能无损处理同一个整数。

## 4. 两版消息实际如何兼容

`compat/v1` 与 `compat/v2` 使用不同 protobuf 全名避免本进程 descriptor 冲突，同时保持公共字段的编号/类型。此实验验证消息二进制字段读写，不声称改 RPC service/package 名也自动兼容。

`runCompatibility` 的路径如下：

1. V2 写 `sku/quantity`、显式 0 的 discount、空 email owner、price/currency，`proto.Marshal` 生成真正的 binary wire。
2. V1 `proto.Unmarshal` 只认识字段 1/2/3，读到 sku/quantity，新字段留在 `ProtoReflect().GetUnknown()`。本次 unknown 是 12 字节。
3. V1 `proto.Marshal` 中转，再由 V2 decode，`proto.Equal` 验证全部新字段都恢复。
4. V1 写旧 legacy_note，V2 decode 成功，编号 3 不再是已知字段，仍作为 unknown 保留。
5. V2 `validatePrice` 拒绝旧消息：它缺 currency，虽然解码成功，仍不符合新业务要求。

实际日志：

```text
[E07] V2→V1→V2 preserved=true unknown_bytes=12; scalar missing==zero wire;
      optional missing!=zero; oneof unset!=empty;
      V1→V2 wire=OK business=REJECT(currency missing); price=12345 minor CNY
```

反例与修复已经直接给出：旧消费者用 `proto.UnmarshalOptions{DiscardUnknown:true}` 会抹掉新字段，独立测试确认后续 V2 的 owner/discount/price 全丢失。手写 `Record{Sku:old.Sku,Quantity:old.Quantity}` 做中转也只复制已知字段。需要保真中转时使用默认 unknown 保留的 protobuf decode/encode，不用重新拼一个只有旧字段的对象。需要业务升级时定义旧数据的 currency/price 迁移、默认语义或逐步启用校验；不能凭 wire 兼容就直接让旧客户端承担新必填规则。即使编号/类型不变，把 quantity 从“件”改成“箱”也会读成功却改变库存含义。

## 5. 连接构造、Ready 与业务完成

`startHarness` 先 `net.Listen("tcp","127.0.0.1:0")`：由 OS 原子分配独占端口，保留同一 listener 给 `Server.Serve`。不先找空闲端口再另开 listener，也不占共享固定端口。owner 创建一个 `Serve` goroutine，以 buffered `serveDone` 收集其结果。

`grpc.NewClient("passthrough:///"+addr, ...)` 创建逻辑 `ClientConn`，该版本的契约是不执行 I/O。本次构造后打印 `NewClient.state=IDLE`。`NewInventoryClient(conn)` 只是保存这个可复用接口，不是握手，不是远端 Java proxy 的“已连通证明”。真实调用会触发连接；本课为了显式观察，先 `Connect()` 再循环读取状态并 `WaitForStateChange`，直到 Ready 或有界 ctx 到期。`Connect` 自身不等待 Ready，状态变化也不保证下一状态就是 Ready。

Ready 说明传输此时可用；接着 E01 真正写 RPC、E02 typed detail 和后续读 RPC才验收业务。一个 Conn 被所有 unary 和流复用，请求各有 ctx；`Inspect` 不能拿过期写请求的 ctx 再查，否则新查询会立刻失败。`WaitForReady(true)` 只影响调用是否等待可用连接，deadline 仍限制等待，不保证服务端有正确库存。

这里使用 loopback 明文 `insecure.NewCredentials()` 和 `WithNoProxy()`，仅传合成数据；未验证 TLS、证书、生产鉴权、跨主机 DNS 或负载均衡。`passthrough` 是显式固定目标 resolver，避免把一个本地地址实验说成服务发现。遇到生产代码，继续追 target scheme→resolver 更新地址→balancer picker→subconnection/transport；完整治理留到 M32/M33。不要新建一个 Conn 发送一条 RPC 就丢弃它，也不要共享一个可变请求对象给多个并发调用。

## 6. 从生成 stub 到真实 handler，再从 handler 返回 client

E01 的调用者是 `runNormalAndConflict`。它传入请求指针和请求 ctx，生成 `inventoryClient.Reserve` 分配 `ReserveReply`，把完整 method 名交给 `ClientConn.Invoke`。框架 client 链先调用 outer，再调用 inner，最终执行 `invoke` 的真实 `newClientStream→SendMsg→CloseSend→RecvMsg`。返回的 error 可用 `status.Code` 读取；生成 stub 在 error 非 nil 时返回 `nil,err`，成功才把 reply 交回实验。

服务端在注册阶段把同一个 `*inventory` 绑定到 `Inventory_ServiceDesc`。当前 v1.84.0 源码把 unary method 包装为 stream handler，路径是 `Server.handleStream→processRPC→wrapUnaryHandler→md.Handler`。这里 `md.Handler` 是生成的 `_Inventory_Reserve_Handler`：先 `dec(in)` 解码当前请求，再建立调用业务 `srv.(InventoryServer).Reserve(ctx, req.(*ReserveRequest))` 的 final handler，交给 unary interceptor 链。

业务 `Reserve→commit` 在 mutex 下读取 20、检查数量 2、写成 18，生成 reply。返回经过 inner→identity→recovery→outer；框架把 reply 和最终 status 写回传输，客户端 `RecvMsg` 完成后返回 inner/outer，最终由实验检查 reply=18。`stock.commit` 是本课副作用发生的证据，单独的 ClientConn Ready 或响应对象分配都不是写入证据。

这是本课核对过的真实版本源码入口，旧教程中的 `processUnaryRPC` 并非本版本实际入口。可沿固定 tag 对照：

- [clientconn.go：NewClient](https://github.com/grpc/grpc-go/blob/v1.84.0/clientconn.go#L156)、[call.go：Invoke/invoke](https://github.com/grpc/grpc-go/blob/v1.84.0/call.go#L33)。
- [server.go：wrapUnaryHandler/processRPC](https://github.com/grpc/grpc-go/blob/v1.84.0/server.go#L1281)、[server.go：unary/stream interceptor 的分支](https://github.com/grpc/grpc-go/blob/v1.84.0/server.go#L1446)。
- [stream.go：SendMsg/RecvMsg](https://github.com/grpc/grpc-go/blob/v1.84.0/stream.go#L1053)、[stream.go：server SendMsg](https://github.com/grpc/grpc-go/blob/v1.84.0/stream.go#L1808)。

读当前手写 Go 时同时辨认这些最小规则：`reply,err := ...` 在当前作用域声明新变量，`if err := check(...); err != nil` 的 err 只在这个 if 的作用域；`Run` 的 named err 让 deferred closure 能把清理错误合并到返回结果。`*inventory` 接收者与 plan 指针指向共享对象，绝不是每次调用复制整份库存；mutex 与 channel 分别保护数据和通知状态。`any`/生成 client 接口保存动态类型和值，类型断言的 `ok` 分支让未知类型变成错误处理；`opts...` 把选项 slice 展开传给下层。`defer log.add(id,...)` 注册时固定 id/文本参数，`defer func(){...}()` 则在退出时读取捕获对象；它只等当前函数退出，不负责等待任意新 goroutine。

## 7. metadata、拦截器顺序与 panic owner

`ownedContext` 用 `metadata.NewOutgoingContext` 添加 `x-tenant=course-m31`。这是客户端 outgoing MD，框架把它传给服务端 incoming MD。`context.Value` 是本进程对象链，不会自己跨网络传过去。`identityUnary` 校验 incoming MD，建立携带 `tenantKey{}` 的派生 ctx，继续把这个 ctx 传给 inner/handler。自定义 key 类型避免不同包都写字符串 tenant 而冲突。

handler 日志 `handler.tenant=course-m31` 证明这次请求收到派生归属；header 的 `x-tenant/x-rpc-owner` 和 trailer 的 `x-course` 证明返回 metadata 也经过真实 RPC。设置 Header/Trailer 不是业务响应完成，不要把它们当数据库提交。metadata 里的字符串可由客户端填写，课程的归属检查不是可信生产认证。

E01 断言完整顺序：

```text
client.outer.enter → client.inner.enter
  → server.outer.enter → server.recovery.enter → server.identity.enter
    → server.inner.enter → handler.enter → handler.tenant → stock.commit
    → server.inner.exit → server.identity.exit → server.recovery.exit → server.outer.exit
  → client.inner.exit → client.outer.exit
```

链是嵌套同步调用：前向按配置顺序进入，正常返回按反向退出，和 Java interceptor 链有可用类比。Go 闭包捕获共享 journal/层名，每次调用的 ctx/req 是参数；不能因为写了一个闭包就认为 ctx 可共享到别的请求。客户端拦截器必须把 `opts...` 交给 invoker，否则 Header/Trailer 等选项可能丢失；服务端拦截器必须调用 handler 才会到业务层。

E02-panic 在扣库存前触发 panic；inner 的 defer 先退出，identity 的 defer 再退出，recovery 的 deferred closure 在 **同一个 RPC goroutine 的栈** 中 recover，把 named 返回值设为 `nil, status.Internal`，outer 再退出。客户端收到 Internal，下一次真实 Inspect 仍成功且库存=18。recover 后不会回到原 panic 下一行。这个边界包围 identity/inner/handler，外层 trace 不在里面；如果 handler 另 `go worker()`，worker 的 panic 在另一 goroutine 中，不能由这里的 recover 接住，worker owner 需要自己的边界与完成等待。本课不启动这种未受控 panic goroutine。

Watch 不走 unary 链；当前 `processRPC` 虽统一管线，明确对 unary 走 wrapped unary handler，对流走 stream interceptor。`identityStream` 独立校验该 stream 的 incoming MD、设置 header，恢复该流 goroutine 内的 panic，再把 **真实 srv** 和 stream 交给 handler。不能把 srv 换成 nil，也不能指望 unary metadata 校验自动保护流。

## 8. 业务错误、传输错误和响应事实

| 结果 | 产生位置/本课含义 | 需要检查的额外事实 |
| --- | --- | --- |
| OK + ReserveReply.remaining=18 | `commit` 成功，客户端读到响应 | 本课最终 Inspect/写次数确认副作用；无数据库持久化承诺 |
| FailedPrecondition + `Conflict` detail | `commit` 检查 99>18，未扣库存 | details 动态类型、requested=99、available=18、writes=0 |
| Unauthenticated | `identityUnary` 缺少/错误归属，在业务前拒绝 | 没有库存写入；不等于生产凭证鉴权已验收 |
| InvalidArgument | `Reserve` 拒绝 quantity=0 | 是业务参数无效，不应当作可重试网络错误 |
| Internal | 同一 RPC goroutine 的 panic 被恢复 | 本课 panic 发生在提交前，库存不变；别的时机不能依此推断 |
| DeadlineExceeded / Canceled | 本地 deadline/取消或服务端 ctx 错误映射 | 服务端可能未执行、正退出、已经提交；必须查实际写入事实 |

缺货不是 `remaining=0` 的成功响应，也不是 Go panic/Java unchecked exception。`status.New(...).WithDetails(&pb.Conflict{...})` 把可解析业务事实放入 status details；框架用标准 status + Any 传输，客户端用 `status.Convert(err).Details()` 和 comma-ok 类型断言取回。遇到不认识 detail 类型或 details decode error，应显式处理，不能强制断言导致新的 panic。`WithDetails` 也会返回构造错误，代码把这个异常转换为 Internal。

## 9. deadline/取消的三个真正不同的库存时间线

`controlledWrite` 的 client goroutine 属于实验 owner；服务端 handler goroutine 属于 gRPC Server。二者 ctx 通过协议关联取消，却不是同一个 context 对象。每次最多 400ms 请求 deadline；手动取消实验先看到 started 屏障再 cancel。

| 信号/资源 | 发出者 | 谁等待 | 表示什么，不表示什么 |
| --- | --- | --- | --- |
| `gatePlan.started` | handler 关闭 | 实验 owner `waitSignal` | 已进入 handler；不保证已扣库存 |
| `gatePlan.gate` | 实验 owner `release` 关闭 | handler `waitGate` | 允许继续慢业务；不是取消通知 |
| `gatePlan.committed` | `commit` 写完后关闭 | 提交后超时实验 owner | 库存已变化；不保证客户端收到 reply |
| `ctx.Done()` | cancel 或 deadline | RPC transport、请求 ctx 的业务等待 | 取消通知；不是强杀 goroutine、回滚或等待退出 |
| `gatePlan.done` | handler 最后 defer 关闭 | 实验 owner | 业务 handler 到收尾屏障；仍在 defer 中，外层 interceptor/框架可能还未返回 |
| `callDone` | client goroutine 发出结果后关闭 | 实验 owner 的 defer | client 调用体完成，已把结果交给 owner |
| `serveDone` / `graceDone` | Serve / GracefulStop goroutine | harness owner | 最终框架服务退出/优雅停止完成 |

E03 丢 ctx 的反例已经实际实现，修复对照在同一个 `Reserve` 中：

| 步骤 | client/owner | server 的当前语句与业务状态 |
| --- | --- | --- |
| 1 | 登记 gate，再发起真实 Reserve | handler 关闭 started，走 `DROP_CONTEXT_BEFORE_COMMIT` |
| 2 | 等 started，然后等 RPC 返回 | 错误地 `WithTimeout(Background(),2s)`，`waitGate(lost,p)` 等 gate；lost 没有 tenant/deadline/请求取消 |
| 3 | 400ms 到期，client 返回 DeadlineExceeded | handler 仍在 lost ctx 的等待，writes=0；取消原 ctx 不影响 lost |
| 4 | owner 确认 writes=0，再 release gate | handler 继续，`commit(...,honorContext=false)` 扣 2 件 |
| 5 | owner 等 handler.done，再用新 ctx Inspect | 真实库存从18变16，writes=1；错误码发生在实际扣减之前 |

本次日志存在 `client.return.DeadlineExceeded→owner.release.after-client-error→stock.commit` 的机制保证顺序，这不是一次幸运调度。坏版本的 2 秒保险只防永久挂起，不恢复请求取消语义。

E04 的修复把业务等待改成 `waitGate(ctx,p)`，后续写点也检查请求 ctx。手动 cancel 和 deadline 两个实验都保持 gate 不释放；handler 从 ctx.Done 返回，记录 `cancel.before-commit`，没有进入 commit，writes=0、库存仍16。owner 分别等 client 结果、handler.done，并最后等 client goroutine；不能把 `cancel()` 一行当成这些等待的替代。客户端错误与服务器 cancel 日志谁先出现取决于传输和调度，不做全局固定顺序断言。

E05 故意把慢等待放在提交之后：owner 等 committed 确认已从16扣到14，再等400ms超时；handler 从 ctx 返回 `cancel.after-commit`，client 得到 DeadlineExceeded。这里 ctx 从头到尾正确传递，仍无法撤销已经扣过的库存。这解释了为什么“修复丢ctx”与“避免重复写入”不是同一个修复。

随后代码明确发送 **第二个** NORMAL Reserve，同一个 request_id 又扣2，库存14→12、写次数=2。request_id 只是记录字段，Go、gRPC、Protobuf 都不会替你把它变成幂等键。这是实际坏重试，不是库隐式重试。生产修复需要服务端把幂等键、请求载荷匹配、去重记录、业务更新和稳定结果放在同一可靠原子边界；超时方查询该键的最终状态，或按幂等协议重放并返回原结果。仅在客户端重试前先 Inspect 库存并不能可靠识别某一请求是否已经执行，也不能阻止并发重复扣减。本课完整解析这个方案边界，未把内存写次数 map 宣称成持久化幂等系统。

`commit` 在锁内检查 `ctx.Err()` 只能拒绝当时已观察到的取消。检查后取消仍可能与写入竞速，不存在“通知之后永远不会副作用”的语言保证。真正写库还需要事务、提交结果与未知结果处理；写 RPC 不能仅按某个 status code 直接重试。

## 10. 双向流：消息协议与半关闭

proto 的 `rpc Watch(stream WatchRequest) returns (stream Stock)` 表示双方都可发送多条消息，生成 `grpc.BidiStreamingClient[WatchRequest,Stock]` / server 接口。`Stock.sequence` 是本流发送顺序，不是全局库存版本号。课程每个流只允许一个 WatchRequest、samples 1..64、padding 0..256KiB；服务端发送对应快照后再 `Recv` 等客户端 EOF/取消。本课主动给流设数量/内存上限。

调用链是 `InventoryClient.Watch→ClientConn.NewStream→GenericClientStream.Send/Recv→ClientStream.SendMsg/RecvMsg`；服务端 `Inventory_ServiceDesc.Streams[0]→processRPC→identityStream→_Inventory_Watch_Handler→GenericServerStream→inventory.Watch`。生成的泛型 wrapper 只把 typed 消息转给框架收发接口，不是 Java Stream 的惰性集合计算，也不是本地 channel 共享对象。

E06-half-close 的具体次序：client Send 一个 samples=2 请求→`CloseSend()` 表示发送方向没有更多请求→server Recv 到请求，Send seq1/2→server 下一次 Recv 得到 EOF，返回 nil→框架发 OK 终止 status→client Recv 完两条消息再得到 io.EOF。断言两条都是库存12、seq1/2且流 header tenant 正确，等 handler.done。客户端发送半关闭后仍能收两条响应，不能把 CloseSend 当整个 RPC 关闭。

正常读完时 `io.EOF` 是协议正常完成；Recv 返回其他 error 必须读 status。`Send` 成功仅交给传输调度/缓冲，不保证对方已经 Recv，更不保证对方已处理业务。当前 gRPC 契约允许同一 stream 一个发送 goroutine 与一个接收 goroutine并行；同方向多个 goroutine 并发 Send、多个并发 Recv 不安全。Send 后不修改该 message，因为 tracing/stats 可能延迟读取。本课每次生成独立 Stock/padding，不重复修改已发送对象。

## 11. 流早退、背压与修复

危险版本是拿到流后读取一条就 return，既不读完、不 cancel，也不关闭 Conn。`CloseSend` 只结束发送，仍不能保证接收/服务端资源释放。当前 `ClientConn.NewStream` 契约明确要求完成其中一种结束路径：关闭 Conn、取消本流 ctx、Recv 到终止 error，或从 Header/Send 获得指定终止错误；否则流相关 goroutine/context 可能泄漏。[当前 API 契约](https://pkg.go.dev/google.golang.org/grpc@v1.84.0#ClientConn.NewStream)

E06-early 是实际修复：Send samples=4→Recv seq1→owner cancel 本流 ctx→继续 Recv 到 Canceled（可能先取出已到缓冲的额外消息）→等服务端 handler.done。server 可能已发完四条而正等下一次 Recv，也可能仍在 Send；取消分别使对应 I/O 返回。`cancel()` 不关闭共享 Conn，后续背压流仍用同一 Conn 成功创建。

E06-backpressure 故意不 Recv，client 固定接收 stream/connection window 为64KiB，每条包含256KiB padding，最多64条。server 在 Send 前写 attempts、返回后写 sent，记录 channel 容量64，诊断记录本身不会堵住 handler。owner 先观察真实 Send 进入，再用150ms有界计时检查是否返回；本次第2条未返回。这个计时用于观察当前等待点，不是用 sleep 猜 server 是否启动。窗口/缓冲决定背压位置，确切序号和耗时不是协议普遍保证。

```text
[E06-backpressure] stream.send.enter
[E06-backpressure] stream.send.return
[E06-backpressure] stream.send.enter
[E06-backpressure] observer.Send(2).not-returned-for-150ms; client.Recv.calls=0
[E06-backpressure] stream.send.error
[E06-backpressure] stream.handler.done
[E06-backpressure] ASSERT cancel→Send returns error→server.done; bounded memory/messages
```

修复没有再起无限制生产 goroutine：owner cancel→传输结束使被阻塞 Send 返回错误→handler defer 发 done→owner 等 done并读到流终止结果。真实背压发生在 gRPC/HTTP2 流控和框架写额度，不是 `watchPlan.attempts` channel；即使把这个流控叫作背压，也不能把 Send 成功理解成接收者业务已处理。需要“客户端处理了第N条”时应设计应用 ack/游标协议，本课 Stock.seq 未实现这种持久进度。

## 12. 最后的连接、监听器和 goroutine 归属

`Run` 的 defer 调用 `harness.close`，无论断言成功、RPC 出错还是等待超时都执行。owner 释放仍登记的 gate，关闭共享 ClientConn，启动 GracefulStop 并等待其完成信号；最多2秒，若未结束则 Stop，再等待 graceful 返回。随后读取 serveDone，确认 `Server.Serve` 已返回，框架 listener 已关闭，ClientConn 状态是 Shutdown。创建 client 失败时 startHarness 也立即 Stop/等 Serve，不留孤儿监听器。

| goroutine | owner/实际等待位置 | 停止与完成 |
| --- | --- | --- |
| CLI / `Run` | CLI，顺序等待 RPC、实验屏障 | 所有实验后 defer close，结果交给 main |
| `Server.Serve` | harness，gRPC accept/transport | GracefulStop/Stop 关闭本 listener，serveDone 报告返回 |
| unary handler | gRPC Server，慢例子等 gate 或 request ctx | handler 返回，外层链/传输收尾；GracefulStop 最终等待活动 RPC |
| `controlledWrite` client goroutine | 实验 owner，stub 内等待真实响应/状态 | ctx 取消/deadline使 RPC 返回，结果交 results，callDone 被 owner 等到 |
| stream handler | gRPC Server，Recv 或 Send 的实际 I/O | 本流 cancel/EOF，defer done；最后服务器停止确认框架结束 |
| `GracefulStop` goroutine | harness，等待活动 RPC 结束 | graceDone，超过2秒 owner Stop 后仍等它返回 |
| ClientConn 内部 goroutine/transport | gRPC ClientConn | 流结束路径加 owner 最后 Close，连接状态 Shutdown |

全套实验总 ctx=12秒；慢坏例子保险2秒、正常/早退流2秒、背压流3秒、停服优雅等待2秒。timer 的 owner 在返回时 Stop，派生 ctx 的 cancel 由创建者 defer 调用。最终 `PASS stock=12 ...` 是实验业务结果，后面的 `E99 connection_released=true` 是实际清理结果，二者分别验收。

## 13. race 检查与本课证据范围

Windows/amd64 的 `-race` 需要可用的64位C工具链。如果默认是32位 MinGW，普通纯 Go run/test/vet 可以通过，race 会在 runtime/cgo 编译时报64位不支持；这不是课程数据竞争已被检出的结果。使用已准备的 amd64 编译器运行：

```powershell
# 在模块目录；替换为已有兼容 amd64 compiler 的真实路径。
./scripts/test-race.ps1 -CompilerPath '<便携工具链>/bin/gcc.exe' -CxxPath '<便携工具链>/bin/g++.exe' -BinDirectory '<便携工具链>/bin'
```

脚本只设置本进程的 CC/CXX/CGO_ENABLED 和 PATH（前置工具链 bin），再实际执行 `go -C <模块> test -race -count=1 -timeout 60s ./...`，finally 恢复环境。当前机器使用已验证的 WinLibs GCC16.2.0、target `x86_64-w64-mingw32`，实际已通过，详细路径与结果在 STATUS 中；不使用 `go env -w` 修改全局。race 通过仅覆盖本次执行的路径，不能证明未运行的所有路径都无竞争。

验收覆盖真实库存 RPC、生成工具、状态/detail、归属 metadata、链顺序/panic、deadline/cancel 副作用、重复写、binary compatibility/presence、bidi 半关闭/EOF/背压/早退、Conn/Server/listener 收尾。TLS/生产鉴权、跨进程崩溃恢复、数据库幂等与持久性、服务发现/负载均衡治理未纳入本课实证。原 `files/开发框架` 只有 Gin/GORM/Redis/微服务框架阅读线索，没有完整 RPC 教程，不能当成 gRPC 的已验收示例。

核对入口来自 [gRPC Go 教程](https://grpc.io/docs/languages/go/basics/)、[固定版本 API](https://pkg.go.dev/google.golang.org/grpc@v1.84.0)、[deadline 契约](https://grpc.io/docs/guides/deadlines/)、[流控说明](https://grpc.io/docs/guides/flow-control/)、[proto3 编号与兼容指南](https://protobuf.dev/programming-guides/proto3/)、[Go 生成消息接口](https://protobuf.dev/reference/go/go-generated/)、[字段 presence](https://protobuf.dev/programming-guides/field_presence/)。本课实现和判断仍以固定版本源码、实际日志和断言为依据。
