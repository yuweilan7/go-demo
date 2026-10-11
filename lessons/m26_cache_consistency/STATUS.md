# M26 实施与真实验证记录

状态：**READY_FOR_REVIEW（R1返工完成）**。记录日期2026-10-12（Asia/Shanghai）。主agent首轮独立实库demo/race/vet/mod verify均通过，但指出三项实质缺口，首轮未获接受。本记录保留首轮历史，以下R1证据才对应当前修复版本；本课全部范围与三项返工已自验，写入本记录并完成只读范围核对后冻结，等待再次独立验收。不自报ACCEPTED、不提交或推送、不继续其他模块。

唯一写入目录M：`C:/Users/Lenovo/.codex/worktrees/go-course-m26/go-demo/lessons/m26_cache_consistency/`。worktree根W：`C:/Users/Lenovo/.codex/worktrees/go-course-m26/go-demo/`。实际基线 `003854afb2e9b848164922a4ca5eb835a584de38`。工具workdir和所有Go命令的 `go -C`均显式W/M，主目录仅只读规范、启发材料、基础设施索引/凭据/工具链。没有写共享文件/其他模块、根go.mod/go.sum/go.work、examples/files；没有创建子agent、切分支、Git add/commit/push、服务安装/stop/start、新隧道、共享配置/ACL或DeepHelp数据变更。

## 交付文件与阅读路径

12个非私有文件：`.gitignore`、`go.mod`、`go.sum`、`README.md`、`STATUS.md`，以及7个手写Go文件：

| 文件/符号 | 实际职责 |
| --- | --- |
| `cmd/demo/main.go` | 70s总预算、Run返回后才决定exit，避免os.Exit跳过defer |
| `cachecourse/config.go` | READY索引、秘密只内存读取、真SQL connector/Redis客户端与预算 |
| `cachecourse/store.go` | DTO深拷贝、真实权威SELECT、容量2非排队回源、先扣Active再释放permit的接替边界、Queries/Active/Peak/Rejected、显式Tx提交/回滚 |
| `cachecourse/cache.go` | scope键、精确registry、cache-aside、错误与miss分支、4KiB bypass、jitter、版本校验、代际Lua |
| `cachecourse/lifecycle.go` | gate、go前owner登记、独立预算join、DoChan body候选/drainer、请求Result与最终Done |
| `cachecourse/course.go` | E01～E04真实实验、44项独立业务/协议断言、精确表/键清理 |
| `cachecourse/course_test.go` | 显式真实TestLive及7个辅助断言：复制/键/Result-Done/caller取消/error树/接替计数/提前失败真实join；每个owner创建即注册唯一无条件收尾guard |

README提供10～20分钟核心路线、完整符号调用链、SQL/缓存/并发时间线、正常/故障/取消/清理解析、错误版与修复对照。源码说明非平凡函数职责/caller/值/资源、关键屏障/错误/收尾与本课使用的Go语法。没有extra exercise或模拟服务替代。

## 版本与前置事实

| 项目 | 实際核验值 |
| --- | --- |
| Go / OS | `go version go1.27.0 windows/amd64` |
| env | GOVERSION=go1.27.0，GOTOOLCHAIN=auto，GOOS=windows，GOARCH=amd64，GOROOT=D:/Go，GOWORK空 |
| M的module | `go-demo/lessons/m26_cache_consistency`，go=1.27.0，无toolchain/replace |
| MySQL | driver `github.com/go-sql-driver/mysql v1.10.1`；实际SQL VERSION8.4.11/DATABASEgolearn_m20；READY loopback127.0.0.1:13306 |
| Redis | `github.com/redis/go-redis/v9 v9.23.0`；真实INFO server_version8.2.8；READY127.0.0.1:16389/DB0 |
| singleflight | `golang.org/x/sync v0.24.0`，实际缓存singleflight.go的Do/DoChan/doCall/Forget/Result已读 |
| 直接依赖 | 上面三项均锁定；go.sum已生成、mod verify通过 |
| go.mod间接依赖 | edwards25519v1.2.0、xxhash/v2v2.3.0、go.uber.org/atomicv1.12.0、x/sysv0.48.0；module graph还含第三方测试依赖，不是本课额外导入 |
| SQL/Redis容量 | 每SQL池MaxOpen4/Idle1；每source容量2立即拒绝；正常/故障Redis各独占client，MaxActive4/MaxRetries=-1 |
| race | 主agent已登记WinLibs16.2.0posix-14.0.0-ucrt-r2，真实target x86_64-w64-mingw32；仅进程CC/CXX/CGO/PATH与integration开关；R1在PowerShell7.6.5逐项验证存在性与原值同时恢复 |

