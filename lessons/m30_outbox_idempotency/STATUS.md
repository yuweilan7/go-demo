# M30 实施与真实验证记录

状态：**READY_FOR_REVIEW（主验收返工 r1 完成）**。2026-10-12 04:46 +08:00，主验收指出的1062错误树保留与README环境变量三态还原已修复；修改后完整真实demo、enabled race、六个强杀窗口、重启/offset/查账及精确清理复验通过。原通过证据和历史失败保留，重新冻结等待主agent独立验收。只自报READY_FOR_REVIEW，不自报ACCEPTED、不提交。

工作目录W：`C:/Users/Lenovo/.codex/worktrees/go-course-m30/go-demo`；模块M：`W/lessons/m30_outbox_idempotency`。唯一写入范围为M及其忽略`.local/`。基线 `003854afb2e9b848164922a4ca5eb835a584de38`。没有修改根/前置/原examples/files、共享go.work、索引或其他module；没有下级agent、Git add/commit/push、分支切换或服务控制。

## 环境、前置与范围

实际Go `go1.27.0 windows/amd64`，GOTOOLCHAIN=auto；本课独立GOMOD，GOWORK空，go.mod为1.27.0，无toolchain/replace。实际锁定mysql driver1.10.1、franz-go1.22.1、kadm1.19.0；间接edwards25519 1.2.0、compress1.20.0、lz4/v4 4.1.30、kmsg1.14.0。mod tidy/download/verify实际通过。

完整读取M30 prompt、TEACHING_STYLE、ORCHESTRATION、INFRASTRUCTURE、根README/go.mod、适用祖先AGENTS（Codex AGENTS为空）。读取M21真实README/STATUS/withTx/unknownCase和M29真实README/STATUS/Config/NewClient/Close/ProduceAndWait/ConsumerOptions/Commit契约；前3层31课已主验收基线上复用机制证据，未重跑全部前置、复制全套前置或修改前置。只读主目录files/开发框架、Context/defer启发材料，README纠正取消通知≠同步退出及defer不能处理OS强杀。采用go-learning-by-running的实际运行教学方法。

官方SQL事务、Kafka4.3设计、franz-go查证入口实际访问；本地Go1.27.0 database/sql与精确锁定kgo/kadm源码实际读取ProduceSync、CommitRecords(r+1/分区响应错误)、Close/Leave、Metadata/ListEndOffsets/WithAuthorizedOps，不凭记忆混用API。

只消费主agent READY服务：MySQL8.4.11 `127.0.0.1:13306` / golearn_m20；Kafka4.3.1 `127.0.0.1:19192` / 单KRaft / PLAINTEXT / none。每run实际SQL VERSION()/DATABASE()与索引匹配，Kafka broker metadata实际advertised地址匹配。Kafka版本来自安装者镜像核验，不假称metadata返回版本。秘密只在内存读credential_file，未打印/复制DSN/password。父池max2、子池max1，同一run父子同时最多3连接；没有并发运行两套实库课程进程。

每run先落盘无秘密精确资源登记，创建前确认对象不存在再取得清理授权。表`m30_<随机16hex>_stock/orders/outbox/inbox/projection`，11个`golearn.m30.<run>.<case>` topic，12个登记group名，其中DB-first从未创建消费成员，其余11个group真正使用。每topic单分区/单副本、retention10分钟/1MiB、消息不超过2条，整课broker共12条。单relay，串行max1消费，不宣称多relay协议。

## 实际命令和结果

命令工具workdir始终显式W/M；下表M只是路径缩写，实际Go -C指向自己的模块（`-C .`时工具workdir=M），未在默认共享目录写入。

