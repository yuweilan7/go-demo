# M27 实际交付与验证状态

记录日期：2026-10-12，Asia/Shanghai。**READY_FOR_REVIEW：完整课程与执行者验证已完成，目录冻结，等待/root独立验收。** 本文件不是主 agent 的 ACCEPTED 记录。未创建下级agent、未git add/commit/push、未切换主分支、未写其他课/根索引、未安装/启动/停止服务。

## 交付、授权范围与依赖

唯一写入目录为 `C:/Users/Lenovo/.codex/worktrees/go-course-m27/go-demo/lessons/m27_redis_leases_limits/`，下文记为 **M**；worktree根为 `C:/Users/Lenovo/.codex/worktrees/go-course-m27/go-demo`，记为 **W**。基线HEAD是 `5929af4af6d8a599362bdbb20e2d2d3cfa2cf6ba`，本课没有提交。所有命令工具显式workdir=W或M，Go检查在M用 `go -C .`；race脚本用其PSScriptRoot解析的M绝对路径。

独立module `go-demo/lessons/m27_redis_leases_limits`，`go 1.27.0`。交付为 `.gitignore`、`go.mod/go.sum`、详细中文 `README.md`、本 `STATUS.md`、`cmd/demo/main.go`、`internal/lesson/{config,run,ownership,lease,sql,experiments,fault,limiter,lesson_test}.go` 和 `scripts/integration-race.ps1`。源码职责/关键等待/副作用/错误与资源owner旁有中文注释，README给七组完整解析、调用链、时间线和10～20分钟core阅读运行入口；没有extra exercise。

读取本机适用AGENTS（`.codex/AGENTS.md`为空，未发现额外worktree/module AGENTS）、本worktree完整M27 prompt、TEACHING_STYLE、ORCHESTRATION、INFRASTRUCTURE、modules.json的M27硬依赖/层条目和根README/go.mod。核对M25/M13/M20的真实STATUS及直接配置、任务/Done、故障socket、SQL adapter/Tx接口：M25真实Redis已完成；M13取消不强杀/SQL仅阅读的边界明确；M20真实MySQL与Finished主审修复可消费。主agent已验收前两层的事实按派发与基线使用，未重跑前置全课。只读主目录 `files/开发框架` 的基础Redis启发，不复制原材料。

使用go-learning-by-running教学方式；不导入/复制前置internal项目、不建立replace/go.work或循环依赖。派发约定GPT6.1-SOL/xhigh，priority沿会话配置/工具支持，不宣称本课修改了实际模型/服务等级。

## 实际版本与资源

| 项目 | 本次实际核对 |
| --- | --- |
| Go | `go version go1.27.0 windows/amd64`；GOVERSION go1.27.0，GOTOOLCHAIN auto，GOWORK空，M的GOMOD正确 |
| Redis | 真实INFO返回8.2.8，`127.0.0.1:16389`，DB0/RESP2，主agent登记的共享DeepHelp实例与现有隧道 |
| MySQL | 真实SELECT VERSION/DATABASE返回8.4.11/golearn_m20，`127.0.0.1:13306`，新课程实例，课程账号仅本库授权 |
| go-redis | v9.23.0实际下载/Go module cache源码/API核对；自身最低Go1.26.0；sum `h1:/TTVdJa8BRNsybwwGGnmiYktHNlUXtbOQxlWpHBPL3s=` |
| MySQL driver | v1.10.1实际下载/源码核对；sum `h1:arlSnNLq6a5yxGxV7qg9lF4j0C+KwD6NbQyKr9QL6ME=` |
| 间接编译依赖 | filippo.io/edwards25519 v1.2.0、cespare/xxhash/v2 v2.3.0、go.uber.org/atomic v1.12.0、x/sys v0.48.0；go.sum由本module tidy产生 |
| 池/时间上限 | SQL MaxOpen4/MaxIdle2；Redis A/B主池各MaxActive2，故障client1；总ctx40s，read/write默认2s，SQL读5s，独占响应读350ms；cleanup join独立4s+必要救援4s，Run清理新预算8s |
| 数据/并发上限 | 2个随机SQL表、state6行+orders1行；13个可能触及的登记键；最多12个同时worker，完整入口17个登记任务 |
| 清理事实 | 当前完整run：表逐名remaining0、13键EXISTS0、DB open/inuse/idle0，17/17任务Finished join，6/6Redis socket关闭 |

