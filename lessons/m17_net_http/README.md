# M17：从订单调用读懂 net/http 的请求、响应体、连接与取消

这课创建真实本机 HTTP/1.1 服务和客户端，使用 `127.0.0.1:0` 动态端口。一轮运行能看到正常订单、重复订单、取消后仍发生的订单副作用，以及错误状态、坏 JSON、过大响应、早断开、慢响应和泄漏响应体。没有外部依赖，不启动远端服务；所有账本都是本轮独占的合成内存数据。

你要建立的最小模型是：`Client.Do` 同步等待响应头；拿到 `Response` 后，调用方还拥有一个必须消费、关闭的 `Body`。`Transport` 管理 TCP/TLS 与连接池，服务端 `Handler` 执行业务；取消客户端等待不会替你撤销服务端已经发生或继续发生的业务副作用。

本课默认 Go 1.27，实际核验版本为 **go1.27.0 windows/amd64**，module 是 `go-demo/lessons/m17_net_http`，`go.mod` 为 `go 1.27`，没有第三方依赖或 `toolchain` 强制覆盖。Go 版本差异，尤其 Go 1.27 的有限自动 body drain，见下文。已有 `files/开发框架` 只提供了 Gin/GORM 等学习方向，没有真实 HTTP 实现；本课从标准库建立它们共同依赖的协议基础。

## 运行入口与 10～20 分钟核心阅读

在仓库根目录运行：

```powershell
go -C lessons/m17_net_http run ./cmd/demo
go -C lessons/m17_net_http test ./... -count=1 -v
go -C lessons/m17_net_http vet ./...
go -C lessons/m17_net_http test -race ./... -count=1
```

本机默认32位MinGW不能编译amd64 race runtime；本轮已用课程统一的可移植amd64 compiler实际通过race。在本课目录复跑当前课程提供的compiler，可只在当前PowerShell进程临时设环境并恢复：

```powershell
$m17Compiler = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' -Raw | ConvertFrom-Json
$m17OldCC, $m17OldCXX, $m17OldCGO, $m17OldPath = $env:CC, $env:CXX, $env:CGO_ENABLED, $env:PATH
try {
    $env:CC = $m17Compiler.cc
    $env:CXX = $m17Compiler.cxx
    $env:CGO_ENABLED = '1'
    $env:PATH = $m17Compiler.bin_dir + ';' + $env:PATH
    go test -race -count=1 -timeout 60s ./...
} finally {
    $env:CC, $env:CXX, $env:CGO_ENABLED, $env:PATH = $m17OldCC, $m17OldCXX, $m17OldCGO, $m17OldPath
}
```

其他机器使用实际支持目标架构的C compiler即可；这个私有课程环境索引不属于module交付，也不要求复制它进仓库。

本课的独立工作目录是 `lessons/m17_net_http`。进入它之后，可分别用 `go run ./cmd/demo`、`go test ./... -count=1 -v`、`go vet ./...`、`go test -race ./... -count=1`。根 module 的 `go test ./...` **不会递归测试这个嵌套 module**。

核心阅读使用同一 `sku-m17` 合成订单：先读 E01 客户端复用，接着 E02 泄漏修复，再用 E03 的 HTTP200/未提交、E04 的重复副作用、E05 的取消时间线把“网络完成”和“业务完成”分开。完整入口也会运行 E06～E14，每个阶段都有实际错误和清理断言。讲清前五个实验约需 10～20 分钟，命令本身通常几秒完成；耗时受机器负载和首次编译影响，不作为语义保证。

阅读顺序：

1. `cmd/demo/main.go`：建立整轮 30 秒 ctx，同步调用 `lab.Run`。
2. `internal/lab/client.go`：`NewClient`、`RequestJSON`、`countedBody.Close`、`AttachTrace`。
3. `internal/lab/fixture.go`：`NewFixture`、`serveHTTP`、`readOrder`、`gatedHandler`、`Ledger.Commit`、`Fixture.Close`。
4. `internal/lab/owned.go`：`OwnedRequest`、`Result`、`StopAndJoin`，区分业务结果与请求收尾完成。
5. `internal/lab/scenarios.go`：14 个实验中的 12 个，包含屏障、错误判断、body 作用域与 cleanup。
6. `internal/lab/tls.go`：E09 的真实 TLS 成功、真实拨号拒绝、真实 TLS peer 握手停滞。
7. `internal/lab/cleanup_proof.go`：E14 的真实取消请求、独立清理屏障与owner等待事实。
8. `internal/lab/lab_test.go`：直接根据账本、状态码、字节数、error 原因、实际 handler 次数与最终资源计数做独立断言。

