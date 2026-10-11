# M20 交付与真实验证状态

日期：2026-10-12，Asia/Shanghai。执行目录为 `C:/Users/Lenovo/.codex/worktrees/go-course-m20/go-demo`，起点 `90f7381b495c08f4995dd13400fbc34fca69e8c0`。只写 `lessons/m20_sql_pools/`，不提交/推送，不修改根/其他模块/examples/files/go.work。主 agent 是统一基础设施安装者；本执行者不安装、升级、重启、停止服务，不写共享索引。

**主审后的Finished契约修复已完成并重新冻结待验收。** 修复版确定性清理gate测试、正常入口、完整真实integration race、vet和mod verify均有新的退出0证据；下列首版历史记录保留，不充当修复版通过证据。全部实验使用真实MySQL8.4.11，以SELECT、Stats、服务端等待和独立Finished广播验收。

## 当前实际版本与交付

- 当前工具链：`go version go1.27.0 windows/amd64`，GOROOT=`D:/Go`，GOPATH=`C:/Users/Lenovo/go`，GOTOOLCHAIN=`auto`，CGO_ENABLED=`1`。
- 独立 module：`go-demo/lessons/m20_sql_pools`，go=`1.27.0`，无 toolchain/replace/go.work。
- 实际下载锁定：`github.com/go-sql-driver/mysql v1.10.1`，`filippo.io/edwards25519 v1.2.0`；go.sum校验存在。官方发布页与v1.10.1 README、本机标准库与driver源码已读取。
- 已读：适用AGENTS、根README/go.mod、ORCHESTRATION、TEACHING_STYLE、modules.json、M20 prompt、INFRASTRUCTURE、M00 STATUS/README/INSTALLATION_CONTRACT/with-install-lock.sh，以及DeepHelp当前退役MySQL入口证据。files目录只读检索，未发现SQL/MySQL专属材料，未复制整批材料。
- 源码为cmd/demo/main.go与internal/course的config/money/task/lesson/values/pools/sqlwait/course_test。中文README提供完整调用链、六个实验时间线、反例/修复、业务SELECT、Stats解释与资源owner。
- 私有接入：默认主目录`.local/course-orchestrator/infra-services.json`；只读READY索引/另一个credential_file；credential schema username/password/database，预定数据库`golearn_m20`、仅该库权限，每run独占`m20_<24 hex>`表。

## 已执行命令与真实结果

所有命令工具workdir均显式使用上述worktree根。

| 命令 | 实际结果与范围 |
| --- | --- |
| `go version`、`go env GOVERSION GOROOT GOPATH GOOS GOARCH GOTOOLCHAIN CGO_ENABLED` | Go1.27.0/windows/amd64，参数如上 |
| `go -C lessons/m20_sql_pools mod download` / `mod tidy` | 退出0，下载并锁定上述真实driver/间接依赖 |
| `git check-ignore -v lessons/m20_sql_pools/.local/config.json lessons/m20_sql_pools/.secrets/mysql.json` | 退出0，匹配本模块.gitignore；未复制/打印秘密 |
| `gofmt -w lessons/m20_sql_pools/cmd lessons/m20_sql_pools/internal` / `gofmt -l` | 退出0，已格式化；-l无输出 |
| `go -C lessons/m20_sql_pools test -v ./...` | 退出0，金额边界/标识符/提前返回取消join与错误链三项辅助测试PASS；TestMySQL明确SKIP，未请求实库 |
| `go -C lessons/m20_sql_pools vet ./...` | 退出0，无诊断 |
| 进程级读取主目录`toolchain.json`、设置`CC/CXX/CGO_ENABLED=1/PATH`后 `go -C lessons/m20_sql_pools test -race -v -count=1 -timeout 100s ./...` | 退出0，三项辅助测试PASS，2.053s；TestMySQL仍SKIP；finally恢复环境，没有安装/改全局工具链 |
| 准备阶段`go -C lessons/m20_sql_pools run ./cmd/demo` | **历史预期拒绝**退出1：`MySQL 尚未 READY；实库实验保持未验证`。当时索引mysql.state=PENDING；没有发数据库调用 |
| `go -C lessons/m20_sql_pools env GOMOD GOWORK GOVERSION` | GOMOD是本课go.mod，GOWORK空，GOVERSION=go1.27.0 |

## 首版READY后的真实命令与历史结果