基础设施索引与toolchain索引只读，credential_file在内存加载，不复制/打印密码。复用安装者的持久性/重启/锁竞争/容量核验，没有新增隧道、实例、配置或停止共享Redis。Redis键一律本课随机prefix，存在键最后核对正TTL<=30s；只按登记列表DEL，不FLUSH或SCAN-delete。SQL只动本run自建m27表，不复用M20或其他课数据。

官方资料实际打开Redis分布式锁、Lua、SET/INCR、go-redis v9.23.0和mysql v1.10.1发布页、MySQL8.4 InnoDB行锁/事务说明；本机锁定源码核对cmdable.Do、Client.Process/processWithRetry/_process/withConn、WithWriter/WithReader、Error接口、Int64Slice与mysqlTx.Commit/Rollback。本机go doc及D:/Go/src核对Context、net.Conn、DB池与Tx Commit。Redis在线说明现在包含8.4新增DELEX，实际8.2.8用Lua，未调用不存在命令。

## 真实执行命令

下面Go命令均在 **M** 的显式workdir执行；本课私有无秘密日志在 `.local/`，已被本模块.gitignore排除。日志不是唯一交付证据，关键结果直接记录在此与README。

| 实际命令/环境 | 真实结果与范围 |
| --- | --- |
| W中 `go version`、`go env GOVERSION GOOS GOARCH GOTOOLCHAIN GOMOD GOWORK CGO_ENABLED GOPROXY`；M中`go -C . env GOMOD GOWORK` | Go1.27/windows/amd64，独立GOMOD确认；GOPROXY现有goproxy.cn,direct未改 |
| `go -C . mod tidy` / `list -m all` / `mod download -json github.com/redis/go-redis/v9 github.com/go-sql-driver/mysql` | 下载/锁定/校验退出0，只有本module依赖文件改变 |
| `gofmt -w cmd/demo/main.go internal/lesson`；最终`gofmt -l cmd/demo internal/lesson` | 格式化成功，最后无未格式化文件 |
| 最终 `go -C . run ./cmd/demo` | **退出0**，run `54eb13e1cf3ec3394ea057aa`，28项PASS，真实7组全部完成；CLEAN全部通过；工具wall14.117s，日志`.local/demo-final.log` |
| 最终 `go -C . run ./cmd/demo -scenario core` | **退出0**，run `0c2c8db1b2db86d3680a3d93`，7项PASS，2/2任务/2键/2表/2socket清理，wall4.881s；日志`.local/core-final.log` |
| 最终 `go -C . test -v -count=1 -timeout 30s ./...` | **退出0**，四项辅助测试PASS，package0.854s；TestIntegration明确SKIP，不当实库证明；日志`.local/test-final.log` |
| 最终 `go -C . vet ./...` / `go -C . mod verify` | 各退出0，无vet诊断；all modules verified |
| `GOLEARN_M27_INTEGRATION=1`后 `go -C . test -v -count=1 -timeout 60s ./...`，finally恢复开关 | **历史完整版本退出0**，run`89a156f1632506e0bdd560dc`，27项、17/17任务、13键/2表/6socket清理，TestIntegration11.92s/package12.820s；日志`.local/integration.log`。当时尚未加取消后真实SQL保留第28断言，不冒充最终28项证据 |
| 最终 `& ./scripts/integration-race.ps1`：进程级CC/CXX/CGO/PATH与integration开关，内部`go -C M test -race -v -count=1 -timeout 65s ./...` | **退出0**，四项辅助+完整真实TestIntegration全PASS，无race诊断；run`3020f903b792b815b4b663a0`、28项、17/17任务、13键/2表/6socket清理；实库11.87s/package13.931s、工具wall18.806s；日志`.local/race-final.log` |
| race后 `go -C . env CC CXX CGO_ENABLED GOMOD GOWORK` | gcc/g++/1，M的GOMOD，GOWORK空；进程原值已恢复，无go env -w/全局PATH改变 |
| `git check-ignore -v .local/demo.log .secrets/config.json` | 匹配本课.gitignore，退出0；没有复制秘密配置 |