现在必须理解 `Do` 与 `Body.Read` 的分界、每个 body 的 owner、ctx 取消和业务副作用的分界、最终状态选择时机、预算各管哪段。HTTP/2 池实现、TLS 密码套件、生产代理 IP 链解析和完整认证系统，可以遇到对应源码再查；下文明确本课的证明范围。

## 五个对象分别负责什么

| 对象 | 当前源码中的创建者/使用者 | 责任与返回边界 | Java 经验可以怎样帮助理解 |
| --- | --- | --- | --- |
| `http.Server` | `NewFixture` 创建，`Serve` 使用独占 listener | accept、协议解析、调用 handler、管理连接与 deadline | 接近 Servlet 容器的协议侧；`Serve` 返回时还必须等待 `Shutdown` 完成 |
| `http.Handler` / `HandlerFunc` | `Server.Handler=HandlerFunc(f.serveHTTP)` | `ServeHTTP(w,r)` 同步执行当前请求业务 | 可类比 Servlet/controller 入口；Go 没有必须声明的 async 方法 |
| `*http.Request` | client 构造；server 从实际网络解析另一份 | URL/header/body/ctx 描述一次请求；两端不是同一个 Go 指针 | 可类比请求对象，但 client 的 ctx 不跨网络传对象，仅取消产生连接/流协议后果 |
| `http.ResponseWriter` | server 提供，handler 使用 | 在 handler 返回前选择响应头、状态、写 body | 可类比响应输出对象；不能在 handler 返回后另起 goroutine 继续写 |
| `http.Client` | `NewClient` 创建一次 | 重定向/整体超时/调用策略，`Do` 返回 response 或网络/策略错误 | 可类比复用的 HTTP client；非 2xx 不会自动形成 `Do` error |
| `http.Transport` | `NewClient` 创建并交给 client | dial、TLS、HTTP、连接池、响应头预算 | 可类比底层连接管理器；它不是你的订单事务或业务限流器 |

`HandlerFunc` 是函数类型，标准库为它提供 `ServeHTTP` 方法，所以函数能适配 `Handler` 接口。`serveHTTP` 的 `f *Fixture` 接收者是指针：不同请求会访问同一 fixture、同一账本和 gate map，故共享状态需要锁。`r *http.Request` 属于当前请求；`Order` 解码到局部 struct，是值数据，账本 `append` 保存它的值。

`:=` 在当前作用域声明变量。例如 `resp, err := client.HTTP.Do(req)` 得到当前请求的 response 指针和 error 接口；error 动态类型可能是 `*url.Error`、包装后的网络 error。Go 通常用 `(value,error)` 返回而非抛异常，所以每次早返回前必须知道当前已经成功取得哪些资源。

## 从入口到实际副作用的调用链

正常订单的客户端链：

```text
main -> lab.Run -> duplicateExperiment -> withFixture
     -> RequestJSON(ctx, client, POST, /orders, orderJSON, safe-key, 2048)
        -> context.WithCancel -> http.NewRequestWithContext
        -> Client.Do -> countedTransport.RoundTrip -> http.Transport.RoundTrip
        -> TCP / HTTP -> response header 交回 RequestJSON
        -> io.ReadAll(io.LimitReader(resp.Body, max+1))
        -> HTTP 状态检查 -> json.Unmarshal -> Code/Committed 检查
        -> defer Body.Close -> defer child cancel -> Reply/error 交回实验
```

同时发生的服务端链：

```text
NewFixture -> net.Listen(127.0.0.1:0) -> go Server.Serve(listener)
Server accept -> ConnContext 编号 -> ConnState(StateNew)
HTTP/1 解析 request -> HandlerFunc.ServeHTTP -> Fixture.serveHTTP
/orders -> readOrder -> method/auth/content-type/大小/完整JSON/业务参数检查
        -> Ledger.Commit -> lock 内查 key 与 append(rows) -> orderID
        -> writeJSON(201, Code=OK, Committed=true, OrderID)
        -> handler 返回 -> 连接转空闲或关闭
```

**真实副作用在 `Ledger.Commit` 的 `append` 发生**。`writeJSON` 发生在提交之后，网络写失败不能撤销账本；收到 201/Committed 只是服务端协议声明，本课还直接检查 `Ledger.Count` 与订单 ID。该账本只证明进程内状态，不证明数据库持久性、进程重启恢复或跨实例幂等。生产订单需要持久化事务、唯一约束以及幂等键与请求内容的一致性；本课没有把内存 map 当成这些保证。

## Response.Body 的所有权、EOF 与复用

`Do` 的 `err==nil` 时，调用方得到非 nil body，**包括非 2xx**；所有分支都要关闭它。`RequestJSON` 在成功拿到 response 后立刻注册 `defer`，一次请求一个函数作用域。不要在长循环外层 defer 所有 response body，直到循环结束才关闭。

