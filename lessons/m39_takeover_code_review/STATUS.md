# M39 交付、自验与冻结记录

状态：**READY_FOR_REVIEW**。记录日期2026-10-12（Asia/Shanghai）。本课所有必需运行已完成，公开文件冻结后停止写入，等待主agent独立验收与最后第6层整层提交；本文件不自标ACCEPTED，不替代主验收，不推进其他模块。

## 权限范围与前置

唯一写入目录 `C:/Users/Lenovo/.codex/worktrees/go-course-m39/go-demo/lessons/m39_takeover_code_review/`，基线 `cf11684d94745063faf6e2c34bf345205e562853` 是已验第5层提交。所有shell显式指定此worktree/module；主目录、前置、共享调度/服务配置、用户files均只读。没有Git add/commit/push、切分支、下级agent、外部PR、资料上传、自动化管理或共享基础设施变更。

已读适用祖先AGENTS（`.codex/AGENTS.md`为空，其余相关祖先/仓库/lessons无文件）、根README/go.mod、本课完整prompt、TEACHING_STYLE/ORCHESTRATION/INFRASTRUCTURE/ACCEPTANCE，硬前置M32/M38 README/STATUS及直接调用/owner源码；按新切片真实调用链阅读M37公开Store/Cache/Resources/Broker/Service、M31生成Inspect接口，没有漫游或重跑全部前置。使用已安装go-learning-by-running的教学方法，用户明确不布置额外exercise优先于skill的可选练习建议。

独立module `go-demo/lessons/m39_takeover_code_review`，`go 1.27.0`，无toolchain指令或go.work；明确local replace M37与M31。新业务是按原key查询状态的Gin网关，复用唯一M37业务，不import前置internal、不复制整套前置项目、不循环依赖、不修改前置。M32方法用于锁定Gin真实框架链，M38故障证据仅作为前置边界，未冒充本课重跑。

## 实际环境、依赖和协议

Go1.27.0/windows/amd64；GOTOOLCHAIN auto，CGO_ENABLED1，普通CC=gcc，GOROOT D:/Go，PowerShell7.6.5，GOPROXY `https://goproxy.cn,direct`。完整race使用只读登记的WinLibs GCC16.2.0，实际`-dumpmachine=x86_64-w64-mingw32`；只设当前进程CC/CXX/CGO/PATH/实库开关，finally恢复存在性+精确原值，`M39_ENV_RESTORED=True`。没有go env -w、系统PATH变更或旧32位MinGW使用。

实际锁定Gin1.12.0、GORM1.31.2/mysql1.6.0、MySQL driver1.10.1、go-redis9.23.0、franz-go1.22.1/kadm1.19.0、gRPC1.84.0、Protobuf1.36.12。本课不生成新协议；消费M31已验protobuf/Inspect注册和client两个生成文件，grpc Reserve/Watch未实现。Go mod tidy最终只整理新增直接import的GORM依赖分类，版本图未改变。

真实服务使用主agent登记READY入口：MySQL8.4.11/127.0.0.1:13306/课程库golearn_m20；共享Redis8.2.8/127.0.0.1:16389/DB0；Kafka4.3.1/127.0.0.1:19192/单KRaft/ISR1/PLAINTEXT none。LoadConfig在内存读私有credential_file，不复制/打印DSN、口令、token或SSH命令。本课固定唯一SQL池max3、无额外observer；独立维护复核fresh max1且业务池已关闭。M37构造初始日志max4随后明确M39_BUDGET=3，没有将初始配置输出解释为实际并发4条连接。

`source-map.ps1`实际读取锁定官方module缓存及符号。Gin ServeHTTP662/pool.Get667/pool.Put674/handleHTTPRequest690；Context.reset103/Next188；CustomRecoveryWithWriter53；franz CommitRecords3094、offset+1在3107。4份实际源文件SHA256在`.local/logs/source-map.txt`，README对应官方链接。首次在线gin.go读取超时，未作为事实结论；锁定本地源码正常读取和校验。

