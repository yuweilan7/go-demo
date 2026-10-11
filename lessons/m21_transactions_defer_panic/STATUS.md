# M21 实施与真实验证记录

状态：**READY_FOR_REVIEW**。2026-10-12 03:44 +08:00，完整课程已实施并冻结，自验通过，等待主 agent 独立验收。本文件不自报 ACCEPTED，不含提交/推送声明。10～20分钟主实验、中文README、10份手写Go源码的详细中文注释、实库实验与五个辅助独立断言全部交付，没有待补关键实验或extra exercise。

唯一写入目录：`C:/Users/Lenovo/.codex/worktrees/go-course-m21/go-demo/lessons/m21_transactions_defer_panic/`，以下简写M。worktree根W为 `C:/Users/Lenovo/.codex/worktrees/go-course-m21/go-demo`，基线实际 `5929af4af6d8a599362bdbb20e2d2d3cfa2cf6ba`。读取规则/前置/材料时tool workdir显式W，创建/格式化/执行/验证命令tool workdir显式M；所有Go执行命令也显式 `go -C M`。没有用共享默认cwd写文件，没有写根、其他模块、原examples/files、共享go.work或基础设施索引，没有子agent、add/commit/push、切分支、服务安装/stop/start。

## 当前版本与前置核对

| 项目 | 真实值/使用范围 |
| --- | --- |
| `go version` | `go version go1.27.0 windows/amd64` |
| `go env GOVERSION GOTOOLCHAIN GOOS GOARCH GOROOT` | go1.27.0 / auto / windows / amd64 / D:/Go |
| 本课GOMOD/GOWORK | M/go.mod / 空 |
| 模块 | `go-demo/lessons/m21_transactions_defer_panic`，go=1.27.0，无toolchain/replace/go.work |
| 实际driver | `github.com/go-sql-driver/mysql v1.10.1`；其go.mod=1.24.0 |
| 间接依赖 | `filippo.io/edwards25519 v1.2.0`，go.sum存在，mod verify通过 |
| 真实服务 | MySQL8.4.11，安装者READY索引127.0.0.1:13306；实际SQL `VERSION()/DATABASE()`匹配8.4.11/golearn_m20 |
| 凭据 | 只读索引中的credential_file；只在内存加载username/password/database，不打印/复制/提交 |
| 活跃连接设计 | 锁等待holder1+observer1+worker1，最多3；代理case主连接1+代理连接1；每池最多1闲置，阶段/整课owner关闭 |
| C/race工具链 | 安装者既有WinLibs16.2.0posix-14.0.0-ucrt-r2；实际 `-dumpmachine` 为x86_64-w64-mingw32 |

已读适用AGENTS（祖先Codex AGENTS为空，没有模块额外规则）、根README/go.mod、M21完整prompt、TEACHING_STYLE、ORCHESTRATION、INFRASTRUCTURE、modules.json。前两层26课已主验收的基线上，读取M20/M05/M06/M07真实STATUS和直接代码：M20 Config/openPool/sqlWait/Finished修复，M05 Classify未知优先级/Join，M06 captureTooEarly/shadowResult/explicitReturn，M07 RunEffects/reportFailure与worker取消收尾。不从prompt存在推断前置完成，不代做/重跑完整前置。

主目录files只读检索并阅读基础语法defer/异常捕获材料，未复制或提交。官方执行事务、带版本的database/sql@go1.27.0、Go1.27 spec、driver v1.10.1及本机 `sql.go` 的BeginTx/awaitDone/Commit/rollback/Conn.Close、driver transaction/connection/errors源码已重新核对。pkg.go.dev默认页当时已指向更新补丁，因此使用1.27.0精确页面解释本机行为，未升级工具链。

## 实际命令与退出结果

M/W在下表只是阅读缩写，工具调用中workdir和go -C均写完整绝对路径。