服务端的 `Request.Body` 由 net/http 管理关闭；`readOrder` 用 `MaxBytesReader` 包装为实际读取上限并主动关闭。客户端发送的 `Request.Body` 通常由 Transport 关闭，即使 `Do` 出错也如此，可能在 `Do` 返回后异步完成。服务端 request body 与客户端 response body 是不同资源，不要混淆。

EOF 是当前 HTTP body 分帧已经读完的事实：HTTP/1.1 只有确认消息边界后才安全地让后续请求使用同一连接。本课 `RequestJSON` 对小响应完整读取后关闭；E01 用服务端 `X-Conn-ID=1,1` 和客户端 `GotConn.Reused=true` 两端证明复用。并非每个读到 EOF 的连接都一定复用：服务端可声明 close，连接可能坏掉，空闲池可能满或已有 deadline 到期。

`io.LimitReader(body,max+1)` 返回的 EOF 也可能只表示这个**限制器**已用完，不代表底层 body 的网络 EOF。因此 `/large` 超限读到 1025 字节后明确取消本次交换，再关闭 body；它不会为保住连接继续读取攻击者全部响应。整个读取还受 request ctx/Client.Timeout 的时间上限约束，大小上限不能代替慢速响应时间上限。本课关闭透明压缩，避免把“解压后字节数”与线上压缩字节数混在一起；线上若开启压缩，要对实际消费的解压后字节施加上限。

Go 1.27 有版本相关行为：`Body.Close()` 后 Transport 会尝试有限自动 drain。实际核验的 `D:/Go/src/net/http/transport.go` 在 `maxPostCloseReadBytes`、`maybeDrainBody`、`persistConn.readLoop` 中限制到约 256 KiB，并有时间保护；这属于当前实现细节，不是调用方可以依赖的无条件复用承诺。不要断言“没读 EOF 只 Close 一定新建连接”。本课危险版是**既不读也不 Close**；大响应超限版先 cancel，主动选择结束交换。公司旧 Go 版本应重新核对它的实现和文档。

`OpenBodies` 是课堂 wrapper 的所有权计数，不能等同 socket/goroutine 数；EOF 后未 Close 的 body 仍计入 ownership，即使 socket 已经可复用。实际 socket 用服务器 `ConnState` 的 `live` 检查。`CloseIdleConnections` 只回收池中空闲连接，E02 的占用仍有 body owner，因此不受它修复。

## E01～E05：核心正常、错误与修复解析

| 实验 | 输入和实际观察 | 直接原因与修复边界 |
| --- | --- | --- |
| E01 `reuseExperiment` | 连续 `/ok`，连接 ID `1,1`，第二次 `Reused=true`，body owner=0 | 复用同一 client/transport，并消费完整小响应；`RequestJSON` 同步返回 |
| E02 `leakExperiment` | `/leak` 响应 1 MiB 未读未关；第二请求 deadline；`handled=1`；修复后第三请求连接仍为1 | `MaxConnsPerHost=1` 被第一交换占用；第二请求没到 server。对已知 1 MiB 本地响应有界读到 EOF 并 Close 后复用 |
| E03 `errorsExperiment` | 下表的状态、JSON、业务、大小、提前断开等八条路径 | 不用 HTTP200 作为业务验收，不把 body 的错误误放在 `Do` 阶段 |
| E04 `duplicateExperiment` | 危险重复新增2行；正常同 key 返回 `3,3`，只新增1行；再观察提交后丢响应，账本3→4但client EOF | 网络调用没有天然 exactly-once；客户端关闭POST body回放，服务端原子查重写入，直接验证handler只调用一次 |
| E05 `cancellationExperiment` | 危险账本 `0->1`；修复账本 `1->1` | 取消只终止等待/通知 ctx；业务代码是否停止、是否提交是另一个事实 |

E03 按同一客户端逐个请求，全部错误路径最后 body owner 都为0：