| 实际命令 | 已观察结果 |
| --- | --- |
| `go version` / `go env GOOS GOARCH GOVERSION GOMOD GOWORK CGO_ENABLED CC`（W，最后M复核） | exit0，版本和隔离如上 |
| `go -C M mod download` / `mod tidy` / `gofmt -w cmd internal` | exit0，真实依赖go.sum与8份手写Go格式化 |
| 源码组装期 `go -C M test ...` | 历史exit1，`l.totals undefined`；补齐独立最终查账后修复 |
| `go -C M test -v -count=1 -timeout 30s ./...` | exit0，4辅助断言PASS；实库/child入口明确SKIP，不作实库证据 |
| 首轮 `go -C M run ./cmd/demo`，run dc7db8c13e933c4e | 历史exit1，topic创建响应后缺少partition0 end；1topic/1group/5表删除返回与SQL清理成功，最终另做直接协议读回 |
| 第二轮默认入口，run55c853729e6ed0b6 | 历史exit1；E01～E06真实通过，E07空poll预算/Leave超时与NON_EMPTY_GROUP；10topic/5表清理，offset-bad group当轮未删除，保留失败并精确恢复 |
| `go -C M run ./cmd/demo -recover-registry .local/m30_55c853729e6ed0b6/resources.json -recover-group golearn.m30.55c853729e6ed0b6.offset-bad.group` | exit0，原进程已退出后精确Delete/Describe该group；`.local/recovery-2.log` |
| 修复后的默认入口，run3c2100ff3eb1a427 | exit0，E01～E08完整真实业务、6次强杀、重启与最终清理；`.local/demo-3.log` |
| `GOLEARN_M30_INTEGRATION=1`（当前进程try/finally）+ `go -C M test -v -count=1 -timeout 6m ./...` | exit0，run081a6394fdc611f5，TestRealMySQLKafka105.32s，包110.393s；4辅助PASS，完整实库/强杀/最终查账/清理通过；`.local/integration-final.log` |
| 首次 `& ./scripts/verify-race.ps1 -Integration` | Go test本身exit0，run ee999dddef8c0e55，TestReal111.29s/包113.412s，无DATA RACE，全部实库与清理通过；但脚本整体exit1，`M30_RACE_ENV_RESTORED=False`；`.local/race-final.log` |
| PowerShell独立环境还原诊断 | exit0：original_null=True / restored_null=False / restored_empty=True / Remove-Item后removed_null=True；说明.NET string参数null转换问题，未改全局环境 |
| 修复脚本后的同一真实race命令 | **exit0**，runa165b1a7dfc9dfe0，TestReal119.56s/包120.826s，全部实库/强杀/重启/查账/清理+4辅助断言PASS，无DATA RACE，`M30_RACE_ENV_RESTORED=True`；`.local/race-repaired.log` |
| `go -C M run ./cmd/demo -verify-cleanup <三个对应resources.json>` | 各入口exit0；dc7db8c13e933c4e、55c853729e6ed0b6、3c2100ff3eb1a427逐项5表/11计划topic/12计划group不存在，真实metadata且FetchOffsets为空 |
| 同一只读核验入口，分别传入internal/lab/.local中integration/两个race的resources.json（实际Go -C绝对M） | 各入口exit0；081a6394fdc611f5、ee999dddef8c0e55、a165b1a7dfc9dfe0每份登记5表/11topic/12group全部不存在且offset为空；六轮历史资源最后均实际读回 |
| `go -C M vet ./...` / `go -C M mod verify` / `gofmt -l cmd internal` | exit0，无vet诊断，all modules verified，格式无输出 |
| `git -C W check-ignore -v .../.local/... .../.secrets/... internal/lab/.local/...` | exit0，全部命中本模块.gitignore |
| `git -C W status --short --untracked-files=all` / `diff --check` | 仅本模块新增文件，diff无空白诊断，未暂存/提交 |
| 最后严格内存读password并逐文件精确扫描（日志均已关闭） | exit0，扫描208个文本文件，M30_PASSWORD_MATCH_COUNT=0；没有输出密码，不把之前partial0当证明 |
| 解析最终race的PROCESS_CRASH/PROCESS_EXIT并查询其精确PID | exit0，M30_RECORDED_CHILDREN=15，M30_RECORDED_CHILD_PIDS_STILL_PRESENT=0；所有Wait实际记录先于查询 |
| 最后`go -C M env CC CXX CGO_ENABLED GOMOD GOWORK` | exit0，gcc/g++/1，本课GOMOD，GOWORK空；脚本自身finally逐变量还原也True，无全局改动 |

## 持久业务和协议事实

