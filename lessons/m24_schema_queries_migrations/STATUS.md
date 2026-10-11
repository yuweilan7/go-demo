# M24 交付与真实验证状态

日期：2026-10-12，Asia/Shanghai。起点 `5929af4af6d8a599362bdbb20e2d2d3cfa2cf6ba`。本次唯一写入 `C:/Users/Lenovo/.codex/worktrees/go-course-m24/go-demo/lessons/m24_schema_queries_migrations/`；所有命令工具显式workdir为该worktree根，Go命令进一步用 `-C lessons/m24_schema_queries_migrations`。未切分支、未add/commit/push、未写根go.mod/go.sum/go.work、共享索引、其他课程或files。**READY_FOR_REVIEW：已冻结，等待主agent独立验收。**

## 交付与前置

- 独立module：`go-demo/lessons/m24_schema_queries_migrations`，go=`1.27.0`，无toolchain/replace/go.work。
- 当前工具链：Go1.27.0 windows/amd64，GOROOT=`D:/Go`，GOMOD是本课文件，GOWORK空，GOTOOLCHAIN=auto，CGO_ENABLED=1。实际下载并锁定mysql driver v1.10.1、edwards25519 v1.2.0，go.sum存在，mod verify通过。
- 已读：适用AGENTS（`.codex/AGENTS.md`为空，无额外指令）、根README/go.mod、完整M24 prompt、TEACHING_STYLE、ORCHESTRATION、INFRASTRUCTURE、learning_prompts/modules.json的M24/M20依赖与层约定。M20 STATUS实际已验收，Config/Run/values/money/task接口已核对；不重复全跑M20。
- `D:/IdeaProject/go-demo/files`只读检索/阅读了开发框架MySQL CRUD方向与Go编码规范循环Rows/defer/注入提醒；未复制整批材料。
- 已浏览官方Go1.27、database/sql、mysql driver、MySQL8.4类型/联合索引/EXPLAIN/类型转换/LIMIT/隐式提交/在线DDL锁文档；读取本机Go1.27 sql.go的Tx.awaitDone/Rows.Close与真实driver v1.10.1 Config源码。README有按证明点组织的官方链接和旧材料修正。
- 交付README、cmd/demo入口、internal/course八个职责文件（config/lesson/values/plans/pages/task/migrations/course_test）、必要独立断言、go.mod/go.sum与本状态，共14个非私有文件。核心15～20分钟按四段输出讲清，无额外exercise。中文README给出入口/调用链、真实时间线、SQL/计划、错误/修复/业务影响与取消/清理完整解析；手写源码均有贴近当前对象/等待/所有权的中文说明。

## 当前基础设施与隔离

只读安装者索引 `D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json` 的services.mysql：READY、MySQL8.4.11、127.0.0.1:13306、owner=/root、learning_root=/srv/go-demo-learning、container=golearn-course-mysql、persistence_restart_verified=true。凭据只从credential_file加载username/password/database，不打印/复制正文。授权学习库golearn_m20；不建库、不改全局权限、索引或别课表，不读写DeepHelp正式/退役库。

安装/持久性/锁/资源证据复用主agent与M20已经验收的独立实例成果：max_connections32，课程实例512MiB/1CPU，独立named volume，日志5MiB×2，volume无硬磁盘quota。M24未自行安装、stop/start、建隧道或做全库管理；这些事实不是本课另跑安装验收的声明。

每Run随机24hex前缀，七张精确独占表；CREATE前SELECT不存在，不用IF NOT EXISTS认领对象；1050已有表拒绝认领。主池MaxOpen4/MaxIdle2，单阶段最多holder/worker/observer三个连接。订单1203行（3条值例+1200种子），types5行，pages最多7行，evolve4行、versions3行、audit1行，数据有界。种子12批×100行。总ctx90秒、DDL会话lock_wait_timeout2秒、观察1.5秒、task清理join12秒、表清理新ctx15秒；没有sleep猜状态或全局timeout变更。

## 实际命令与结果

以下W均为上述worktree根；命令明确在W执行。最新相关源码在最终run/race前已gofmt。

