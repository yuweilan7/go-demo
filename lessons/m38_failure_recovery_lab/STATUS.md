# M38 交付与实际验证

状态：`READY_FOR_REVIEW`。2026-10-12 06:14（Asia/Shanghai），公开源码、文档与验证脚本冻结，执行者自验和精确清理完成，等待主agent独立验收与第5层整层提交。本文件不代写ACCEPTED，不推进M39；没有git add/commit/push、切分支、下级agent或共享基础设施变更。

## 范围、前置与版本

唯一写入目录：`C:/Users/Lenovo/.codex/worktrees/go-course-m38/go-demo/lessons/m38_failure_recovery_lab/`。基线 `81c802b18292d52f196f538e7627d5d9bdf6a9d3` 为已验第4层提交。只读适用AGENTS（`.codex/AGENTS.md`为空，其余相关祖先/仓库/lessons无文件）、根README/go.mod、M38完整prompt、TEACHING_STYLE/ORCHESTRATION/INFRASTRUCTURE/ACCEPTANCE，M37/M33/M34 README/STATUS与直接契约。使用go-learning-by-running的中文教学方法；没有复制业务或改前置源码。

独立module `go-demo/lessons/m38_failure_recovery_lab`，`go 1.27.0`，无toolchain指令或共享go.work。明确本地replace M37及M31，唯一业务入口来自M37公开 `orderbackend`，不import前置internal。

实测Go1.27.0/windows/amd64；GOROOT `D:/Go`，GOTOOLCHAIN auto，CGO_ENABLED1，普通CC=gcc，PowerShell7.6.5，GOPROXY `https://goproxy.cn,direct`。完整race只在当前进程使用登记的WinLibs GCC16.2.0，实际 `-dumpmachine=x86_64-w64-mingw32`；CC/CXX/CGO/PATH和两个本课开关finally按原存在性和精确值恢复，实际 `M38_ENV_RESTORED=True`。

沿用真实READY服务：MySQL8.4.11/127.0.0.1:13306/课程库golearn_m20；共享Redis8.2.8/127.0.0.1:16389/DB0；课程Kafka4.3.1/127.0.0.1:19192/单KRaft/ISR1/PLAINTEXT none。凭据只由LoadConfig在内存读取，无DSN/口令/SSH命令复制或输出。未安装、重启、停共享服务、改ACL/配置或管理隧道。

实际固定依赖继承M37：Gin1.12.0、GORM1.31.2/mysql1.6.0、MySQL driver1.10.1、go-redis9.23.0、franz-go1.22.1/kadm1.19.0、gRPC1.84.0及M31生成协议。实际消耗的M37七个公开Go源码、M31两个生成Go文件及两module的go.mod/go.sum共13文件，Git内容对象与上述已验基线完全一致；108个实际远端module独立校验通过。

原 `go mod verify` **exit1仍保留**：M31与M37 v0.0.0本地replace均报告 `missing ziphash: open hash: The system cannot find the file specified.`。`scripts/verify-deps.ps1`先执行并记录原exit1，再按实际远端图生成忽略目录临时module执行download/verify，输出all modules verified与`M38_REMOTE_GRAPH_VERIFIED=108`；这不改写原整命令为成功。详细原输出 `.local/logs/deps.txt`。

## 完整实际命令

以下cwd均为本模块绝对目录；根目录复跑可以采用README的 `go -C lessons/m38_failure_recovery_lab ...`。耗时只是这次观察，不是性能保证。