| path | 观察值 | error 的来源 | 业务影响 |
| --- | --- | --- | --- |
| `/missing` | 404，38字节 | `HTTPError`，`Do` 本身并不因404失败 | 拒绝处理，无副作用 |
| `/bad-json` | 200，8字节 | `json.Unmarshal` 的 `*json.SyntaxError` | 消费完成并关闭，不能把内容当成功 |
| `/business-error` | 200，Code=`OUT_OF_STOCK` | `BusinessError` | 业务拒绝，无提交 |
| `/legacy` | 200，Code=`OK`，Committed=false，账本0 | `BusinessError{NOT_COMMITTED}` | 真实对照：响应写出与业务提交分离 |
| `/large` | 200，实际只读取1025字节，上限1024 | `ErrTooLarge`，先 cancel 再 Close | 内存有界，不无界 drain |
| `/early` | 200，只读2字节，声称100字节 | body 读取的 `io.ErrUnexpectedEOF` | 不能把已拿到头的 response 当完整响应 |
| `/late-status` | 先选200再选500，客户端仍200 | body Code=`REJECTED` 的业务 error | 状态选择太晚；writer 不是可反悔事务 |
| `/fixed-status` | 在写任何 body 前选409 | `HTTPError{409,REJECTED}` | 修复在协议提交前确定最终状态 |

错误处理使用 `errors.Is(err, sentinel)` / `errors.As(err,&typed)`，`%w` 保留网络/读取原因。HTTP 错误响应不一定 JSON，代码先保留非2xx状态再尝试解析 code，避免解析失败覆盖401/413/500。JSON 解码成功也不代表业务成功，要按 API 定义检查业务字段。

E04 还核对了标准库的隐式重试：`NewRequestWithContext` 遇到 `strings.Reader` 会自动提供 `GetBody`（可重新取得body副本）；Go Transport 对带 `Idempotency-Key` 且body可回放的POST，在已复用连接遭遇某些网络错误时可能自动重发。仅仅“业务代码没写for重试”不能证明没有重发。本课的订单POST要求非空body，并显式把 `req.GetBody=nil`；配合本课明确HTTP/1配置，这阻止当前 `shouldRetryRequest` / `isReplayable` 的订单回放路径。GET仍可能有标准Transport的安全恢复行为，不能笼统宣称整个client从不重试。

实际 `/lost-order` 危险端点先 `Ledger.Commit`，然后 `panic(http.ErrAbortHandler)`，标准HTTP/1 server关闭连接而不发送response。先发成功请求保证下单使用可复用连接，再观察client EOF、server handler增量恰好1、账本3→4，独立测试也检查同样事实。这里的panic是明确实验故障入口：net/http只结束本请求连接，它不能回滚已经写入的账本；正常入口最终仍完成全部实验。客户端在真实跨进程场景只知道响应丢失，不能靠看本机实验账本推断线上成功。

重试规则直接给结论，不给本课加自动下单重试循环：读取类请求还要按协议确认幂等性；业务写入先采用服务端持久幂等记录和请求内容校验；收到超时/EOF后把结果列为未知，按幂等键查询最终状态；明确允许重试时复用同一业务键，并限定总deadline、次数和退避。鉴权/非法输入/坏JSON/业务拒绝一般不能靠盲重试修复。即使服务端支持幂等，也不能随意加 `Idempotency-Key` 并假定客户端或服务端已替你实现exactly-once。

E02 的时间线：

| 步骤 | 当前参与者与实际等待 | 状态变化与断言 |
| --- | --- | --- |
| 1 | owner 调 `Client.Do(/leak)`，拿到 headers 后保存 body | open body=1，HTTP/1连接仍被此交换占用 |
| 2 | owner 调 `CloseIdle` | 不影响占用；没有空闲连接可回收 |
| 3 | 第二请求在 Transport 池等待，ctx=150ms | deadline 返回；server handled 仍为1，证明没发出第二请求 |
| 4 | owner 对已知小上限 fixture 做 `Copy(LimitReader(1MiB+1))` | 得到1048576字节和真实 EOF；不是任意远端 drain 策略 |
| 5 | owner Close 原 body，发第三请求 | same conn=1、Reused=true、body owner=0 |
| 6 | `withFixture` owner CloseIdle、Shutdown、Serve join | live=0，listener 已关；危险窗口已修复后才宣称 PASS |

E05 的时间线：

| 步骤 | owner/client 请求 goroutine | server handler | 业务状态 |
| --- | --- | --- | --- |
| 1 | owner 创建 request ctx，`doAsync` 调 `RequestJSON` | 解码订单并关闭 request body，关闭 `Entered`，等 gate | rows=0 |
| 2 | owner 等 Entered 后 `Cancel()`，再读取一次请求 Result | HTTP/1连接取消使 `r.Context().Done` 可观察 | client result error=`Canceled`，尚非finished证明，未提交 |
| 3 | owner 等 `Canceled` | 危险 handler 关闭 `Canceled`，继续等 `Release` | owner 独立断言 rows仍0 |
| 4 | owner 关闭 Release，并等 `Done` | 危险 handler 调 Ledger.Commit，再试写 response并返回 | rows=1，即客户端已取消仍新增订单 |
| 5 | 对 `/cooperative-order` 重复前面动作 | 正常 `waitGate` 选取消分支并返回，不调 Commit | rows仍1，没有第二次新增 |

