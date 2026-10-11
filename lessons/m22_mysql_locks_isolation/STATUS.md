# M22 实施与真实验证记录

状态：**READY_FOR_REVIEW**。2026-10-12 04:38 +08:00，主审R1要求的三项提前失败/环境恢复返工已完成，当前源码的全量demo、完整实库race、默认SKIP/HTTP断言、vet和mod verify均已重新通过，重新冻结等待主agent独立验收。不自报ACCEPTED，不提交，没有额外exercise。

唯一写入范围是 `C:/Users/Lenovo/.codex/worktrees/go-course-m22/go-demo/lessons/m22_mysql_locks_isolation/`（下文M）。worktree根W为 `C:/Users/Lenovo/.codex/worktrees/go-course-m22/go-demo`，实际基线 `003854afb2e9b848164922a4ca5eb835a584de38`。读取/写入/执行工具都显式workdir=W或M，所有Go执行均显式 `go -C M`。未修改根、其他已验收模块、examples、主目录files、共享索引/配置，没有下级agent、切分支、add/commit/push。

## 实际版本、前置与范围

| 项目 | 本轮实际值/契约 |
| --- | --- |
| Go | `go version go1.27.0 windows/amd64`，GOROOT D:/Go；GOTOOLCHAIN=auto |
| 模块 | go-demo/lessons/m22_mysql_locks_isolation，go=1.27.0，无toolchain/replace/go.work；GOMOD=M/go.mod，GOWORK空 |
| driver | github.com/go-sql-driver/mysql v1.10.1，实际driver go.mod为1.24.0；间接edwards25519 v1.2.0 |
| 服务 | 安装者READY索引127.0.0.1:13306；SQL VERSION/DATABASE实际8.4.11/golearn_m20 |
| 配置实证 | SQL只读 `@@innodb_rollback_on_timeout=0`、`@@innodb_deadlock_detect=1` |
| 连接/预算 | MaxOpen4/MaxIdle1；一般两业务会话+一观察连接；总ctx80秒，独立清理/最终join8秒，session锁等待2秒，driver读10秒 |
| 命名空间 | 每run crypto/rand生成12字节/24位hex；精确四表 `m22_<run>_stock/orders/ranges/scan`，只操作自有合成行 |
| 凭据 | 只读安装者index/credential_file，仅内存加载username/password/database；不复制、不打印DSN或秘密 |
| Windows race | 既有WinLibs16.2.0posix-14.0.0-ucrt-r2；实际compiler `-dumpmachine=x86_64-w64-mingw32`；process临时CC/CXX/CGO/PATH/开关，finally全部恢复True |

已读取完整M22 prompt、TEACHING_STYLE、ORCHESTRATION、INFRASTRUCTURE、根README/go.mod与适用祖先/模块AGENTS位置（没有附加规则）。首次把规范按根路径读取时得到文件不存在，随后从learning_prompts读取完整真实文件，未移动/改共享文件。主目录files/开发框架是只读启发材料，只有CRUD学习入口，不含已验证的隔离/锁结论。

硬前置M21已读取本worktree真实README、STATUS、txcourse/config.go/task.go/transaction.go/store.go。M21的模块STATUS保留其当时READY_FOR_REVIEW历史，但本基线 `lessons/ACCEPTANCE.md` 的第2层明确记录主agent已验收M21及真实run/race/表不存在证明，不能只看旧模块状态推断缺前置。本课复用其显式Tx/退出Rollback/UNKNOWN/Result与Finished契约；不导入internal、不复制整套课程、不重跑前置实验。M21 Store/Request/Place的真实接口与状态已核对，M22使用自己的独占表与锁实验业务。

2026-10-12通过官方MySQL8.4事务模型/隔离/锁设置/死锁/错误处理、Go1.27 database/sql与本地锁定driver源码重新核对。driver BeginTx实际发SET TRANSACTION设置下一Tx；本课为了准确读取@@transaction_isolation，使用固定Conn的SET SESSION与默认BeginTx，退出恢复RC。源码/官方入口和教学解读见README，不把语言保证、库契约、优化器计划或一次PROCESSLIST State混在一起。

## 命令与真实退出码

M/W在下表只是文字缩写，实际工具调用的workdir与go -C都是完整绝对路径。所有实库命令按次序执行，没有在本课同时开两套实验占连接。