| 命令 | 实际结果/范围 |
| --- | --- |
| `go version` / `go env GOVERSION GOOS GOARCH GOTOOLCHAIN CGO_ENABLED GOPROXY` | Go1.27.0 windows/amd64，auto、1，GOPROXY为https://goproxy.cn,direct；未更改全局Go环境 |
| `go -C lessons/m24_schema_queries_migrations mod tidy` | 退出0，真实依赖锁定如上 |
| `gofmt -w lessons/m24_schema_queries_migrations/cmd lessons/m24_schema_queries_migrations/internal` | 退出0，交付源码格式化 |
| `go -C lessons/m24_schema_queries_migrations test -v ./...` | 退出0；初次3项辅助测试PASS，TestMySQL明确SKIP；最终默认测试再核对新增取消集成也SKIP |
| 第一版 `go -C lessons/m24_schema_queries_migrations run ./cmd/demo` | **历史失败**：逐行1200次INSERT跨机往返耗尽90秒总预算，context deadline exceeded；已建三表DROP读回0，DB open/inuse/idle0 |
| 第二版同入口 | **历史失败**：实际10列误写断言11列，查询形状断言失败；已建三表DROP读回0，DB0；改为正确10列并增加实际wide结果/4000字节断言 |
| 修复后同入口 | 退出0，四组完整真实实验PASS；namespace=`m24_da954aa24c3add05fbbedb8d`，七表remaining0、DB0 |
| `GOLEARN_INTEGRATION=1`（进程临时）后 `go -C lessons/m24_schema_queries_migrations test -v -count=1 -timeout 120s ./...` | 退出0；三项辅助测试/完整TestMySQL/真实TestMySQLCanceledOwner全部PASS；完整21.19s，取消4.04s，package25.961s；finally恢复环境 |
| `go -C lessons/m24_schema_queries_migrations env GOMOD GOWORK GOVERSION GOROOT GOMODCACHE CC CXX CGO_ENABLED` | 本课GOMOD、GOWORK空、Go1.27.0、D:/Go、依赖缓存C:/Users/Lenovo/go/pkg/mod、gcc/g++/1 |
| `go -C lessons/m24_schema_queries_migrations vet ./...` / `mod verify` | 退出0，无vet诊断；all modules verified |
| 最终 `go -C lessons/m24_schema_queries_migrations run ./cmd/demo` | **退出0**，新增计划断言/非覆盖ANALYZE/真实wide结果/缺账恢复均已执行；namespace=`m24_c7d487fb3e903e58596f2ed4`，DDL worker连接173，七表remaining0、DB0 |
| process-local CC/CXX/CGO_ENABLED/PATH/GOLEARN_INTEGRATION后 `go -C lessons/m24_schema_queries_migrations test -race -v -count=1 -timeout 120s ./...` | **退出0**，三项辅助/两个真实integration全部PASS，无race诊断；完整21.09s、取消4.09s、package27.077s。完整namespace=`m24_0ba7dd65b67eb42dda9703d2`、取消namespace=`m24_3dc228f238a560ecca017e9f`；DDL worker176；全部目标remaining0、DB0。编译器只读既有toolchain.json，不安装/改全局配置 |
| race finally后的 `go -C lessons/m24_schema_queries_migrations env CC CXX CGO_ENABLED` | 恢复gcc/g++/1；原环境在finally恢复，未写全局 |
| 最终默认 `test -v ./...`、`vet ./...`、`mod verify`、`gofmt -l` | 退出0；三项辅助PASS、两个真实integration明确SKIP（实库已经由上方race执行）；vet无诊断，all modules verified，gofmt无输出 |
| 最终仅内存加载密码，对本课14文件与忽略日志作字符串核对 | 退出0，credential_password_match_count=0，不输出密码正文 |
| `git check-ignore -v` / `git diff --check` / `git status --short` / `git rev-parse HEAD` | 退出0；私有日志全部匹配本课.local忽略，只有本课未跟踪目录，HEAD仍基线5929af4；diff --check对已跟踪范围无诊断，不把它冒充未跟踪内容的全量检查 |

无秘密日志在本课被忽略`.local/run-current.txt`、`run-final.txt`、`integration-current.txt`、`race-final.txt`；失败精确消息、namespace与清理摘录保存在`.local/failures.txt`。这些私有日志不纳入提交。CLI捕获过程中pipeline后的shell状态与实际Go结果区分：日志/工具输出明确记录run_exit/integration_exit/race_exit，失败1不会被写成通过。

## 独立语义与资源证明