所有工具调用workdir均为上述worktree根W，`go -C`进一步进入本课。真实服务/客户端版本：MySQL8.4.11、Go1.27.0、driver v1.10.1。实际SELECT确认数据库golearn_m20、session time_zone=+00:00、max_connections=32。

| 在W执行的命令 | 真实结果 |
| --- | --- |
| READY后`go -C lessons/m20_sql_pools run ./cmd/demo` | **退出0**，全部六组真实范围通过；本run表`m20_2bc14a833a116d3c227b6af2`；DROP SELECT remaining0；DB最终open/inuse/idle均0 |
| 进程环境`GOLEARN_INTEGRATION=1`后`go -C lessons/m20_sql_pools test -v -count=1 -timeout 100s ./...` | **退出0**，三个辅助测试及完整TestMySQL PASS；实库9.75s、package14.165s；表`m20_e9bf67dbac2b2710358112f2`，DROP/DB关闭通过，finally恢复环境 |
| `go -C lessons/m20_sql_pools vet ./...` | **最终退出0**，无诊断 |
| 进程级读主目录toolchain.json，设`CC/CXX/CGO_ENABLED=1/PATH/GOLEARN_INTEGRATION=1`后`go -C lessons/m20_sql_pools test -race -v -count=1 -timeout 100s ./...` | **退出0**，三个辅助测试及完整真实TestMySQL PASS，无race诊断；实库9.82s、package11.823s；表`m20_dc137e107724237eac8314c8`，DROP/DB关闭通过 |
| race finally后`go -C lessons/m20_sql_pools env CC CXX CGO_ENABLED` | 回到gcc/g++/1；没有改系统环境/全局go env，没有重复安装工具链 |
| 最终仅内存加载课程password，对本课全部交付文件与忽略日志做字符串比对（不输出password） | 退出0，credential_password_match_count=0；没有凭据正文泄漏到课程/报告 |
| 最终`git status --short` / `git check-ignore -v`三个当前日志 / `gofmt -l` | 仅本课未跟踪目录；日志匹配本课.local忽略；gofmt无输出；14个非私有交付文件 |

race编译器读取主agent提供的既有WinLibs tag=`16.2.0posix-14.0.0-ucrt-r2`，GCC16.2.0、mingw-w64 14.0.0，路径在主目录`.local/toolchains/`。完整进程级方式可复跑：

```powershell
$toolchain = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' | ConvertFrom-Json
$saved = @{ CC=$env:CC; CXX=$env:CXX; CGO_ENABLED=$env:CGO_ENABLED; PATH=$env:PATH; GOLEARN_INTEGRATION=$env:GOLEARN_INTEGRATION }
try {
    $env:CC=$toolchain.cc
    $env:CXX=$toolchain.cxx
    $env:CGO_ENABLED='1'
    $env:PATH=$toolchain.bin_dir + ';' + $env:PATH
    $env:GOLEARN_INTEGRATION='1'
    go -C lessons/m20_sql_pools test -race -v -count=1 -timeout 100s ./...
} finally {
    $env:CC=$saved.CC
    $env:CXX=$saved.CXX
    $env:CGO_ENABLED=$saved.CGO_ENABLED
    $env:PATH=$saved.PATH
    $env:GOLEARN_INTEGRATION=$saved.GOLEARN_INTEGRATION
}
```

## 首版默认入口的历史证明点

| 实验 | 真实业务/资源观察与断言 |
| --- | --- |
| sql.Open/Ping | Open后OpenConnections=0；Ping后仍继续真实会话SELECT、业务INSERT/SELECT验证，不把Ping当写读完成 |
| 参数/读写 | 注入形状code完整保留，total2/exact-match1；INSERT RowsAffected2；两次UPDATE分别1/0，SELECT都101；真实遍历两行SUM301 |
| NULL/DECIMAL/时间 | id1 Note/Shipped.Valid=false；id2 Note.Valid=true/V空字符串，发货时间有效；99999999.99→9999999999分/0.10→10分；UTC微秒2024-02-29T23:59:59.123456Z精确读回 |
| 结果错误 | id999 Scan=可识别ErrNoRows；NULL→string/文本→int64两个真实Scan转换错误；正常Rows.Err=nil，提前退出有Close |
| Conn | 两次服务端ID17/17，InUse1；owner Close后InUse0 |
| Rows持池 | MaxOpen1；屏障WaitCount1/WaitDuration0/InUse1；timeout后WaitDuration1.5005116s/InUse1；Close后WaitCount2/WaitDuration1.5055595s/InUse0；SELECT101，没有持久901 |
| Tx误用DB | WaitCount从前一实验2到3，delta1；Tx内部读777不再借池；本段WaitDuration增加1.2005096s；Rollback后SELECT101，没有持久777/888 |
| SQL I/O deadline | worker连接ID18在自身PROCESSLIST见标记UPDATE，State本次updating，workerDB InUse1/WaitCount0；3秒工作ctx DeadlineExceeded；Rollback/Conn.Close完成，释放holder后服务端ID消失；SELECT101，没有持久404；新连接SELECT1成功 |
| Rows取消 | 第一行id1后cancel→观察InUse0→Next=false/Rows.Err=Canceled；重新SELECT101，资源/业务恢复 |
| 清理 | 本run DROP后information_schema SELECT remaining0；Run唯一DB owner最后Close，open/inuse/idle均0；integration/race各自独占表也通过同样清理 |