| 命令（工作目录M，读取规范时W） | 退出/观察 |
| --- | --- |
| `go version`、`go env ...`、`go -C M list -m all` | exit0，版本/模块/driver如上 |
| `go -C M mod tidy` | exit0，独立go.sum/间接依赖生成 |
| `gofmt -w cmd lockcourse`；最终 `gofmt -l cmd lockcourse` | exit0，R1后10份手写Go文件；最终-l无输出 |
| 首次 `go -C M test ./...` | exit0，实施初期编译检查，当时未有测试；不算实库证据 |
| 首次 `go -C M run ./cmd/demo` | exit0；m22_dc16375c1f4f9b8e22729ed1；E01～E06全部SQL/HTTP通过，四表remaining0/DB全0；当时snapshot EXPLAIN取太早，后续增强见下 |
| EXPLAIN/资源分类增强后 `go -C M run ./cmd/demo` | exit0；m22_5bf342fa50540a436018adb0；计划已在实际行存在后获取且key/access独立断言；全部旧/新契约通过 |
| 显式GOLEARN_INTEGRATION=1 + `go -C M test -v -count=1 -timeout 100s ./...` | exit0；m22_3e8d3100b56c27b1676c8ea2；TestMySQL37.55s，package43.325s；五辅助断言通过，四表清理 |
| 首轮实库 `go -C M test -race -v -count=1 -timeout 100s ./...`（临时amd64环境/显式integration） | exit0；m22_0ef5d09b1e3df6f1b6339984；TestMySQL34.23s/package41.055s；无DATA RACE，五辅助通过，四表remaining0 |
| qty参数/qty2/qty0补充后 `go -C M test -v -count=1 -timeout 35s ./...` | exit0，五辅助断言PASS/package5.734s；TestMySQL明确SKIP，不冒充实库 |
| qty补充后的完整 `go -C M run ./cmd/demo` | exit0；m22_c36c216398aca6a87cbb200a；quantity.guard stock1/orders1/qty2，全部E01～E06通过，四表remaining0/DB全0 |
| R1前全量实库 `go -C M test -race -v -count=1 -timeout 100s ./...`（临时amd64环境/显式integration） | exit0；m22_7a00bf72ef9a922012f2649f；TestMySQL38.20s/package44.970s；qty边界、全部SQL/真实HTTP/五辅助断言通过，无DATA RACE；历史证据，不覆盖R1新增边界 |
| R1前核心入口 `go -C M run ./cmd/demo -part stock` | exit0；m22_05d9fe3d2209ef1559e72466；超卖/三修复/1062回滚/qty守恒与输入拒绝全部通过，四表remaining0/DB全0 |
| **R1当前源码全量入口** `go -C M run ./cmd/demo` | **exit0**；m22_872f0f4ca53661c8de6d60ae；全部E01～E06重新通过，四表DROP后remaining0，DB open/inuse/idle均0 |
| **R1当前源码完整实库race** `go -C M test -race -v -count=1 -timeout 110s ./...`（临时amd64环境/显式integration） | **exit0**；主实验m22_f835eeb36793537022ccff1b；TestMySQL38.54s，新增实库DDL1.52s，HTTP尾部0.02s，package46.904s；8项测试全部通过，无DATA RACE，主实验四表remaining0/DB全0 |
| **R1当前源码默认测试** `go -C M test -v -count=1 -timeout 35s ./...` | **exit0**/package5.782s；6项默认辅助/真实loopback HTTP通过；TestMySQL和TestCreateAttemptOwnsUnconfirmedEffect两项明确SKIP，无开关时不触实库 |
| R1当前源码 `go -C M vet ./...`、`go -C M mod verify` | exit0，无vet诊断，all modules verified |
| race finally恢复后 `go -C M env CC CXX CGO_ENABLED GOMOD GOWORK` | gcc/g++/1，本课GOMOD/GOWORK空，process_environment_restored=True，无全局修改 |
| `git -C W check-ignore -v .../.local/run-final.txt .../.secrets/check.json` | exit0，匹配本课.gitignore |
| 内存加载password，对本课交付及.local日志精确字符串扫描（不输出密码） | exit0，credential_password_match_count=0 |
| `git -C W rev-parse HEAD/status --short --untracked-files=all/diff --stat` | 基线正确，只新增本模块交付路径；未修改其他tracked文件 |
| 两个README finally写法的实际PowerShell轻量检查 | 普通integration与race写法各在“不存在/存在非空/存在空串”三种状态下运行；6项exact_restore均True，probe进程环境最后恢复原存在性/值 |

