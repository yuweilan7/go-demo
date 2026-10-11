# M26：用真实副作用理解缓存一致性与回源预算

这课围绕一个租户用户资料查询：**MySQL 是权威源，Redis 是可以失效、过期、拒绝写入的派生缓存**。你会实际读到“数据库已经 v2，缓存又被旧请求填成 v1”，随后比较版本键与代际失效协议，最后在缓存认证失败时观察有界回源。这里的错误版、修复、断言和解析都已交付，没有额外 exercise。

实测平台为 Go **1.27.0 windows/amd64**、MySQL **8.4.11**、Redis **8.2.8 DB0**。锁定 `go-sql-driver/mysql v1.10.1`、`go-redis/v9 v9.23.0`、`x/sync v0.24.0`，完整真实结果和清理记录在 [STATUS.md](STATUS.md)。课程只声明本机有限实验得到的事实，不把双删、TTL、singleflight 或锁称为通用强一致方案。

## 15 分钟核心阅读

程序通常几十秒结束，10～20 分钟用来沿输出读懂一个完整缓存查询，而不是等待服务。

| 时间 | 入口/符号 | 要解释清楚的事实 |
| --- | --- | --- |
| 0～3 分钟 | `Run → scopeAndRollback → cache.aside` | GET命中；miss才SELECT/SET；键遗漏租户/用户/契约版本会返回别人的表示 |
| 3～8 分钟 | `staleRefill → source.Read/update` | 旧SQL已返回但未SET；新Commit/DEL完成；旧SET仍能留下脏缓存 |
| 8～12 分钟 | `versioned / fillFence / fenceInvalidate` | 谁提供最新版本，怎样原子比较代际；提交到失效的间隔仍存在 |
| 12～15 分钟 | `flightCases → startFlight` | 一个leader、多个follower；取消请求、共享副作用、最终Done分别是什么 |
| 15～20 分钟 | `policies → unavailable` | 负缓存窗口、大DTO绕过、热点6请求1SQL；故障2回源/6拒绝的真实预算 |

现在必须理解：Go值传递与map/slice别名、短声明作用域、函数值/闭包捕获、channel关闭通知、select取消、显式事务边界、singleflight key范围、请求与服务ctx分离、结果与join分离。Redis Cluster/Sentinel/副本、缓存事件可靠投递、跨实例协调、生产熔断与压测可在相关源码需要时再查；本课没有这些拓扑或生产证明。

## 运行与接入

从仓库根执行独立module；根 `go test ./...` 不会进入嵌套module。

```powershell
go -C lessons/m26_cache_consistency run ./cmd/demo
go -C lessons/m26_cache_consistency test -v -count=1 -timeout 20s ./...
go -C lessons/m26_cache_consistency vet ./...
go -C lessons/m26_cache_consistency mod verify
```

默认 `test` 的 `TestLive` 明确 **SKIP**，只执行七个辅助断言，不能算作实库验收。完整测试需要显式开关；`demo` 始终使用真实服务，缺少接入时失败。

```powershell
$m26LivePath='Env:GOLEARN_M26_INTEGRATION'
$m26LiveExisted=Test-Path -LiteralPath $m26LivePath
$m26LiveValue=if ($m26LiveExisted) { (Get-Item -LiteralPath $m26LivePath).Value } else { $null }
try {
    $env:GOLEARN_M26_INTEGRATION='1'
    go -C lessons/m26_cache_consistency test -v -count=1 -timeout 100s ./...
} finally {
    if ($m26LiveExisted) {
        Set-Item -LiteralPath $m26LivePath -Value $m26LiveValue
    } else {
        Remove-Item -LiteralPath $m26LivePath -ErrorAction SilentlyContinue
    }
}
if ((Test-Path -LiteralPath $m26LivePath) -ne $m26LiveExisted -or
    ($m26LiveExisted -and (Get-Item -LiteralPath $m26LivePath).Value -cne $m26LiveValue)) {
    throw 'integration 环境的存在性或原值恢复失败'
}
```

环境变量“不存在”与“存在但值为空字符串”是不同状态。不能只保存`$env:X`并把null赋回，
也不能只用`-eq`比较值来宣称恢复成功。这里同时保存存在性与原值，不存在时明确Remove-Item。