## 真实命令、结果和日志

以下命令cwd均为本模块上述绝对目录，根复跑用README的`go -C`。耗时只是本次观察，不是性能保证。私有原日志不修改、不提交；公开文档摘录核对真实数字。

| 命令 | 实际结果 / 范围 | 原证据 |
| --- | --- | --- |
| `go mod tidy`、`gofmt -w cmd internal`、最终`gofmt -l cmd internal` | exit0，最终-l空；只写本module | go.mod/go.sum、8个手写Go文件 |
| `go run ./cmd/demo -scenario isolation -candidate bad` | **预期真实exit1，9.916s**；Bob完整HTTP200返回Alice/qty1，独立不变量要求Bob/qty2失败；仍完整清理 | `.local/logs/candidate-bad.txt` |
| 首轮`go run ./cmd/demo` | exit0，63.991s；6类+UNKNOWN，当时未包含后来增加的3种owner提前退出与HTTP panic回归 | `.local/logs/demo-first.txt` |
| `go run ./cmd/demo -scenario owners` | exit0，20.250s；3种真实Tx阶段提前error/panic/cancel收尾 | `.local/logs/owners-first.txt` |
| **最终`go run ./cmd/demo`** | **exit0，82.825s**；8组完整入口、10个namespace，包括所有最终业务/error/panic/cancel/owner断言 | **`.local/logs/demo-final.txt`** |
| 最终`go test -v -count=1 ./...` | exit0，review0.889s；3辅助PASS、3Live明确SKIP；SKIP不作实库证明 | `.local/logs/test-final.txt` |
| **`scripts/test-race.ps1 -Integration`** | **exit0，脚本90.668s，review82.391s**；6顶层测试全执行/全PASS，另5个Live子项全PASS，无SKIP、无race报告；环境恢复True | **`.local/logs/race-final.txt`** |
| 最终`go vet ./...` | exit0，无诊断；空输出文件就是实际结果 | `.local/logs/vet-final.txt` |
| `scripts/source-map.ps1` | exit0，锁定版本/符号/hash及本地stdlib契约读取 | `.local/logs/source-map.txt` |
| `scripts/verify-deps.ps1`（最终） | 原verify exit1保留；13实际消费前置文件匹配基线Git对象；108个远端依赖图独立verify成功 | `.local/logs/deps-final.txt`，早期deps.txt保留 |
| 私有`.local/scripts/check-env.ps1` | exit0；ABSENT/EMPTY/NONEMPTY各一次真实Go配置失败，脚本仍恢复存在性/精确原值，8运行文件hash不变，外层6环境变量恢复 | `.local/logs/environment-check.txt` |
| 最终二进制对所有本课精确`owned.json`逐份`-verify-cleanup` | **31份全部成功，62.167s**；155精确表、38键、31topic/group不存在，offset全空；fresh client SQL最后0 | `.local/logs/verify-all.txt`、`verify-counts.json` |
| `git status --short` / `git check-ignore` | 只出现本模块新目录；根`.local`和测试cwd`.local`日志/账本/二进制被忽略 | 冻结检查记录 |

完整race中LiveIsolation9.33s；LiveReviewCases53.28s，transactions9.47s/contexts5.96s/offsets23.19s/pools4.88s/unknown9.77s；LiveEarlyOwners17.53s。没有OS child业务实验或协议代理，因此不提出“父race就是childrace”的证据。正常HTTP/gRPC是真TCP，MySQL/Redis/Kafka是真服务和驱动；R05辅助不冒充实库。

原`go mod verify`两local replace报`missing ziphash: open hash: The system cannot find the file specified.`且exit1。脚本显式保留原结果，再以Git对象核对7个实际M37公开Go源、M31两个生成Go及两module的go.mod/go.sum共13文件与上述已验基线；实际远端图108个module独立download/verify输出all modules verified。不把替代检查写成原整命令成功。