普通实库test和首轮race是数量补充前真实通过的历史，不拿它们单独覆盖后续qty/重复键清理分类增强。R1前的全量实库race与主agent独立验收也不能覆盖随后发现的提前失败缺口。R1当前源码已经重新全量demo与完整实库race，新增DDL真实副作用/归属断言和实际loopback HTTP强制停止/尾部join断言也在同一race命令内通过。没有略过失败、关闭断言、换mock或用PASS文本代替SQL事实。

## 真实业务/锁/退出证据

下面是固定业务预期及本轮真实观察。每个audit来自独立DB查询，所有join均先等最终Finished，再取结果。

| 场景 | 实际结果与独立证明 |
| --- | --- |
| check-then-update反例 | 两不同connection普通读都1，ready屏障后无条件扣减；stock=-1/orders2/qty2/unique2 |
| 原子条件UPDATE | stock>=qty与扣qty同一SQL；12请求qty1，committed_success5，stock0/orders5/qty5/unique5 |
| FOR UPDATE修复 | 当前读/检查/扣减/订单同Tx；12请求qty1，成功5，stock0/orders5/qty5/unique5 |
| version CAS修复 | 首轮两Tx明确读version0；至少1次影响0行、退出旧Tx再重试；最终race本次重试5次，成功5，stock0/orders5/qty5/unique5；次数不作通用保证 |
| 真1062与回滚 | 三方案各选已实际存在订单key；新sku库存1扣后INSERT同key真实1062，回滚后新sku依然stock1/orders0/qty0 |
| qty数量/输入边界 | 初始3件买2后stock1/orders1/qty2；再买2 ErrNoStock/0行；qty0在Begin前拒绝；独立审计仍1/1/2/unique1 |
| RC普通/锁定读 | A初次10，B独立Tx提交20，A普通20/锁定20/再普通20；实际session READ-COMMITTED |
| RR普通/锁定读 | A初次10，B提交20，A普通10/锁定20/再普通10；实际session REPEATABLE-READ；计划PRIMARY/const |
| RR范围/gap条件 | 初始PK10/20/30，RR `[10,20)` FOR UPDATE、PRIMARY/range，另Conn INSERT15 PROCESSLIST命中且真实1205；holder未释放时probe Finished；id15最终0条 |
| RC相同范围 | holder仍在，INSERT15成功/最终1条；不宣称RC在外键/重复键路径从无gap |
| RR唯一命中 | 已存在PK10等值FOR UPDATE、PRIMARY/const，holder仍在INSERT15成功/最终1条；不外推到缺失键或复合部分键 |
| 无索引扩大范围 | bucket无索引、EXPLAIN access=ALL；RR WHERE bucket1 FOR UPDATE仍使不匹配id30 UPDATE真实1205 |
| 索引修复 | 自有表新增ix_bucket，本实验FORCE INDEX，EXPLAIN ix_bucket/ref；holder仍在时id30 UPDATE成功，独立value1，实际索引ix_bucket/PRIMARY |
| 真实死锁与恢复 | A持30/B持31；先放A并观察自己的实际等待SQL，再放B闭环；真1213/40001，R1当前racevictim connection662/request deadlock-1；显式Rollback后原key新Tx按30→31；两最终订单/qty4/unique2，两SKU都stock3，总初始10−剩6=4，恰一次重试 |
| lock timeout区别 | holder锁40，waiter先扣41再等40，真1205；rollback_on_timeout0，同Tx仍读41=4；应用明确Rollback后独立41=5/orders0；新Tx顺序FOR UPDATE锁40/41成功，均5 |
| 慢外部调用持锁 | 真loopback HTTP `/bad` handler等门；worker已锁50，竞争UPDATE进入实际PROCESSLIST并真1205；放门后坏worker提交，独立stock50=4 |
| preflight修复 | `/fixed` handler仍等门时DB InUse0，竞争UPDATE51实际完成1行；再放HTTP/开短Tx扣一次；独立stock51=3；无条件close按实际接纳账本等待两handler最后defer |
| 工作结果与退出 | 结果已入buffer，wrapper清理门未开，测试owner已实际进入join等待却未返回；开门后Finished才允许返回，工作/清理两原因独立保留 |
| owner提前失败 | 生产scope.close先解除门、取消、Background新预算真实join；原错误/工作错误/fn清理错误均Is可见，cleaned与Finished真实关闭 |
| 清理预算耗尽 | 故意20ms join返回ErrJoin+DeadlineExceeded+原失败，不标joined；放门后新预算真join，未留下任务 |
| panic与清理 | resource fn先登记capture，后资源defer先写Cleanup，最后recover保留原error型panic；owner等Finished，两个原因均可追溯 |
| retry分类 | UNKNOWN/txCleanupFailure优先于同error树中的1213/CAS，1205不被死锁策略重试；辅助合成错误仅用于分类断言，不冒充实库故障 |
| CREATE副作用已发生但caller未确认 | 新实库DDL测试在发送前断言owned_attempts=1，真实CREATE成功后应用边界返回确认错误；独立information_schema查询表为1，精确DROP IF EXISTS后为0。测试表m22_6bb197962b0582ed37355a8e_stock；不是wire响应丢失 |
| 已占用名字不认领 | 同一随机表由准备owner实际创建，blocked store preflight拒绝/清单0；blocked.drop后独立查询仍1；准备owner精确drop后0。仅使用本测试自己的准备表，没有碰他人对象 |
| HTTP提前关闭与handler尾部 | 真loopback只接纳/bad，/fixed从未接纳；handler尾部defer受门控制。Shutdown20ms真实DeadlineExceeded、TCP Close使client退出、Serve Finished后，关闭owner在新Background2秒handler join仍未完成；放门后最终done/owner Finished，原失败+Shutdown失败+尾部失败均Is可见，无ErrJoin/ErrHandlerJoin |
| 最终资源 | 每次实际run/test/race四精确表DROP后逐名remaining0；主DB open/inuse/idle0；HTTP Body/Transport/handler/Serve已owner收尾 |

