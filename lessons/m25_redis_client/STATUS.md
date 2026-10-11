# M25 实际交付与验证状态

记录日期：2026-10-12，Asia/Shanghai。执行目录为 `C:/Users/Lenovo/.codex/worktrees/go-course-m25/go-demo`，以下记为 **W**。完整模块已实现并完成本执行者的真实验证，**冻结等待主 agent 独立验收**；本文件不代替主 agent 的 ACCEPTED 记录。没有 commit、push、切换分支、创建下级agent或写其他模块/共享文件。

## 当前交付与前置

独立 module `go-demo/lessons/m25_redis_client`，`go 1.27.0`，默认入口 `cmd/demo`。文件为 `.gitignore`、`go.mod/go.sum`、`README.md`、本 `STATUS.md`、`cmd/demo/main.go`，以及 `internal/lesson/{config,run,commands,concurrency,atomic,tasks,gate,faults,lesson_test}.go`。所有手写入口、业务/并发/连接故障、收尾与关键测试具有中文职责和就地机制注释；README提供符号调用链、正常/错误/取消时间线及完整修复解析。

已读取适用AGENTS（本机 `.codex/AGENTS.md`为空，未发现本worktree额外AGENTS）、根README/go.mod、完整M25 prompt、ORCHESTRATION、TEACHING_STYLE、INFRASTRUCTURE、modules.json的M25条目、M00 README/STATUS及接入/安装隔离契约；仅从主目录只读 `D:/IdeaProject/go-demo/files/开发框架`，没有复制原材料。M00的接入契约可消费，本课不导入/复制M00整套项目、不改根go.mod/go.sum/go.work。推荐M03/M05/M06/M13只影响阅读顺序。

## 实际版本与资源边界

| 项目 | 本次真实结果 |
| --- | --- |
| Go/gofmt | `go version go1.27.0 windows/amd64`；本课GOMOD明确位于M25目录 |
| GOTOOLCHAIN / GOWORK | `auto` / 空；模块没有toolchain/replace指令 |
| 默认CC/CXX/CGO | `gcc` / `g++` / `1`；race完成后实测恢复该状态 |
| go-redis | `github.com/redis/go-redis/v9 v9.23.0`；官方发布页与实际`go list -m -versions`一致，下载成功；依赖自身最低Go1.26.0 |
| 直接锁定内容 | v9.23.0 sum=`h1:/TTVdJa8BRNsybwwGGnmiYktHNlUXtbOQxlWpHBPL3s=`；go.sum保存实际下载校验；`go mod verify`通过 |
| 当前实际编译图间接require | xxhash/v2 v2.3.0、go.uber.org/atomic v1.12.0、x/sys v0.48.0；本模块mod tidy产生，未改全局缓存配置 |
| Redis协议/版本 | 实际AUTH/INFO返回Redis8.2.8，业务命令全部使用真实go-redis，RESP2/DB0 |
| Redis入口 | `127.0.0.1:16389`；主agent持有的严格SSH转发，owner=root，主agent登记PID755296。本课不创建/停止此共享隧道 |
| 服务与秘密索引 | 只读 `D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json` 的READY redis条目；credential_file指向独立忽略JSON，字段username/password/db，秘密没有打印/落进交付文件 |
| race工具链 | 只读主目录toolchain.json；WinLibs GCC16.2.0，实测target=`x86_64-w64-mingw32`；只在当前进程设置CC/CXX/CGO/PATH，不安装/改全局 |
| 客户端上限 | 正常池PoolSize4/MaxActiveConns4；pipeline专池显式-1；独占故障池1；MaxRetries=-1、DialerRetries=1、默认dial/read/write2s，池等待1s |
| 时间与任务上限 | 全入口ctx35s；每个owner cleanup join3s+必要救援join3s；总键清理新预算6s；TTL观察最多3s；最多8个并发worker、完整入口总18个已登记任务 |
| 故障范围 | 仅本课独占连接装饰器扣留下一份真实回复；pool200ms、ctx250ms、响应丢失read250ms；无服务重启、CLIENT PAUSE、长/无限Lua、ACL变更 |

库源码在实际Go module cache的v9.23.0中核对了Get/Client.Process、withConn/releaseConn、_process/context、ConnPool.waitTurn/dialConn、Conn.deadline、DurationCmd.readReply、Pipeline.Exec/TxPipeline和HasErrorPrefix。Go1.27的`go doc context.WithCancel/WithTimeout/net.Conn`实际核对取消、deadline与Close契约；README连接固定tag符号及官方Redis文档。公司Go/生产Redis、Cluster、AOF持久化与真实订单提交均未推断。

## 实际执行命令与结果

以下每条shell的workdir均为W；`go -C lessons/m25_redis_client`进一步选中课程module。私有运行日志/报告在本模块`.local/`，由本模块.gitignore忽略；公共关键结果在后表留存。