## 固定业务证据与前后行为

| 实验 | 真实副作用 / 独立断言 |
| --- | --- |
| R01最小PR / 缓存用户归属 | Alice/shared/qty1、Bob/shared/qty2均201同Tx；候选SQL仅key使Bob200得到Alice；修复SQL带认证owner、Cache.Query继续认证owner，完整ID/owner/SKU/qty/金额299/598分/CNY/ACCEPTED/v1核对，各第二次真实Redis hit。409/401/404回归，无额外写，stock7/orders2/outbox2/effects0 |
| R02错误遮蔽 / defer / recovery | 真MySQL1062被if局部err+return nil吞掉，坏defer误Commit stock10→9/orders0；修复返回1062且Rollback，stock保持9。真Create写后error/panic皆回滚0订单/outbox；实际Gin handler panic→500/UNKNOWN且SQL仍stock9/orders0。未把修复期望改回10来伪装撤销坏提交 |
| R03 handler返回后ctx | 真202已完整返回，worker观察真实request Canceled，用结束ctx调用真gRPC取消，SQLstock10/orders0；修复明确owner、Command值副本、10s工作预算、job最终Done后stock9/orders1/outbox1。202仅进程内接纳，无持久异步承诺 |
| R04并发offset | 坏例低0在业务前门等待、高1真实SQL效果/失效/commit，low仍PENDING而next2/effects1；低worker取消/Done、旧consumer.Close后，fresh第一条实际offset2探针，raw3/effects1/next2。修复高先完成collector连续next0、broker仍-1；低SQL+失效done后连续next2才commit，fresh首2且raw3/effects2/next2。探针同高event Key/Value，无reset进度 |
| R05语言辅助 | typed nil接口非nil、动态类型*review.typedFault；直接nil接口为nil；defer实参旧err=false、闭包晚读err=true。辅助test另查error panic的errors.Is身份 |
| R06池耗尽 | 真3Conn全部持有，第4QueryRow120msDeadline，Stats.InUse3/WaitCount delta1；归还后SELECT1正常、InUse0；最后池Open0/InUse0。未增池掩盖owner问题 |
| R07 UNKNOWN原key恢复 | 真Commit之后hook才cancel并观察ctx，原cause同时UNKNOWN+Canceled；SQLstock9/order1/outbox1，原owner/key/full payload查询匹配、原缓存PENDING。只续原outbox/event，真ACK/sent、Apply Tx、Redis失效、groupnext1，完整原order RECORDED，raw1/payload完全相同/effects1 |
| owner提前error/panic/cancel | 每种先抵真实未提交TX_WRITES门，再让owner失败；stop/cancel后放自己的门，独立join所有caller/handler/Serve；**清表前**fresh Background查询stock10/orders0/outbox0。原cause和Canceled保留，errors.Is仍可识别，无ErrCleanup；验证后精确删fixture |

最新default10个namespace依次为：isolation `m37_29a66534a0c55aba`、transactions `m37_c4835952756f33d3`、contexts `m37_d2d43f2bd2214676`、offset bad `m37_ef564d0ad2e82ed2`、offset fixed `m37_09f31be8d9bff9e9`、pool `m37_fecad2595df3696e`、unknown `m37_a8e7213f951db215`、early error `m37_16fd291e6a43069c`、early panic `m37_a314be691b888869`、early cancel `m37_52ad5a21a2d368f6`。账本在module根`.local/<namespace>/owned.json`。

完整race10个namespace对应顺序为 `m37_d93e4f3d3b844715`、`m37_2d9361cd04255039`、`m37_9446e3b072204938`、`m37_f7c855893dd5b456`、`m37_87dc01ee0b664e81`、`m37_87d0e356516ec6dd`、`m37_7b38cd73e27e1aec`、`m37_f3f8abced6e15a10`、`m37_c05732d91c7a8867`、`m37_582df83da979854d`，在`internal/review/.local`。复核维护入口只读精确账本，不Provision、不删除、不收编清理权。