以下都由固定预期的SQL/原始Event比较/真实broker offset断言，不仅日志输出。成功run3c2100ff3eb1a427与实库test081a6394fdc611f5、两个真实race ee999dddef8c0e55/a165b1a7dfc9dfe0均有这些结果。默认入口通过后追加的panic清理原因保留、producer goroutine panic边界、E07立即登记保护defer、子进程正常Wait日志和只读维护入口均由最后完整race/维护命令覆盖；没有把早期默认运行当新增收尾逻辑的唯一证据。

| 实验 | 真实事实 |
| --- | --- |
| E01 DB先提交再发送失败 | 源stock9/orders1/outbox0；独占TCP真实连接后发送前主动关闭，driver非nil；broker记录0，下游投影10/effects0 |
| E01 Kafka先发送再DB回滚 | 真实ACK后普通ErrEffect回滚源Tx，stock10/orders0；broker孤儿Event匹配；下游effects1/inbox1/projected9，next1 |
| E02 ACK响应真实丢失 | Produce请求已写真实socket、correlation对应响应被吞；caller error/UNKNOWN；broker读同event ID/payload1条；原key查询与重复Place返回原订单，源只扣1 |
| E03 订单commit前强杀 | 独立child实际Tx写三项后ready，父Process.Kill/Wait exit1；锁定读后stock10/orders0/outbox0；原key恢复下单两次仍订单1/outbox1/扣减1 |
| E03 commit后强杀 | 真实Commit nil后ready/强杀Wait；stock9/orders1/outbox1/sent0；原key权威查询恢复COMMITTED，重复请求稳定ID，无第二次扣减；改qty2拒绝 |
| E04 relay ACK后强杀 | ACK后、sent前独立child强杀；sent0；独立新relay进程重投后sent1，broker2条同ID/value；consumer两个真实offset0/1，只一个SQL效果，inbox1/projected9/effects1/next2；同ID改qty拒绝 |
| E05 inbox插入后强杀 | child已INSERT inbox、未效果/未Commit；强杀Wait后锁定读inbox0/effects0/next-1；同group独立新OS进程读原offset0并成功，inbox1/effects1/next1 |
| E05 业务commit后offset前强杀 | inbox/效果已Commit，强杀后inbox1/effects1/next-1；新OS进程重投原offset0，持久inbox命中applied=false，effects仍1，next1 |
| E06 inbox后效果普通失败 | 实际1062唯一/业务ErrEffect路径分开；效果故障整体rollback，inbox0/effects0/next-1；Close后同group新client重投成功才next1 |
| E07 offset-before错误强杀 | child CommitRecords原offset0→next1，尚无业务时强杀；inbox0/effects0；同group重建真实Stable后有界poll无消息；课程repair group重放原事件，业务成功后next1/effects1 |
| E08 版本乱序 | 源两笔订单v1/v2库存8/outbox2，Kafka实际offset0=v2/offset1=v1；首次ErrVersion rollback inbox，next-1/effects0；重新读取两原记录并对账源订单，按v1→v2业务提交，最后才next2；inbox2/effects2/projected8/version2 |
| 最后查账 | orders11/qty11、stock99、outbox9/sent9、inbox11、projection99/effects11；11topic共12条包含relay的重复事件。坏例丢/多效果总量恰好抵消，逐SKU断言不可省略 |
| 最后清理 | 正常run 11实际topic/12登记group名逐项真实协议读回，5表information_schema不存在，DB open/inuse0；所有子进程由唯一Wait owner join，客户端真正Close后done |

6个崩溃模式为order-before、order-after、relay-ack、consume-inbox、consume-commit、offset-before。每个ready都位于真实Tx写入/Commit/ACK/offset确认点，父主动强杀并独立Wait记录exit1；普通ErrEffect不冒充崩溃。正常子进程9个，包括relay和consumer重启，Wait exit0；日志在各run private spec旁。主process正常返回时全部子进程已有Wait结果。

当前辅助断言5项：稳定event_id/安全表前缀；gate ready存在仍未done，取消后真join；net.Pipe实际分段Kafka frame/correlation提取（只辅助协议，不冒充broker）；errors.Join保留原原因、关闭原因和ErrCleanup；Place实际1062恢复函数的错误树边界（r1新增）。原历史轮次4项不改写成5项。显式实库测试额外覆盖全部E01～E08。Result、ACK、SQL Commit、broker offset和finalDone在代码/README分别定义。