`loadConfig` 默认只读 `D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json`；新机器可用 `GOLEARN_SERVICE_INDEX` 指向相同契约的索引文件。`services.mysql/redis` 必须 READY、IPv4 loopback、合法port、非空server_version及credential_file。相对credential_file相对索引目录解析。MySQL秘密字段为username/password/database，限定 `golearn_m20`；Redis秘密字段为username/password/db，限定 DB0。只在内存读取，密码/DSN不打印、不复制、不提交。本课不建隧道、不安装或控制共享服务。

每run创建一张 `m26_<24位随机hex>` 表，最多五行，最大的name仅5000字节；Redis名字是 `golearn:m26:<同run>:` 加完整后缀。先查表不存在，再CREATE；首次触键先登记完整名，再EXISTS确认未占用。出现占用拒绝覆盖并撤登记，清理不删除那个已有键。成功写入都有期限：数据25～35秒、负缓存2秒、代际90秒，短TTL实验100/300毫秒。结束先join，再按32个精确登记名DEL/EXISTS、DROP/information_schema查询、Close池；无FLUSH、SCAN-delete、全局SET或共享服务stop/start。进程强杀不会执行defer，随机表没有自动过期；需要按失败日志的精确目标恢复，不能做宽泛前缀删除。

## 从入口到真实副作用

| 文件/符号 | caller、职责与返回去向 | 资源与值关系 |
| --- | --- | --- |
| [cmd/demo/main.go](cmd/demo/main.go) `main` | runtime→70秒总ctx→Run→收尾后决定退出码 | `os.Exit`放在Run返回后，避免跳过资源defer |
| [cachecourse/config.go](cachecourse/config.go) `loadConfig / clients / redisClient` | Run→内存加载READY和秘密→构造真正驱动 | sql.DB最多4连接、1 idle；Redis最多4活跃连接，MaxRetries=-1；Run关闭 |
| [cachecourse/course.go](cachecourse/course.go) `Run / emptyKey / check` | 建独占合成数据→E01～E04→逐项业务断言→精确清理 | lab是共享指针；每实验有自己的owner、gate、结果槽 |
| [cachecourse/store.go](cachecourse/store.go) `source.Read / update / currentVersion` | miss/leader→真实SELECT；写入口→Tx UPDATE/Commit→publish | source借DB不Close；Read的permit保持到函数返回；Tx由update拥有 |
| [cachecourse/cache.go](cachecourse/cache.go) `aside / get / put / versioned` | GET→miss回源→SET；期望版本不符拒填 | DTO结构体值副本仍含map/slice共享；JSON编码字节独立 |
| 同文件 `generation / fillFence / fenceInvalidate` | reader读代际；writer提交后推进代际并删缓存；Lua比较再填 | 一个短Lua内原子比较/写；跨MySQL/Redis仍有窗口 |
| [cachecourse/lifecycle.go](cachecourse/lifecycle.go) `owner.start/join/finish / startFlight / ticket.wait` | 实验预登记→启动→请求接结果或取消→owner取消/放门/join | body候选与drainer各有Done；库通道接一次，不能range |
| `course.go` `scopeAndRollback / staleRefill / flightCases / policies / unavailable` | Run按顺序调用四阶段，失败停止后续业务 | 每入口defer先处理其任务；最后Run处理表/键/池 |
| [cachecourse/course_test.go](cachecourse/course_test.go) | testing→真实完整TestLive或七项辅助断言 | 辅助验证复制、键、Result/Done、取消/error树、接替计数、提前失败join，不冒充实库 |

完整正常路径如下。每个层次都检查error，低层错误用 `%w` 或 `errors.Join` 保留；遇到明确预期的故障，先独立查业务状态再继续下一个实验。