| 工作目录 | 实际命令/环境 | 结果与证明点 |
| --- | --- | --- |
| W | `go version`、`go env GOVERSION GOTOOLCHAIN GOOS GOARCH GOPATH GOMOD CGO_ENABLED CC`；本module `env -json` | Go1.27.0/windows/amd64、GOMOD边界、GOWORK和环境实测 |
| W | `go list -m -versions github.com/redis/go-redis/v9`；本module `mod download -json`、`mod tidy`、`list -m all` | v9.23.0可下载且校验存在；仅本module依赖文件改变 |
| W | `gofmt -w` 本课cmd/internal目录；最终`gofmt -l`两目录 | 格式化成功；最终无未格式化文件输出 |
| W | **首跑** `go -C lessons/m25_redis_client run ./cmd/demo` | 退出1，Lua输入验证的断言prefix写错；真实count仍1；18登记键EXISTS0、任务11/11、socket0/4，失败报告保留 |
| W | 修正HasErrorPrefix并补强前缀/TTL后 `go -C lessons/m25_redis_client run ./cmd/demo` | **退出0，46项真实断言**，23登记键剩余0、任务18/18、socket0/10，completed=true |
| W | `go -C lessons/m25_redis_client run ./cmd/demo -part core -report .local/core.json` | **退出0，30项真实断言**，18登记键剩余0、任务11/11、socket0/4；15存在键均正TTL、3预期不存在 |
| W | 默认 `go -C lessons/m25_redis_client test -v -count=1 -timeout 50s ./...` | 退出0；3个无网络测试PASS（host包含4个子场景）；3个实库测试明确SKIP，不把此命令当实库证明 |
| W | 当前进程`GOLEARN_REDIS_INTEGRATION=1`后同一`test -v -count=1 -timeout 50s ./...` | **退出0**；全部6个测试PASS，suite7.76s，包8.926s；新client独立EXISTS0，真实早退Done/Close，真实warmup AUTH错误链保留；finally恢复开关 |
| W | 当前进程只读toolchain索引，设CC/CXX/CGO/PATH和integration开关后 `test -race -v -count=1 -timeout 60s ./...` | **退出0**；全部6个测试PASS，suite7.80s，包10.153s；未报告race；真实46项和收尾都运行，finally恢复环境 |
| W | `go -C lessons/m25_redis_client vet ./...` | 退出0，无诊断 |
| W | `go -C lessons/m25_redis_client mod verify` | 退出0，`all modules verified` |
| W | `git check-ignore -v` 本module `.local/latest.json`、`.secrets/example.json` | 两条匹配本module.gitignore；没有创建秘密示例正文 |
| W | `git status --short` / `git diff --name-only` | 仅新增本module；已有跟踪文件diff为空，未修改共享文件 |
| W | 内存中加载实际password，对本module23个交付/忽略证据文件比对，不打印secret | `secret_value_matches=[]`，没有当前真实秘密正文匹配；Git HEAD仍为起点90f7381b495c08f4995dd13400fbc34fca69e8c0 |

最后完整入口启动UTC=`2026-10-11T18:41:49.4323892Z`（北京时间10-12 02:41:49），prefix=`golearn:m25:02dc5303529085fe:`。core启动UTC=`2026-10-11T18:42:22.2689356Z`，prefix=`golearn:m25:ef5c55280fdf238d:`。最后普通实库suite prefix=`golearn:m25:b5e04b25b4db089d:`，race suite prefix=`golearn:m25:a6676072afd73fd2:`。不同run使用不同前缀，各自按精确清单删除，不复用前一run的键。

## 独立业务与协议断言事实