已完整读M26 prompt、TEACHING_STYLE、ORCHESTRATION、INFRASTRUCTURE、根README/go.mod；适用祖先AGENTS为空，无模块额外规则。读实际M25/M15/M21 README/STATUS、基线ACCEPTANCE索引和直接接口：M25 Config/tasks/Redis错误契约；M15 Gate/Await/Cleanup/Tracker；M21 connector/rollback/withTx/Place。三课已随31课基线集成验收，复用真实基础设施/课程证据，未重跑全部前置，未复制整个工程。用go-learning-by-running技能。

主目录files只读检索，阅读 `files/语法进阶/Sync` 的sync.Pool对象复用/共享状态段落；README纠正其不能代替业务缓存/容量/一致性协议及一次地址观察不能作为保证。执行时核对Go1.27官方发布说明、精确database/sql@go1.27.0、singleflight@v0.24.0、go-redisv9.23.0源码/Redis keyspace；driver与前置契约按实际v1.10.1源码解释。单节点服务/安装持久性证据复用安装者，未重复环境stop/start验收。

## R1三项缺口、修复与当前版本证据

1. **回源计数接替顺序。** 原Read的defer先从slots取回permit，再锁mu扣Active，确实可能让新工作先计Active，容量2却观察到Peak3。Read现在共用`admitRead`与唯一`finishRead(nil)`路径，先完成锁内Active--，最后才让permit可被新请求取得。注释限定Active/Peak是应用已计数且仍持permit的工作，接纳后、计数前暂停可以短暂少计，不能把该计数说成MySQL服务端执行并行度。新增`TestAdmissionCompletionBound`把旧工作固定在扣数前，16轮独立验证第三项被拒绝、旧工作Done后新接纳成功、Peak始终2、Rejected16、Active与permit最终0、Queries始终0。它直接使用同一预算helper，不访问数据库，不伪称SQL集成证明。
2. **测试提前退出的真实收尾。** 原`TestCanceledCallerCleanup`在等待/Done断言Fatal时没有已注册finish保护；另有Result测试重复finish、error测试的protective结果无人观察。现在每个资源测试在owner创建后立即注册一个唯一guard，finish使用新Background预算取消/放门/真join，并观察或合并收尾error；正常路径观察join不重复finish。新增`TestEarlyFailureJoinsRegisteredTasks`在三个已登记worker进入后提前返回原失败，由唯一guard使三个worker实际结束并完成各自收尾槽，外部核对全部Done与原失败/工作收尾/取消三个原因都仍在error树。不是把父ctx取消当join证据。
3. **PowerShell不存在变量的恢复。** 原README两段finally仅把保存的值赋回Env，保存null不足以区分原变量不存在与存在空串。两段现在先保存`Test-Path`存在性和原值；原本存在用Set-Item恢复，原本不存在用Remove-Item恢复；随后分别核对存在性与原值。PowerShell7.6.5上用独占`GOLEARN_M26_RESTORE_PROBE`实际验证不存在、存在空串、存在非空三种情况，全部same_exists/same_value为True。普通integration与完整race实际执行后也逐项核对：CC/CXX/CGO_ENABLED/GOLEARN_M26_INTEGRATION原本不存在，结束仍不存在；PATH原本存在，结束存在且原值完全相同。最后连probe也恢复初始不存在状态，没有修改全局环境。

为了验证计数断言确实能抓住原缺陷，在内存保存修复后的store.go完整字节，短暂把同一`finishRead`改成先释放permit的原错误顺序，只运行该辅助测试。它在step=0确定失败：`err=<nil> counts={Queries:0 Active:3 Peak:3 Rejected:0}`，实际子命令退出1。finally逐字节恢复原修复源码并核对一致，随后才执行下表当前版本的完整实库测试与demo。这个受控失败没有触碰SQL/Redis，没有sleep猜调度，未放宽断言；失败日志在`.local/old-order-mutation.txt`。

下表M/W均是路径缩写，实际tool workdir与go -C写完整绝对路径。`.local`证据被本课.gitignore忽略，凭据仅内存读取，无秘密输出。