| 实际命令 | 结果 / 范围 | 原始证据 |
| --- | --- | --- |
| `go mod tidy` | exit0，独立go.mod/go.sum锁定，无根或前置变更 | 模块文件 |
| `gofmt -w cmd internal`，`gofmt -l cmd internal` | 完成，最后-l无输出 | 所有手写Go |
| `go run ./cmd/demo -scenario core` | 修正后exit0，30.720s；core全部风险恢复/清理 | `.local/logs/core-r2.txt` |
| `go run ./cmd/demo -scenario proxies` | 首分组exit0，26.603s；真实MySQL/Redis通信故障，尚未含后来补F07 | `.local/logs/proxies-first.txt` |
| `go run ./cmd/demo -scenario kills` | exit0，48.498s；三真实Kill/Wait窗口全部恢复 | `.local/logs/kills-first.txt` |
| **最新 `go run ./cmd/demo`** | **exit0，117.514s**；all含core、三proxy/取消组、三强杀组，7个namespace全部恢复及清理 | **`.local/logs/demo-final.txt`** |
| 最新 `go test -v -count=1 ./...` | exit0，lab0.876s；4项辅助通过，3项Live明确SKIP，SKIP不计实库证明 | `.local/logs/test-final.txt` |
| **`scripts/verify-race.ps1 -Integration`** | **exit0，命令127.042s，lab119.158s**；7项测试全执行/全通过，无SKIP，无race诊断；LiveCore28.56s/LiveProxies40.08s/LiveKills48.31s；child实际race元数据true | **`.local/logs/race-final.txt`** |
| `go vet ./...` | exit0，无诊断 | `.local/logs/vet-final.txt` |
| `scripts/verify-deps.ps1` | 原verify exit1保留；13实际前置文件匹配；108远端module独立verify成功 | `.local/logs/deps.txt` |
| 用最新本课二进制逐份 `-verify-cleanup <精确original.json>` | 34份全部exit0；独立fresh只读client核对170表、92键、34topic、34group不存在，所有offset空；71.265s | `.local/logs/verify-all.txt`、`verify-all-counts.json` |
| 只读 `Get-Process -Id <本课精确登记PID>` | 15份历史child PID记录，present_pids为空；每次此前均已真实Process.Wait非零退出与stdout/stdin收尾 | `.local/logs/pids-gone.json` |

普通入口默认真实全矩阵；不需要实库环境开关。测试启用开关为`GOLEARN_M38_INTEGRATION=1`。完整race脚本还设置`GOLEARN_M38_CHILD_RACE=1`，childBinary实际带`-race`编译，Worker读取BuildInfo并断言父要求的race存在。普通默认child的actual_race=false不被算作race证明。测试package工作目录为`internal/lab`，其账本保存在`internal/lab/.local`，其他入口保存在模块根`.local`；两处均由.gitignore排除。

## 最新业务与协议证据

本课每单qty1，核心期望为3单/qty3/stock7，与M37原核心qty4/stock6属于不同run；没有改M37旧断言或数据。权威SQL独立核对库存守恒、订单/outbox数量与冻结完整载荷、inbox payload/topic/partition及最低first_offset、receipt order/event/quantity/effect_count，独立无group Kafka reader核对原始Key/Value/连续offset，admin读业务group持久next，真实HTTP核对原key/原order/owner/金额299分CNY/ACCEPTED/v1及PENDING→RECORDED。