## 真实失败及修复

首轮 `缺少partition0 end`：先前“不存在”metadata被kadm缓存，Create成功不保证随后缓存已更新。补充等待分区/leader真实存在，并在创建/删除/offset查询用锁定版kadm的WithAuthorizedOps走直接请求；最终再核验旧run全部计划名真实不存在，未把缓存当删除证据。

第二轮E07把空PollRecords借完整15秒probe预算，调用不返回就没机会读Stable；异常Close时Leave3秒又不足，错误组留下NON_EMPTY_GROUP。修复每次poll只借200ms子预算、外层按真实Describe谓词观察Stable，正常consumer创建后立即登记保护defer；Leave独立8秒，最后Close与独立10秒done join；group清理先Describe无成员再Delete/读回。未吞超时/组非空，旧run已退出后精确恢复删除该唯一group，随后三个历史run所有精确资源/offset只读查账成功。

首次race实库/语言断言本身通过，但脚本finally把原本不存在变量用.NET string参数还原成空字符串，检测False并正确整体exit1。独立复现后修复为原值null时Remove-Item Env:精确名，已有值才SetEnvironmentVariable；只作用当前进程。原失败日志保留，没有把Go test PASS扩成整个包装命令成功。

首轮秘密扫描在race日志仍打开时遇ReadAllText sharing violation，命令当时仍exit0且输出partial match0；这一轮不作完整秘密扫描证据。最后日志关闭后设置ErrorActionPreference=Stop重新全量扫描，208文本中精确password命中0，exit0。

## 主验收返工 r1：错误树与文档环境还原

主agent已经独立完整阅读8份手写Go、README/STATUS、SQL/Kafka/子进程owner；它的原版默认demo106.497s、enabled integration race120.527s、vet/mod verify均exit0，六次强杀/九次正常重启、逐SKU/总量和精确清理通过。它保留原证据于自身`parent-review/M30-pre-review-r1`，但要求先修复以下两处才继续验收；该主验收记录不是本agent自报ACCEPTED。

第一处：原Place在`errors.As`找到1062后，只要QueryOrder找到原单就返回nil，会抹掉Join树中的ErrCleanup或ErrUnknown。现在实际Place失败出口调用`recoverOrderDuplicate`，先用`errors.Is`检查整棵树；含任何这两种状态就原样返回cause、不调用查询。只有不含严重状态且确为1062才查原key，匹配参数才恢复原Event。查询失败、参数冲突用Join保留原1062和新原因。回滚/提交/Close协议及六个崩溃窗口没有放宽。

`TestOrderDuplicateRecoveryBoundary`直接测试Place实际使用的函数，8个子项：普通包装1062恢复；1062+cleanup、1062+UNKNOWN、1062+两者均拒绝且查询次数0/原树原样/各cause仍可Is；其他SQL拒绝；查询失败、原key不存在、参数冲突保留原因。这些1062/cleanup/UNKNOWN cause是在辅助测试内构造，属于分类与错误身份保留证据，不声称实际网络断连、Commit丢失或Rollback失败。

完整真实实验E03另外在order-before/order-after恢复后，用新Tx重插已提交的同一订单，实际MySQL返回1062；withTx安全回滚后走同一恢复函数，权威查询所得完整原Event相等。每轮两个`ORDER_DUPLICATE_RECOVERY sql=1062 rollback_safe=true original_match=true`均通过。最终逐SKU仍只有订单1/扣减1/outbox1，未增加消息/订单，不把注入cause当实库故障。

第二处：README普通integration片段现在保存Test-Path存在性与GetEnvironmentVariable原值；原不存在用精确Remove-Item，原已存在才SetEnvironmentVariable原值。当前PowerShell7.6.5实际短探针运行相同finally代码，`.local/env-three-state-r1.log`记录：

| 初态 | before存在/null/空 | after存在/null/空 | restored |
| --- | --- | --- | --- |
| 缺失 | false / true / false | false / true / false | true |
| 已有空字符串 | true / false / true | true / false / true | true |
| 已有非空值 | true / false / false | true / false / false | true，原值精确相等 |

探针外层也恢复原环境，`M30_ENV_PROBE_OWNER_RESTORED=True`，exit0；不改持久环境。race脚本在原版已经修过，此轮没有修改它，也没有安装/全局go env修改。