| 当前修复版本命令/证明 | 实际退出码及结果 |
| --- | --- |
| `go -C M test -v -count=1 -timeout 20s ./...` | **0**，7个辅助PASS，TestLive明确SKIP，package0.860s；`.local/unit-review-fix.txt` |
| `go -C M test -v -count=1 -timeout 100s ./...`，显式`GOLEARN_M26_INTEGRATION=1` | **0**，44项真实PASS+7辅助PASS；run`92e672eeeb4d938f710eaf47`，TestLive13.81s/package13.986s；`.local/integration-review-fix.txt` |
| `go -C M test -race -v -count=1 -timeout 100s ./...`，显式integration+登记的进程CC/CXX/CGO/PATH | **0**，44项真实PASS+7辅助PASS，无DATA RACE；run`db27d948f7f82ff85619fcb8`，TestLive14.44s/package16.455s；`.local/race-review-fix.txt` |
| `go -C M run ./cmd/demo` | **0**，44项真实PASS；run`b61e7ba3fa79fd864d6df894`；`.local/demo-review-fix.txt` |
| `go -C M vet ./...`、`go -C M mod verify` | **0/0**，无vet诊断，all modules verified |
| `gofmt -l cmd cachecourse`、`git -C W diff --check` | **0/0**，均无输出；当前7个Go文件已格式化 |
| `.local/env-review-proof.ps1`执行与finally | **0**，PowerShell7.6.5；三种Env初态与实际integration/race的存在性/原值均恢复一致；integration_exit=0/race_exit=0；完整输出`.local/env-review-proof.txt` |

当前三次实库运行分别创建自己一张随机表，每次均精确清理32个登记Redis键并另查EXISTS0、精确DROP表并另查information_schema remaining0；SQL池open/inuse/idle均0、真实Redis.Close成功。没有重跑前置课，没有复用旧run来冒充当前结果。当前demo热点source Queries1/Peak1；真实AUTH故障source Queries2/Peak2/Rejected6，最终Active0；成功Commit后缓存报错仍独立查出MySQL版本6。44项业务断言的完整观察范围见下方表格，R1没有改变这些有限保证或故障边界。

## 返工前首交付命令历史

下表M/W是阅读缩写，执行工具中workdir及go -C实际写完整绝对路径。共享配置只在进程内读取，无秘密输出。

| 命令、工作目录 | 退出码及结果 |
| --- | --- |
| `go version`、`go env GOVERSION GOTOOLCHAIN GOOS GOARCH GOROOT GOMOD GOWORK`，W/M | 0，以上版本与独立module真实值；基线rev-parse正确 |
| `go -C M mod tidy`，M | 0，锁定三项真实依赖、本课go.sum生成 |
| `gofmt -w cmd cachecourse`，M；最终 `gofmt -l cmd cachecourse`，M | 0，7份源码格式化，最终-l无输出 |
| 首轮 `go -C M test -v -count=1 -timeout 20s ./...`，M | 0，TestLive明确SKIP、5个辅助PASS；从未把这项称实库通过 |
| 首轮 `go -C M run ./cmd/demo`，M | 0，E01～E04真实通过；run30cea102ae2fdbfc88b64148，31精确键EXISTS0、表remaining0、SQL池0 |
| 版本校验/真实大DTO完善后的 `go -C M run ./cmd/demo`，M | 0，run5a33a6f714af495b2ddb9a0a，32键/表清理0，所有最终业务机制通过 |
| 首轮完整实库 `go -C M test -race -v -count=1 -timeout 100s ./...`，M（临时integration+工具链） | 0，run763def64ab0526848c22afd2，TestLive13.76s/package15.791s，无DATA RACE，32键/表/SQL池均0 |
| 首交付 `go -C M test -v -count=1 -timeout 20s ./...`，M | **0**，5个辅助PASS，TestLive明确SKIP，package3.199s；`.local/unit-final.txt` |
| 首交付 `go -C M run ./cmd/demo`，M | **0**，44项真实PASS，run2d360602763e707378b04c97，32精确键EXISTS0、表remaining0、SQL池0、redis_close_ok=true；`.local/demo-delivery.txt` |
| 首交付完整实库 `go -C M test -race -v -count=1 -timeout 100s ./...`，M，`GOLEARN_M26_INTEGRATION=1` + 进程amd64 CC/CXX/CGO/PATH | **0**，44项真实PASS + 5辅助PASS；runf64b5cac1921291b2ae60e45，TestLive13.49s/package15.585s，无DATA RACE；32键/表/SQL池0；`.local/race-delivery.txt` |
| 首交付race finally与 `go -C M env CC CXX CGO_ENABLED ...`，M | 当时值比较恢复True；gcc/g++/1，本课GOMOD与GOWORK空；**该值比较不能证明原本不存在的变量仍不存在**，R1已纠正文档并补实际存在性证明 |
| 首交付 `go -C M vet ./...`、`go -C M mod verify`，M | **0/0**，无vet诊断，all modules verified |
| `git -C W diff --check / diff --name-only / status --short --untracked-files=all` | 0，已跟踪文件无变化，未跟踪内容仅本课；没有Git写操作 |
| `git -C W check-ignore -v .../.local/demo-delivery.txt .../.secrets/config.json` | 0，分别匹配本课.gitignore；秘密目录未创建/未复制凭据 |
| 内存读取两个password，对模块与.local日志扫描精确字符串，不打印口令 | 0，credential_password_match_count=0 |