| 风险 | 最新真实事实与恢复 |
| --- | --- |
| F01超时/停止 | Tx writes门已进入；client原DeadlineExceeded/UNKNOWN、server实际观察取消；handler/caller/App所有Done后SQL仍0单、stock10。Stop通知不代替Done；server根ctx取消先于放门。 |
| F02提前error/panic | 真同Tx未提交写后触发，Gin错误，独立SQL stock10/orders0/outbox0；正常后续创建对照真实提交。 |
| F03池饱和 | business池真实3Conn全占；第4请求WaitCount delta1、真实pprof DB.conn栈、500ms等待预算到期；Conn由持有owner归还，observer max1，总≤4。 |
| F04 HTTP回复丢失 | 代理实际完整201/COMMITTED/order后断下游；caller UNKNOWN，原key/API PENDING订单1/stock9，正常消费后RECORDED/effect1；没有盲写重试。HTTP Serve与hijacked handler独立Done。 |
| F05 COMMIT wire丢失 | 真COM_QUERY COMMIT包及真实server seq1 OK已捕获，driver得到invalid connection与UNKNOWN；SQL原单/outbox已提交、broker0/next-1。代理Done/旧clientSQL0后fresh原key恢复，effect1/next1。 |
| F06 Redis慢/不可用 | 真GET reply捕获但client i/o timeout；真消费Apply已提交后EVAL回复丢弃，父读generation2/data不存在、effect1/next-1，原error+UNKNOWN。旧worker/client/代理Done后fresh同group重投、去重、再次失效，next1/RECORDED。 |
| F07提交后Go取消 | 真实ACK和真实消费SQL commit后hook cancel生命周期ctx并实际读Done/Err，二者保留UNKNOWN+Canceled；前者sent0/raw1，后者effect1/next-1。恢复同event raw2/effect1/next2；这不是网络故障或OS Kill替身。 |
| K01 commit→publish | child真实AfterOrderCommit阶段/PID/identity认证，SQL1单/stock9/ob1sent0/raw0/next-1后Kill+Wait；fresh同原key/API/cache恢复到sent1/effect1/raw1/next1。 |
| K02 ACK→sent | child真实ACK后屏障，父raw offset0及SQL sent0后Kill+Wait；恢复重发相同event/payload，raw2、inbox/receipt/effect1、first_offset0、next2，完整API RECORDED。 |
| K03 consumeCommit→offset | child真实SQL effect1/offset-1/cache命中PENDING后Kill+Wait；fresh同原key先真实旧缓存，再重投Apply=false、再次失效、next1/API RECORDED，effect仍1。 |
| duplicate | core原offset0/1/2同Key/Value；后两次Apply=false而仍失效/确认；inbox首次0、effect1、next3。 |
| rebalance | broker真实Stable成员1→2→1，实际generation3→4→5，standby assignment空；只Poll/AllowRebalance，无ProcessRecord/CommitRecords，next3不动。standby workerDone后Leave/Close再继续core。没有在途旧generation写fencing声称。 |
| 缓存晚回填 | AfterRead真实旧PENDING，consumer提交/失效后晚回填。坏UnsafeFill使API真实命中PENDING而SQL RECORDED，精确Invalidate修复；正常Lua generation拒旧accepted=false，下一API RECORDED。core最后3单/qty3/stock7、ob/inbox/receipt各3、effect3、raw5/next5。 |

最终默认7个namespace依次为core `m37_d35a0828dc1c0e73`、MySQL `m37_efe2c793fcebd850`、Redis `m37_9f4043390984a6ba`、F07 `m37_601375369811788d`、K01 `m37_d95f3992adb56896`、K02 `m37_65a77cd87a8e7af3`、K03 `m37_7a7131f5957be855`；每份原账本位于模块根`.local/<namespace>/original.json`。

完整race7个namespace为core `m37_438b3a8c31a524d2`、MySQL `m37_8e79a6101bec4b04`、Redis `m37_5e9e46b57ff3b77c`、F07 `m37_ab095712081e8ebc`、K01 `m37_096cd003cc82bcc6`、K02 `m37_55afa38a1e7a73d2`、K03 `m37_3af443755900922f`；原账本位于`internal/lab/.local/<namespace>/original.json`。race三child实际PID1012840/1027768/1050200、actual_race=true，真实强杀后各exit status1、日志drain与stdin收尾，独立当前PID查验均不存在。

## 失败与修正记录