| 覆盖范围 | 实际观察与修复结果 |
| --- | --- |
| 真实string读写与JSON | 合成订单ID=synthetic-25、state=cached_only；miss为found=false/error=nil；损坏JSON为found=true/SyntaxError且不是redis.Nil；重写合法JSON后读成功 |
| 真实HASH | HSET新增2、HGET state=pending、HGETALL两个字段；GET hash实报WRONGTYPE，改HGET读成功 |
| TTL/遗漏/覆盖/单位/过期 | 无TTL=-1ns；EXPIRE90s后正TTL；SET无TTL覆盖会移除期限；KeepTTL后正PTTL；Duration(30)=30ns；30ms错误期限真实过期（GET nil、TTL=-2ns），改30s后PTTL约29.938s |
| 读改写丢更新→INCR | 两worker固定都读0、都写1，最终GET1；8个worker INCR回复唯一1..8，最终GET8 |
| pipeline部分失败 | Exec前目标GET nil；Exec返回中间WRONGTYPE，前后Cmd成功；独立GET两写均保存；没有声称观察过跨client插入次序 |
| MULTI/EXEC运行错误 | 中间INCR hash=WRONGTYPE，前后SET都成功保存；不具备SQLrollback |
| MULTI排队语法错误 | 参数数量错误导致EXECABORT；独立GET目标不存在，与运行期错误的副作用不同 |
| Lua运行错误 | SET前键保存，INCR hash报WRONGTYPE，后SET未执行；独立GET前有后无，不期待自动回滚 |
| INCR+EXPIRE窗口 | 第一请求INCR1后停在gate；第二INCR2时TTL=-1ns；第一取消并真实Done，EXPIRE未发，INCR仍存在 |
| Lua期限修复 | orphan计数3且PTTL约89.938s；新计数1即有TTL；无效TTL0在写前拒绝且GET保持1 |
| 认证 | 本课独占错误口令client真实WRONGPASS；正常client仍可认证；未改共享Redis用户/ACL |
| 池等待取消/timeout/归还 | held真实GET回复扣住；池total1/idle0/pending1；第二context.Canceled、第三ErrPoolTimeout；release后held读真实值并join，池pending0/idle1；owner Close后ErrClosed |
| ctx默认边界 | ContextTimeoutEnabled=false时250ms ctx已到期但task仍停Read；release后GET成功；不将其扩展成借池/拨号也不看ctx |
| ctx deadline参与socket | true且250msdeadline，驱动设置较早读期限；代理返回net.Error timeout、本次ctx DeadlineExceeded、任务已Done/join |
| 无deadline手动cancel | true但无request deadline；cancel已通知，task仍Read；owner release后GET成功、Done/join，cancel不撤销命令 |
| INCR真实响应丢失 | caller的真实回复已扣住，另一连接GET1；随后read250ms timeout，另GET仍1、MaxRetries=-1，无自动重放 |
| 所有登记键TTL总核对 | 完整入口20实际存在键逐个PTTL均>0且≤90s，3本应未写入键为-2哨兵；随后23键DEL、EXISTS0 |
| 生命周期与早退 | 完整18任务全部Done/join；10条真实TCP连接Close计数归零。另一次真实回复已捕获后的owner故意早退，1/1任务退出、socket0/1；取消与Done分开证明 |
| warmup错误链 | 另一个真实AUTH失败，经faultClient的Join/%w后errors.As仍可找到redis.Error、HasErrorPrefix仍识别WRONGPASS；返回nil client/gate，socket0/1 |

## 精确副作用与收尾

最后完整run登记23个键后缀：`order`、`missing`、`bad-json`、`order-hash`、`forgot-ttl`、`wrong-unit`、`lost-update`、`pipeline-before/wrong/after`、`tx-before/wrong/after`、`tx-queue-abort`、`lua-before/after`、`incr-expire-window`、`lua-new-counter`、`pool-value`、`context-disabled`、`context-deadline`、`manual-cancel`、`response-lost`。键实际完整名为上面的随机prefix+后缀，提前登记防止请求响应丢失遗漏清理。

`missing`、`tx-queue-abort`、`lua-after`在预期路径从未成功写入，所以23是登记名而非23个已存储键。无TTL故障窗口经过实际观察和修复，其他存在键都有正TTL；最后Run的独立6秒清理预算按此精确slice DEL并EXISTS，remaining=0。真实集成测试随后使用另外一个client读同一精确清单，remaining仍0。没有SCAN-delete、FLUSH、其他模块或DeepHelp键操作。

任务18的归属是：读改写2、INCR修复8、EXPIRE窗口1、池实验3、响应实验4。所有在启动前登记；正常路径join、早退路径cancel/release并新预算join；真实warmup失败没有启动应用任务。`socketStats`记录本课真实Dial/Close，完整run0/10、core0/4、单早退0/1、warmup失败0/1。故障连接wrapper没有创建后台goroutine/监听端口/代理进程；正常长寿命client由Run最后关闭，不能在单请求里Close。主agent的共享SSH仍由其owner维护，不属于本课应关闭资源。

私有证据保留 `.local/run-initial.log`、`run-initial.json`、`run-final.log`、`latest.json`、`run-core.log`、`core.json`、`test-live.log`、`test-race.log`；没有生成/遗留本课可执行文件。首跑的原始失败保留：`断言 lua_validate_before_side_effect 未满足，观察=invalid_ttl_error=ERR ttl must be positive integer milliseconds count=1`。原因是本版HasErrorPrefix剥掉ERR后再匹配，旧prefix带ERR导致false；不是Redis Lua验证失效。改prefix为ttl后验证通过，并把说明放在源码和README。

## 未验范围与准确边界

没有重启/停共享Redis、做AOF/复制/恢复持久化验收、加载Redis Function、测Cluster/Sentinel/跨槽、故障真实服务进程、测试长Lua阈值或生产负载；响应故障来自本课真实连接的延迟代理，明确不冒充服务宕机。默认test的SKIP不作实库证据，已另外实际执行integration与race。race只覆盖执行过的课程/驱动路径，不证明所有未执行路径无竞争。

没有订单事实库或数据库Commit，所以缓存有数据不能证明订单已提交；本课返回的DTO/计数只证明Redis对应副作用。TTL期限、池等待和socket预算是课程值，未推断生产配置或延迟SLO。没有网络强杀/进程SIGKILL实验；defer不能承诺在强杀后清理无TTL反例，已在README解释报告精确键恢复边界。

交付完成后不自动推进M26或其他模块。主agent按整层屏障独立复验并统一汇总/提交；此执行者冻结等待review。
