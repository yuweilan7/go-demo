# M27：Redis 租约、旧 worker、SQL fencing 与限流

这课围绕一个很常见的后台任务展开：A 取得 Redis 锁，准备刷新一份业务状态，途中停顿；锁过期后 B 取得同一把锁并完成刷新；A 恢复后把旧结果写了回去。两个请求从来没有同时在 Redis 中拥有同一个 key，数据库仍然可能被旧工作覆盖。

你要掌握三个分别由不同参与者维护的事实：**Redis 的 owner token 保护释放/续租对象；下游 SQL 的单调版本保护已接受的新状态；业务唯一键与数据库事务保护一次订单效果和库存守恒。** `context` 负责通知调用链停止，owner 负责解除等待、等清理真正完成。它们不能替代其中任何一个业务协议。

默认入口会实际执行危险路径，并把危险结果作为预期反例验收，然后执行修复路径。所有数据是本 run 的合成数据，最后精确清理。本课是独立 module，根目录的 `go test ./...` 不会进入这里。

## 现在必须看懂什么

必须看懂 `acquire` 的四种结果、随机 token 比较释放、A/B 的暂停时序、`last_token < token` 的下游接受规则、`Result` 与 `Finished` 两个 channel、限流的窗口定义、订单唯一性与库存事务。读代码时一直问：当前在哪里等待，谁能解除等待，哪个真实副作用已经发生，谁收到最后完成信号。

本课不用你先研究 Redlock、多节点故障转移、Cluster 槽位、Lua 内部解释器和 MySQL 完整隔离级别。最后给出这些问题的边界；遇到公司代码再按实际部署/版本展开。这里的限额、超时和连接数用于有界教学，并非生产容量建议。

## 前置、版本和接入

硬前置已经从本 worktree 的真实 `STATUS.md` 与直接源码核对：

| 前置 | 实际可消费的成果 | 在本课怎样组合 |
| --- | --- | --- |
| M25 `m25_redis_client` | Redis 8.2.8，go-redis v9.23.0；真实 AUTH/TTL/原子边界/响应未知与精确键清理 | 两个真实 Client、Lua、独占响应故障连接；不复制整套前置项目 |
| M13 `m13_context_budgets` | 通知、观察、退出、join 分开；真实 HTTP 的晚效果反例；SQL adapter 明确仅作阅读 | 每组工作继承 ctx，取消不撤回已执行副作用，清理用新预算 |
| M20 `m20_sql_pools` | MySQL 8.4.11、driver v1.10.1、真实池/Conn/Tx/SQL 取消；主审已修复 Finished 契约 | 同一课程库内自建 `m27_<run>_state/orders`，SQL 条件写/真实事务/独立完成广播 |

本课不跨 module 导入前置的 `internal` 包，不需要 `replace` 或 `go.work`；组合的是已核验的服务与资源契约。本课源代码自足，不改前置课。M14/M22 只是推荐阅读知识。

