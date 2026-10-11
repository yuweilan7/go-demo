# M23 实施与真实验证记录

状态：**READY_FOR_REVIEW**。2026-10-12，课程实现、详细中文README/源码注释、真实MySQL/AMQP演示、完整integration race和六个辅助独立断言均已完成。最终源码与文档冻结，等待主agent独立主审；本课不自报ACCEPTED，没有待补关键实验，没有extra exercise或下一模块工作。

2026-10-12 主审文档返工已完成：README 普通 integration 示例的 finally 改为原变量不存在时 `Remove-Item Env:GOLEARN_INTEGRATION`、原有值时按原值恢复。本进程分别检验原不存在和原有合成值两种情况，均精确还原，轻量检查退出码0；私有记录为 `.local/rework-env-restore.json`。返工前后13份Go、go.mod、go.sum、race脚本共16个运行文件SHA256逐一相同，changed=0。只修改README和本记录，没有重跑实库；主agent已独立报告真实demo28.201秒、完整integration race29.033秒及vet/mod verify均退出码0，继续复用该主审证据。文档修复后再次冻结，状态恢复READY_FOR_REVIEW。

唯一写入目录M：`C:/Users/Lenovo/.codex/worktrees/go-course-m23/go-demo/lessons/m23_gorm_semantics/`。worktree根W：`C:/Users/Lenovo/.codex/worktrees/go-course-m23/go-demo`；基线SHA为 `003854afb2e9b848164922a4ca5eb835a584de38`。命令workdir明确指定W/M；最终运行的go -C明确绝对M。没有修改共享根/其他模块/files/go.work/调度索引，没有子agent、Git add/commit/push、切分支、安装/服务stop/start、全局设置或新隧道。

## 版本与前置

| 核对项目 | 实际值/范围 |
| --- | --- |
| Go | `go version go1.27.0 windows/amd64`；GOTOOLCHAIN=auto，CGO_ENABLED=1 |
| module | `go-demo/lessons/m23_gorm_semantics`，go=1.27.0，无toolchain/replace/go.work；GOMOD明确M/go.mod，GOWORK空 |
| GORM/dialector | `gorm.io/gorm v1.31.2`、`gorm.io/driver/mysql v1.6.0`，通过go list/version源码核对 |
| driver/AMQP | `github.com/go-sql-driver/mysql v1.10.1`、`github.com/rabbitmq/amqp091-go v1.15.0` |
| 本module间接依赖 | edwards25519 v1.2.0、inflection v1.0.0、now v1.1.5、x/text v0.20.0；go.sum已生成，mod verify通过 |
| 实库 | READY `127.0.0.1:13306`，SQL VERSION()/DATABASE()实际为8.4.11/golearn_m20 |
| broker | READY安装索引RabbitMQ4.3.6，`127.0.0.1:15673`，限定既有vhost `/golearn_m28`；本课实际握手/confirm/Get/Ack/delete/passive协议通过；不重新安装/重启 |
| race编译器 | 只读安装者toolchain.json中WinLibs16.2.0 amd64编译器；实际-dumpmachine=x86_64-w64-mingw32 |
| 资源预算 | 一个SQL池MaxOpen4/Idle1；holder+worker+observer最多3；AMQP一个连接，owner绝对75秒上限及独立8秒清理预算；Run80秒，测试120秒 |

读过M23完整prompt、TEACHING_STYLE、ORCHESTRATION、INFRASTRUCTURE、根README/go.mod；检索适用祖先/模块AGENTS，未发现本仓库额外规则。主目录files/开发框架只读启发，没有复制整批材料。

硬前置M21的README（包括末尾资源/源码/race部分）、STATUS、txcourse/transaction.go/config.go/store.go实际契约已读取。M21已经前层主审集成在本基线；它的STATUS保留实施当时READY_FOR_REVIEW记录。本课迁移库存5/扣1/订单1、同Tx/error/panic/查账/取消清理契约，不修改、不复制整套前置、不重跑所有前置。