| 命令（工作目录） | 实际结果/覆盖 |
| --- | --- |
| `go version`、`go env ...`（W；最终M再核对） | exit0，版本/模块隔离如上 |
| `go -C M mod tidy`（M） | exit0，锁定真实driver/间接依赖，生成本课go.sum |
| `gofmt -w cmd txcourse`、最终 `gofmt -l cmd txcourse`（M） | exit0，10个手写.go格式化；最终-l无输出 |
| 首次 `go -C M test ./...`（M） | **历史exit1**，startOwned漏第三参数、mysql.LoggerFunc不存在；下方保留原诊断并已修复 |
| 首次完整 `go -C M run ./cmd/demo`（M，idle增强前） | exit0，E01～E04实库全部通过；m21_f1bf5bace47812f2d130145b两表精确DROP remaining0，DB全0 |
| idle增强后的 `go -C M run ./cmd/demo`（M） | **历史exit1**，idle取消/锁恢复已通过，但保护defer重复Conn.Close返回ErrConnDone；下方记录修复 |
| 修复后的 `go -C M run ./cmd/demo`（M） | exit0，m21_703ec06d7c26a9999031f087两表；全部业务/故障/资源断言通过 |
| `GOLEARN_INTEGRATION=1`（进程临时）+ `go -C M test -v -count=1 -timeout 100s ./...`（M） | exit0，TestMySQL17.63s，package23.283s，命令24.279s；m21_78311f7e0d10e6550e727e79两表DROP0；当时四个辅助断言均PASS |
| 首轮完整实库 `go -C M test -race -v -count=1 -timeout 100s ./...`（M，临时amd64编译器与integration环境） | exit0，TestMySQL15.33s，package22.044s，命令24.427s，无DATA RACE；m21_4cc8ddfcb2efd931cfafed2d两表DROP0 |
| **最终源码** `go -C M run ./cmd/demo`（M，SQL清理结果独立字段/提前return断言补齐后） | **exit0**；m21_9ccc690f71ab7346f33aba96两表；取消明确cleanup_error=nil；所有SQL/真实协议/最终清理通过 |
| **最终** `go -C M test -v -count=1 -timeout 35s ./...`（M） | **exit0**，五个辅助断言PASS，package5.676s；TestMySQL明确SKIP，不以此项冒充实库 |
| **最终完整实库** `go -C M test -race -v -count=1 -timeout 100s ./...`（M，临时amd64编译器+integration环境） | **exit0**，TestMySQL15.68s、五个辅助断言PASS，package22.438s，命令24.859s，无DATA RACE；m21_b1a647372668475320e77a42两表DROP0，DB全0 |
| **最终** `go -C M vet ./...`、`go -C M mod verify`（M） | exit0，无vet诊断；all modules verified |
| race finally环境恢复、再 `go -C M env CC CXX CGO_ENABLED GOMOD GOWORK`（M） | process_environment_restored=True；gcc/g++/1，本课GOMOD、GOWORK空；未改全局配置 |
| `git -C W check-ignore -v .../.local/run-final.txt .../.secrets/mysql.json` | exit0，分别匹配本模块.gitignore；未把私有报告/秘密纳入交付 |
| `git -C W rev-parse HEAD`、`status --short --untracked-files=all` | 基线SHA正确，只有本课交付文件未跟踪，未修改其他路径 |
| 内存加载password，对本模块交付与.local日志检查精确字符串（不输出密码） | exit0，credential_password_match_count=0 |

普通完整实库测试与首轮race是当时真实通过的历史，最终源码额外把fn资源清理错误和工作错误分开，防止预期Canceled掩盖关闭失败，并新增生产保护defer的提前return断言。最终默认入口、五个辅助测试、完整实库race和vet已覆盖这些最终变更；不把早期结果作为新增清理契约的唯一证据。

## 真实业务与协议证据

以下值均由实际SQL固定预期断言，不是根据error猜测。主键重复原因由真实 `*mysql.MySQLError.Number=1062` 核对，Join/包装后仍可As/Is。

| 场景 | 真实观察/断言 |
| --- | --- |
| 正常提交 | Tx UPDATE影响1行/内部库存4，INSERT影响1行；外部stock4/orders1/qty1/SKU一致 |
| 第二步重复主键 | 同原key第二次Tx内暂时stock3，真实1062失败；退出后仍stock4/orders1，无第二次扣减 |
| 库存业务冲突 | 条件UPDATE影响0行，ErrConflict；stock0/orders0 |
| 正确panic策略 | 扣库存后panic，helper先回滚再传播，边界带现场栈失败；stock5/orders0 |
| 具名err危险提交 | panic时named err仍nil，defer真的Commit；外部已恢复为panic失败，stock4/orders0 |
| :=遮蔽危险提交 | 内层真实1062被吞，外层err=nil，caller=nil；stock4/orders1；修复stock5/orders1 |
| 注册时旧err危险提交 | caller真实1062，defer保存旧nil仍Commit；stock4/orders1；修复stock5/orders1 |
| 忘Rollback | caller失败，真实pool.InUse1，未结束Tx仍读库存4；外部stock5/orders1；harness观察后显式Rollback，InUse0、stock5/orders1 |
| DB/Tx混用 | DB连接自提交订单，Tx库存回滚，stock5/orders1；同Tx修复后stock5/orders0 |
| Begin前ctx取消 | BeginTx返回Canceled/nil Tx |
| idle Tx取消 | 两步SQL已执行后取消，实际Commit本轮context canceled；RollBack/Conn归还后stock5/orders0，新Tx FOR UPDATE成功取得该SKU锁 |
| 真SQL锁等待取消 | PROCESSLIST Info命中m21_cancel_lock+本表；最终race连接ID198/State本次updating；取消后Finished完成、工作Canceled保留、清理error=nil、worker.InUse0；holder释放后原服务端ID消失，stock5/orders0 |
| COMMIT未转发通信故障 | 代理真的收到COM_QUERY COMMIT，forwarded=false/serverOK=false；客户端invalid connection/UNKNOWN+原key；原服务端会话退出后stock5/orders0 |
| 服务端提交但OK丢失 | forwarded=true，收到真实MySQL OK包，主动response_dropped=true；客户端同样invalid connection/UNKNOWN+原key；原key查账stock4/orders1/qty/SKU匹配，恢复COMMITTED，无第二次下单 |
| Commit后的本地对象 | 两个通信失败后实际Rollback=sql.ErrTxDone；不能据此猜服务端未提交 |
| Result/Finished独立 | 测试先接收Result并缓存，再卡wrapper清理gate；owner实际到join等待点仍未返回；放行后才真实Finished/owner Done，原失败/工作/两层清理错误全保留 |
| 取消后的清理预算故障 | gate固定未清理，独立20ms预算耗尽返回原错误+ErrJoin+DeadlineExceeded，Joined=false；随后放门、用新预算真join并保留cleanup错误 |
| 提前return保护 | 实际deferred stopAndJoin取消worker并等Finished；原错误、Canceled、fn/wrapper两层清理错误都保留，真实清理哨兵完成 |
| packet/Accept辅助 | 真TCP framing拆包/截断/长度/短写断言，未连接时真实Accept由owner关闭并join，listener不再可Dial；不冒充MySQL提交证明 |
| 最终清理 | 每次实库运行两表精确DROP，information_schema remaining0，主DB open/inuse/idle全0 |