| 证明点 | 真实观察与对应断言 |
| --- | --- |
| NULL/default/空串 | 缺note为Validtrue/未填写，显式NULL为Validfalse，空串为Validtrue/空；三行shipped.Validfalse |
| ID/DECIMAL/时间 | BIGINT9007199254740993准确；DECIMAL16777217.03→1677721703分；UTC微秒2024-02-29T23:59:59.123456Z准确 |
| 错误类型 | FLOAT文本返回16777200.00、库内CAST16777218.00，DOUBLE-ID9007199254740992；INT越界1264后1行；VARCHAR超长1406后原4行；DECIMAL1.005写入错误nil但同Conn警告1265、SELECT1.01 |
| 字符/比较 | 小明🛍️完整读回；ai_ci的A=a为1，bin为0；Req-A与req-a实际共存 |
| 业务约束 | duplicate1062后订单3行、原金额不变；外键1452/CHECK3819/NOT NULL1048后仍3行 |
| 索引计划 | 覆盖feed为ref/idx_feed/Using index无filesort；缺tenant/列函数为index扫描+filesort；错误排序为filesort；SELECT*不覆盖；文本=文本ref、文本=数字index |
| 计划与实际结果 | conversion文本比较1行、数字比较3行；ANALYZE真的执行覆盖SELECT返回10行、非覆盖SELECT返回20行；估计行数采样可变，不断言精确成本或毫秒 |
| 查询形状 | SELECT*10列/投影2列；无界真实遍历1203行、页面10；N+1=11次/Join1次且姓名/顺序一致；非覆盖20行真实unused payload4000字节 |
| tie/插入/删除 | 第一页[6 5 4]；时间-only[2 1]漏3，tuple[3 2 1]；插入后offset[4 3 2]重复4，keyset[3 2 1]；删除头部后offset[2 1]漏3、keyset仍[3 2 1] |
| 可变排序/修复 | keyset在时间更新后[2 1 5]重5漏3；同一RR事务两页仍[6 5 4]/[3 2 1]，结束后新读[3 6 4]证明外部更新已commit |
| 兼容演进 | v2列nullable，旧单写/新双写及fallback读兼容；第一次回填2行，旧写产生NULL1，收紧1138后nullableYES/ledger2；门禁无DDL；第二次回填1行后contract数据4/ledger3/新读原值/旧SQL1054 |
| 重复/恢复 | 同run重复expand/contract无多余DDL；真实构造“列在/ledger2不在”恢复状态，expand读结构补账；没有声称杀进程或多个迁移者互斥验收 |
| 隐式提交 | UPDATE20→成功ADD→Rollbacknil→SELECT20；UPDATE30→失败ADD1060→Rollbacknil→SELECT30；stamp列始终存在 |
| 实际DDL锁 | 连接ID定向PROCESSLIST观察Waiting for table metadata lock；会话2秒后1205，finished后holder释放，lock_probe不存在、value30、InUse0 |
| Result与Finished | 独立cleanup gate测试：result缓冲1时finished不能关闭；释放gate后join保留work/cleanup两个错误链 |
| 主取消清理 | 可观察事件在Explain发出前取消，Run识别Canceled，三张实际表用新cleanup预算DROP/SELECT0、DB0，不是mock |
| 最终清理 | 所有Rows/Tx/Conn属于局部owner、worker被finished+join等待；七表精确逆序DROP/SELECTremaining0；Run最后DB.Close后open/inuse/idle0 |

## 失败与修复的实际界限

首版逐行准备数据是实现预算错误，不是MySQL不可用：第一段真实读写已经成功，进入1200行阶段才到期。修复为12次参数化批量INSERT，总数据量仍1200，不扩大到无界导入。第二版断言11列是对schema字段数的误写，修复10列并保留独立结果计数；没有修改业务结果迎合断言。两次失败清理均读回目标不存在，未留给共享实例管理者收拾。

辅助测试验证本课解析/标识符/任务协议；完整实库验证真实driver/MySQL副作用。虽然README把核心讲解划在15～20分钟，机器实际耗时只是本次记录，不是保证任何网络/负载下都一样快。EXPLAIN估计行数、cost与actual time不写死为永久事实。

## 未验范围

没有关键M24 prompt实验缺口。未声称生产在线DDL工具、备份恢复、分片/复制、跨节点宕机、Commit响应丢失、迁移多owner锁/完整签名校验、无限滚动跨请求快照、公司旧Go/旧驱动、生产容量/磁盘读取次数已验。本课缺账恢复是构造准确数据库状态后重进迁移，不是实际进程crash证明；取消是在SQL发出前触发，不冒充正在执行SQL的网络故障。

Race工具链复用 `D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json`：WinLibs16.2.0posix-14.0.0-ucrt-r2、GCC16.2.0、mingw-w64 14.0.0，只在当前进程临时设置，finally恢复CC/CXX/CGO_ENABLED/PATH/GOLEARN_INTEGRATION。本课不修改go env、系统PATH或安装编译器。

本机race复跑的完整进程环境方式（在W运行）：

```powershell
$toolchain = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' | ConvertFrom-Json
$saved = @{ CC=$env:CC; CXX=$env:CXX; CGO_ENABLED=$env:CGO_ENABLED; PATH=$env:PATH; GOLEARN_INTEGRATION=$env:GOLEARN_INTEGRATION }
try {
    $env:CC = $toolchain.cc
    $env:CXX = $toolchain.cxx
    $env:CGO_ENABLED = '1'
    $env:PATH = $toolchain.bin_dir + ';' + $env:PATH
    $env:GOLEARN_INTEGRATION = '1'
    go -C lessons/m24_schema_queries_migrations test -race -v -count=1 -timeout 120s ./...
} finally {
    $env:CC = $saved.CC
    $env:CXX = $saved.CXX
    $env:CGO_ENABLED = $saved.CGO_ENABLED
    $env:PATH = $saved.PATH
    $env:GOLEARN_INTEGRATION = $saved.GOLEARN_INTEGRATION
}
```

收尾确认：本课所有exec会话已结束，没有自建后台进程/代理/隧道、活跃worker或遗留本run表。共享MySQL继续由安装者管理，不作为课程清理目标。源码/README按入口完整自审，STATUS与实际最终run/race和取消日志相符；冻结为READY_FOR_REVIEW，主agent仍需独立复验后才可标ACCEPTED。