官方网页和锁定本机源码重新核对：GORM transactions/update/method_chaining/hooks/preload/delete/context/the_generics_way；v1.31.2 gorm.go/finisher_api.go/generics.go/callbacks.go与相关callback；dialector v1.6.0；AMQP v1.15.0 channel/confirms/connection；Go1.27.0 database/sql契约与spec。当前官网部分泛型Update示例只接error，实际v1.31.2接口/实现为(int,error)，课程按源码纠正。上游module依赖图包含GORM自己的SQLite/工具测试依赖，课程未使用SQLite实库/驱动替代MySQL。

## 实际命令与结果

下表M/W是阅读缩写，工具命令的workdir和最终go -C均明确写实际路径。所有实际日志在M/.local，未纳入Git。

| 命令（工作目录） | 退出码与观察 |
| --- | --- |
| `go version`、`go env ...`（W/M） | exit0，版本/模块隔离如上 |
| `go list -m -versions gorm.io/gorm`、dialector（W，只读） | exit0，选择实际发布v1.31.2/v1.6.0，未凭记忆写API |
| `go -C M mod download/mod tidy`（M） | exit0，锁定直接/间接依赖，生成本课go.sum |
| 初始及最终 `gofmt -w cmd gormcourse`（M） | exit0，13份手写Go文件；最终gofmt -l无输出 |
| 默认 `go -C M test -v -count=1 -timeout 30s ./...`（M，初版） | exit0，五辅助PASS；实库明确SKIP，不算实库PASS |
| 第一次 `go -C M run ./cmd/demo`（M） | **历史exit1**，RabbitMQ4.3拒绝transient非exclusive队列541，附probe关闭504；该run五表精确DROP remaining0，pool全0 |
| 兼容队列后的 `go -C M run ./cmd/demo`（M） | exit0，E01～E06全部真实验证；m23_5976b216e6dae94ec438b865队列passive404、五表remaining0、pool全0 |
| 首轮脚本integration race（M） | Go测试exit0，TestRealMySQLAndAMQP31.16s/package33.471s，无DATA RACE；m23_d099feab8f8c5865735d0124对象清理通过；当时脚本环境恢复检查False，下方明确修复，不冒充精确恢复成功 |
| 环境恢复修复后的脚本integration race（M，netbudget增强前） | exit0，TestRealMySQLAndAMQP26.77s/package27.978s，无DATA RACE；m23_da4d6a98689fed0ca6bd2693对象清理；process_environment_restored=True |
| boundedConn增强后的首轮完整race（M，测试观察者退出增强前） | exit0，TestRealMySQLAndAMQP31.40s/package33.711s，无DATA RACE；m23_52100eb837711e397cbafda9对象精确清理；环境恢复True；报告race-before-observerjoin保留 |
| **最终源码默认** `go -C M test -v -count=1 -timeout 30s ./...`（M） | **exit0**，六辅助PASS（含net.Pipe两个子case），package1.101s；真实integration明确SKIP |
| **最终源码** `go -C M run ./cmd/demo`（M，boundedConn增强后） | **exit0**；m23_bef9bc4fe65edd64674320b4；真实SQL/AMQP/行锁deadline/服务器退出/精确清理全部通过；完整run-final.txt |
| **最终完整实库race** `scripts/verify-race.ps1` → `GOLEARN_INTEGRATION=1` + `go -C M test -race -v -count=1 -timeout 120s ./...`（M） | **exit0**；TestRealMySQLAndAMQP31.35s/package33.766s，六辅助PASS，无DATA RACE；m23_1fef4b609b6d14fb2aba271d精确清理；process_environment_restored=True |
| **最终** `go -C M vet ./...`、`go -C M mod verify`（M） | **exit0**，无vet诊断，all modules verified |
| `git -C W check-ignore -v M/.local/... M/.secrets/...` | exit0，分别匹配本模块.gitignore；没有复制凭据 |
| 内存读取两个password，对本模块交付和.local日志检查精确匹配 | exit0，credential_exact_match_count=0；没有打印密码正文 |
| `git -C W status --short --untracked-files=all` | 只有本课路径未跟踪；没有其他路径修改、add/commit/push |