```text
main → Run
  loadConfig → mysql.NewConfig/NewConnector → sql.OpenDB
             → redis.NewClient
  VERSION()/DATABASE() + Redis INFO(server) → 独占CREATE/INSERT
  scopeAndRollback
    cache.aside → cache.get → client.Get.Bytes
      miss → source.Read → 接纳permit → QueryRowContext.Scan → put → JSON → client.Set.Err
      hit → 返回缓存DTO，无source.Read
    source.update → BeginTx → Tx.ExecContext → RowsAffected → rollback返回
  staleRefill
    owner.start(oldRead) → SQL完成通知 → oldRead等待hold
    source.update → Tx.Commit → Redis.Del
    owner放hold → 旧put → owner等Done → 独立GET/SELECT
    versioned地址 / generation Lua / 提交失效窗口
  flightCases → startFlight → Group.DoChan → leader fn → source.Read/put
              → 每个drainer接Result → [Clone] → 发布请求Result → cleanup门 → Done
  policies → 负缓存 / PTTL / 大DTO / 热点 / 独占WRONGPASS客户端有限降级
  各实验defer.finish → cancel + 放所有门 → 新Background预算join
  Run.defer → DEL精确slice → EXISTS=0 → DROP精确表 → information_schema=0 → Close
main → COMPLETE，或FAILED/exit1
```

`*sql.DB`、`*redis.Client`参数复制的是指针句柄，仍借用同一池；单请求不Close共享池。`scope`、version、string是值。`QueryRowContext(...).Scan`同步等真实驱动返回并关闭其Rows；没有向caller外借裸Rows。动态SQL仅拼接本课crypto/rand生成的表标识符，业务值用 `?` 绑定，未开多语句或插值。

## E01：cache-aside与漏维度

最小规则是“先读缓存；miss才读权威源并填缓存”。`get`先判断 `errors.Is(err,redis.Nil)`；nil回复是正常miss。AUTH/网络错误是调用失败；存在但JSON损坏或超限是内容错误，不能一律换成miss。`aside`中超限的**源DTO**例外允许bypass，它返回真实SQL结果而不填缓存；缓存已有坏JSON仍明确失败。课程缓存数据由自己写入，尚未实现完整外部DTO验签/鉴权；正确key本身也不能替代请求的授权校验。

合成表主键为 `(tenant,user_id)`：A/u1=`A-user1`，B/u1=`B-user1`，A/u2=`A-user2`。危险实验先把A/u1写到三个故意漏维度的键，再以B/u1、A/u2和schema2读它。真实结果都是 `{Tenant:A User:u1 Schema:1 ...}`，分别构成跨租户、跨用户、表示契约混用。schema是输出格式/授权投影版本；Version是数据业务修订号，不能混为一项。把 `version=1`写在值里而不在选择协议里检查，不会自动纠正键碰撞。

修复统一用 `registry.key(scene,scope,suffix)`：租户、用户、schema全部编码，`url.QueryEscape`避免分隔符输入制造碰撞；版本键额外加 `:v=N`。四个正确scope分别得到自己的字段，再次命中A/u1时 `source.Queries`保持7。7是三次危险初次回源+四个修复初次回源，本课独立计数，不是健康PING的次数。

回滚实验在同一Tx内UPDATE版本和name，随后返回 `ErrRollback`。`update`的defer读取具名err，将Rollback失败与原失败Join。body没有到Commit，所以publish callback完全不被调用。外部SELECT仍v1/`A-user1`；原缓存仍同值；`committed=false, published=0`。这三类证据一起支持“回滚没有发布成功失效”。若回滚后发送变更消息、先删缓存再试写库，程序就可能把一个未提交的请求当作成功变更，增加无意义回源甚至误驱动消费者。

Go没有JDBC/Spring的隐式线程事务绑定。`tx.ExecContext`明确使用该Tx的连接；用外层DB执行将绕过事务。`Commit`成功后deferred Rollback得到 `sql.ErrTxDone`，只按对象已结束的契约忽略；Commit失败返回UNKNOWN，不自动重试。缓存失效失败发生在成功Commit之后，`committed=true`继续返回，Redis错误不能抹去SQL事实。

## E02：稳定的旧读回填，以及修复的证明范围

`staleRefill`不用sleep安排竞态。`seen`由reader在真正Scan完成之后关闭；reader随后停在 `await(ctx,hold.c,...)`。writer只在收到seen之后开始事务。

