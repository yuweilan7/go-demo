# M25：从一次缓存读取到 Redis 的原子边界

这课已经在真实 Redis **8.2.8**、Go **1.27.0 windows/amd64**、锁定的 **go-redis/v9 v9.23.0** 上运行。入口的 46 项断言包括真实键值、TTL、并发更新、事务/脚本错误、认证、借池与响应读取故障；`STATUS.md` 保存准确命令、失败修复和证据范围。这里把 Redis 当缓存和合成限流计数器，没有订单数据库、支付或真实订单提交。

先花 10～20 分钟读 core：一个订单缓存、一条计数器、一次错误写入的真实结局。再读 all 中的资源和超时实验。程序执行本身约数秒；教学时间用来沿注释跟调用和解释日志。无需准备练习答案，反例、修复和完整解析就在代码及本文中。

## 现在必须理解什么

`*redis.Client` 是可被并发请求共享的长寿命客户端和连接池 owner。一次 `GET` 借一条连接，发送命令、读取 RESP 回复、归还或淘汰连接；不是每次请求都新建并 Close 一个 client。`*StringCmd`/`*IntCmd` 保存某条命令的结果与 error。Redis 的一条 `INCR` 原子，不表示 Go 中多个调用组成的业务流程原子。`cancel()` 是通知，`Done` 关闭、socket I/O 返回和 goroutine 全部退出必须分别跟踪。

可以延后学习 Cluster/Sentinel、WATCH 冲突重试、PubSub、客户端缓存、自动 pipeline、Redis Functions、AOF/复制配置。本课是单节点 DB0 的实际协议与副作用；没有测这些扩展，也没有测 Redis 重启恢复。Redis 8.4+ 的新 CAS 或更高版本的 `INCREX` 不应从当前在线文档直接抄到 8.2.8。本课用这一实际版本支持的 `INCR`/`PEXPIRE`/Lua。

## 运行与接入

从课程仓库根目录执行，`go -C` 必须选中这个独立 module。根 `go test ./...` 不进入嵌套 module。

```powershell
go -C lessons/m25_redis_client run ./cmd/demo -part core
go -C lessons/m25_redis_client run ./cmd/demo
go -C lessons/m25_redis_client test -v -count=1 -timeout 50s ./...
go -C lessons/m25_redis_client vet ./...
```

默认 `-part all` 运行完整实验，`-part core` 只运行键/TTL/并发/原子边界。默认单测不连接 Redis，三个实库测试会明确 SKIP；要复跑已经验过的真实测试，在**当前 shell 进程**设置开关并恢复：

```powershell
$savedLive=$env:GOLEARN_REDIS_INTEGRATION
try {
    $env:GOLEARN_REDIS_INTEGRATION='1'
    go -C lessons/m25_redis_client test -v -count=1 -timeout 50s ./...
} finally {
    $env:GOLEARN_REDIS_INTEGRATION=$savedLive
}
```

`LoadConfig` 默认只读主 agent 的 `D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json`。可用 `GOLEARN_SERVICE_INDEX` 或 `-index` 指向另一份相同契约索引；本课不启动 SSH、不改服务索引。`services.redis` 必须有 `state=READY`、`host=127.0.0.1`、合法 `port`、实际 `server_version` 和 `credential_file`。秘密文件是独立 JSON，字段为 `username/password/db`，DB 必须为0；相对秘密路径相对索引目录解析。这里不输出示例口令或整个配置，也不提供将口令放命令行的入口。

本次服务是主 agent 持有的 SSH 转发 `127.0.0.1:16389`。严格 SSH/host key 和基础设施 owner 见 M00 的 [接入/隔离契约](../m00_environment_infra/infra/INSTALLATION_CONTRACT.md)。此端点与本机绝对路径是本次环境资产；新机器需要提供自己的 READY 索引和凭据，不能仅凭 Git 文件假定隧道存在。PENDING、缺字段或非显式 IPv4 loopback 会在 client 构造/拨号前返回错误。