## 故障与修复保留

主agent先读全部原源码/README/STATUS并独立全量demo、实库race、vet/mod verify通过，随后在提前失败审查中发现三项真实缺口，验收暂挂REWORK。其原四项proof保留在主目录 `.local/course-orchestrator/parent-review/M22-pre-review-r1/`（本agent只读），不能据正常路径通过替代这些错误路径。R1修复如下：

1. 原create只在CREATE返回成功后登记表名，确认未知时会漏归属。现在精确preflight确认不存在后、实际CREATE之前登记owned尝试，drop精确DROP IF EXISTS并逐名查询不存在。新增有界实库测试先证明真正CREATE副作用，再恢复到不存在，并验证已占用名字不能被清理。注入发生在应用执行边界，不伪称真实wire丢响应。
2. 原slowService.close在Shutdown预算耗尽后复用其ctx做Serve join，且handler等待仅在externalCase正常末尾。现在接纳前立即记record，close无条件seal并快照实际接纳项，解除门后执行Shutdown/Close，再分别用独立Background预算join Serve和实际handler最后defer；不等从未接纳/fixed。新增真实HTTP尾部控制门证明强制TCP关闭/client完成/Serve返回均不足以让close返回。测试没有sleep猜调度，原失败、Shutdown DeadlineExceeded与尾部清理失败全部保留；证明仅覆盖有限fixture应用owner。
3. README原两个PowerShell finally保存null再赋回会改变“不存在”状态。现在保存Exists与Value：不存在Remove-Item Env，存在精确Set-Item。普通与race两个版本在不存在/非空/空串各实测一次，六项结果均True，race整轮恢复CC/CXX/CGO_ENABLED/PATH/GOLEARN_INTEGRATION也全部True。

R1初次默认断言已经通过；最后加强测试自己提前失败的兜底：先开所有自有门、用新预算join唯一closing owner，owner真正完成后再补close/client join，避免两个caller并发操作task.join状态。完整race与最终默认测试使用该最后源码，不拿首轮R1默认证明替代。R1没有编译或意外实库失败，所有刻意确认错误/Shutdown超时/尾部失败均在断言中保留并最终清理。

本轮没有应用编译失败或意外实库失败；预期真实1062、1213、1205都保留原driver错误和实际业务后果。它们在正常入口是明确故障case，由固定预期与独立SQL验证，不用仅含预期错误的error树掩盖其他资源失败。

首次全量入口虽然业务/资源断言通过，snapshot计划在seed之前取得，日志实际为 `"message":"no matching row in const table"`。自审确认它不足以支持索引说明，保留 `.local/run-first.txt`，把EXPLAIN移动到真实行存在后，并新增JSON解析断言PRIMARY/const、PRIMARY/range、ALL、ix_bucket/ref。后续普通run/实库test/race均重新通过；不把初次空表计划写成有效索引证据。