`cancel()` 只发通知；`Canceled` 是 server **观察到通知**；`Done` 是 handler **到达返回路径**；`OwnedRequest.Result` 是 client **交付请求结果**；`OwnedRequest.Finished` 关闭才是 client **收尾完成**。收到结果不代替finished，工作ctx取消不代替join。fixture cleanup还要等server连接回收和`Serve`返回。

修复的 `ctx.Err()` 二次检查覆盖“Release 与 ctx 同时 ready 时 select 可能选 Release”的窗口，但检查不能与远程数据库 commit 原子化。取消与提交在更晚时刻竞态时，客户端仍可能不知道业务是否成功；生产应通过幂等键查询最终订单状态，并依业务约束决定重试，不把 `context.Canceled` 翻译成“订单一定没创建”。

## 超时分阶段理解：E06～E11

| 预算/事件 | 当前等待点 | 本课实际证据与重要边界 |
| --- | --- | --- |
| `net.Dialer.Timeout=500ms` | 新建 TCP 连接 | E01/E09 有真实 `ConnectDone=true`；E09 关闭本机端口得到 `*net.OpError{Op:dial}` 的拒绝。**本次没有制造OS TCP拨号超时**，不把拒绝当超时 |
| `TLSHandshakeTimeout=150ms` | TCP已通，但 TLS 没完成 | E09 raw peer 实际接收 ClientHello 首字节后不回答；trace TCP=true，TLS started=true/completed=false，无HTTP首字节，握手超时 |
| `ResponseHeaderTimeout=150ms` | 请求完整写出后等 response headers | E06 Entered=true，handler未写头，客户端返回 awaiting response headers；server观察取消并退出 |
| `Client.Timeout=180ms` | connect、重定向、headers和body 的整体调用预算 | E07 `Do=200,err=nil` 后 `ReadAll` 0字节返回 deadline error；计时没有因 Do 返回而结束 |
| request ctx cancel | 取得连接/发送/等头/读body的当前请求范围 | E05 取消等头；E08 Do返回200后显式cancel，Body.Read返回Canceled。无须等 Client.Timeout=3s |
| `Server.ReadHeaderTimeout=250ms` | server 等完整 request headers | E10 raw TCP只写半个header，读取到EOF，handler调用0 |
| `Server.ReadTimeout=700ms` | 从请求读取开始到整个body读完的预算 | E10写完整header但body只写 `{`；Decode被实际读deadline打断，response400，ledger0 |
| `Server.WriteTimeout=100ms` | server实际写入response网络 | E11 handler entered后 owner 等明确220ms预算timer过期再release；Flush=i/o timeout，client=EOF，handler仍执行到Done |

client 的 `Timeout=0` 意味着没有它提供的整体预算，但 request ctx 仍可有截止时间。不能只配置 `ResponseHeaderTimeout` 就宣称“整次请求不会无限挂住”，因为完整 headers 可以很快到达，body 却永远不结束。每个 endpoint 的预算要服务业务SLO，线上需考虑 DNS、连接池等待、建连、TLS、服务端计算和流式读取。

E09 的普通 TLS 使用 `httptest.NewUnstartedServer().StartTLS()`，它有真实动态 loopback TCP 与 TLS，client 仅信任本次测试证书池；没有 `InsecureSkipVerify`。这证明真实握手和本地证书校验路径，没有验证公网 DNS、外部 CA 运维、mTLS 或 HTTP/2。TLS stall peer 是自己的 listener/goroutine，accept/read 设置2秒deadline，等待Release/peer ctx/timer后关闭连接；owner等待`peerFinished`关闭才读取peer error。它与client请求的finished是两个不同收尾信号，任何失败都通过defer先取消/释放/关闭listener，再分别用独立预算join。

`WriteTimeout` 给连接设置写deadline，它不会帮你中断 handler 的 CPU循环或强制撤销数据库更新。E11 明确看到 handler **仍运行到 Flush** 才得到失败。外部依赖要用自己的 ctx/预算；写response失败后还要依据账本/持久状态认定业务结果。

## 服务端的大小、身份和响应提交边界

`NewFixture` 实际配置 `ReadHeaderTimeout=250ms`、`ReadTimeout=700ms`、`WriteTimeout=2s`、`IdleTimeout=500ms`、`MaxHeaderBytes=8KiB`。短JSON订单通过 `MaxBytesReader=256` 额外限制 body。`MaxHeaderBytes` 管 headers，不管 body；标准库的内部缓冲与读边界有实现细节，不把它误写为任意输入恰好第8193字节就稳定拒绝的承诺。配置有实际运行，body超限、header/body慢读取与write/idle行为都有实际协议实验；本课没有为每一种畸形header做全面fuzz。