race使用只读安装者 `D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json` 中64位WinLibs GCC16.2.0的cc/cxx/bin_dir。当前默认32位GCC限制已经在前置课定位，本课直接复用已有64位工具链，未重新安装或故意复现旧GCC失败。

## 实际业务状态与协议边界

| 场景 | 真实结果 | 说明 |
| --- | --- | --- |
| A GET检查后暂停，TTL实际过期，B写入，A恢复 | A旧SQL rows1；SELECT A-old/effects2；A ctx仍nil | 过去GET/有效ctx不能跨越暂停保护SQL，租约过期不停止goroutine |
| 修复版同一时序 | A token1/B token2；B先接受后A rows0；SELECT B-new/effects1/last_token2，独立issued_token2 | 持久SQL接受边界拒旧；发号Tx持权威行锁跨越有界SET/Commit，授予顺序不是随意领号 |
| NX竞争 | CONTENDED/nil业务error | 明确未写，与命令/网络失败不同 |
| 错误释放 | 过期A raw DEL1；B EXISTS0 | 无token误删新owner真实复现 |
| 比较释放/续租 | A DEL0/renew0、B owner保留；B两次release1/0 | Lua保护释放对象，不保护下游旧工作 |
| 续租与取消/清理 | 真实续租1→暂停→实际过期→B成功→旧续租0→cancel；Result可读但Finishedfalse，放cleanup门后真实join | 取消/结果/全部清理是分别观测的事实 |
| 取消后已接受SQL | 取消前SQL接受id5更新，丢租/cancel/Finished之后SELECT A-before-cancel/effects1 | 不能把协作取消当撤销已接受副作用的协议 |
| 真实SET服务端拒绝 | PX-1真实ERR、EXISTS0，REJECTED | 单条原子SET的明确命令失败 |
| 真实SET响应未知 | reply字节已到本机被门扣住，另一ClientGET到owner；caller350mstimeout、UNKNOWN；后来比较释放1 | 没有自动重放或派发业务；GET只核对过去执行事实，不成为新授权 |
| 并发限流 | 12请求实际允许3/拒绝9，独立GET3/PTTL2.770s | 首次允许请求开固定窗口；准入次数与同时工作数不同 |
| 拒绝与隔离 | count仍3；TTL2.770s→2.655s；bob下单/alice读接口各count1 | 拒绝不计数、不延长窗口；用户+规范化接口隔离 |
| 固定窗口burst | 旧窗口末2+新窗口3，在Redis TIME911ms内允许5次 | 不保证任意滚动2秒最多3；早先开窗1次未计入这5次 |
| Client不可用策略 | ErrClosed与真实AUTH WRONGPASS均critical拒绝、optional降级允许；失败key未写 | 基础设施error保留Cause，不伪装成限额拒绝；未破坏共享服务 |
| 同逻辑订单两次有效锁 | 坏例stock0/effects2 | 顺序重复也能重复业务效果 |
| 真实SQL唯一订单+库存Tx | 修复stock1/effects1/orders1/duplicate回执1 | 业务唯一性与库存效果在同一Tx内；锁不自动成为事务 |

## 精确对象与收尾证据

本课本次完整默认run前缀是 `golearn:m27:54eb13e1cf3ec3394ea057aa:`；它与最终race前缀 `golearn:m27:3020f903b792b815b4b663a0:`、normal integration前缀 `golearn:m27:89a156f1632506e0bdd560dc:` 完全隔离。每个完整run均使用下面13个精确后缀，表名为 `m27_<run>_state` 与 `m27_<run>_orders`：