原始无秘密日志仅保存在本课已忽略`.local/`：`run-initial.txt`（首次READY运行内容）、`run-current.txt`（显式记录退出0）、`integration-current.txt`、`race-current.txt`。不提交日志/凭据/二进制。README保留默认运行关键真实输出与逐段解析。

## 安装者的独立实例、锁、资源与持久化证据

本课只消费主agent `/root` 的安装成果。实际只读索引`D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json`登记READY、loopback127.0.0.1:13306、learning_root=/srv/go-demo-learning、container=golearn-course-mysql、persistence_restart_verified=true、installed_receipt=install-mysql-stdout.jsonl。凭据正文没有输出/复制/提交。

- 固定镜像`mysql:8.4.11`，digest=`mysql@sha256:6ea90827b1100f8f2ae306a539f86d2c264a26ed435a2a9f75551dd5c3aeb242`。已读取install-pull-stdout.jsonl的真实image-pinned记录：2026-10-11T18:41:00.837560+00:00。
- 初选`mysql:8.4.12`的Docker Library manifest not found失败已由安装者保留；install-plan.json.mysql_tag_correction记录按官方可拉取8.4标签更正8.4.11，不把8.4.12写成真实运行版本。
- 已读取install-lock-runtime.json：学习根0700，lock_syntax=true，竞争者退出75、正常owner退出0、timeout退出124且子进程已结束、后续owner退出0、owner元数据无残留、same_lock_inode=true。运行核验时间2026-10-11T18:33:56.963658+00:00；固定inode内核flock，不删除活跃锁抢装。
- 安装者提供安装前server-inventory.json摘要：MemAvailable3435MiB、4CPU、磁盘可用28504MiB；学习root初始不存在，旧deephelp-mysql为Exited且没有恢复。安装者计划mysql512MiB/1CPU/max_connections32/innodb128MiB/log5MiB×2，独立named volume=golearn-course-mysql-data。volume没有硬磁盘quota，日志保留与合成数据限制不冒充硬磁盘上限。
- 已读取install-plan.json：MySQLmemory_mib512，三组件内存提案合计1792MiB。并已读取installed-service-resources.json（2026-10-12T02:49:15.653556+08:00）实际Docker HostConfig：memory_bytes=536870912（512MiB）、memory_swap_bytes=536870912、nano_cpus=1000000000（1CPU）、pids_limit=256、json-file日志5m×2；3306/tcp仅绑定127.0.0.1:13306。volume=golearn-course-mysql-data、driver=local，Compose project=golearn-course，owner=root-course-orchestrator/service=mysql的labels已核对；state=running、OOMKilled=false，当前MemAvailable3295MiB。volume_disk_quota明确为none，不把日志保留或本课两行数据说成硬磁盘配额。
- 已读取install-mysql-stdout.jsonl：starting→restarting-owned-service→completed；完成时间2026-10-11T18:42:41.205236+00:00，server_version8.4.11、persistence_restart=true、synth_table_removed=true。安装者另用课程用户/Go mysql v1.10.1做身份SELECT、建表、两行SUM13读回、DROP，本课随后独立执行全部六组实验。
- 独立容器、新volume和课程账号，只在golearn_m20有ALL权限；无全局管理或CREATE DATABASE权限。端口13306现在指向新课程实例，实际container/volume/版本/账号证明归属，没有恢复旧DeepHelp数据卷/密码/容器。

## 范围与未扩大承诺