`readOrder` 的顺序是 method → Authorization → Content-Type → 有界body → DisallowUnknownFields → 第二次Decode必须EOF → 业务SKU/qty → Commit。E04实际拒绝大body413、第二个JSON对象400、无Authorization401，拒绝后新增行数0。独立测试补未知字段400、非法数量422。校验在 Commit 前，防止无效输入形成账本副作用。常量token是合成本机教学身份，不是用户秘密或可用于生产的认证方案。

`/identity` 只用真实 `Request.RemoteAddr`。测试发送伪造 `X-Forwarded-For=203.0.113.99` 后得到loopback peer，证明未盲信用户头。生产需显式确认受信代理网段、代理是否清洗/覆盖头，再按部署协议计算真实来源；不能按某框架默认行为推断可信范围。客户端不跟随重定向，避免订单方法语义和token意外跨origin；本课未实现跨域安全策略全集。

`writeJSON` 先Marshal再选择最终状态，然后写body。第一次 `Write` 未显式WriteHeader时会隐式选200；选过最终2xx～5xx后不能重新选择。E03 late-status的第二个500被忽略，客户端仍200；修复把拒绝决策移到第一次写之前。1xx信息响应和trailers有额外规则，当前实验不用它们。写出body以后发生业务错误时，若协议不是长流，只能依据已提交response处理连接/日志，不能假装状态还能改回500。

## E12～E13：长流和空闲池

E12 `/stream` 用两段NDJSON。server在第一段写入前设置 `ResponseController.SetWriteDeadline(now+1s)`，写 `part=1` 并Flush；client**实际读到第一行**之后 owner 释放gate，server更新下一段deadline，写 `part=2` 并Flush，client最后读到EOF再Close。不是“调用Flush一定对方已收到”的绝对化结论，接收者实际读取才是对照事实。

普通JSON往往有短的总写预算，长流则可能持续数分钟，固定短 `Server.WriteTimeout` 会不适合它。可为stream独立listener/预算，或在支持的writer上由handler用ResponseController控制段写deadline，并为每段读取等待、整体生命周期、取消和业务数据大小设置策略；当前两段测试只证明这些API在真实标准HTTP/1 writer上的路径。没有在handler返回后继续用writer，没有把它交给无owner的后台任务。

E13故意让server `IdleTimeout=500ms` 小于client `IdleConnTimeout=2s`，等待真实ConnState通知直到live=0，再发下一请求。下一连接编号2、`Reused=false`，所以回收来自服务器空闲预算；两端设相同值会混淆触发源，代码已避免。`CloseIdleConnections` 用在owner退出边界，不在每次请求后调用。

`MaxIdleConnsPerHost` 只限制保留多少空闲连接；`MaxConnsPerHost` 限制某host的拨号、活动、空闲连接总数。E02的HTTP/1.1一个占用确实让第二请求排队，但不能从这里推出“我的业务最多同时执行N个”：不同host有自己的池，HTTP/2一条连接可同时承载多条stream，server收到请求后还可能继续做业务。业务并发限制应有明确的semaphore/队列/上限及取消策略，不能由socket池参数冒充。

## E14：已取消工作ctx，不代表请求已经收尾

主验收发现首版一个必要缺口：`waitRequest(ctx,done)`同时等结果和工作ctx；工作ctx取消时它可能直接返回，caller仍未确认请求goroutine退出。`cancellationExperiment`的Entered等待失败只cancel；header/write实验和测试的早返回也没有显式join。首版正常路径的输出确实通过，但这不足以证明这些失败路径收尾正确。原日志保存在`evidence/v1-*.txt`，不把旧PASS当新失败路径的证据。

修复把异步请求移到`OwnedRequest`中。`doAsync`创建独立child ctx和两个信号：容量1的`result`仅交付一次请求error；`finished`通过关闭广播表达最终收尾。`RequestJSON`已经返回并关闭body之后，result可能先就绪；worker的附加清理、child cancel和owned计数释放之后才关闭finished。`Result(ctx)`返回两个error：第一个是实际请求结果，第二个是owner停止等结果的原因。后者不会伪装成请求已经结束。

每个doAsync caller创建后立即注册退出清理。具体顺序是：

1. 取消owned request的child ctx，让真实HTTP调用有机会返回。
2. 打开这个请求需要的handler/cleanup gate；解除gate必须放在join之前。
3. 用`context.Background()`创建独立3秒清理预算，等待`finished`关闭。不能继承已经取消的工作ctx。
4. 用`errors.Join`保留主错误和清理错误。如果清理预算到期，返回“未完成收尾”错误，不写join成功。
5. 再由fixture owner关闭空闲池、Shutdown、等待Serve与连接归零。