1. 初次构建 `go test ./...` exit1：`internal\lab\wire.go:6:2: "context" imported and not used`。修正未使用import后才进入真实实验，该次仅编译失败不算实库证明。
2. 首轮core500ms请求预算未抵达AfterOrderWrites门：日志已有RPC_HINT而无本次TX_WRITES，最终`等待 真实Tx writes后: context deadline exceeded`，Go日志`exit status 1`。原命令wrapper尾部Get-Content使wrapper自身显示0，故后续wrapper显式保存/exit原Go退出码；没有把wrapper0当业务成功。raw `.local/logs/first-core.txt`保留。原run `m37_817c2cfdd137e915`最终App safe=true、jobDone、5表/6键/topic/group清理与VerifyGone、SQL0全部完成；独立34份复查再次确认不存在。
3. 请求预算修为2秒，阶段await独立15秒/child20秒；不改期望SQL数字，也不把未进入窗口的运行称通过。core-r2实际进入hook并观察取消，完成整个core。
4. 主agent草稿预读在真实强杀前指出owner释放边界：只join业务jobs/Apps不足以涵盖OS process与proxy；StdoutPipe并发Wait可能提前关reader；HTTP Hijack handler不能靠Shutdown证明退出；未消费job error及动态error panic须保留。已经改父自有os.Pipe/单Wait owner/readerDone/stdin收尾，所有process/wire/HTTP safe加入释放判定，hijacked handler独立join，动态error panic使用%w，未消费工作cause保留。之后还把手动Kafka clients登记、UNKNOWN caller错误纳入guard。新默认与完整race均在这些最终guard上实际执行。
5. F07在首分组验证后补齐“提交后取消不能吞UNKNOWN”的独立错误路径；旧分组/早期all不冒充该项。`.local/logs/demo-before-f07.txt`、`demo-before-caller-guard.txt`保留此前all的真实成功范围；唯一最新全证据是demo-final与race-final。

## 资源收尾与未适用范围

所有取得的进程、代理、caller、App、手动consumer/standby即刻登记guard。收尾先停止入站、取消/放自己的门，独立Background预算Wait/Join全部Done；业务非零ExitError和是否真正退出分开判定。任何未Done、驱动/handler/proxy join或清理失败均保留对象，不先删状态掩盖失败。errors.Join保留原failure、UNKNOWN、cleanup；清理不是重新执行业务。

original SQL观察池max1；当前business/child max3；不同恢复client顺序close后fresh，最多4条本课SQL连接。original保留全部精确对象责任，child/fresh只BorrowCacheKeys授权名单、不Provision旧对象，借用者清理账本为空；最终原owner逐项清自己的对象。每组恢复期间未删除旧状态、未reset offset、未回滚陈旧数据；成功后才删除并独立读回。原client始终供父观察，故最后使用原owner CleanupOwned；公开CleanupOwnedVia能力只读复用前置契约，没有假称本课另测了原owner已关闭的分支。

完整最新default及race各7组35表/18键/7topic/group全部清理，后续新只读client又核对所有34份历史原账本共170表/92键/34topic/group与offset空；15历史child PID独立不存在。正常进程退出、hijacked handler/Serve、全部代理双方向/WG/socket0、caller最终Done、App SafeToRelease、SQL OpenConnections/InUse0均记录。原始日志、无秘密账本/spec/PID、等待栈和编译产物只留模块忽略`.local`，无秘密提交候选。

同聚合乱序不适用：M37只有不可变创建v1，没有v2更新或多版本投影；重复v1/不同order不冒称乱序，不额外投毒后删除消息/reset进度。Redis lease旧owner过期、续租/fencing不适用：M37没有这些机制，BorrowCacheKeys不是租约或分布式锁。没有服务节点宕机、多副本耐久性、跨业务服务幂等、支付/发货、生产SLA、全库内部goroutine零泄漏或长期稳定性证明。

公开文件共20个：`.gitignore`、`go.mod/go.sum`、`README.md/FAULT_MATRIX.md/STATUS.md`、`cmd/demo/main.go`、`internal/lab/{audit,core,http,kills,lab_test,owner,process,proxies,rebalance,suite,wire}.go`、`scripts/{verify-race,verify-deps}.ps1`。冻结哈希清单位于`.local/logs/frozen-files.json`。主agent可据此只读检查、独立运行默认全入口/完整enabled race/vet及精确维护入口；执行者已停止写入等待复审。