继续自审发现重试/预期1062分支若只匹配原因可能掩盖Rollback失败，新增txCleanupFailure分类，UNKNOWN/清理失败优先停止；拥有资源的fn先登记captureTaskPanic，确保panic后资源defer先把cleanup错误存具名结果，原error型panic原因仍可追溯。五辅助测试与最终全量实库race覆盖当前契约。最后明确qty参数并补实库qty2/非法qty0对照，不只讲stock>=qty公式；最终race与核心入口重新通过。

## 文件职责、所有权与边界

非私有交付共15个文件：README/STATUS/.gitignore/go.mod/go.sum，10份Go源码。README给15分钟核心路径、完整调用链、六组实验时间线/正常与错误/修复、Java类比差异与未验边界。源码文件职责见README的表；每个非平凡手写函数旁解释caller、参数/结果、资源owner及关键等待/错误/退出状态。R1仅修改course.go/external.go/README/STATUS，并新增rework_test.go，无共享修改。

事务owner是transact或各holder实验函数；session归caller/worker；scope在任务创建后立刻登记所有权。scope退出先开自有门，再cancel、独立8秒预算join尚未完成任务；work/cleanup/join与owner原错误Join保留。result容量1仅交付数据，finished最后关闭；共享字段只在finished同步后读。lockProbe手动Rows读完Close，Tx锁仍存在到Rollback。

HTTP是真loopback动态端口，每路径最多一次门控请求。externalCase在所有提前失败也先通过scope解除门/join client，再slowService.close无条件封接纳并快照实际record、解除门。Shutdown/Close、Serve join、handler join各有自己的新Background预算；实际接纳record的done只能在最后defer关闭，尾部清理写入cleanup必须更早完成。client结果、TCP Close与Serve Finished不替代handler done，未接纳路径不产生待等record。request负责完整Body与Close，HTTP语义body不代替SQL审计。此有限fixture不声明join任意标准库内部goroutine。

所有主实验实跑命名空间都四表精确不存在，DB全0；新增DDL独占测试表也逐步查询1→0/1→0并最终不存在。每次CREATE前确认精确名字不存在，实际发CREATE前登记本次尝试，未知确认/部分创建失败也会精确DROP IF EXISTS清理；已占用名字绝不登记。没有prefix broad delete、建库、全局SET、general_log、启用performance_schema、KILL连接、服务stop/start/安装、隧道控制、DeepHelp活动/退役数据变化。主agent继续拥有共享实例；本课没有后台实验进程/HTTP server/worker。

忽略 `.local/` 保留无秘密原始日志：run-first.txt、run-final.txt、integration-final.txt、race-final.txt、unit-first.txt、unit-final.txt、run-final-qty.txt、race-final-qty.txt、core-final.txt；R1新增rework-r1-unit-first.txt、rework-r1-demo.txt、rework-r1-race.txt、rework-r1-unit-final.txt、rework-r1-env-restore.txt。凭据仍在安装者私有文件，没有复制或生成二进制。最终Go/source变更范围仅本课；状态重新冻结为READY_FOR_REVIEW。

未验证生产吞吐/无限并发、多节点复制/重启/binlog耐久性、rollback_on_timeout=ON、外键/所有复合或缺失索引形态、Serializable/Read Uncommitted、purge压力/磁盘增长、网络分区全集/提交响应未知再测、真实MySQL CREATE wire响应丢失、任意net/http内部goroutine join、公司旧Go/driver/其他OS、生产TLS、真实发货/扣款/跨系统原子协议。performance_schema初始按安装者OFF约定，不在本课启用或读取data_locks；锁结论来自实际SQL/计划/自有PROCESSLIST、固定holder与真实错误/持久事实。M21未知提交实证和基础设施安装/持久性证据复用已验收记录，不重复停共享服务。

R1主审建议先看course.go的preflight/发送前attempt/drop恢复，external.go的admit/seal/closeWith三阶段预算与实际handler最后done，rework_test.go的真实副作用/占用名保护及强制关闭尾部屏障，README两个精确env finally。原stock/isolation/deadlock/owner业务证明也已当前源码全量重跑通过。等待独立验收，未自报ACCEPTED、未提交。