`cancellationCase`使循环每次请求有自己的defer作用域；header/write实验的具名返回error让清理失败能够进入最终返回。测试在doAsync后立即注册`ownRequestForTest`，它的`t.Cleanup`后注册先执行，因此`t.Fatal`也会先停止并join请求，然后才执行fixture清理。TLS原始peer同样在创建后注册完整退出defer，listener.Close还会解除尚未完成的accept。正常路径已经消费result之后，StopAndJoin只等finished广播，**不会二次接收已耗尽的结果通道**。

E14故意在真实请求取消后的附加清理中放一个独立gate，使用真实HTTP的`/headers`路由，无sleep或NumGoroutine猜测。工作ctx已经取消，result也已消费；owner再开始StopAndJoin，测试控制者在确认owner进入join等待后才释放cleanup gate：

| 步骤 | 参与者/真实等待点 | 实际观察 |
| --- | --- | --- |
| 1 | request在真实handler等待response headers，owner等Entered | 请求确实已到服务器 |
| 2 | 取消work ctx；RequestJSON返回Canceled，worker交付result，再进入cleanup gate | result可读；server观察取消；owned_requests=1 |
| 3 | owner调用StopAndJoin，独立预算等待finished | cleanup gate未释放：finished=false、owner_returned=false |
| 4 | 控制者打开cleanup gate | worker记录cleanup-finished，完成其defer，关闭finished |
| 5 | owner接收finished后返回 | 事件严格为`[cleanup-finished owner-return]`，owned_requests=0 |
| 6 | 再次StopAndJoin | finished已关闭，可立即重复等待，不再消费result |

`joinWaiting`只表示owner已经检查到finished未关闭并进入等待，不能当完成信号。提前状态快照与最终事件顺序一起断言，屏障未打开之前cleanup无法正常返回；如果join错误提前返回，事件次序或owner_returned断言会失败。fault cleanup仍有独立3秒安全timer，任何中途错误的defer都先开gate再join两个owned参与者。

新增独立测试分别验证：真实取消的cleanup等待顺序；只取消结果观察者ctx不会结束真实请求；已取消ctx用于waitFinished会得到join失败；Entered早返回仍join；主错误和实际清理错误同时保留。`OwnedRequests`只计本课创建且尚未finished的请求，不是全进程goroutine数量。

## goroutine、屏障和资源 owner 表

| 对象/参与者 | 创建/owner | 真实等待点 | 停止与最后等待 |
| --- | --- | --- | --- |
| demo主goroutine | `main` | `Run`、每项实验 | 30秒ctx；正常所有函数返回 |
| `Server.Serve` goroutine | `NewFixture` / `withFixture` owner | accept listener、标准库server工作 | `Shutdown`关闭listener；owner读取 `serveDone` |
| HTTP连接/handler goroutine | net/http / fixture server | 协议读写、`waitGate`、body Decode | request ctx/Read/Write deadline；Shutdown等handler归空闲，ConnState确认live=0 |
| `doAsync`请求 goroutine | 当前实验owner / `OwnedRequest` | `RequestJSON`的Do/Body读取、E14的cleanup gate | Result仅交付结果；defer Cancel/Open，独立3秒预算等Finished；body由RequestJSON关闭 |
| TLS stall peer goroutine | `tlsExperiment` | accept/read deadline，Release/peer ctx/timer | 自有conn.Close后关闭peerFinished；失败defer Cancel/Open/listener.Close，再独立join |
| `Gate.Entered` | handler关闭，owner等待 | 证明handler已到指定协议阶段 | 只通知，不停止任何任务 |
| `Gate.Canceled` | handler观察ctx后关闭，owner等待 | 证明server观察了取消 | 只通知，不代表已退出/已回滚 |
| `Gate.Release` | 实验owner关闭，handler等待 | owner控制后续处理 | 失败清理也由Fixture.Open所有gate |
| `Gate.Done` | handler退出路径关闭，owner等待 | 证明handler已走完 | 还需fixture最终关闭连接/Serve join |
| Client/Transport/Response.Body | withFixture创建，RequestJSON拥有单次body | Transport池、网络body | 每body Close；owner退出CloseIdle；ownership=0 |
| raw TCP conn | E10/E09当前函数 | SetDeadline后的协议读 | defer Close；不管理别的模块连接 |
| deadline/timer | main/每实验/request或waitGate | ctx.Done/timer.C | cancel/Stop由创建者defer执行 |