默认安全报告 `.local/latest.json` 只记录版本、检查、精确键名和收尾数字，`.local/`、`.secrets/` 都被本模块忽略。每次产生随机 `golearn:m25:<run>:` 前缀；没有扫描删除、FLUSHDB/FLUSHALL 或 DeepHelp 键操作。正常写入使用90秒 TTL，单位修复使用30秒；无TTL反例观察后立即修复，最后 owner 对已登记的精确键执行 `DEL` 与 `EXISTS`。这不是抵抗进程强杀的万能 finally：强杀不能运行 defer，故意无TTL窗口仍可能留下需按原报告精确核对的孤儿键。

## 按什么顺序读源码

| 文件与符号 | 职责与 caller | 返回/资源归属 |
| --- | --- | --- |
| `cmd/demo/main.go: main → execute` | runtime 调入口；flag/env 提供索引、part、报告路径 | 总ctx最多35秒；execute结束后main才决定退出码，避免os.Exit跳过资源defer |
| `internal/lesson/config.go: LoadConfig` | execute/真实测试读取READY和秘密 | 返回Config值副本；密码只在内存进入driver，不序列化 |
| `run.go: Run → options` | Run创建正常client、报告、键registry、socketStats | lab共享client指针；Run是长寿命client、精确键和总报告的owner |
| `commands.go: basic → readOrder / waitMissing` | 按GET/SET/HASH/TTL/miss/格式错误做串行实验 | 不Close共享client；返回业务error上交Run |
| `concurrency.go: lostUpdate` | 用双GET屏障复现丢更新，再8次INCR | 每个局部taskGroup拥有任务、cancel和gate；return前join |
| `atomic.go: atomicBoundaries / expiryWindow` | pipeline、MULTI/EXEC、Lua与INCR→EXPIRE窗口 | 检查Cmd.Err之后再独立GET/PTTL核对真实副作用 |
| `faults.go: authentication / poolWait / socketBudgets` | all追加错误凭据、借池、四类响应阶段实验 | 每项拥有独占client；故障不污染正常client或共享服务 |
| `gate.go: faultClient → gatedConn.Read` | driver Dialer装饰真实连接，只扣留一份真实回复 | 无后台代理goroutine；captured表示Read已到gate，deadline来自driver |
| `tasks.go: taskGroup.start/wait/finish` | 实验owner登记有限goroutine并收尾 | 结果槽写入→defer close(done)→owner receive；真实Done之后读结果 |
| `run.go: Run.defer → DEL → EXISTS → Close` | 任意步骤失败也运行，新预算独立于已失败请求 | 清理error与原error Join；所有连接Open=0和任务Started=Joined才completed |
| `lesson_test.go` | testing调用三项无网络断言、三项显式真实实验 | suite后另一client独立EXISTS；早退在真实响应扣住时验证join/Close；warmup真实认证失败保留错误链并Close |

完整应用链是：`main → execute → LoadConfig → Run → INFO(真实版本) → basic → lostUpdate → atomicBoundaries → expiryWindow → [all: authentication → poolWait → socketBudgets → responseExperiment×4] → 各函数defer finish/Close → verifyTTLs → Run.defer精确清理/Close → WriteReport → main退出码`。任何步骤返回error，后续业务步骤停止，但已经登记的资源仍按defer清理。

## 普通 Get 的库内调用链