首交付demo/race在改进Close返回值日志与preflight错误撤登记后曾重新执行；仍有主agent发现的三项缺口，不能视为本次返工已验证。主agent另报告其独立首审demo/race/vet/mod verify均exit0、44项业务与精确清理通过，但明确没有接受；其只读证据在主目录`.local/course-orchestrator/parent-review/M26-pre-review-r1`，本agent没有写该目录。当前版本证明在上方R1表，私有日志全部留在忽略.local内，不纳入交付；没有生成可执行二进制。

## 真实观察与副作用

| 不变量/反例 | 最终独立观察 |
| --- | --- |
| 维度错误 | 三个危险键分别给B/u1、A/u2、schema2返回A/u1/schema1；不是只比较键字符串 |
| 维度修复 | 四个scope实际得到对应tenant/user/schema；再命中A/u1 source.Queries保持7 |
| rollback | 实际Tx UPDATE后回滚，外部SELECT仍v1/A-user1、缓存同值、committed=false/published=0 |
| 脏回填与两次DEL | old Scan v1→hold；writer真Commit v2/DEL/第二DEL→放hold→oldSET；实际cache1/MySQL2/sourceQueries1 |
| TTL | 对脏键PExpire300ms后反复真实GET最终nil；不是sleep猜测，TTL没有避免刚才错读 |
| 版本键 | old键v2仍存在；权威SQL版本3→新键v3实际值3；版本查询成本明确1次另计 |
| 版本lookup变化 | caller expected2、真实SQL3；ErrVersionChanged，目标EXISTS0，未无限重试 |
| fence | reader generation0/v3；writer提交v4并原子generation1+DEL；旧Lua返回0/data miss；新fill1/DTOv4 |
| 提交失效窗口 | writer提交v5未失效时实际cache4/MySQL5，随后本课显式失效结束窗口 |
| SF key错与修复 | A/B用同key时B真实收到A、SQL1；完整scope键分开，各对应正确，合计SQL3/峰值2 |
| leader ctx | SQL后hold固定未放，取消leader；两个caller都收到Canceled、Shared=true、SQL1 |
| follower ctx | follower.wait先Canceled，body仍Active1；放门后真实SETv5，其他caller成功 |
| Result/副作用/Done | caller已有Result、真实Redis已v5、sourceActive0，但drainer cleanup门未放/Done未关闭；放门后才join |
| 共享对象污染 | 顺序修改A的map/slice，B也看到caller-A/changed-A，无Go数据race但业务结果污染 |
| Clone | 两drainer交付前分别深拷贝，A修改后B仍mysql/original，实际SQL1 |
| 两Group | 相同key但独立注册表，真实SQL2/峰值2；不是OS多进程实测 |
| 负缓存 | missing两读SQL1；INSERT后仍负结果；DEL后真读新行，SQL2 |
| jitter | 12真实键每项PTTL>0且≤25..35s配置；具体种数随run的key hash而变，实际日志逐项记录；不是随机概率证明 |
| 大DTO | 本课真TEXT SQL返回5000字节name；业务拿完整DTO，>4096 JSON拒SET，EXISTS0；不是造一个Go对象冒充源结果 |
| 热点 | 100ms数据真实GET过期nil后，6项同步DoChan登记→sourceQuery1/Peak1/Sharedtrue |
| 缓存不可用 | 独占坏口令client真WRONGPASS；两读sourceQuery2/Active2/Peak2；后6项ErrOverload且保留AUTH原因，Rejected6 |
| 有限降级收尾 | 放hold、真join；两项得到v5，Active0；正常Redis真实PING成功，未破坏共享服务 |
| Commit后缓存错 | 真SQLCommit后badClient.Del真WRONGPASS；committed=true，独立SQL版本6；不把Redis错误当作没有写入 |
| 清理 | 每run精确DEL账本键→另一条EXISTS0；精确DROP表→information_schema0；Close成功、SQL open/inuse/idle全0 |