```text
stale-unsafe
stale-fenced
unlock-bad
unlock-fixed
renew-cancel
set-rejected
set-unknown
limit:c3798d1898a4535ae0671e0fd0a966422424939b04d5cc3b3fcdad8b97dc0851
limit:5b1d8a174592474c8c928fcaafa152a2088c98e611a1ba7cc2eeadf843cf5ea3
limit:70daff2bb1ae772b6e7f4a3b695845d8b7c5604191a010e85a37987a70a53818
limit:31cce0f70e95a8456d7fbda4a464b5c709db1b1995543472e45703d8cddacacf
limit:2f2e70d0b53133a79f66ca0f95200a12e3f31ecf4f3a28055194d2e11465a678
order-lock
```

hash五项分别是alice下单、bob下单、alice读目录、burst下单、接入故障下单。登记不等于全部已写：set-rejected/接入故障键实际未写，其他键可正常过期/释放。每次结束逐键PTTL允许不存在-2，禁止无TTL-1；随后按记录DEL/EXISTS0。先证明随机SQL名字不存在才登记CREATE尝试，CREATE回复未知也能按本run名字收尾；DROP后逐名information_schema count0。

最终core只使用`stale-unsafe/stale-fenced`两个键，prefix为`golearn:m27:0c2c8db1b2db86d3680a3d93:`，两张表也同run命名；2/2任务与socket全部收尾。此前历史演示完整run `0a10c9eb2ea6a0c6b62e1ba7`（26项）、`dd41c36ea8bf55fbd83cc076`（27项），历史race `9600a804d1b82fa3951ee86b`（27项）与历史core `9b338d2fd67ebcb8914f74b3` 均有自己的精确键/表清理成功日志；不把历史断言数当最新完成数。

完整入口每组cancel/解除所有门→独立预算join真正Finished之后才返回上层。Run最后DROP/DEL/查最终状态，再Close长寿命Client与DB。没有开放Rows/显式Conn逃逸、没有故障代理进程/监听器、没有本课服务器进程或新隧道；真实net.Conn包装归所属Client，Read timer与ticker均Stop。测试辅助join owner也在所有失败出口等待ownerDone；工作与清理错误链实测保留。基础设施由/root继续管理，不属于本课停止清理对象。

## 真实失败、修复与未验范围

最初辅助测试误把`redis.Error`当具体字符串类型，真实`test/vet`编译失败：

```text
internal\lesson\lesson_test.go:24:20: cannot convert "ERR invalid expire time" (untyped string constant) to type redis.Error
```

读取v9.23.0/error.go发现它是带RedisError方法的接口，改用实际库导出的`redis.ErrNoScript`只辅助验证分类；真实非法SET仍用服务器返回error验收。之后test/vet通过，未把构建失败算成已运行行为。正常demo没有遗留业务失败：后来增加Commit后保守期限检查、空lease不触键保护、取消前SQL结局屏障（成功/失败均发布）、真实取消后SQL保留断言，并对最终源码重新执行默认入口、完整真实race与静态检查。

本课证明单Redis实例与真实MySQL在上述有限时序中的业务状态，**未验**：Redis进程宕机/全局配置/复制或Sentinel/Cluster切换、AOF/恢复后号安全、多节点Redlock、时钟跳变、多机器延迟与生产吞吐；本次新表的服务重启持久性、SQL Commit响应丢失/进程强杀、下游非SQL副作用、库存不足/并发库存/死锁分支的独立实测、限流响应丢失的另一次注入。相关契约/边界在README说明，基础设施owner的既有重启证据也不冒充本课新表的重启验收。

fencing只能拒绝比**最后已接受版本**更旧的写，不能回溯撤销先到的效果；每个授予号本例仅一次接受，真实多步骤需自己的版本/幂等协议。authority的持久性/恢复不复用号以及所有写入者遵守条件写是必要假设。限流错误策略实测为关闭Client与真实AUTH拒绝，不冒充服务停机；fail-open不承诺全局额度。库存下单只验同订单顺序重试的唯一效果，不代做M22并发库存课。没有M27要求的关键真实实验缺口，目录冻结等待主审。