这一链来自实际下载的 **v9.23.0**，不是根据方法名字猜出来的。普通 `client.Get(ctx,key)` 在调用内部就等待完成；随后 `.Result()` 读取已保存在命令对象的值/error。pipeline对象的同名方法只排队，必须另看下一节的差别。[官方Go指南](https://redis.io/docs/latest/develop/clients/go/)

```text
cmdable.Get → NewStringCmd → Client.Process → processHook → baseClient.process
→ processWithRetry / _process → withConn → getConn / _getConn → ConnPool.Get
→ waitTurn（借池额度）→ [无连接: dialConn → 本课Dialer.DialContext → initConn HELLO/AUTH]
→ pool.Conn.WithWriter → SetWriteDeadline → RESP写命令
→ pool.Conn.WithReader → SetReadDeadline → StringCmd.readReply
→ withConn.defer releaseConn（健康归还；错误可能淘汰）
→ Get返回*StringCmd → Result返回(value,error)
```

源符号定位：[`string_commands.go:Get:139`](https://github.com/redis/go-redis/blob/v9.23.0/string_commands.go#L139)、[`redis.go:Client.Process:2723`](https://github.com/redis/go-redis/blob/v9.23.0/redis.go#L2723)、[`withConn:1326`](https://github.com/redis/go-redis/blob/v9.23.0/redis.go#L1326)、[`_process:1724`](https://github.com/redis/go-redis/blob/v9.23.0/redis.go#L1724)、[`ConnPool.waitTurn:1205`](https://github.com/redis/go-redis/blob/v9.23.0/internal/pool/pool.go#L1205)。维护公司代码时先看实际go.mod和选中的go-redis版本；本版最低Go为1.26，本课go指令为1.27.0，没有toolchain/replace或共享go.work。

Go的参数传递是值传递；`*redis.Client`参数复制的是指针值，仍指向同一个池对象。`GET`得到的string和 `.Int()` 得到的int则是worker专属结果值。client适合共享，不表示`Pipeline`也适合共享：本版`Pipeline`的排队slice不是并发安全容器，每批在一个函数里创建和使用。[锁定源码Pipeline契约](https://github.com/redis/go-redis/blob/v9.23.0/pipeline.go#L51)

## 实验1：缓存命中、miss、格式错误与TTL

业务场景是读订单摘要。`basic → readOrder`将 `GET` 与JSON解码分成两个阶段：

| 输入与日志 | 实际键/结果 | 应该走的业务分支 |
| --- | --- | --- |
| `get_set_json` | `order`值为`{"order_id":"synthetic-25","state":"cached_only"}` | found=true；读到的是缓存DTO，不是数据库提交凭证 |
| `redis_nil_is_miss` | 登记的`missing`从未写过；GET回复nil | found=false,error=nil；可以决定回源，本课没有伪造回源订单 |
| `json_error_is_not_miss` | `bad-json`实际有`{broken-json` | found=true且错误链含`*json.SyntaxError`；不能把数据损坏伪装成miss |
| JSON修复 | 同键SET完整DTO，90s TTL，再次readOrder | 修复真实格式后可解码 |
| `hash_fields` | HSET新增2字段，HGET state=pending，HGETALL两字段 | HSET的2是新增字段数；已存在字段的覆盖不一定新增 |
| `wrongtype_is_server_error` | 对hash执行GET得到WRONGTYPE | 合法请求到Redis后类型不匹配；改为HGET，不能重复GET盲重试 |
| `forgot_ttl` | SET(...,0)后TTL=-1ns | 0是无过期；不是立即到期；观察后EXPIRE(90s)修复 |
| `set_overwrite_drops_ttl` | 已有90s TTL的键被SET(...,0)覆盖 | 旧TTL被移除；修复SET带90s，然后SET KeepTTL保存旧期限 |
| `duration_units` | `time.Duration(30)=30ns`，目标`30*time.Second` | duration底层为纳秒；裸数字不能读成秒 |
| `millisecond_ttl_mistake` | 故意SET 30ms；本次PTTL=-2ns | SSH往返后它已经到期，符合“只活30ms”的实际副作用，不要求一定读到正剩余值 |
| `actual_expiry` | 有界重复GET观察到redis.Nil，TTL=-2ns | GET与TTL共同证明真实过期；不是等sleep后猜测过期 |
| `unit_repair` | 同键SET 30s，本次PTTL约29.936s | 修复单位后实际剩余期限接近30s，不能要求毫秒精确值 |

Redis协议TTL以秒、PTTL以毫秒返回剩余时间；`EXPIRE`/`PEXPIRE`分别使用对应协议单位，go-redis方法接收`time.Duration`并换算。特殊状态 `-1`=存在但没过期，`-2`=不存在。[TTL命令](https://redis.io/docs/latest/commands/ttl/) [EXPIRE命令](https://redis.io/docs/latest/commands/expire/)

**本版库的额外表示规则必须读源码**：`DurationCmd.readReply`对`-1/-2`不乘precision，直接保存`time.Duration(-1/-2)`，因此打印`-1ns/-2ns`。它们是状态哨兵，比较`-1*time.Second`会错；正值则由TTL乘秒、PTTL乘毫秒。`basic`使用正确比较并由真实日志证明。[DurationCmd源码:1709](https://github.com/redis/go-redis/blob/v9.23.0/command.go#L1709)

普通`Set`返回`StatusCmd`，`Get`返回`StringCmd`，`Incr`返回`IntCmd`，`Expire`返回`BoolCmd`。error=nil是命令没有报错，不意味着Bool一定true：对不存在键EXPIRE可返回false。示例对已有键操作，真实HGET/TTL继续核对存在和期限。只用`.Val()`会把miss/错误下的零值读成成功，`readOrder`始终先处理error。[官方结果处理入口](https://redis.io/docs/latest/develop/clients/go/)

## 实验2：每条命令原子，读改写仍丢更新

把`lost-update`初始化为0并带90s TTL，两个goroutine都是同一个client的caller。每个worker的GET已完全返回后向容量2的`read` channel通知；owner收齐两份才close共享gate。

| 顺序 | 参与者与确切等待点 | 状态与下一步 |
| --- | --- | --- |
| 1 | owner: SET0、登记两任务 | client共享，task结果槽和局部value各自独立 |
| 2 | worker A/B: Get(...).Int() | 两个GET都得到0；不保证A/B先后 |
| 3 | A/B: `await(workerCtx,gate)` | owner还没close gate，两worker都不能发SET |
| 4 | owner: 收两次read通知→release | 此刻旧值已固定；close gate使两worker可继续 |
| 5 | A/B: SET(value+1) | 各写1；单条SET原子，最终仍是1 |
| 6 | A/B退出defer close(done)；owner g.wait | 真正join两任务后独立GET，证明 final=1而业务希望2 |
| 7 | owner再SET0；8个有限worker各INCR | 每条加1在Redis内完成；返回1..8次序不保证 |
| 8 | ownerjoin8个Done→收集唯一结果→GET | 本次unique_replies=8、final=8；独立断言总数和返回值范围 |

这没有Go共享int数据竞争，但存在业务读改写竞争；race检测器不会替你发现Redis层丢更新。修复把“读取并加1”的边界移到服务器单条命令。Java里的原子对象类比可帮理解“复合操作要有统一边界”，不过这里是跨连接的远端协议，Java本地锁只保护本进程，不能覆盖其他服务实例。[Redis INCR与计数用法](https://redis.io/docs/latest/commands/incr/)

## 实验3：pipeline、MULTI/EXEC和Lua各保证什么

`atomicBoundaries`用同一组“前写→错误→后写”结构解释实际副作用。

| 实验与时间线 | 真实error和键结局 | 修复/业务含义 |
| --- | --- | --- |
| Pipeline先排队SET before、INCR hash、SET after | Exec前另一次GET before=miss；Exec后第一/第三Cmd成功，中间WRONGTYPE，独立GET见两次写入都保存 | pipeline减少往返、逐条回复；没有自动业务原子性或回滚。排队Cmd.Err=nil只是尚未执行 |
| TxPipeline包MULTI/EXEC，同样三命令 | EXEC隔离执行；第一写成功，中间WRONGTYPE，第三仍成功；独立GET见两写 | 运行期错误不撤销已成功命令。不可把它当SQL transaction rollback |
| TxPipeline排SET后放参数数量错误的INCR | EXECABORT；独立GET目标=miss | 排队期语法错误与执行期WRONGTYPE结局不同；不能把两个error都解释成“事务全撤销” |
| Lua先SET before(PX90000)，INCR hash，再SET after | `redis.call`遇WRONGTYPE终止；before存活、after=miss | 原子执行是不能被其他命令插入；不是脚本错误会回滚此前写入 |

`Pipeline.Exec`给出命令列表以及首个错误，不能只看一个总error后假定所有命令都失败；代码检查每个Cmd再GET实际值。一次运行没观察到其他client插入pipeline，不会改变其缺少隔离契约的事实；本实验实证的是批量失败的部分副作用，没有用“前后都在Redis”倒推出隔离。TxPipeline源码确实用`wrapMultiExec`，库里没有为它生成SQL式回滚。[Redis事务错误规则](https://redis.io/docs/latest/develop/using-commands/transactions/) [v9.23 TxPipeline:2892](https://github.com/redis/go-redis/blob/v9.23.0/redis.go#L2892)

修复需要先问业务不变量：单字段计数选INCR；跨命令组合选短小的Lua或合适的事务条件，但要在写之前校验类型/参数、分析每一个后续失败点。`redis.pcall`可以把命令错误转成Lua可处理的值，仍不会自动撤销早先写入。复杂订单事实不能靠Redis里“存在一个状态键”代替数据库提交、幂等约束或事件进度。

Lua脚本执行期间会影响其他客户端，长循环会放大共享服务延迟；官方默认执行时间阈值为5秒，但超阈值不是自动强制回滚/终止，而会进入BUSY处理。已执行写入的脚本不能随意安全KILL。这是机制说明，本课没有向共享服务注入慢/无限脚本，也没有改`busy-reply-threshold`。[脚本运行时间边界](https://redis.io/docs/latest/develop/programmability/#maximum-execution-time) [Lua API](https://redis.io/docs/latest/develop/programmability/lua-api/)

## 实验4：INCR成功、EXPIRE没发出的真实窗口

业务是“第一次请求创建计数窗口，只在计数1时设期限”。`expiryWindow`的第一任务执行INCR=1后关闭`afterINCR`，随后停在`expireGate`。owner收到这个屏障才让第二请求INCR；它得到2，旧代码的`n==1`分支不会设TTL。owner此时真实TTL=-1ns，证明“已经有数据”与“数据会过期”是两件事。

owner先取消第一任务，**保持expireGate关闭**，使任务只能走取消分支；随后使用独立3秒预算等待Done。第一INCR副作用仍存在，EXPIRE从未发送，任务返回context.Canceled；真实TTL仍是-1。取消通知没有反向撤销Redis写入。

修复 `incrementWithExpiry` 在同一短脚本中执行INCR，首次计数或已有孤儿TTL<0时PEXPIRE。对这个坏键运行后计数=3且PTTL约89.939s；对新键首次计数=1时即有期限。后续正常计数不会重复刷新窗口，因此不会因每次请求都EXPIRE而不断延期。TTL参数必须是正整数毫秒；脚本在写之前拒绝0，实际GET保持1，证明这一验证路径未递增。

这个修复保证当前脚本成功执行内的计数与过期组合不受请求间插入，不保证网络返回超时就没执行，也不保证任意扩展脚本的运行错误会回滚。给已有计数器加TTL是一项业务政策：本课选择恢复孤儿窗口；实际限流系统要定义孤儿该恢复还是报警。90秒参数是课程合成策略，不是生产推荐数值。

## 实验5：三类错误，以及资源 owner

最基本的三分法是 **miss / 服务端或应用数据错误 / 调用与资源等待错误**，再在每组内保留具体原因：

| 判断方法 | 本次事实 | 处理方向 |
| --- | --- | --- |
| `errors.Is(err,redis.Nil)` | GET不存在键 | 缓存miss；按业务回源，不能伪造订单成功 |
| `redis.HasErrorPrefix(err,"WRONGTYPE")` | GET hash / INCR hash | 修命令与数据类型；重试同样命令没有意义 |
| `errors.As(err,&json.SyntaxError)` | Redis里有损坏JSON | 记录内容损坏、校验/失效/重建策略；不能当miss吞掉 |
| `redis.HasErrorPrefix(err,"WRONGPASS")` | 独占错误口令client AUTH被拒绝 | 修接入秘密/账号；正常client仍能PING，未改共享ACL |
| `errors.Is(err,context.Canceled)` | 第二请求真实借池等待后取消 | 该请求尚未发GET；owner仍须等任务Done |
| `errors.Is(err,redis.ErrPoolTimeout)` | 第三请求借池超过200ms | 等待池额度失败；不是Redis nil，也不是命令响应timeout |
| `errors.As(err,&net.Error)`且Timeout | 已发送命令的响应阶段deadline | 可能已有副作用，分类和核对结果；不能盲重试INCR |
| `errors.Is(err,redis.ErrClosed)` | owner已关闭client后又GET | 生命周期已经结束，修调用归属；不是服务认证或缓存错误 |

`HasErrorPrefix`在本版先剥开头`ERR `，所以脚本`ERR ttl ...`传prefix`"ttl"`。首跑写成`"ERR ttl"`，计数实际保持1但断言失败；现已按实际源码纠正，失败日志未改写。[Error接口和前缀实现](https://github.com/redis/go-redis/blob/v9.23.0/error.go#L36)

`poolWait`只创建一个client的1连接主池，先warmup完成真实认证。持有任务发送GET后真实socket.Read已经取到回复，被本课gate扣住；该任务仍持有借池额度。这里只故障这条独占连接，正常l.client可继续使用共享服务。

| 顺序 | 参与者/等待点 | 真实观察与下一步 |
| --- | --- | --- |
| 1 | held worker: gatedConn.Read等待gate.release | captured关闭；池total=1、idle=0；不是猜它在网络I/O里 |
| 2 | waiter: ConnPool.Get→waitTurn→信号量Acquire | PendingRequests≥1被owner观察；只有一个池额度已被held占有 |
| 3 | owner requestCancel→等待waiter.done | waiter=context.Canceled；held仍在Read；取消一个请求不Close共享client |
| 4 | timed worker: 同一借池等待 | 200ms PoolTimeout后返回redis.ErrPoolTimeout，尚未发命令 |
| 5 | owner open gate→g.wait | held得到真实`pool-real-value`并Done；三任务全部join |
| 6 | owner读PoolStats | pending=0、idle=1，证明连接可归还；WaitCount不是当前等待者屏障 |
| 7 | owner Close专属client→再尝试GET | ErrClosed；Close发生在所有worker join之后 |

v9.23.0的`waitTurn`先观察ctx，再抢信号量；慢路径Acquire受ctx和PoolTimeout控制。`WaitCount`只在成功等到额度后递增，不能用它证明当前取消等待者已入池。本课用当前`PendingRequests`及total/idle状态定位真实等待。[实际pool源码](https://github.com/redis/go-redis/blob/v9.23.0/internal/pool/pool.go#L1205)

## 实验6：ctx、socket deadline与已执行但回复丢失

`context.WithTimeout`在Go层给出请求期限，不保证所有库、所有协议阶段在同一瞬间中断。Go1.27实际`go doc`与官方context契约都说明cancel关闭Done并释放相关资源；调用方仍有责任等工作退出。[context文档](https://pkg.go.dev/context)

本版的三个阶段要分开：借池`waitTurn`收到原ctx；拨号`dialConn`为每次尝试合并DialTimeout并由本课DialContext观察ctx；读写`_process`调用`c.context(ctx)`，只有`ContextTimeoutEnabled=true`才传请求ctx，否则传Background。`pool.Conn.deadline`选socket timeout和ctx deadline中的较早时间。取消一个**没有deadline**的ctx不会凭空改动已经设置的socket deadline。[context选择:1892](https://github.com/redis/go-redis/blob/v9.23.0/redis.go#L1892) [deadline计算:1382](https://github.com/redis/go-redis/blob/v9.23.0/internal/pool/conn.go#L1382) [dialConn:695](https://github.com/redis/go-redis/blob/v9.23.0/internal/pool/pool.go#L695)

每个响应实验都先用独占client完成PING/auth，再arm只扣住下一条业务回复。`gatedConn.Read`先取**真实Redis响应字节**，关captured作为屏障，然后按driver实际设置的ReadDeadline等待或超时；不造RESP假包，不需要启动代理进程。故障`gateTimeout`明确是本课网络响应代理给出的`net.Error`，不是服务器报错。真实写入事实另由正常client核对。

| mode / 日志 | 时间顺序与关键等待点 | 结果和界限 |
| --- | --- | --- |
| `context-disabled` | 真实GET回复已扣住；请求250ms到期；owner等ctx.Done后检查task尚未Done；再open gate | 即使ctx已DeadlineExceeded，GET仍成功返回真实值。本版默认关闭context deadline参与socket时间选择；不能据此说借池/拨号也不看ctx |
| `context-deadline` | 开启ContextTimeoutEnabled；GET回复扣住；不open gate；driver给Read设请求250ms deadline | 返回代理read timeout，本次ctx已DeadlineExceeded，任务Done已join；错误动态类型是net.Error，不必直接等于context.DeadlineExceeded |
| `manual-cancel` | 开启ContextTimeoutEnabled但请求无deadline；reply已扣住；cancel关闭ctx.Done；task仍在Read；owner再open gate | GET成功；cancel通知没有自动改既有2s socket deadline。业务必须分析副作用、停止信号与资源收尾 |
| `response-lost` | READ timeout250ms；发送INCR后reply已扣住；另一连接GET=1；不open gate直至read timeout | caller报timeout而副作用已经发生；MaxRetries=-1后另GET仍=1，无自动第二次INCR |

这些时间线由captured、ctx.Done、真实PoolStats与任务Done保证；没有`time.Sleep`猜谁先运行。250ms/2s/200ms是课程注入预算，不是生产延迟指标，精确耗时和8worker顺序不受保证。真实网络特别慢时“reply在deadline前已捕获”会失败，报告保持未完成；不能改成假回复让验收通过。

响应丢失意味着“请求是否成功”和“业务副作用是否存在”不能由同一个error决定。这个实验可以从独立GET确认计数1；生产订单可能需要幂等键、权威数据库状态和可核对的提交结果才能消除UNKNOWN。将正常client的PING或缓存键存在当订单提交证据会把两个系统的事实混在一起。本课没有订单持久数据库，`cached_only`明确保留这一范围。

## 最后谁收尾

`taskGroup.start`在go语句前登记任务，任务写value/error后由defer关done。`wait`等done再读字段，避免把“结果已可读”“ctx已取消”当成完整join。`finish`用于所有早退路径：cancel→释放本课gate→独立3秒预算join；仍未退出才Close本课专属client以打断I/O，再用新3秒预算join，错误加入返回链。正常共享client不能作为一个请求取消的手段。

`poolWait`与每个`responseExperiment`的defer注册顺序使taskGroup先finish、client后Close。全部步骤成功后先`verifyTTLs`逐个读PTTL：20个实际存在键都有正TTL、3个本应未写入的键不存在，确保修复没有遗漏。任何步骤返回后，Run用独立6秒预算DEL已登记精确键，再EXISTS确认0，最后关闭正常client并核对socket计数。`socketStats`在真实Dial成功时加计数、实际net.Conn.Close时减计数，不用Redis里键已清空推断连接结束。

本次完整日志最后是：

```text
CHECK read_modify_write_loses_one  two_read=0 two_write=1 final=1 expected_business=2
CHECK incr_repairs_lost_updates    workers=8 unique_replies=8 final=8
CHECK incr_expire_competing_window second_count=2 TTL=-1ns before first EXPIRE
CHECK pool_has_real_waiter         total=1 idle=0 pending=1
CHECK pool_wait_cancelled          waiter=context canceled held_still_waiting=true
CHECK no_automatic_write_replay    after timeout GET=1 MaxRetries=-1
CHECK registered_key_ttls          existing=20 all_positive_ttl=true expected_absent=3
CLEANUP exact_keys=23 remaining=0 cleaned=true sockets=0/10 tasks=18/18 completed=true
```

23是**登记的精确键名**，其中missing、tx-queue-abort、lua-after从未成功写入，也一样被安全纳入精确清理。任务18来自读改写2、INCR8、EXPIRE窗口1、池实验3、响应实验4；10是本次实际TCP连接数而非语言保证（正常client最多4连接、独占实验各1）。实库测试另建client读23个键，EXISTS仍0；真实早退测试在回复扣留后故意返回owner错误，单任务也已join、socket=0/1。warmup错误测试用另一个独占错误口令client使真实AUTH失败，返回nil资源、socket=0/1，`errors.As`仍找到`redis.Error`、`HasErrorPrefix`仍识别WRONGPASS；warmup与Close原因通过`errors.Join`和`%w`保留。race测试覆盖这些执行路径，不代表所有未执行第三方路径均无竞争。

## Windows race复跑

本次使用主 agent 已提供的便携WinLibs，没有安装编译器、改全局PATH或`go env -w`。只读toolchain索引并只对当前进程生效，finally恢复：

```powershell
$record=Get-Content -Raw 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' | ConvertFrom-Json
$savedCC=$env:CC; $savedCXX=$env:CXX; $savedCGO=$env:CGO_ENABLED
$savedPath=$env:PATH; $savedLive=$env:GOLEARN_REDIS_INTEGRATION
try {
    $env:CC=$record.cc; $env:CXX=$record.cxx; $env:CGO_ENABLED='1'
    $env:PATH=$record.bin_dir+';'+$env:PATH
    $env:GOLEARN_REDIS_INTEGRATION='1'
    go -C lessons/m25_redis_client test -race -v -count=1 -timeout 60s ./...
} finally {
    $env:CC=$savedCC; $env:CXX=$savedCXX; $env:CGO_ENABLED=$savedCGO
    $env:PATH=$savedPath; $env:GOLEARN_REDIS_INTEGRATION=$savedLive
}
```

本机真实CC为GCC16.2.0、target=x86_64-w64-mingw32；测试通过。没有此索引/编译器的新机器要用自己的可用race工具链；不能把普通go test当race，也不能为通过而关闭CGO。

## 启发材料与版本纠正

只读了主目录`files/开发框架`里“redigo或go-redis、基本结构实践”这部分。它是选题入口，没有可据以验证TTL/ctx/事务边界的源码；本课按锁定go-redis和真实Redis展开，未复制整批材料。资料中“抓大放小”不能让缓存miss、TTL遗漏、复合操作和连接归属被一笔带过。Java经验可加速理解连接池、错误和事务，但SQL回滚、线程中断、方法同步返回这些类比必须在当前协议边界停止。

官方核对入口包括 [Go客户端](https://redis.io/docs/latest/develop/clients/go/)、[事务](https://redis.io/docs/latest/develop/using-commands/transactions/)、[Lua执行](https://redis.io/docs/latest/develop/programmability/eval-intro/)、[v9.23.0发布](https://github.com/redis/go-redis/releases/tag/v9.23.0)。源码事实以上面的固定tag和符号为准；实际命令和副作用以本课 [STATUS](STATUS.md) 为准，不能用最新版文档代替8.2.8实证。