普通无race的显式integration命令作为README复跑入口提供，本轮最终实库验收使用带integration=1的race完整执行，不把默认SKIP或早期历史结果当最终新增boundedConn证明。

## 实际SQL/外部副作用

| 场景 | 固定预期与实际读回 |
| --- | --- |
| 参数化SQL | 实际DryRun SQL含?，Vars=[false 0 zero alice]；随后同条件真UPDATE+独立SELECT；恶意owner辅助断言只在Vars |
| 零值漏写 | Updates全零struct不发UPDATE/Rows0，SELECT qty9/active=true；map修复0/false；Select修复0/false |
| 行数解释 | 不含UpdatedAt，ClientFoundRows=false；同值Update Error=nil/Rows0，记录仍存在；条件扣减严格要求Rows1 |
| 全表保护 | 缺WHERE返回ErrMissingWhereClause，bob库存未改；WHERE1=1真实影响本run3行，证明有条件仍可能全表 |
| 漏归属 | alice路径只按SKU真实修改bob记录；补owned scope后Rows0，bob保持故障后的6/true |
| 空结果 | First=ErrRecordNotFound/Rows0；Find=nil/Rows0/empty slice；generics First也返回真实error |
| 链污染 | 传统base复用产生quantity1 AND quantity2，结果1/0；Session修复1/1；NewDB清掉owner后得到5条 |
| 泛型 | v1.31.2复用分支1/1；Update/Updates接rows/error并验1；Select零值真实写入chain2=0/false |
| manual/gorm正常 | 均真实数量4/订单1；manual普通error和panic均5/0，原失败保留 |
| callback外层DB | root订单在另一连接先提交，callback失败后库存5/订单1；同tx修复5/0 |
| 吞error | 真MySQL1062被吞后callback nil，库存4/旧订单1；传播1062修复库存5/旧订单1 |
| panic内层恢复成功 | 具名err仍nil，Transaction提交库存4/订单0；外层恢复修复先回滚库存5/订单0 |
| 默认两单写 | 第一步默认事务已提交，第二步1062失败，库存4/旧订单1；没有自动整段业务事务 |
| 软删除 | Delete实际UPDATE deleted_at；默认0行、物理COUNT1、标记非NULL1、Unscoped1 |
| 关联N+1 | 三订单逐条子查：真实SELECT4；Preload2；父owner未传播给子时Line4，补子owner后Line3/SELECT2 |
| 分页 | COUNT3；唯一ID排序page1 assoc01/assoc02，page2 assoc03；无重叠且非法请求SQL前拒绝 |
| 危险hook | AfterCreate真实*sql.Tx；auditSQL未提交；AMQP confirm Ack后callback ErrBusiness；库存5/订单0/audit0/outbox0，但Get真实投递hook-unsafe/body一致，Ack后basic.get-empty |
| outbox回滚修复 | 库存5/订单0/audit0/outbox0，broker真实basic.get-empty |
| outbox成功修复 | Commit后库存4/订单1/audit1/outbox1但queue仍空；relay随后confirm/Get/Ack，Sent真实SQL=true |
| context真实等待 | holder FOR UPDATE，workerINSERT未提交；PROCESSLIST INFO含自有表/m23_cancel_lock；deadline后工作DeadlineExceeded且CleanupErr=nil；Done真join后放holder，原服务器ID消失；数量5/订单0/InUse0 |
| 本轮最终connectionID | 最终demo395、最终race480；ID/耗时只是本次观察，非调度保证 |
| 任务辅助 | Result到达不等于Done；join到等待点后清理门未开不能返回；放门后完整错误树；预算失败保留原错误/ErrJoin；早退cancel后真join |
| I/O预算辅助 | 真net.Pipe的owner期限已过后，库式clear/heartbeat延长均仍Read deadline错误；没有sleep猜网络状态 |