source指标只统计Read发起的权威SELECT，update/currentVersion的SQL另计；每个场景使用新source避免把其他阶段混进期望。Peak/Active是已接纳的应用回源工作，包含SQL已返回后仍在课堂hold门等待的工作；不是声称MySQL服务端同时执行两条SELECT的CPU/锁测量。实验没有生产RPS或延迟SLO证明。

## 失败、修复与资源边界

首交付正常编译/测试/demo/race没有失败，但通过未覆盖三项后续实质缺口；R1实际进行了一轮预期失败的旧顺序mutation，结果与逐字节恢复事实见上方，不能把这轮有意失败抹去。修复版正常单元、实库integration、完整race、demo、vet/mod verify均通过。首交付自审也曾补强：原大key观察只验证Go对象拒写，改成真SQL TEXT行+bypass返回完整业务结果；原版本地址增加expected/DTO.Version校验与真实版本不符拒填，说明版本指针查找不是免费神谕。另把硬编码redis_closed日志改为真实Close错误判断，并在preflight存在/读取未知时撤登记，保证清理不把未经确认的键当成本run数据。这些机制在R1当前完整实库中重验，未修改库版本、关闭race、放宽断言或改成mock。

所有本课go语句在启动前由owner登记；singleflight在DoChan前登记候选函数Done，并登记每项应用drainer。库未当选的callback没有资源，收到Result后结束空候选；当选body真实退出后才关闭其Done。caller取消只结束wait，drainer继续消费容量1库Result，不因无人读取而泄漏。owner正常/提前失败均cancel、释放自己的门、用新Background预算join，并Join保留工作/原失败/清理失败。R1补齐测试的owner创建即注册唯一guard，辅助测试独立核对Result不等于Done、caller取消后真正加入、提前失败三个登记worker全部实际结束，以及三类错误同时保留。

x/sync内部doCall goroutine由库拥有，API没有内部goroutine join句柄；本课没有伪造这个句柄。实际锁定源码在用户fn返回后仅做有限个缓冲结果分发，本课收齐注册结果并等待所有body/drainer Done后没有应用SQL/SET/cleanup副作用。README明确这是应用资源终态边界，不冒称整个库内部线程已经由某公开Wait API加入。fn入口recover覆盖普通panic错误，Goexit/os.Exit与永久不合作阻塞不在helper契约内。

每次表/键清理都来自自己的完整随机namespace，存在键均有有限TTL；没有扫描他人键、删除共享数据或停止任何实例。本课没有后台代理进程、listener、隧道或未结束实验进程；Go命令已退出，正常/故障客户端都Close。库维护goroutine生命周期由相应Client.Close处理，未用NumGoroutine快照当泄漏证明。

未验：真实多OS进程部署、RedisCluster/Sentinel/主从、eviction/重启/复制/AOF耐久性、MySQL副本/硬断电、所有网络分区/响应丢失排列、生产负载/大key性能、旧Go/旧driver、可靠事件/outbox恢复、跨实例锁/分布式强一致。故障实证是独占客户端AUTH拒绝导致本应用缓存不可用，不能改名为Redis进程停机证明。90s代际在本70s实验有效，丢失/过期归零的ABA风险与提交→失效窗口均保留，不把双删、TTL、版本键或锁包装成强一致。

本次主审可先查`store.go:admitRead/finishRead`与`course_test.go:TestAdmissionCompletionBound/TestEarlyFailureJoinsRegisteredTasks`、所有资源测试的唯一guard，以及README两段Env存在性恢复代码。原范围仍可查`course.go:staleRefill`三个SQL/Redis屏障与Commit窗口、`lifecycle.go:startFlight`候选body/真实Done/drainer、`course.go:unavailable`的AUTH+2/6计数及错误链。完整重跑命令、源码解析与证据范围已在README；本课保持READY_FOR_REVIEW，完成本记录后的只读范围核对即停止写入。