## 真实失败与修复

首个编译失败原诊断：

```text
txcourse\cancel.go:44:28: not enough arguments in call to startOwned
have (context.Context, func(workCtx context.Context) (workErr error))
want (context.Context, func(context.Context) error, func() error)
txcourse\config.go:101:19: undefined: mysql.LoggerFunc
```

补齐nil wrapper cleanup参数；核对锁定driver v1.10.1的实际 `Logger` 接口与 `NopLogger` 指针实现。最终使用 `mysql.NewConnector → sql.OpenDB`，保留不能编码进DSN的NopLogger配置。没有降Go版本、换driver版本、关闭vet或略过行为检查。后续任务结果接口增强为taskResult属于清理证据改进，源码与测试已同步。

idle增强首次运行 `m21_6e26cd508185ad777d0e0ea8` 的真实业务SELECT为stock5/orders0，新FOR UPDATE已成功，却最后失败：

```text
[cancel.idle] Commit=context canceled；独立SELECT无副作用；新事务FOR UPDATE成功
[cleanup.tables] remaining=0
[cleanup.DB] open=0 inuse=0 idle=0
M21 FAILED: sql: connection is already closed
exit status 1
```

原因是显式Conn.Close后保护defer又Close一次，第二次契约返回ErrConnDone。修复在该单一owner内登记connClosed，仅在尚未显式关闭时defer收尾；未吞掉真正首次Close失败。错误不是数据库回滚失败，也没有删去SQL/锁验证来掩盖；失败run自己的两表也已经精确清理。README与源码旁解释Tx/Conn重复调用契约不同。

最后自审额外发现“error树含Canceled”不能证明没有清理失败，于是把taskResult.Err/CleanupErr、joinedCleanup分开；正常取消路径既验证Canceled也验证cleanup nil。独立测试同时保留fn/wrapper两层清理错误，最终run/race重新通过。没有以预期取消覆盖资源错误。

## 资源所有权与未验证范围

每run创建的只有两张精确随机表，业务SKU/key都是合成。所有已执行run/普通实库test/race/失败run的DROP查询均remaining0。正确Tx owner是withTx；忘清理反例受harness兜底；cancel worker拥有Conn/Tx、wrapper最后关闭Finished；owner创建独立Background预算join。idle显式Conn归还后才查账。Rows使用QueryRow.Scan内部关闭，不返回裸Rows。

代理仅single loopback连接。accept wrapper最终Finished；两方向packet pump先返回结果再关闭自己的Done，serve实际等待两个Done后才能最终关闭Finished。提前return先关闭listener/所属socket解除Accept/ReadFull，再以独立预算join；真实关闭错误与serve原失败通过Join保留，重复net.ErrClosed作为已结束结果处理。两个故障case原服务端connection ID都实际消失；无本课后台代理/worker/实验进程。没有服务/全局代理/共享数据变更，MySQL和课程隧道继续由唯一安装者持有。

本机证据仅在忽略 `.local/`：run-final.txt、run-idle-close-failed.txt、integration-final.txt、race-first.txt、race-final.txt、unit-final.txt。没有复制凭据，没有生成/提交二进制。最终交付15个非私有文件：README/STATUS/.gitignore/go.mod/go.sum与10个手写Go文件。

本课没有验证生产负载、MySQL重启/硬断电/binlog/副本耐久性、生产TLS或压缩/大包多连接代理、所有网络故障排列、公司旧Go/旧driver、其他OS、发货/MQ分布式事务或生产幂等存储。共享基础设施的安装、运行锁/资源限制/重启持久性复用主agent已验收证据，本课不重复停服务。没有用fake Commit/纯Go测试/健康检查代替上述真实库存、订单与COMMIT故障验收。

主审可优先检查 `transaction.go` 的显式Commit/panic清理、`cancel.go` 的三条取消位置与服务器结束证明、`task.go` 的Result/Finished/cleanup独立事实、`proxy.go` 的单包COMMIT真实OK截断与两方向Done、`unknown.go` 原key权威SELECT。完整重跑入口和race命令见README，状态保留READY_FOR_REVIEW等待独立验收。