所有库日志是GORM Explain后的合成SQL显示，不宣称packet抓包；真实执行传SQL/Vars给driver，独立database/sql查账确认行内容。Preload与hook可能早于父操作Trace显示，因为Trace在完整callbacks后执行，README解释了显示顺序与实际发送/副作用顺序的区别。

## 保留的失败与修复

首run `m23_0a860573c9ae44ca337a8fde` 真实错误：

```text
Exception (541) Reason: INTERNAL_ERROR - Feature `transient_nonexcl_queues` is deprecated.
By default, this feature is not permitted anymore.
Exception (504) Reason: channel/connection is not open
```

RabbitMQ4.3会在transient非exclusive声明（包括本课原探测flags）拒绝。修复QueueDeclare/Passive使用durable=true；先确认不存在再仅创建随机名字，结束精确删除。probe协议错误关闭后不再重复Close制造504。没有改feature全局开关、停止/重装broker。该失败尚未创建本run队列，五张已建自有表全部DROP remaining0，pool全0；原日志保留run-first.txt。

第一轮race的业务测试通过，但脚本 `SetEnvironmentVariable(name,null)` 在本机Windows/.NET变成存在的空串，精确恢复核对False。实际诊断只输出Name/null/length/equality，不暴露值。修复：原先null的变量用Env provider精确Remove；非null按原值恢复，finally再逐项比较，不相等则脚本失败。修复后的所有race均True，未执行全局go env/PATH改动。历史race-first/race-first-script保留，不能把首轮环境宣称已精确恢复。

自审发现AMQP库 `connection.go:openComplete` 会清deadline、heartbeater会延长read deadline，原一次socket.SetDeadline无法保障绝对上限。补boundedConn只装饰真实socket的deadline方法，所有Read/Write仍真实协议；库的clear/extend截到owner上限，owner可设新清理预算。新增真实net.Pipe断言后，最终demo/完整integration race/vet全部重跑通过。不是简单改文案回避无界等待。

## 资源清理与未验范围

每个成功run精确五表和一个queue：前置information_schema五名不存在、AMQP passive404；owned后立刻登记清理。默认交换机是已有对象，不创建/删除exchange/group；最多两条合成记录，无后台consumer。最终demo/run、三个历史race及最终race均输出exact_queue/passive404、五表remaining0、pool open/inuse/idle全部0。首失败run仅创建五表，亦remaining0。

正常及提前失败路径都释放holder、cancel并独立Background预算join worker，错误与清理原因Join；Result不当Done，wrapper最后close Done。工作Conn/Tx、observe ticker/context由创建函数收尾；GORM Rows内部读取关闭、QueryRow.Scan自动关闭行。AMQP connection.Close释放库管理的连接任务，socket上限保证失败RPC有界。没有遗留本课HTTP服务、listener、代理/worker进程；没有改课程实例/安装者隧道或他人对象。

最终13份手写Go文件，连README/STATUS/.gitignore/go.mod/go.sum/race脚本共19个非私有交付文件。报告只在忽略.local：run-first、run-second、run-final、race-first、race-before-netbudget、race-before-observerjoin、race-final与各脚本记录；没有二进制、秘密文件、复制的原材料或公共全局索引。

未验：生产负载/权限体系、公司旧Go/GORM/driver、其他OS、MQ/DB服务重启与硬断电耐久性、生产TLS、多节点broker、所有通信故障、MQ响应丢失的恢复、完整后台outbox relay幂等/恢复和分布式事务。Commit UNKNOWN边界沿M21已交付实证解释，不在本课重复代理/停服务。RabbitMQ durable队列定义与当前confirm不宣称消息重启耐久性。安装/容器资源/重启持久性/隧道归属复用主agent既有READY验收，本课只消费并精确清理自己对象。

主审建议先沿 `transactions.go` 的三种callback危险副作用、`models.go/hidden.go/broker.go` 的未提交真实MQ与outbox修复、`cancel.go/task.go` 的真实等待/Done/服务器结束、`netbudget.go` 的库deadline覆盖边界检查，再按README运行本module。状态保留READY_FOR_REVIEW，原agent等待主审返工指令。