| 步骤 | 参与者、当前函数/等待点 | 状态变化 | 下一步证据 |
| --- | --- | --- | --- |
| 1 | owner `emptyKey`，reader `aside → get` | 缓存miss | 才接纳一次回源 |
| 2 | reader `source.Read → Scan` | 已在内存得到v1；关闭seen | owner知道旧SQL完成 |
| 3 | reader `await(hold)` | permit仍占，未发SET | 屏障限制真实回填顺序 |
| 4 | writer `update → Commit` | MySQL成为v2 | 才执行DEL |
| 5 | owner在DEL返回后再DEL一次 | 缓存仍空，reader仍停 | 两次删除都不能约束未来SET |
| 6 | owner `hold.open`；reader `put` | 旧v1重新进入缓存 | worker返回并关闭Done |
| 7 | owner先等Done，再GET/SELECT | cache=v1，MySQL=v2 | `dirty-refill-and-double-delete`独立断言 |
| 8 | owner设300ms期限，`waitMissing`反复GET | 实际GET最终nil | TTL限制这份旧值驻留，已经发生的错读仍成立 |

旧请求返回它之前读出的快照本身未必违反业务允许性；危险是它把旧快照留给**后来**的请求。两次删都在旧回填前时，双删也失败。生产“延迟双删”依赖旧读取/回填延迟、重试、网络停顿、进程存活和第二次DEL成功等假设，固定延时不能覆盖无限延迟。先删后写同样可能让reader在提交前读旧源再填回来。分布式锁还需所有reader/writer参与、租约续期、过期旧owner的fencing，以及源事务/缓存操作的时序；单独给GET加锁不能自动解决这些窗口。本课未部署跨实例锁。

**版本地址实验**：旧reader读v2后被扣住；writer提交v3；旧reader只填 `:v=2`。新reader向MySQL真正查询当前版本3，再选择 `:v=3`并读数据。旧键仍包含v2，新读取获得v3。当前版本信息需要可信来源；这里额外一次 `currentVersion` SQL，日志明确 `version_query=1`，不把它算成免费缓存命中。若把“当前版本指针”也放Redis并让它落后，新reader仍可能选旧地址。

版本查找到数据读取之间也可能有新提交。`cache.versioned`验证DTO.Version等于expected，拒绝把v3写到期望v2的空键；真实 `version-lookup-change-rejects-fill`断言是expected2、SQL3、EXISTS0。caller可以按预算有限重读或返回冲突；本课直接返回错误，没有无限重试。若业务需要一个更严格的读快照，可在权威源协议中同时读版本和数据；仅改变键格式不是该协议。命中之后立刻又提交也仍有读取线性化边界问题，这个实验没有声称实时最新。

**代际失效实验**：reader先GET generation，不存在按字符串`"0"`处理，再读v3、等待hold。writer提交v4后执行一条短Lua：INCR generation→PEXPIRE90s→DEL data。reader放行后的 `fillFence`在一条Lua内比较“当前代际是否等于读前代际”，发现1≠0，返回0，不SET。GET data仍miss。新reader拿generation1和真实v4填充，Lua返回1。分开GET generation和SET会再暴露一个比较到写入的竞态，故比较/SET必须在统一Redis原子边界里。

该协议的条件要写在实现旁：所有相关writer都成功执行相同失效；代际记录在所有旧reader与对应缓存有效期内不丢失；读预算有上限；缓存值TTL有限。课程总业务预算70s、源读超时6s、代际90s，不会在本次实验内过期归零。若代际被evict、重启丢失或90s后归零，旧代际0可能出现ABA；仅加TTL并不能解决该问题。Lua里的写操作也不是SQL rollback，当前短脚本针对自己已验证的字符串键；任意扩展脚本仍须分析运行错误/部分副作用。

**提交到失效的窗口实测**：成功填v4后，writer提交v5但故意不失效。GET仍命中v4，MySQL查询v5；`commit-invalidation-window`真实通过。随后执行失效结束该受控窗口。这个反例同时限制版本值、fence、TTL的声明：没有跨服务事务、可靠变更投递与恢复协议时，提交后进程崩溃/失效失败都可能留下旧缓存。订单金额/库存的最终写判断必须在权威事务里完成，不能用这个派生资料缓存证明支付或下单成功。

## E03：singleflight合并什么，谁负责取消和收尾

`singleflight.Group`的命名空间是该Group对象的内存。相同Group内、同一key且前一次函数仍在执行时，重复调用共享一次函数及其结果；函数完成后新调用可以再执行。它不是持久缓存、全局锁或跨进程互斥。`Result.Shared=true`表示结果分给多caller，也可以出现在leader上，不能拿它识别谁是leader。