| 修改后实际命令（工具workdir=M，Go -C均绝对M） | 实际结果 |
| --- | --- |
| gofmt -w三份改变Go；`go -C M test -v -count=1 -timeout 30s ./...` | exit0，5辅助PASS，含8个新边界子项；真实入口显式SKIP，不作实库证明；`.local/unit-repair-r1.log` |
| `go -C M run ./cmd/demo` | exit0，run8d0ca75d717ef807，整命令112.139s；`.local/demo-repair-r1.log` |
| `& ./scripts/verify-race.ps1 -Integration` | exit0，run91b92b55ced4ed5d，TestReal120.06s/包122.151s/包装命令125.321s；5辅助与完整E01～E08 PASS，DATA RACE数量0，ENV_RESTORED=True；`.local/race-repair-r1.log` |
| `go -C M run ./cmd/demo -verify-cleanup .local/m30_8d0ca75d717ef807/resources.json` | exit0，5表/11topic/12group实际不存在，直接metadata与group offsets为空 |
| 同一只读入口传`internal/lab/.local/m30_91b92b55ced4ed5d/resources.json` | exit0，同样精确5表/11topic/12group不存在且offset为空；两份`.local/cleanup-repair-r1-*.log` |
| `go -C M vet ./...` / `go -C M mod verify` / gofmt -l / `git -C W diff --check` | exit0，无vet/格式/空白诊断，all modules verified |
| 逐本轮两份完整日志断言强杀/Wait/1062/最终查账/关闭计数并查询精确子PID | exit0，每份6强杀+9正常Wait+2真实1062+1固定FINAL_AUDIT+1DB0/0，DATA RACE0；共30已登记子进程PID仍存在0；`.local/audit-repair-r1.log` |
| 全部本轮日志关闭后的严格内存凭据精确扫描 / 最后gofmt -l与diff --check | exit0，当时364文本文件password命中0，扫描无sharing violation/partial；格式/空白诊断0；`.local/security-repair-r1.log` |

两轮都复验全部六个真实强杀窗口与九个正常子进程恢复，Result/ACK/Commit/offset/finalDone和精确清理仍分别成立。relay broker2条→inbox/effect1→next2；inbox未commit强杀/普通效果失败均inbox0/effect0/next-1；效果commit后强杀重启命中持久inbox，效果仍1才next1；offset-before坏例next1且效果0、repair group恢复；版本v2先到拒绝提交，v1/v2受控恢复后next2。最终均orders11/qty11、stock99、inbox11、outbox9/sent9、projection99/effects11、brokerrecords12，逐SKU断言照常执行。

返工只改5个公共文件：store.go（恢复函数/Place实际出口）、lab_test.go（8子项分类边界）、experiments.go（E03真实安全1062）、README.md（原理/读取链/三态命令）、STATUS.md（本次实际证据）。依赖锁、race脚本和其余交付文件未改。所有新日志/spec/ready/登记仍位于自己的忽略.local内；没有重跑前置、写共享/前置、Git或服务控制。

## 未验证边界

未做多relay claim/lease/CAS/SKIP LOCKED、生产多节点/复制/硬断电耐久性、SASL/TLS、吞吐/长压测、长期inbox/outbox保留/压缩、公司旧Go/旧库/其他OS。默认单relay，每个partition串行max1，E08只做最多两条的显式受控对账恢复。Kafka单ISR的AllISRAcks不宣称多副本容灾。SQL幂等不扩成外部发货/扣款幂等；Saga只给不可回滚发货的具体状态图，没有外部支付/发货运行或通用框架。

本课没有服务停启、容器/代理/全局设置变更、DeepHelp数据操作、全库删除或他人topic/group清理。私有日志/spec/ready/资源登记留在已忽略.local，未复制凭据、未保留交付二进制。无本课遗留进程/业务任务/协议对象，服务继续归唯一安装者持有。冻结交付14个公共文件（README/STATUS/.gitignore/go.mod/go.sum、8个手写Go文件、race脚本），SHA256清单在忽略`.local/delivery-manifest.json`。目录停止写入，等待主agent独立验收；如验收指出缺口，由原agent返工。