M20 prompt五项全部覆盖：独立实例/锁/资源/持久化成果由唯一安装者提供；真实driver与DB/Conn/Tx；参数化值映射/错误；池设置与有界耗尽/等待；SELECT独占数据、无行/NULL、取消/恢复、Rows修复和DB owner。所有本run表精确删除，客户端池/worker清理完成，无故障代理；真实服务/隧道由安装者继续管理，不在本课清理。

SQL故障实际先触发context deadline，readTimeout8秒未先触发，不声称完成独立socket ReadTimeout测量。lifetime/idle清扫器没有专门耗时实测，设置依据/契约已说明；Commit响应丢失、生产池容量、其他驱动/公司旧Go/生产数据都不冒充已验。没有关键M20实库缺口，等待主agent独立审查。

## 主审Finished修复与最新复验证据

主审指出首版容量1的outcome接收不能证明wrapper最后defer完成。修复在task.go拆分result和finished：result交付一次值，finished只由wrapper最早注册的defer最后关闭；wrapper附加cleanup、cancel之后才close finished。join先以独立预算等finished，再读result或pending、合并wrapper清理error，最后写joined缓存。即使缓存存在也先核对finished。stopAndJoin不把pending当完成；sqlWait提前result分支只缓存pending，其defer继续新预算join。源码/README同步讲清结果通知和全部清理完成的区别。

新增TestResultReadyStillWaitsForWrapperCleanup：取消工作后先消费result，再用cleanup gate阻住wrapper；通过finishedProbeContext.Done求值的channel屏障证明owner已经进入join选择，结果已可读也不能返回；释放gate后才能Finished关闭/owner返回。断言保留owner主错误、工作Canceled、工作清理错误及wrapper清理错误。测试所有提前失败也放门、取消、独立预算等待worker/owner，未用sleep猜调度。

| 在同一W执行的修复版命令 | 最新真实结果 |
| --- | --- |
| `test -v -run 'TestEarlyReturnCancelsAndJoins\|TestResultReadyStillWaitsForWrapperCleanup' -count=1 -timeout 30s ./...`（前缀`go -C lessons/m20_sql_pools`） | 退出0，两项受影响断言PASS；package0.843s |
| 第一次修复版`go -C lessons/m20_sql_pools run ./cmd/demo` | **保留真实失败**退出1：第二个Rows waiter已使WaitCount2/InUse0，但立即归还连接时第二段WaitDuration=0，累计仍1.5006369s；原先强制第二段正耗时断言过严。表`m20_526fe75fd5a69ae882e0ed48`仍DROP/SELECT remaining0、DB关闭成功，日志run-finished-fix.txt保留 |
| 准确统计契约修正后同一正常入口 | **退出0**，表`m20_b1569e66c691934c90bc86a6`；六组全部通过；RowsWaitCount delta2/WaitDuration1.50523s/InUse0，Tx追加delta1/耗时1.2000649s，SELECT101；SQL连接44服务端UPDATE/updating、WaitCount0，deadline后连接消失/SELECT101/恢复；DROP remaining0、DBopen/inuse/idle均0 |
| 进程级便携CC/CXX/CGO/PATH和`GOLEARN_INTEGRATION=1`后 `go -C lessons/m20_sql_pools test -race -v -count=1 -timeout 100s ./...` | **退出0**，四项辅助测试与完整真实TestMySQL均PASS，无race诊断；实库9.66s、package11.607s；表`m20_7f0ef7130c3b1ae372330cd1`，Conn48/SQL49，真实业务/池/取消/清理全部通过，DROP remaining0、DB全0；finally恢复进程环境 |
| `go -C lessons/m20_sql_pools vet ./...` / `mod verify` | 各退出0，无vet诊断；all modules verified |

第二段允许0不是用延时制造通过：Go1.27先增加WaitCount，再起等待计时；计数屏障后的立即Close可能让耗时记为0。第一次1500ms排队deadline仍要求正耗时，第二次恢复仍精确要求WaitCount delta2、累计不回退、SELECT101和InUse0。修复删除了无库契约保证的严格正耗时要求，并保留真实失败。

最新日志为本课忽略目录`.local/run-finished-fix-current.txt`、`.local/race-finished-fix-current.txt`；首版日志、第一次修复的统计失败与主目录parent-review/M20旧验收记录均保留，未用它们替代上述新运行事实。README输出也改为Finished修复版正常入口。