本次实际环境：Go **1.27.0 windows/amd64**，`GOTOOLCHAIN=auto`，`GOWORK` 空，独立 `go.mod` 写 `go 1.27.0`，无 `toolchain` 指令。直接依赖锁定 `github.com/redis/go-redis/v9 v9.23.0` 与 `github.com/go-sql-driver/mysql v1.10.1`，`go.sum` 有实际下载校验，`go mod verify` 通过。go-redis v9.23.0 本身要求 Go 1.26，新增独立 pipeline 池等契约需要按该版本阅读，不能把旧文章的默认连接行为套过来。[go-redis 官方 v9.23.0 发布说明](https://github.com/redis/go-redis/releases/tag/v9.23.0)、[MySQL driver 官方 v1.10.1 发布](https://github.com/go-sql-driver/mysql/releases/tag/v1.10.1)。

入口只读安装者索引，默认：

```text
D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json
Redis READY -> 127.0.0.1:16389 / Redis 8.2.8 / DB0
MySQL READY -> 127.0.0.1:13306 / MySQL 8.4.11 / golearn_m20
```

`LoadConfig` 从条目的 `credential_file` 读取密码，不输出或复制正文。可以用 `-index <路径>` 或 `GOLEARN_SERVICE_INDEX` 指定另一份安装者提供的索引；仍要求 READY、登记的 loopback 和课程数据库。默认测试不访问服务。共享服务/SSH 隧道归主 agent 管理，本课不安装、不停机、不改全局 Redis 参数。

## 10～20 分钟主实验

在仓库根目录运行：

```powershell
go -C lessons/m27_redis_leases_limits run ./cmd/demo -scenario core
```

程序本身几秒结束。按下面顺序读，就能在 10～20 分钟把核心机制讲清：

1. 用 2 分钟看 `cmd/demo/main.go::runMain` 与 `run.go::Run`：谁建立整课预算、谁持有两套 Redis Client 与唯一 SQL 池。
2. 用 3 分钟看 `lease.go::acquire/release`：随机 owner 与期限是不同的值，释放只比较 owner。
3. 用 4 分钟看 `experiments.go::stale` 和下面的时间线：A 已通过 GET 后真正停在哪里，B 的 SQL 在何时被接受。
4. 用 4 分钟看 `sql.go::acquireFenced/writeValue`：授予顺序怎样形成，数据库怎样在接受边界原子拒旧。
5. 用 2 分钟看 `ownership.go::join/finish` 与最后 CLEAN 输出：工作完成广播、表/键/池最终状态怎样证明。

`-scenario core` 只执行第 1、2 组，仍创建两个合成表并完整清理。默认 `all` 执行下面七组完整课程，不需要额外作业。

```powershell
go -C lessons/m27_redis_leases_limits run ./cmd/demo
go -C lessons/m27_redis_leases_limits test -v -count=1 -timeout 30s ./...
go -C lessons/m27_redis_leases_limits vet ./...

# 同一PowerShell进程内请求真实Redis+MySQL测试，最后恢复原环境值。
$savedIntegration = $env:GOLEARN_M27_INTEGRATION
try {
    $env:GOLEARN_M27_INTEGRATION = '1'
    go -C lessons/m27_redis_leases_limits test -v -count=1 -timeout 60s ./...
} finally {
    $env:GOLEARN_M27_INTEGRATION = $savedIntegration
}

# Windows/amd64完整真实integration race；读安装者64位CC/CXX索引。
& ./lessons/m27_redis_leases_limits/scripts/integration-race.ps1
```

race 脚本显式 `go -C` 到所属 module，只改当前 PowerShell 的 CC/CXX/CGO_ENABLED/PATH 和 integration 开关，`finally` 恢复。它不安装编译器，不做 `go env -w`。本课实际 race 退出 0，结束后默认 `CC=gcc/CXX=g++/CGO_ENABLED=1` 已恢复。race 没报告数据竞争只覆盖这次实际执行路径，跨机器协议正确性仍靠业务断言。

## 文件与完整调用链

| 文件 | 职责与关键符号 |
| --- | --- |
| `cmd/demo/main.go` | `main -> runMain`：flags、总预算、返回之后再决定进程退出码 |
| `internal/lesson/config.go` | `LoadConfig/readJSON`：只读索引与秘密，确认授权端点 |
| `internal/lesson/run.go` | `Run/createTables/key/check/finish`：唯一池 owner、登记精确键/表、独立事实断言、清理 |
| `internal/lesson/lease.go` | `acquire/classifySET/release/renew/expired`：SET NX PX、失败分类、Lua 比较、真实过期屏障 |
| `internal/lesson/experiments.go` | `stale/unlocking/renewCancellation`：A/B 时序、错误释放、续租与取消 |
| `internal/lesson/sql.go` | `acquireFenced/writeValue/readValue/placeOrder`：权威发号、接受旧写的边界、订单与库存事务 |
| `internal/lesson/fault.go` | `acquireFailures/gatedConn.Read`：真实 SET 响应已经到达但 caller 读不到 |
| `internal/lesson/limiter.go` | `allow/windowLua/rateLimits/boundaryBurst/withPolicy`：固定窗口语义与故障策略 |
| `internal/lesson/ownership.go` | `group.start/join/finish`：任务登记、结果与完成、提前失败收尾 |
| `internal/lesson/lesson_test.go` | 错误分类、命名范围、清理门、早退错误链、完整真实集成及另 Client 清理读回 |

串行入口是 `main -> runMain -> LoadConfig -> Run`。`Run` 创建 A/B Client 与 DB 池，真实 `INFO/SELECT` 核对版本，`createTables` 创建本 run 表，然后逐组调用实验。每组实验返回之后才进入下一组；它的 defer 已停止并 join 本组全部任务。最后 `Run` 返回先执行 `finish`，精确 DROP/DEL/查询最终状态，再 Close Client/DB；`runMain` 看最终返回的 error 决定是否打印 M27 PASS。

本课 Go 的几个连接点：`func (l *lab)` 是指针接收者，方法借用同一个 lab/Client/DB，不拷贝连接池；`:=` 在当前块声明新的局部变量，内层短声明需要留心遮蔽 `err`；具名 `err` 是返回槽，defer 的闭包在函数退出时修改同一槽。`errors.Join` 保留主失败和清理失败，调用方可以用 `errors.Is/As` 查原因。`chan<-`/`<-chan` 只限制可用操作，channel 谁能 close 仍是协议责任；本课屏障由 owner 或指定 worker 唯一关闭。

真实 Redis 调用链在锁定的 v9.23.0 中核对过：

```text
acquire -> Client.Do -> Cmd.Text
        -> Client.Process -> baseClient.processWithRetry/_process
        -> withConn -> pool.Conn.WithWriter -> 真实socket.Write
                    -> pool.Conn.WithReader -> 真实socket.Read -> RESP解析
release/renew/allow -> cmdable.Eval -> 同样的Process/socket链 -> Cmd.Int64/Int64Slice
```

`ContextTimeoutEnabled=true` 使上下文 deadline 参与读写期限选择；无 deadline 的主动 cancel 不是保证立即中断已开始 socket I/O 的通用强杀机制。因此 socket 本身也有 Read/WriteTimeout，owner 有解除响应门/关闭独占 Client 的救援路径。`MaxRetries=-1` 禁止普通已派发写命令自动重放；拨号重试和未上网线前的恢复是不同阶段。本课响应丢失后实际状态仍只有一次 SET。

真实 SQL 路径为 `BeginTx -> Tx.ExecContext/QueryRowContext.Scan -> Tx.Commit/Rollback`，Tx 绑定真实借池连接；`writeValue -> DB.ExecContext -> MySQL UPDATE -> Result.RowsAffected`；`readValue -> QueryRowContext.Scan -> 独立SELECT业务状态`。`QueryRow.Scan` 消费内部 Rows，不向外返回开放的 Rows。mysql driver 的 `mysqlTx.Commit` 实际向连接执行 `COMMIT`，连接错误并不产生“肯定未提交”的承诺。

## 第 1、2 组：同一时序，两个接受规则

业务表 `m27_<run>_state` 是 InnoDB：id=0 保存 `issued_token`，id=1 是危险刷新，id=2 是 fencing 刷新，id=3/4 是下单对照，id=5 是取消后已接受效果的观察行。初始业务行 `value_text=initial/effects=0/stock=2/last_token=0`。另一个 `m27_<run>_orders` 以 `order_id` 为主键。不同组使用不同 id，证据不会互相覆盖。

| 步骤 | 参与者 → 函数/真实等待点 | 状态或副作用 | 为什么可以进入下一步 |
| --- | --- | --- | --- |
| 1 | owner → A `acquire`，修复组为 `acquireFenced` | A 的随机 owner 写入 Redis，TTL=250ms；修复组授予号=1 | 读到确定 OK，修复组发号事务也确定 Commit |
| 2 | A worker → `Get` 后 `close(checked)` → `await(resume)` | A 曾确认自己 owner；还没写业务 SQL，正在门上暂停 | owner 等 checked，而非猜 goroutine 已运行 |
| 3 | owner/B Client → `expired` → `EXISTS` 查询 | Redis 真实过期并返回 0；A 的 ctx 仍有效 | 查询状态为 0 才继续申请 B |
| 4 | owner → B 申请 → `writeValue` | B 租约确定成功，SQL 接受 `B-new`；修复组 token=2/last_token=2 | 受影响行=1，B 的同步调用返回 |
| 5 | owner → `open(resume)` | 解除 A 的实际等待；A 带着原先的检查结果恢复 | A 门已关闭；不需要 sleep 猜调度 |
| 6 | A → `writeValue` → wrapper 最后 `close(Finished)` | 危险版再次写入；修复版条件写被拒 | owner 的 `join` 收到真实 Finished 后读结果 |
| 7 | owner → `readValue` 独立 SELECT | 危险值 A-old/effects2；修复值 B-new/effects1/last_token2 | 验证的是实际业务行，不只是 worker 返回“完成” |
| 8 | A 比较释放 → B 比较释放 | A 返回0且 B owner仍在；B 自己释放返回1 | 旧工作不得误删 B，最终由 owner 收尾 |

危险 SQL 是：

```sql
UPDATE m27_<run>_state
SET value_text = ?, effects = effects + 1
WHERE id = ?;
```

实际日志：

```text
TIMELINE A_checked -> actual_TTL_expired -> B_granted(token=0) -> B_SQL_accepted -> resume_A(token=0)
PASS GET_ctx_do_not_fence_SQL A rows=1; SQL value=A-old effects=2; ctx_error=<nil>
```

A 的 `GET` 是过去的观察，之后任意暂停都能让观察失效。把 `GET` 移到 SQL 前面仍有“读完 → 停顿 → SQL”的间隙。检查 `ctx.Err()==nil` 只说明本地取消范围没有结束，Redis TTL 本身不会关闭该 ctx。即使续租器检测丢租并 cancel，在下游已接受更新后才观察通知也不能撤销该更新。Go context 与 Java interrupt/Future.cancel 一样需要具体工作/IO 配合；Go 没有从外部安全强杀任意 goroutine 的接口。

修复 SQL 把接受新版本的判断与写入放在同一个数据库动作：

```sql
UPDATE m27_<run>_state
SET value_text = ?, effects = effects + 1, last_token = ?
WHERE id = ? AND last_token < ?;
```

B token=2 后，行内已经保存 `last_token=2`，A token=1 的谓词失败。实际结果是：

```text
PASS stale_SQL_rejected A token=1 rows=0; B token=2; SQL value=B-new effects=1 last_token=2
PASS authority_durable_issued_token separate SELECT issued=2 accepted=2
```

**token 必须由可信权威按授予顺序严格增加，数据库必须持久保存最后接受号，所有写入者必须遵守接受规则。** 随机 owner 不可排序。自己在 SQL 之前 `GET`/客户端比较版本，再发无条件 UPDATE，也会重新产生间隙。InnoDB UPDATE 的行锁/条件写承担本例的原子接受边界。[MySQL 8.4 官方 InnoDB 行锁说明](https://dev.mysql.com/doc/refman/8.4/en/innodb-locking.html)。

### 发号为什么也必须设计顺序

本课没有简单地“Redis 获锁之后，随时去 DB 取个递增号”。那样 A 拿锁后暂停，B 取得新锁并领2，A 恢复再领3，旧 A 反而被下游视为更新的持有者。

`acquireFenced` 的具体协议是：`BeginTx -> UPDATE id0 issued_token+1` 取得权威行锁 → 当前 Tx 查询该号 → 在仍持有行锁时申请 Redis → 只有明确成功才 Commit → 检查 Commit 结束时保守租约窗口仍有效 → 返回授予号。下一任授予必须等前一任发号 Tx 终结。普通竞争或未知申请不执行业务，比较释放本次可能留下的 Redis owner 后回滚发号 Tx。Commit 错误或超出有效期都返回 UNKNOWN，业务不能继续。发号行与业务行都在真实 MySQL，不用 Redis INCR 的易重置状态充当本例的持久权威。

这为了读清协议而让发号行锁跨越一次有界 Redis IO，会增加串行瓶颈和耦合，并非要求生产系统照搬。生产可以使用已有线性化授予/版本权威，但必须保留同样的授予顺序和下游拒旧契约。所有参与者都要经过该权威；绕过它的普通锁或无条件 SQL 仍能破坏结果。

本课证明的是**B 的较新写已接受后，A 的较旧写被拒**。如果 A 的旧写先到、B 尚未被下游接受，`last_token<token` 可能接受 A；它不是按 Redis TTL 自动撤销所有旧权限的机制。一个授予号这里只允许一次更新，同 token 的重发也被 `<` 拒绝；真实多步任务需定义每步序号/事务或幂等回执。本课值在 DB 中真实持久保存并由另一 SELECT 读到，未重启服务测试该新表的故障恢复。发号权威的备份回退、复制切换、数据恢复后号复用必须另行设计；单 Redis 重启/复制安全性不能从本例推断。

## 第 3 组：owner token 解决哪个问题

`acquire` 每次生成一份 `crypto/rand` 的 12 字节随机值，用 24 字符十六进制保存。课程运行次数有限，随机碰撞极低；真实协议仍需要按请求规模定义唯一性/随机位数。`Until` 从本机发送前的 `time.Now()` 加 TTL 得到，用于保守预算；本机的这个时间不是 Redis 的持久权威。

请求明确发送 `SET key owner NX PX <毫秒>`，一次命令原子地维护“不存在才写”和过期。官方单节点锁模式要求每次申请独立随机值，释放前比较值；目前在线文档还展示 Redis 8.4 新增的 `DELEX`。我们实际服务是 **8.2.8**，因此本课实现 Lua 比较删除。[Redis 官方分布式锁与单节点模式](https://redis.io/docs/latest/develop/clients/patterns/distributed-locks/)、[SET 命令参数](https://redis.io/docs/latest/commands/set/)。

`unlocking` 第一步在 A 未过期时让 B 申请，NX 的 nil 回复分类为 `CONTENDED`，它不是网络失败。然后用真实 EXISTS=0 等过期，B 申请成功，A 故意 `DEL key`，结果 `DEL=1/B EXISTS=0`：删掉的是 B。

修复组换独立 key，再复现相同过期顺序：

```lua
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
```

GET 与 DEL 在同一脚本执行，不会插入其他命令。A 比较释放=0，A 比较续租 `PEXPIRE`=0，B 的 owner 仍在。B 自己第一次释放=1，再释放=0。重复调用安全依赖每次都携带**同一任** owner；用原始 DEL 重试没有这种保护。Lua 的原子执行不等于错误自动回滚，脚本应在写之前校验条件。[Redis 官方 Lua 执行契约](https://redis.io/docs/latest/develop/programmability/eval-intro/)。

## 第 4 组：续租失败以后谁收尾

成功续租只是把当前匹配 owner 的 TTL 延后。续租不能让进程免于停顿，也不是下单幂等。`renewCancellation` 有两个独立 goroutine：一个 worker 先执行真实 SQL 并用 `workAccepted` 发布成功/失败结局，再等待 `workCtx.Done()`；一个 renewer 每次 ticker 后调用比较续租。

| 步骤 | 参与者 → 等待点 | 状态变化/副作用 | owner 的证明 |
| --- | --- | --- | --- |
| 1 | worker → `writeValue`；renewer → 30ms ticker → `renew` | SQL接受A-before-cancel/effects1；Lua返回1延长A TTL至400ms | owner分别等workAccepted与renewed；两者相对先后不固定 |
| 2 | renewer → 下一次 tick → `await(resumeRenew)` | 暂停续租，当前实际等待门 | `paused` 关闭才读过期状态 |
| 3 | owner → `expired`，然后 B `acquire` | 实际过期，B成为owner | EXISTS0与明确SET OK |
| 4 | owner放续租门 → renewer `renew` | 比较失败=0，执行 `workCancel()` | renewer 的 Finished已关闭/join |
| 5 | worker观察Done → 返回 outcome | Result已有Canceled，wrapper cleanup等待门 | `cleanupEntered`关闭且 `len(Result)=1` |
| 6 | owner查看Finished | 仍未完成，不能Close池/算清理结束 | `Finished=false` 的实际断言 |
| 7 | owner放cleanup门 → wrapper结束 → 独立SELECT | cleanup执行完，最后close(Finished)；SQL仍保存A-before-cancel/effects1 | owner join后读Canceled，B owner仍保留，取消没有撤销已接受效果 |

`group.start` 最先注册 `defer close(finished)`，最后执行它；`result <- fn(ctx)` 在 wrapper cleanup **之前**。`join` 先等待 Finished，之后才读取/缓存 Result 和 cleanup error。读取 Result 不能消耗/替代完成事实。测试用 `probeContext.Done()` 被 join 实际求值作为屏障，证明清理门未开时 join 确实还在等待，无需 sleep 猜调度。

本次还有独立断言 `cancel_not_revoke_accepted_SQL`：丢租、取消、Finished和join全部发生后，SQL依旧保存 `A-before-cancel/effects1`。这把“取消不能撤回已接受副作用”落实到了真实MySQL，而不是只用一句理论说明。它不声称取消之后仍派发了新SQL。

正常路径以及每个提前 return 都有同一收尾：`cancel -> release全部门 -> 新Background预算join所有Finished -> 必要时Close独占故障client -> 再用新预算join`。普通 Client/DB 的唯一 owner 是 `Run`，实验完成前只借用；故障 Client 的 owner 是该故障函数。`errors.Join` 保留原错误与清理失败；未读工作结果的早退也会收取其错误。工作 ticker/响应 timer 由创建它们的 goroutine Stop。

本课不故意制造 panic；若手工加入 panic，Go 只展开当前 goroutine 的 defer，别的 goroutine 的 defer 不会因此自动完成，未 recover 的 panic 会终结进程。`defer` 的正常返回保证也不覆盖强杀、断电。生产任务的重做/恢复仍应依赖持久状态，不能依赖最后一个本地 defer。

## 第 5 组：竞争、命令失败、响应未知

| `acquireState` | 确认了什么 | 本课行为 |
| --- | --- | --- |
| `GRANTED` | 读到OK且保守本地窗口尚未结束 | 按协议继续；可能随后的暂停仍使租约失效 |
| `CONTENDED` | SET NX明确nil，当前申请未写 | 正常业务竞争；不误报服务异常 |
| `REJECTED` | 非法输入/派发前已取消、关闭client/池未借到，或这条SET的服务端明确拒绝 | 返回原error；不等于额度竞争 |
| `UNKNOWN` | 网络/响应读取失败，或OK/Commit晚于保守期限 | 不派发业务；精确owner清理/状态核验；不盲重放 |

服务端失败反例实际发 `SET ... PX -1`，真实回复 `ERR invalid expire time`，独立 EXISTS=0，分类 REJECTED。这个分类仅针对单条原子 SET，不能套用到先写后 error 的 Lua、Pipeline 或 Commit。

UNKNOWN 使用本课独占的 `gatedConn`，warmup PING/AUTH 完成后才 arm。关键顺序是：

```text
worker SET上网线 -> Redis真正执行 -> 真实回复到本机Read
 -> captured关闭 -> Read等待release/deadline，caller仍没收到结果
 -> B独立GET看见24字符owner -> 350ms Read deadline使caller返回timeout
 -> worker全部defer/Finished -> owner看到UNKNOWN
 -> 比较释放该次owner=1；从未派发业务
```

门包装只延迟本课真实回复，不生成 Redis 假响应、不监听共享端口、不停服务。响应 timer 来自 driver 给出的读期限，本包装只用于本课“读前设deadline且一次注入”的调用链，不能拿去当完整通用网络代理。超时后回复丢弃，连接被淘汰以防协议错位。B 的 GET证明“本次效果曾发生”，仍只是瞬时观察，不授予继续写 SQL 的权利。未知结果应按业务键/版本和权威状态对账；单纯再 GET 或再次申请锁不能消除原先的晚效果。

## 第 6 组：把限流语义说清再实现

本课选择**首个允许请求开窗的固定窗口**：某个 `(已认证用户, 规范化接口)` 第一次允许时建立计数1与3秒TTL，同一窗口最多允许3次。拒绝不增加计数、不刷新TTL；Redis key过期后下一次允许请求重新开窗。它不是整点对齐窗口、滑动任意3秒窗口，也不是令牌桶。

`limitKey` 用长度编码的 user+route 做 SHA256，完整前缀仍是自己的 `golearn:m27:<run>:`。真实应用的user应来自鉴权身份，route来自规范化接口，任意客户端自报user/随机路径会绕开隔离目的。此处 `alice/bob` 和 `POST /orders/GET /catalog` 为合成输入。

`allow -> Eval(windowLua) -> Cmd.Int64Slice` 接收四项：是否允许、允许计数、PTTL毫秒、Redis TIME毫秒。`count<limit` 时首次 `SET ... PX` 或后续 `INCR`，超出时仅返回拒绝。所有必要计数/TTL逻辑处在同一脚本；`INCR` 本身保留已有TTL，不需要每次刷新。服务端 TIME只供日志观察，**没有把客户端wall clock作为授权条件**。Redis管理到期仍依赖自己的时钟，跳时/恢复等需要生产部署额外评估，不能宣称跨机器绝对精准。

| 实验参与者/等待 | 实际结果 | 业务含义 |
| --- | --- | --- |
| 12 worker等同一start门，放门后真实Eval | allowed=3/rejected=9，独立GET=3，PTTL=2.770s | 库原子边界保证本窗口限额；哪个worker获准没有固定顺序 |
| 再提交同用户同接口一次 | allowed=false/count仍3；TTL从2.770s降到2.655s | 这是业务限额拒绝，不是服务异常；不会把窗口无限续长 |
| bob下单、alice读目录 | 各自allowed=true/count1 | 用户和接口分别隔离 |
| 2秒窗口早先开窗消耗1，实际PTTL<=900ms才花余下2 | 旧窗口尾部2次允许 | 使用真实PTTL作为边界屏障 |
| 实际EXISTS0后新窗口连续3次 | 5个尾部/新窗口请求在Redis TIME 911ms内允许 | 任何2秒最多3不是此算法保证，开窗早先那1次不计入这5次 |

这就是固定窗口的 burst：本算法可在边界附近集中 `2N-1` 个请求，开窗的一个请求早先已消费；对齐固定窗口在边界可达约 `2N`。本课实际观察的5次/911ms属于前一种。时间值是当次观测，不是延迟SLO。若业务要求任意滚动区间上限，要改用符合该规则的滑动窗口；若允许按速率积攒突发额度，要定义令牌桶的容量与补充规则。

错误要与拒绝分开：`allow=false,nil` 是额度拒绝；Redis error由 `withPolicy` 明确转成业务选择，保留 Cause。critical下单路径关闭准入，optional低优先级读路径允许降级并标记Degraded。本课分别实测关闭的独占 Client 返回 `ErrClosed` 与独占错误凭据 Client 被真实 Redis AUTH拒绝（WRONGPASS）；两条策略均符合声明，失败配额key从未写入。没有把接入鉴权失败声称成共享Redis宕机或多节点网络分区。

如果限流回复丢失，服务端可能已经消耗一次额度。关闭准入可能造成保守拒绝，降级放行则可能超过全局配额；这是策略代价，不该被隐藏。未知响应不自动重放INCR/Eval，本课禁止普通命令自动重试；严格场景也不能把UNKNOWN直接当“没计数”。本课未另外丢弃限流回复，第5组已实际证明同一IO边界的SET响应未知。

限流约束的是入站**次数**，不是同时执行的worker数量。一秒允许100次、每个请求占数据库十秒，仍会积累大量工作；需要有界队列、worker上限、SQL池和排队预算独立背压。Redis限流本身也消耗客户端连接与IO预算，本课12个任务只是有限并发，主池单client最多2个活跃连接。

## 第 7 组：为什么订单不能只靠锁

危险版两次顺序请求是同一个逻辑订单。A有效获锁、扣一次库存、释放；B有效获锁、再扣一次库存、释放。每一次 Redis 锁都正确，最终初始stock2变stock0/effects2。锁只能限制同时进入，无法知道这两次业务是否是同一笔订单。

修复版 `placeOrder` 由一个 SQL Tx owner完成：

| 步骤 | 当前函数/资源 | 实际结果和清理 |
| --- | --- | --- |
| 1 | `BeginTx` 借池并绑定一条连接 | 同一Tx执行后续SQL，不能改成外层DB调用 |
| 2 | `INSERT orders(order_id,units)` | 第一次唯一主键写入；同ID重试真实1062 |
| 3 | 新订单 `UPDATE state id4 WHERE stock>=1` | 影响1行才继续；库存不足返回并Rollback订单 |
| 4 | `Commit` | 两项共同提交；错误仍是需按orderID核验的未知提交结果 |
| 5 | 重试1062分支 | 没有扣库存，defer Rollback后返回Duplicate |
| 6 | owner独立SELECT | stock1/effects1/orders1/duplicate_receipts1 |

本课同一orderID只有一个固定载荷，1062的作用域只有本课订单主键。真实业务应保存/校验请求载荷与响应，不能把其他唯一索引冲突或不同载荷的同ID冒充成功。库存条件写的行数同样要处理，取得Redis锁不能替代数据库约束、检查或事务。跨服务多步骤的订单/MQ恢复将在相应模块处理。

## 每项资源的 owner 与最终结局

| 资源 | owner/主要等待点 | 失败路径与最终证明 |
| --- | --- | --- |
| 总ctx的40s timer | runMain | defer cancel；Run返回前已清理 |
| 每组ctx、任务、Result/Finished | 实验函数的group；fn等待resume/start/Done/IO | cancel+release+独立4s join；17启动/17Finished已join |
| workCtx | renewCancellation | 续租0或error时workCancel；早退由g.cancel传播；wrapper清理后才Finished |
| renewer ticker30ms | renewer goroutine | defer Stop；Finished后owner才确认资源完成 |
| observe ticker5ms | 每次observe调用 | 状态成立/错误/ctx返回都defer Stop |
| 回复gate与timer/socket | acquireFailures及其worker Read | owner放gate/必要Close；Read defer Stop；join后Close独占client |
| A/B长期Client、真实socket | Run；业务借用指针 | 所有实验join后精确DEL/EXISTS，再Close；最终6创建/6关闭 |
| AUTH错误Client | rateLimits | 函数defer Close；未改变服务账号/ACL |
| DB池，最多4连接/idle2 | Run | 所有Tx结束、表DROP后Close；open0/inuse0/idle0 |
| 授予Tx与下单Tx | acquireFenced/placeOrder | Commit或defer Rollback；ErrTxDone忽略，其他cleanup error保留 |
| QueryRow内部Rows | database/sql/当前Scan调用 | Scan同步消费并归还，不向调用方逃逸 |
| Redis精确键 | Run登记列表 | 所有存在键TTL>0且<=30s，最后精确DEL+EXISTS0 |
| MySQL精确表 | Run | 先证明随机名字不存在再CREATE；未知响应也登记attempt；DROP后information_schema逐名0 |
| 测试join辅助goroutine | 对应test函数 | 失败也放门/取消，用独立预算等ownerDone，之后才收group |

真实默认入口本次 run=`54eb13e1cf3ec3394ea057aa` 的结尾：

```text
CLEAN table=m27_54eb13e1cf3ec3394ea057aa_state remaining=0 error=<nil>
CLEAN table=m27_54eb13e1cf3ec3394ea057aa_orders remaining=0 error=<nil>
CLEAN DB open=0 inuse=0 idle=0
CLEAN exact_keys=13 remaining=0 tasks=17/17 sockets=6/6 cleaned=true
M27 PASS assertions=28 tasks=17/17 cleaned_keys=13 tables=2 sockets=6/6
```

登记13个键意味着13个可能触及的名字，不意味着最后有13个存在键：锁会释放/过期，非法SET与故障配额键从未成功写入。任何提前失败仍按登记清单清理，不使用FLUSH、SCAN-delete或其他课数据。完整集成另建 Client查询这些精确键仍为0，完整race再次执行真实服务/SQL状态与收尾。完整实际命令、历史编译修复与未验范围在 `STATUS.md`。

## 多节点/Redlock 留到什么情况下再看

如果只把Redis锁用于减少重复计算，偶发重做代价有限，可能接受较弱保证；若锁保护钱、库存、唯一效果，应让权威业务存储维护不变量。Redis官方说明异步复制故障转移存在丢失锁写的窗口，单节点成功不能证明复制切换安全。Redlock还涉及多个独立节点、多数派、获取耗时与时钟/停顿假设；社区对适用安全目标有争论，这课没有搭多节点也没有做该算法验收。

无论选择哪一种协调锁，都应先说清：新授权怎样排序，旧worker怎样在下游被拒绝，未知响应怎样恢复，业务重复怎样幂等。这里已证明一个真实单Redis+MySQL的有限时序；没有承诺它就是生产分布式锁服务。

## 官方与固定版本源码定位

- [Redis Lua 原子执行与脚本约束](https://redis.io/docs/latest/develop/programmability/eval-intro/)；[INCR与计数器示例](https://redis.io/docs/latest/commands/incr/)。
- [go-redis v9.23.0 commands.go](https://github.com/redis/go-redis/blob/v9.23.0/commands.go)：Client借用的`cmdable.Do`；[redis.go](https://github.com/redis/go-redis/blob/v9.23.0/redis.go)：`Process/processWithRetry/_process/withConn/context`；[options.go](https://github.com/redis/go-redis/blob/v9.23.0/options.go)：重试/池/deadline配置；[internal/pool/conn.go](https://github.com/redis/go-redis/blob/v9.23.0/internal/pool/conn.go)：`WithReader/WithWriter/deadline`；[error.go](https://github.com/redis/go-redis/blob/v9.23.0/error.go)：`redis.Error`是接口，`HasErrorPrefix`通过error链识别。
- [mysql driver v1.10.1 transaction.go](https://github.com/go-sql-driver/mysql/blob/v1.10.1/transaction.go)：`mysqlTx.Commit/Rollback`；[connection.go](https://github.com/go-sql-driver/mysql/blob/v1.10.1/connection.go)：Context与连接IO。
- [Go 1.27.0 context源码](https://github.com/golang/go/blob/go1.27.0/src/context/context.go)、[database/sql源码](https://github.com/golang/go/blob/go1.27.0/src/database/sql/sql.go)、[net.Conn接口](https://github.com/golang/go/blob/go1.27.0/src/net/net.go)。执行时也用本机 `go doc` 和 `D:/Go/src`核对，语言取消/同步保证、库行为和本次时序分别说明。

`files/开发框架`只有“redigo或go-redis、练习基础结构”的启发，不构成锁/fencing/限流正确性证明。本课实际锁定一套go-redis API；没有把不同库的API混着用，也没有直接使用8.4才引入的命令。