channel方向 `<-chan` 表示等待方只有读取能力。关闭channel使接收立即就绪，适合作为一次性广播。`Gate.Open` 用 `sync.Once` 防止正常路径和清理路径双重close。`select` 在多个分支同时ready时可选择任意分支，本课不承诺取消之后绝不会走一次Release分支；关键副作用前额外检查并明确竞态边界。

`withFixture` 使用具名 `err`，defer执行时把清理error与业务error用 `errors.Join` 组合；资源清理失败会使整轮FAIL。即便工作函数提前出错，fixture也通知stop、打开全部gate、3秒Shutdown，失败时Server.Close兜底，然后等待Serve返回和live=0。Gate的安全timer不是调度猜测，它是失败上限。E11的220ms timer是主动跨过100ms写deadline来制造故障，E10的读等待是协议超时实验；没有用sleep推测谁先运行。

## 独立断言与证据

`lab_test.go` 不只运行demo文本，直接检查以下事实：

- 8个同时开始的真实POST，同幂等键订单ID全部1，账本只有1行；race检查共享对象访问。
- unsafe重复POST确实留下2行，证明危险代码没有被预期日志掩盖。
- 提交后丢响应的POST返回EOF，账本确实新增1行，handler增量恰好1，证明当前订单路径未被Transport自动重放。
- 超限实际只读取 `max+1` 字节，body owner回到0；提前断开保留 `io.ErrUnexpectedEOF`。
- HTTP200/未提交账本0，晚改状态仍200，修复先选409。
- 第一个body占用时第二请求deadline，server handled仍1；CloseIdle不修复活动body。
- 取消副作用窗口中分别等Entered/Canceled/Done，危险版1行、协作版0行。
- 非法输入无副作用，伪造代理头不成为真实peer。
- 真TLS、慢header/body、WriteTimeout、两段stream与IdleTimeout的阶段实验。
- 已观察取消的真实请求仍在cleanup gate时owner不能返回；释放后事件为cleanup-finished再owner-return；结果已消费仍可重复join。
- 结果观察者取消不是请求退出，Entered早返回也join，主/清理错误都保留。

真实命令输出和清理结论见 `STATUS.md`、`evidence/demo.txt`、`evidence/test.txt`、`evidence/race.txt`。默认本机 `gcc` 是32位MinGW，最初 `-race` 在runtime/cgo编译时失败，原始诊断保留在 `evidence/race-default.txt`。当前验证使用课程统一WinLibs GCC16.2.0 amd64 compiler，只对验证进程设置CC/CXX/CGO/PATH。首版race通过日志保留在`v1-race.txt`；本次返工后的14项测试实际通过，正常test用时3.221s，race用时4.371s，vet无诊断，demo14项通过；全局工具链未改变。

`M17 ALL PASS` 指本机真实协议、当前进程账本、body所有权、关闭自有listener、等待Serve/peer退出。它不包含生产负载、公网故障、HTTP/2多路复用、OS TCP连接超时复现、持久数据库事务、跨实例幂等和完整安全验收。

## 官方契约与当前源码定位

2026-10-12实际打开官方 [`net/http`](https://pkg.go.dev/net/http)、[`net/http/httptest`](https://pkg.go.dev/net/http/httptest)，并用当前Go1.27.0的 `go doc` 核对 `Client.Do`、`Client.Timeout`、`Transport.ResponseHeaderTimeout`、`Server`、`ResponseWriter`、`Server.Shutdown`。以下定位属于本机版本源码，库实现更新后行号可能变化：

- `D:/Go/src/net/http/client.go`：`Client.Do`（约603行）→ `Client.do`，请求策略及整体超时。
- `D:/Go/src/net/http/transport.go`：`Transport.roundTrip`（约611行）、`persistConn.addTLS`（约1781行）、`persistConn.readLoop`（约2444行）、`waitForBodyRead`（约2562行）、`bodyEOFSignal.Close`（约3250行）。沿readLoop能找到body EOF/early close如何决定是否放回空闲池，以及Go1.27有限drain。
- `D:/Go/src/net/http/transport.go`：`persistConn.shouldRetryRequest`（约844行），以及`request.go`的`Request.isReplayable`（约1561行），能定位复用连接网络错误、方法/幂等键和GetBody如何共同控制标准库自动重发。
- `D:/Go/src/net/http/server.go`：`response.WriteHeader`（约1202行）、`response.Write`（约1650行）、`conn.serve`（约1929行）；`conn.serve`经过mux/handler调用同步执行本课`serveHTTP`。

这份源码定位帮助读真实后端，语言规则、公共API契约和当前内部实现分别看待：goroutine并发执行/ctx协作取消是Go编程模型，ResponseWriter生命周期/Client.Timeout含body是库契约，自动drain的具体字节和timer数值是1.27当前实现。