## 失败、诊断、修正与资源结局

真实bad候选的失败未改写：完整200不等于正确授权，原因有实际SQL范围、cache caller与副作用对照；最小两行有效diff和静态被拒AI候选在PR_REVIEW。库文件/版本不升级，前置源码不改。没有未通过的关键业务实验被写成PASS。

实现期主agent只读预审指出：gateway.close不能用最后一项join/Conn.Close的err打印handlers_idle；已保存独立handlerJoinErr。Context案例的门必须启动前装配，已改构造/登记/赋门/start顺序；后台ctx必须有明确预算，已每job派生10s并defer cancel。随后最终完整default/race均在这些实现上运行。新增early实验确保normal/提前error/panic/cancel均有真实owner收尾。

早期owners-first日志以总err判断cleanup_ok，故预期Canceled保留时显示false，但同日志VerifyGone/SQL0真实清理已成功；原日志保留。最终将cleanupErr与工作err分别记录，显示`cleanup_ok=true returned_error_present=true`，没有吞掉原Canceled或把失败归一成nil。最终default/race独立证明修正后的日志含义。race后仅把一个中文注释中的失效makeClient名称改为makeGate，没有改变任何可执行语句；最终vet/gofmt核对当前文件。

环境恢复专项只用GOTOOLCHAIN=m39-invalid触发真实Go配置拒绝（没有启动实库测试、没有伪造PASS）。同一PowerShell进程分别把CC/CXX/CGO/PATH/Live开关设为原不存在、原存在空串、原存在非空，每次被测脚本设置临时工具链后Go退出1，finally恢复；检查精确存在性/值和8个运行文件hash不变，再恢复外层6变量。三个原失败都保留在environment-check日志。

所有取得资源立即guard，停止入站与取消通知先于放门；HTTP Shutdown/强制Close、gRPC Stop、Serve返回都另等实际handler/任务最终Done。非nil工作结果与是否退出分开判断，未消费error/panic/UNKNOWN用errors.Join/%w保留；任何未Done不删对象。early独立事实失败保留中间状态，只在使用者已Done时关client，不能用删除掩盖误Commit。

最后清理只按原attempt账本精确表/键/topic/group，独立VerifyGone查不存在/offset空，SQL池最后0。保留日志、账本、binary和私有环境检查脚本，仅在本module已忽略`.local`。没有本课OS child、常驻工具、代理、共享服务变更或未知PID终止。

独立最终读回覆盖全部31份历史与最新账本：失败候选1、早期default7、早期owners3、最终default10、完整race10，共155表/38键/31topic/31group；全部真实不存在且group offset为空。最新default与race分别50表/12键/10topic/group均已经精确清理，随后新的max1只读client再次核对，没有把相同run的内部VerifyGone重复计为新增账本。所有验证进程正常退出，没有遗留本课listener、handler、任务或手动驱动client。

未证明/未实现范围：生产身份认证/TLS、长期高并发、所有库内部goroutine逐个Join、跨partition/generation在途写fencing、同聚合多版本、Redis lease/fencing、服务节点宕机、多副本容灾、支付/发货、生产SLA。M37只有不可变创建v1，短并发offset正确前缀不扩成上述保证。UNKNOWN只声明实际提交后取消窗口，M38的网络截断/跨OS Kill仍属于前置实证。

公开文件共17个：`.gitignore`、`go.mod/go.sum`、`README.md/PR_REVIEW.md/STATUS.md`、`cmd/demo/main.go`、`internal/review/{owner,gateway,cases,offsets,early,run,review_test}.go`、`scripts/{test-race,verify-deps,source-map}.ps1`。冻结逐文件SHA256清单在`.local/logs/frozen-files.json`；主agent据此独立验收，执行者不自行提交/推送。