锁定v0.24.0源码的 `DoChan`在返回前加锁登记key和结果channel；只有first创建 `go g.doCall`，重复调用把自己的容量1结果channel追加进同一call。owner同步调用六次startFlight后才放source门，因此六项已登记，不是“六个goroutine大概都启动了”的猜测。`Do`则同步阻塞等结果，API没有ctx；`DoChan`返回的通道不关闭，代码只接收一次，不能range等关闭。`Forget`让未来请求重新执行，旧函数仍可能活着；它不是取消或join，本课不靠Forget卸载任务。[锁定singleflight契约](https://pkg.go.dev/golang.org/x/sync@v0.24.0/singleflight)

错误key实验A/u1与B/u1都用 `BAD-only-user-u1`：A的真实SQL先进入门，B同步加入后其callback被库忽略，得到A DTO。修复用同一租户/用户/schema键，两个key分别SQL，B得到B。缓存key与singleflight key必须使用一致的授权/表示边界；“只是合并回源，没有写缓存”也可能泄漏数据。

ctx由**first的fn捕获**决定，库不会合并follower的ctx，也没有“最后一个follower取消就自动停止leader”的契约。两种策略的时间线不同：

| 顺序 | 错误leader请求ctx | 服务owner ctx修复 |
| --- | --- | --- |
| 1 | leaderSQL完成，停hold | 同样有真实SQL和hold |
| 2 | 有效follower同步登记 | 两个follower同步登记 |
| 3 | 取消leader请求ctx，hold保持闭合 | 只取消其中一个follower的waitCtx |
| 4 | body await从取消分支返回 | follower `ticket.wait`返回Canceled，body仍Active=1 |
| 5 | leader与有效follower都收到同一Canceled | owner放hold，body继续真实SET v5 |
| 6 | owner消费结果、join | 另外caller得到成功；离开的caller对应drainer仍消费库Result |

好的策略也有代价：全部caller离开后服务工作仍可能继续，直到服务ctx/源超时或owner取消；不能无限脱离生命周期用Background跑。当前服务ctx继承70s总预算，源库驱动读超时6s、回源接纳容量2，owner在提前失败时取消、释放所有门并用新的6s预算join。单个caller离开不取消共享工作；服务关闭仍由其owner统一停止。

要分别追三项事实：SQL/SET共享副作用、业务Result、应用drainer最终Done。正常body先SET，再返回给库；drainer接库Result并发布自己的容量1result，然后在cleanup门等待。此时独立GET已经v5、`Queries=1 Active=0`、caller已有Result，但Done尚未关闭。owner放cleanup门并join后才可以销毁Run资源。取消follower也不能直接删除键/Close client，因为leader可能稍后还会SET。

| goroutine/事件 | owner、当前等待点 | 终止与最终观察 |
| --- | --- | --- |
| 普通旧读worker | 实验owner在go前登记；Scan或hold等待 | ctx/门解除→函数defer归还permit→task.done→owner.join |
| singleflight内部doCall | x/sync库创建；本课先登记候选body，再传fn | fn内资源结束→body.done；库发布每项Result，库没有暴露goroutine join句柄 |
| 本课每项drainer | 实验owner在go前登记；`r := <-ch` | caller离开仍接一次库Result；发布业务result→cleanup门→Done |
| 未当选callback的候选body | startFlight在调用前登记，无实际fn运行 | drainer收到库Result后确认started未关闭，结束该空候选 |
| 请求waiter | `ticket.wait(ctx)`，实验主goroutine模拟有限请求 | Result或ctx返回；该返回不是body/其他drainer退出证明 |
| hold/seen/cleanup | 创建者有close权限；gate.open幂等 | close是广播，owner仍必须等各Done |

这里“最终Done”指应用拥有的工作函数与drainer已经收尾，不冒称库提供了完整内部goroutine join API。实际锁定源码在fn返回后的defer里进行有限个缓冲结果发送并退出，没有再调用用户fn；本课收齐所有已登记结果且bodyDone/drainerDone都完成后，已经没有应用SQL/SET/cleanup副作用。若替换库版本，需要重新核对该边界。fn入口内recover将教学body panic转为error，避免库DoChan的panic传播策略使进程崩溃；runtime.Goexit/os.Exit或不合作的永久阻塞不在这个helper契约内。

结果污染的反例没有Go数据race却有业务共享：`DTO`按值返回，Labels的map句柄和Tags的slice底层数组仍被两个结果引用。按顺序修改caller A的标签/数组后，caller B也看到`caller-A/changed-A`。这说明singleflight共享的是结果对象，库不会为你复制。修复在**每个drainer交付caller之前** `Clone()`；map新建并逐项复制，slice append到nil分配新数组。string元素不再递归，但如果元素是指针/嵌套map，必须继续复制或设计不可变DTO。修复后B仍`mysql/original`，实际只做一次SQL。

两个独立Group同key时真实SQL次数2、峰值2。这个实验在一进程的两个Group中执行，证明其注册表互不共享；没有冒称启动了两个OS进程。真实多实例仍各有自己的Group，需要系统级预算、事件/版本协议等协调。Java中的“共享Future”可帮助理解leader结果分发，但Go的DoChan不是CompletableFuture：它不带ctx取消、深拷贝或自动资源join。

## E04：策略必须与权威源预算一起落地

| 策略与真实观察 | 当前实现 | 业务代价/边界 |
| --- | --- | --- |
| 负缓存两次读取只1次SQL | 明确 `Found=false`、TTL2秒 | INSERT新行后仍命中不存在；创建后DEL修复，再读得到真实行，Queries=2 |
| jitter有多个真实PTTL | 稳定key hash→25..35s；12键逐项PTTL>0且≤配置值 | 分散不同key的期限，不能保证一个超热key不同时miss；生产参数须按更新频率/容忍度验证 |
| 热点真实过期后6caller | GET=nil确认100ms缓存已过期→6项DoChan同步登记→1次SQL/SET | 一个Group内合并；每个实例仍可能各查一次，不能只靠SF保护整个数据库 |
| 大DTO5000字节 | 真SQL TEXT返回完整name；JSON>4096字节拒SET，aside返回源DTO | 下次仍miss，必须保留回源预算；不截断、不当数据库不存在；4KiB是教学预算不是Redis最大限制 |
| AUTH故障时2回源/6拒绝 | 独占错误口令client，真实WRONGPASS；显式允许资料读从SQL返回 | 两项Source.Read占permit；六项立即ErrOverload+原AUTH；无无限源库排队/自动扩池 |
| SQL提交后缓存失效失败 | Tx真Commit v6，badClient.Del真WRONGPASS | committed=true+error，外部SELECT v6；禁止将失败重试成第二次业务写或缓存持久事实 |

`source.slots`容量2使用非阻塞send接纳，满立即拒绝；select的default保证没有额外请求在这个门等待，已接纳工作defer归还一个permit。`Read → admitRead → finishRead`使用同一接纳/收尾边界：先占permit再计Active，结束时**先在mu下Active--，再释放permit**。旧版反序释放会让新工作拿到permit并Active++时旧Active仍在，源工作容量2却报告Peak3。修复保证所有已计数工作仍持permit；接纳后的暂时停顿可以少计一项尚未进入工作的接纳，不能多计已释放的旧项。Queries只在Read准备真正SELECT时增加，不把纯接纳辅助实验当成SQL。

`TestAdmissionCompletionBound`在真实helper的“旧工作尚未扣Active”处设屏障：两permit都占用时第三项必须立即拒绝；完成旧工作再接替才成功；16轮Active归零、Peak保持2、Rejected16、Queries0。它没有数据库替身，也不声称实库通过；故意把`<-slots`移回扣数之前就会确定性失败，证明它能触发原计数缺陷。整个SQL池最多4连接，留下写/版本查询与清理余量；permit控制的是这个source对象的**应用回源工作**，不控制所有SQL（update/currentVersion不计入Read指标），也不是每秒2请求的速率限制。生产需要按实例数、SQL池、写流量、延迟和业务错误预算统一定容，不能直接照抄2/4。

不可用实验先用坏客户端真实GET得到WRONGPASS，不改Redis ACL。两个owned worker再GET失败、各做一次真实SQL，SQL完成后卡hold，permit仍占；owner收到两份seen后连续发六项坏GET，源预算全部拒绝。日志 `Queries=2 Active=2 Peak=2 Rejected=6`，放hold后join得到两项真正v5结果，Active=0。这里峰值包含SQL已完成但等待回填/实验门的应用工作，不是MySQL服务端同时执行两条SELECT的CPU测量。`normalRedis=PING-ok`进一步证明正常客户端没有被故障动作破坏。

这些2次SQL是源库预算实证，不是仅根据“goroutine数≤2”推测。坏Redis客户端禁重试，每个故障GET只走一次当前调用；认证失败可能使连接无法复用，但没有增加源Read次数。故障表示本应用凭据错误导致缓存不可用；没有验证Redis进程停机、全网分区、TCP超时全集或连接响应丢失，不能把WRONGPASS验收改名成这些网络故障验收。

适合降级的请求必须有业务许可：只读资料允许直接源读；预算满可以明确返回过载、降级展示、有限陈旧数据或稍后重试，具体规则由产品定义。鉴权、价格下单、库存扣减和支付提交不能因缓存失败改用Redis持久真相。本课没有实际订单写入，写实验仅是合成资料版本；M21的事务/UNKNOWN处理仍是订单等权威写路径的基础。

大key还可能放大网络带宽、序列化/反序列化CPU、Redis单线程处理时间、内存和删除延迟。本课真实写入数据都很小，5000字节对象根本未SET，没有对共享服务注入巨大值或声称性能压测。读端先GET再检查大小仍已传输了字节，因此更严格网络预算需分页/字段拆分/受限协议；仅在JSON之后检查4KiB不是网络流量硬上限。

## 错误与清理的完整路径

正常返回：每实验等工作Done或通过defer.finish补全join→Run逐名DEL→EXISTS0→DROP→information_schema0→Close。提前错误：实验具名err收到失败→defer.finish取消服务ctx、打开所有自己登记的gate→新Background预算join→Join保留原失败/工作失败→Run继续独立清理数据/池→main打印FAILED并exit1。结果槽都有足够缓冲，离开的请求不会让worker卡在没人接收的send。

请求取消时ctx.Done仅是通知；库I/O何时返回仍要看驱动契约与deadline。MySQL driver处理取消/读预算，Redis启用ContextTimeoutEnabled并设read/write/dial/pool预算；本课没有声称无deadline的手动cancel会即时打断任意socket.Read。协调门上的await有ctx分支，门由owner最终放开；清理不用已取消请求ctx。

`errors.Join`不会替你判断哪项是业务失败、哪项是清理失败。普通task.err随Done交owner；共享fn业务error随SF Result交caller，而候选body记录的是终态；drainer cleanup错误仍作为task.err传到owner。期望的leader Canceled只在业务断言中接受，不用它覆盖Close/DROP失败。`TestCleanupPreservesErrors`独立证明原业务失败、Canceled和工作收尾失败同时在错误树中；`TestCanceledCallerCleanup`证明caller先离开后finish确实加入所有候选/实际任务。

这些资源测试创建owner后立即登记**唯一**defer guard；`t.Fatal`引发Goexit也会执行当前goroutine的defer，guard独立预算finish/join后明确报告清理error。原测试的重复finish、缺少guard与无人观察的protective error已移除。`TestEarlyFailureJoinsRegisteredTasks`进一步让owner范围在三worker已进入后直接return原失败：只有defer guard负责cancel/join；caller返回后才逐项检查Done与工作自己写的cleanup槽，同时保留原失败、Canceled、收尾哨兵错误，避免把取消通知当退出证据。

## 前置、源码契约与材料纠正

本课已读取M25/M15/M21实际README、STATUS和直接源码接口：M25 `LoadConfig/tasks/get/TTL`给真实Redis配置、精确键/独立预算与错误分类；M15 `Gate/Await/Cleanup/Tracker`给owner、接纳前限额、ctx与join区分；M21 `config/withTx/rollback/Place`给真MySQL限定库、显式Commit、回滚和UNKNOWN。基线课程验收索引已有这三课，不把其历史STATUS中“待主验”当成缺失实现，也没有重跑整套前置。它们的数据不被M26消费；本课组合的是同一真实服务契约与正确生命周期，独立小module不复制前置工程、不循环导入、没有根go.work/replace变更。

查证入口与实际版本：

- [Go1.27发布说明](https://go.dev/doc/go1.27)、[Go语言规范](https://go.dev/ref/spec)；实际本机 `D:/Go`，`go.mod=1.27.0`，未升级工具链。
- [database/sql@go1.27.0](https://pkg.go.dev/database/sql@go1.27.0)：Tx、Commit、ErrTxDone、QueryRow.Scan；对应M21实际 `mysql v1.10.1` connector/事务源码，本课不凭默认pkg网页推断本机补丁。
- [singleflight@v0.24.0](https://pkg.go.dev/golang.org/x/sync@v0.24.0/singleflight)及实际缓存 `singleflight.go`：`Do / DoChan / doCall / Forget / Result`已核对。v0.24.0不是“在线永远最新”，选择与前置相同已锁版本。
- [go-redis v9.23.0源码](https://github.com/redis/go-redis/tree/v9.23.0)：真实GET/SET/EVAL/DurationCmd与Options契约，复用M25锁定源码；[Redis keyspace](https://redis.io/docs/latest/develop/using-commands/keyspace/)给键与过期行为。在线文档可能展示新Redis能力，本课只使用8.2.8支持的命令。

主目录 `files/语法进阶/Sync`的sync.Pool材料只是只读启发。它的对象复用/共享字段风险有助理解DTO污染，但 `sync.Pool`的临时对象会被回收、没有缓存一致性/容量/业务存储契约，不能代替Redis/MySQL或singleflight。本课没有把原例“取回相同地址”的一次观察写成语言保证。资料中的GC机制也不能当作远端键失效协议。

## Windows完整实库race

使用主agent已经登记的amd64工具链，只有这个PowerShell进程临时设置CC/CXX/CGO/PATH，finally恢复；不安装、不 `go env -w`。在仓库根执行：

```powershell
$tc=Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' | ConvertFrom-Json
$m26Names=@('CC','CXX','CGO_ENABLED','PATH','GOLEARN_M26_INTEGRATION')
$m26Saved=@{}
foreach ($m26Name in $m26Names) {
    $m26EnvPath='Env:'+$m26Name
    $m26Existed=Test-Path -LiteralPath $m26EnvPath
    $m26Saved[$m26Name]=@{
        Exists=$m26Existed
        Value=$(if ($m26Existed) { (Get-Item -LiteralPath $m26EnvPath).Value } else { $null })
    }
}
try {
    $env:CC=$tc.cc
    $env:CXX=$tc.cxx
    $env:CGO_ENABLED='1'
    $env:PATH=$tc.bin_dir+';'+$env:PATH
    $env:GOLEARN_M26_INTEGRATION='1'
    go -C lessons/m26_cache_consistency test -race -v -count=1 -timeout 100s ./...
} finally {
    foreach ($m26Name in $m26Names) {
        $m26EnvPath='Env:'+$m26Name
        if ($m26Saved[$m26Name].Exists) {
            Set-Item -LiteralPath $m26EnvPath -Value $m26Saved[$m26Name].Value
        } else {
            Remove-Item -LiteralPath $m26EnvPath -ErrorAction SilentlyContinue
        }
    }
}
foreach ($m26Name in $m26Names) {
    $m26EnvPath='Env:'+$m26Name
    if ((Test-Path -LiteralPath $m26EnvPath) -ne $m26Saved[$m26Name].Exists -or
        ($m26Saved[$m26Name].Exists -and
         (Get-Item -LiteralPath $m26EnvPath).Value -cne $m26Saved[$m26Name].Value)) {
        throw ('race 环境的存在性或原值恢复失败：'+$m26Name)
    }
}
```

这轮实际验证同时比较Test-Path与原值，并覆盖原本不存在、存在空串、存在非空值三种状态；
旧版日志`process_environment_restored=True`只比较值，不能单独证明存在性恢复，已在STATUS保留并纠正范围。

race只证明这些实际执行路径未检测到Go内存数据竞争，不发现远端缓存脏回填或顺序共享对象污染；这些由SQL/GET和独立业务断言证明。未验证生产负载、跨OS/旧Go/旧driver、真实多进程部署、Redis Cluster/Sentinel/主从、eviction/restart耐久性、MySQL副本/硬断电、可靠事件/outbox恢复或网络分区全集。读本课时始终把“观察到的有限保证”与“业务仍需的系统协议”分开。
