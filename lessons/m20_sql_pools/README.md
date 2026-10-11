# M20：database/sql、真实 MySQL 与连接池生命周期

业务场景是一个库存查询接口偶尔超时：MySQL 看起来健康，`PingContext` 能成功，但请求仍卡住。先问“当前调用还在借池，还是已经到数据库执行 SQL”，再检查是谁持有连接、谁应该释放它。本课用真实 MySQL 和真实 `go-sql-driver/mysql`，把正常写读、危险版本、修复版本和副作用 SELECT 放在同一次有界运行中。

核心实验可以在 10～20 分钟内阅读、运行和对照；实际程序只需几秒到十余秒。运行成功的证据是表内容、池统计、服务端等待、取消和最后清理都通过，不是编译、TCP、Ping 或文档中写着 PASS。当前实际结果看 [STATUS.md](STATUS.md)。

2026-10-12 实测：Go1.27.0 windows/amd64、mysql driver v1.10.1、独立 MySQL8.4.11。主审后的独立Finished契约修复版本已通过默认demo、确定性清理门测试、完整真实integration `-race`、vet和mod verify；修复版证据单独记录，不使用首版日志替代。安装者已对新实例执行合成写读→独占重启→读回→精确删除；本课没有管理服务。入口复用安装者的loopback `127.0.0.1:13306` 与课程库 `golearn_m20`，不复用退役容器/数据卷。

## 阅读入口与运行

现在必须理解：`*sql.DB` 是共享连接池；`Conn` 是借来的特定连接；`Tx` 拥有事务连接；`Rows` 尚未完成时可能持池；`QueryRow` 的错误在 `Scan`；`Rows.Err` 与某行 `Scan` 错误不同；`context` 的取消通知与任务退出、资源释放是不同阶段。连接池容量规划、prepared statement 缓存、隔离级别与完整事务业务在后续课继续展开。

从仓库根运行，根测试不会进入本课的嵌套 module：

```powershell
Set-Location 'D:/IdeaProject/go-demo'
go -C lessons/m20_sql_pools run ./cmd/demo
go -C lessons/m20_sql_pools test -v ./...
go -C lessons/m20_sql_pools vet ./...

# 明确请求真实 MySQL 测试。结束后恢复环境变量原值。
$previousIntegration = $env:GOLEARN_INTEGRATION
try {
    $env:GOLEARN_INTEGRATION = '1'
    go -C lessons/m20_sql_pools test -v -count=1 -timeout 100s ./...
} finally {
    $env:GOLEARN_INTEGRATION = $previousIntegration
}
```

程序默认只读 `D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json`。用 `GOLEARN_SERVICE_INDEX` 可以指定另一份安装者提供的私有索引。索引中的 `services.mysql` 必须有 `state=READY`、`host`、`port`、`server_version`、`credential_file`；凭据 JSON 是 `username/password/database`，本次数据库为 `golearn_m20`。密码不通过命令行、不输出、不写报告。模块 `.gitignore` 已排除 `.local/` 和 `.secrets/`；仓库只交付加载代码，不复制共享凭据。

`LoadConfig` 只接收 loopback 转发和 `golearn_` 前缀课程库。安装/隧道由主 agent 统一持锁负责，消费模块不安装、升级、启动、停止服务，不写共享索引。非 READY 时入口明确失败，实库范围保持未完成。M00 的接入验证只证明接入地图/只读状态，不能替代 M20 的业务实证。

阅读顺序与文件分工：

| 文件/符号 | caller、职责与结果去向 |
| --- | --- |
| `go.mod/go.sum` | 独立 module `go-demo/lessons/m20_sql_pools`，Go1.27.0，锁定 mysql v1.10.1 与间接 edwards25519 v1.2.0 |
| `cmd/demo/main.go` 的 `main → execute` | 入口建立 Ctrl+C/75秒范围；`Run` 完成清理后才返回退出码 |
| `internal/course/config.go` 的 `LoadConfig → driverConfig` | 只读私有文件，校验端点与命名，内存中构造 driver 参数；没有凭据打印出口 |
| `internal/course/lesson.go` 的 `Run → openPool/create/drop` | 课程资源 owner：创建池/独占表，依次调用六个实验，最后独立预算 DROP 和 DB.Close |
| `internal/course/values.go` 的 `values → readProduct/quantity/scanFailure/listProducts` | repository 风格的借用者，参数化 SQL、Scan、Nullable、金额/时间映射、业务 SELECT |
| `internal/course/pools.go` 的 `connScope/rowsPool/txPool/rowsCancellation` | 连接作用域、池排队反例与修复、迭代取消，各 owner 负责清理 |
| `internal/course/sqlwait.go` 的 `sqlWait` | 独立三连接行锁实验；服务端 processlist 定位 SQL 阻塞，再核验取消/回滚与恢复 |
| `internal/course/task.go` 的 `startTask/join/stopAndJoin/observe` | 有界异步工作、错误结果交付、取消与独立 join、可观察屏障 |
| `internal/course/money.go` 的 `decimalCents` | 本课非负 DECIMAL(10,2) 文本转整数分，不经过浮点 |
| `internal/course/course_test.go` | 金额/标识符/提前返回收尾的辅助断言，以及显式开启的完整真实 MySQL 测试 |

完整正常调用链是 `main → execute → LoadConfig → Run → openPool(sql.Open) → PingContext → SELECT会话 → create → values → connScope → rowsPool → txPool → sqlWait → rowsCancellation → DROP/SELECT对象不存在 → DB.Close → execute返回 → main决定退出码`。每个 `db.Query/Exec/BeginTx/Conn` 先经 `database/sql` 借池；`tx.Query/Exec` 经 Tx 已持有的连接，不再借外层池；驱动接着执行 MySQL 网络协议。一个 Go 函数调用仍然同步等待结果，只有 `go func` 的 worker 另起 goroutine。

## 最小对象模型：池、连接、事务、结果集

| 对象 | 实际代表什么 | 谁拥有/释放 | 容易带来的 Java 误解 |
| --- | --- | --- | --- |
| `*sql.DB` | 可并发共享的长寿命池，内部管理零到多个物理连接 | 服务启动层/本课 Run 关闭；请求函数只借用 | 它更接近连接池 DataSource，不是一个 JDBC Connection |
| `*sql.Conn` | 从 DB 保留的一条特定连接，同一 session | 取得 Conn 的 owner 调用 Conn.Close 归还池 | Close 此 Conn 不等于关闭整个 DB |
| `*sql.Tx` | 绑定一条连接的一次事务，直到 Commit/Rollback | 开始事务的 owner；ctx取消也会触发 sql 的回滚路径 | 外层 DB 调用不会自动加入事务；没有 Spring ThreadLocal 事务隐式传播 |
| `*sql.Rows` | 正在消费的结果集，通常继续持有这次借来的连接 | QueryContext 的 caller；Close 或完整 EOF 自动结束 | Go 没有自动 lexical try-with-resources，必须在成功取得 Rows 后注册 defer |
| `*sql.Row` | 单行查询结果/延迟错误的包装 | Scan 读取第一行并收尾 | QueryRowContext 返回非 nil Row，不能仅看它就判断查询成功 |

`sql.Open("mysql", ...)` 首先选择已注册驱动、解析配置、构造池。本课在 `config.go` 使用具名 mysql 包以构造 Config；导入包的 `init` 会执行 `sql.Register("mysql", ...)`，不用同时添加空白导入。只有第一次 Ping/SQL/Conn 等借连接时才需要真实网络连接。`PingContext` 证明当次连接建立/鉴权及协议探测成功，不能证明表存在、权限足够、值映射正确或事务业务成立。[database/sql API](https://pkg.go.dev/database/sql)

`*sql.DB` 可以在多 goroutine 之间共享；它的“线程安全”不自动证明本课的 table/out/worker 状态也可以任意共享。worker经容量1的result channel交付`outcome`，另在wrapper所有清理之后关闭finished；owner收到finished再读取wrapper清理结果，日志writer只由owner使用。`Conn`/`Tx` 的特定连接作用域与 goroutine 数没有等价关系。

## 实验1：参数化 SQL、结果与业务事实

`values` 先在本 run 表插入两行：

| id | code/note | amount | created_at/shipped_at | quantity |
| --- | --- | --- | --- | --- |
| 1 | `o' OR 1=1 --;中文🙂` / NULL | `99999999.99` | `2024-02-29 23:59:59.123456` / NULL | 100 |
| 2 | `ordinary` / 空字符串 | `0.10` | 同一微秒时间 / 同一微秒时间 | 200 |

SQL形状是 `INSERT ... VALUES (?,?,?,?,?,?,?),(?,?,?,?,?,?,?)`，值作为 `ExecContext` 后续参数传入。`InterpolateParams=false` 让驱动通过真正 prepared 参数协议处理值；单引号、SQL关键词、中文和 emoji 仍然只是 code 的内容。随后 `SELECT COUNT(*)` 得2、`WHERE code=?` 精确匹配得1、读回 code 与原字符串完全相同。这些共同证明参数边界，没有靠“没报注入错误”猜测。

`?` 不能绑定表名。本课只在 `quotedTable` 接受 `m20_` + crypto/rand 生成的24位十六进制后缀后，才把表标识符放进SQL；列名反例只在两个本地常量间选择。`CREATE` 不用 `IF NOT EXISTS`，随机碰撞必须失败，不能认领已有对象。每次 run 的表独占，课程账号只有 `golearn_m20` 内权限；不创建库、不用旧 DeepHelp MySQL。

`Run`在CREATE之前先用information_schema SELECT证明随机名字不存在，成功CREATE后才认领并安排DROP。若CREATE返回错误，仍用独立预算SELECT对象实际是否存在：读回存在就清理本run刚尝试创建的对象；读回不存在则不DROP；无法读回时保留精确名字与未知结果诊断。API返回网络错误不能推导DDL没有执行。

调用与错误路径：

1. `values → db.ExecContext` 借连接并执行 INSERT，返回 `sql.Result`；`RowsAffected` 只说明驱动返回的行数，继续 `readProduct` 的 SELECT 才证明字段内容。
2. `readProduct → QueryRowContext(...).Scan(&row.ID, ...)` 把结果写进结构体字段。`&` 传的是目标地址，Scan 按列顺序转换；返回的 `product` 是结构体值副本，Nullable.Valid 和文本值都跟着复制。
3. `sql.Null[string]` 的 `Valid=false` 是 NULL，`Valid=true/V=""` 是空字符串；它们不应合并成 Go 空字符串。`sql.Null[time.Time]` 同理区分未发货与有效时间。
4. DECIMAL先Scan到字符串，再按 schema转整数分：`99999999.99 → 9999999999`、`0.10 → 10`。不要先Scan到float64再乘100；整数/小数扫描失败必须交回上层。
5. DATETIME(6) 没有时区；本课约定存UTC、session time_zone为`+00:00`，`ParseTime=true/Loc=UTC`把驱动结果解成time.Time。`loc`选择Go值的位置，不能替你把数据库会话时区设成UTC；本课两项都显式配置。`Time.Equal`验证微秒事实，不用格式相似猜测。
6. 两次同值UPDATE数量为101，默认`ClientFoundRows=false`时RowsAffected分别为1/0，但两次SELECT都为101。第二次0不等于没匹配行，它说明实际改变行数为0。
7. id999无行在Scan得到可识别`sql.ErrNoRows`，经`fmt.Errorf(... %w ...)`保留原因；它是业务“找不到”，不是SQL执行语法失败。
8. `scanFailure` 实际把NULL Scan到string、把code Scan到int64，得到真实转换错误。Scan错误不保证写进Rows.Err，owner必须保留Scan错误并关闭Rows；本课修复就是readProduct的Nullable/正确目标类型。
9. `listProducts` 使用 `for rows.Next()` 拉取、Scan、累计数量；循环后检查Rows.Err，再由defer处理Close错误。两行数量和必须为301，这是独立业务断言。

这些映射使用当前锁定的driver版本；driver配置、ParseTime与context行为以 [v1.10.1 README](https://github.com/go-sql-driver/mysql/blob/v1.10.1/README.md) 为入口，并核对已下载源码的 `packets.go` / `connection.go`，不把其他驱动或旧版本的行为混过来。

## 实验2：Conn保留特定连接

`connScope → DB.Conn(ctx) → Conn.QueryRowContext("SELECT CONNECTION_ID()")` 连续两次读服务端连接ID，它们必须相等且池InUse=1。Conn.Close后InUse=0，连接回池，DB仍可用。为了在这个时间点验证释放，正常分支显式Close；defer的`closed`标志只兜底提前返回，避免重复Conn.Close产生ErrConnDone。Rows.Close是幂等的，Conn.Close的重复错误契约不要混用。

生产代码只有确实需要session范围时才用Conn，例如同一连接上的会话变量。普通业务查询借用DB；事务查询使用Tx。把Conn或Tx保留时间拉长会减少可借容量，即使没有任何SQL正在运行，InUse也可能为1。

## 实验3：未关闭Rows → 借池timeout → Close后恢复

`rowsPool` 的危险版本只对两行结果调用一次Next/Scan，保留第二行未消费，不继续读到EOF，也没有立即Close。即使小结果已经被网络缓冲，本次Rows owner尚未释放，`database/sql`仍把连接算作InUse。本课显式MaxOpen=1，使这一条资源泄漏稳定暴露。

| 步骤 | 参与者、函数与实际等待点 | 状态与证明 |
| --- | --- | --- |
| 1 | owner执行QueryContext/Next/Scan | Rows持唯一连接，InUse=1；记录baseline Stats |
| 2 | worker A执行DB.ExecContext更新901 | DB.conn没有空闲连接且已达MaxOpen，在连接请求channel与ctx.Done之间等待；SQL尚未到服务器 |
| 3 | owner的observe查看WaitCount | 累计WaitCount精确+1后才继续；InUse仍1；该段WaitDuration尚未结算 |
| 4 | A的1500ms工作deadline到达 | DB.conn移除连接请求、累计等待耗时、返回DeadlineExceeded；owner用独立预算join得到退出事实 |
| 5 | owner启动worker B查quantity | 同样先等WaitCount再+1；不是sleep猜worker已执行 |
| 6 | owner调用Rows.Close | 关闭/消费结果收尾并交还连接，B从借池等待继续执行SELECT |
| 7 | owner join B后断言 | B读到101，WaitCount累计delta=2，WaitDuration不回退（第二段允许0），InUse=0 |

owner的Rows.Close与B的SQL执行之间由连接归还机制建立因果关系。B的日志先后和精确毫秒耗时不是语言保证。A的deadline说明它在等待连接时退出，最后B读回101才进一步证明实际表没有被改成901。

修复版本就在`listProducts`和`rowsPool`的Close路径：成功取得Rows后立即defer Close；需要提前结束就显式Close；完整EOF也会自动关闭，但仍必须检查Rows.Err。不要把defer放在长寿命循环的外层函数中积累成百上千个未关闭Rows。也不要用“把MaxOpen调大”掩盖泄漏：只是把耗尽推迟。

## 实验4：Tx持连接后又从DB借连接

| 步骤 | 参与者与调用 | 连接/业务事实 |
| --- | --- | --- |
| 1 | owner `DB.BeginTx → tx.ExecContext` | Tx拿唯一连接，在事务中把101改为777，尚未Commit |
| 2 | 错误worker `DB.ExecContext` 想改888 | 外层DB另借连接，MaxOpen=1，所以在池等待；不会自动复用Tx连接 |
| 3 | owner观察WaitCount+1、join1200msdeadline | 工作退出，InUse仍1，因为Tx还活着 |
| 4 | 修复`tx.QueryRowContext` | 同一Tx/连接读到自己未提交的777；WaitCount不再增加 |
| 5 | owner Rollback，再`DB`查询 | 原事务结束/连接交还，持久SELECT为101，错误DB写入和Tx未提交写入都没留在表里 |

在生产代码中，同一usecase必须明确把`*sql.Tx`或一个统一的SQL执行接口传给repository。池变大后错误DB调用可能真的执行，并在另一个事务提交；从“死等”变成“事务外副作用”并不算修复。本课用MaxOpen=1暴露这种调用关系错误，正确修复是继续使用当前Tx。

`defer rollback(tx)`注册的是该Tx owner的最后收尾；成功Commit或已经Rollback之后ErrTxDone可识别为已经结束，不重新撤销已提交业务。BeginTx的ctx取消会触发`Tx.awaitDone`回滚，但owner仍保留显式收尾并核对持久事实。普通error按返回值沿caller逐层上报，不是Java异常自动跨goroutine传播。

## 实验5：连接已取得，SQL在服务端等行锁

前面两种卡住都在DB借池；这一实验必须证明SQL真的到MySQL。`sqlWait`建立三个池、最多三条连接，全部同一个课程账号和同一独占表：

| 参与者/owner | 当前操作与等待 | 停止与收尾 |
| --- | --- | --- |
| owner/holder Tx，用current.db | `SELECT ... WHERE id=1 FOR UPDATE`持InnoDB行锁；SQL已返回，Tx/锁仍活着 | 正常/失败都Rollback解除行锁 |
| worker goroutine，用workerDB | 自己取得Conn/Tx，再Exec带`m20_io_lock`标记的UPDATE；等待MySQL响应，work ctx最多3秒 | deadline驱动取消，defer Rollback/Conn.Close；result交回主/清理错误，wrapper最后关闭finished |
| owner，用observerDB | 按worker服务端连接ID查询自己的PROCESSLIST | 观察标记UPDATE之后才认定SQL阶段；最后关闭观察池 |

具体时间线：

1. holder锁定id1；worker通过自己的池取Conn并开始Tx，读取CONNECTION_ID，通过容量1 channel交给owner。
2. worker执行`/*m20_io_lock*/ UPDATE ... SET quantity=404 WHERE id=1`。未Commit的独立Tx因holder持锁不能完成UPDATE。
3. owner读`information_schema.PROCESSLIST`，必须看到该ID正在执行标记UPDATE；同时workerDB InUse=1、WaitCount=0。因此这里不是借池排队，是已进入真实SQL协议后等响应。实际State字符串只记录观察值，不能凭其名字假设所有MySQL版本一样。
4. worker工作deadline到达，mysql driver watcher观察ctx.Done，缓存取消原因并cleanup关闭连接。Exec返回DeadlineExceeded。context支持是驱动契约；不支持context的其他驱动未必立即中断I/O。
5. owner独立预算join获得worker已执行defer并返回的事实。随后Rollback holder释放服务器行锁；提前失败的defer也先cancel、解除holder锁、再用新预算join。
6. owner不把客户端错误当成服务器已经停止：用独立8秒清理ctx轮询该连接ID消失。最后SELECT量仍101、workerDB InUse=0，新连接SELECT1成功。

本课从不Commit worker事务。即使SQL执行与取消发生竞争，UPDATE也可能到过“执行了但未提交”的阶段；连接真正关闭/事务回滚后没有持久404，是最后SELECT证明的事实。对于已经发送的autocommit UPDATE或Commit，网络错误可能留下未知业务结果，不能根据error直接重试或断言没执行；完整Commit窗口留给M21。本实验使用Tx、服务端结束屏障和读回事实，把证明范围限定清楚。

MySQL没有给课程账号全局PROCESS权限；官方规则允许非匿名用户在PROCESSLIST看到自身连接，足够定位本课的三个连接。[MySQL PROCESSLIST权限](https://dev.mysql.com/doc/refman/8.4/en/information-schema-processlist-table.html)

## 实验6：Rows迭代取消与Rows.Err

`rowsCancellation`先用独立queryCtx取得两行结果，读第一行后cancel。cancel只是关闭queryCtx.Done；`Rows.awaitDone`之后观察通知并Close结果/归还连接。owner以Stats InUse=0作为资源状态屏障，再调用Next，必须为false，Rows.Err必须可识别为Canceled。这样不会把“已经发取消”误解为“watcher已经执行完”。随后DB重新借池SELECT到quantity101，证明只读取消没有改变业务表且池恢复。

`Next=false`也可能只是EOF；只有检查Rows.Err才能区分正常结束和取消/网络/驱动迭代错误。取消后的预期Rows.Err作为实验观察/断言保留，不把这个已验证的预期错误再当成正常入口失败；Close自己的真实收尾错误仍返回。

## Stats、容量和预算怎么读

| 设置/指标 | 本课值与依据 | 不能从它推出什么 |
| --- | --- | --- |
| MaxOpenConns | 每池1，稳定暴露持连接反例；SQL扩展最多3个池/3连接 | 不是生产建议，生产应按服务端预算、实例/副本数量和并发I/O实测分配 |
| MaxIdleConns | 1，允许课程顺序SQL复用连接，不在每个查询后重建TCP | 空闲数不是正在使用数，空闲池上限也不是最大总连接数 |
| ConnMaxLifetime | 3分钟，在常见5分钟中间件回收之前；本课不验证远端中间件5分钟规则 | 不强制杀死活跃查询，不替代请求deadline；值需按实际服务配置调整 |
| ConnMaxIdleTime | 30秒，课程结束前/空闲时避免长留闲连接 | 不证明回收线程恰好30秒执行；短实验未专门测清扫器 |
| InUse | 被Rows/Conn/Tx/当前SQL占用的连接数 | 不是“服务器正在执行SQL的个数”，事务空闲时也持连接 |
| WaitCount | 池累计发生连接等待的次数；每个独占单worker实验断言delta | 不是当前waiter数量；SQL服务端等锁不计入它 |
| WaitDuration | 已结算连接等待的累计耗时 | 不是当前SQL耗时、平均延迟，也不是每个timer的精确预算 |
| root ctx | 75秒，覆盖正常工作与Ctrl+C协作取消 | 不能把已取消的ctx拿来join清理 |
| pool worker ctx | 1500ms/1200ms，SQL worker3秒 | deadline包含实际开始前的借池/连接/SQL工作，不是单独服务器执行时间 |
| join/表清理ctx | 新Background子ctx，5秒join/8秒SQL/表核验 | 新预算不是永不结束；超限会返回明确清理错误并保留主错误 |
| driver网络预算 | 建连3秒/read8秒/write3秒，SQLctx更短 | ReadTimeout属于socket I/O；本课SQL故障实际先触发context deadline，不冒充独立socket ReadTimeout实验 |

WaitCount在Go1.27源码中加入连接请求时增加；WaitDuration在等待因ctx取消或取得连接结束后累加。本课只在完全由自己拥有的池里一次启动一个worker，才能精确断言delta=1/2。看见WaitCount后立即归还连接时，本次等待可能记录为0；不能据计数推导每段都有正的可测耗时。第一次1500ms排队deadline提供正等待证据，第二次成功恢复断言累计耗时不回退与实际SELECT/InUse事实。公共生产池的Stats可能混入别的请求，要按时间窗口解释。[管理连接](https://go.dev/doc/database/manage-connections)

## 失败、取消和最终资源结局

`startTask`经`startOwnedTask`创建workCtx、容量1的result和独立finished广播。fn返回时只代表fn自己的Rows/Tx/Conn defer已经完成；wrapper发送result之后仍需执行自己的附加cleanup、cancel释放timer。wrapper最早注册`defer close(finished)`，因LIFO它最后执行，finished关闭才代表全部这些收尾已经结束。

`join`先以独立预算等finished，再取result或此前消费到pending的结果，合并wrapper清理错误后才能写joined缓存。result的一次接收可被消费，finished关闭不可消费，后续owner/defer可以重复观察。同样是“channel可读”，两者表达的事实不同。`sqlWait`提前分支只把收到的result存进pending，不标记已join；它的defer仍独立预算等待finished。`stopAndJoin`判断的是joined缓存，不把pending当成已经完成，因此主/工作/清理错误都能保留。

确定性`TestResultReadyStillWaitsForWrapperCleanup`先取消工作并消费result，wrapper附加cleanup却停在releaseCleanup门。测试通过`finishedProbeContext.Done`求值屏障证明owner已经进入join的Finished选择，再断言owner不能返回；放门后才允许cleanup返回、cancel执行、finished关闭和owner返回。测试用两层合成清理错误验证错误链，故障门只用于测试，所有失败分支也放门、取消并独立预算join。没有固定sleep，也没有把“结果就绪”当作“最后defer完成”。

每个startTask后立即注册stopAndJoin。若owner在Stats/SQL/断言处提前return，defer取消worker、用新的5秒预算join，并用`errors.Join`保留原业务错误、未读worker错误和资源清理错误。SQL行锁实验还在join前解除自己持有的holder锁。没有worker需要等待一个永远不会再set/close的外部屏障，也不要求owner先读一个无缓冲结果才让worker退出。

清理层级是 `实验Rows/Conn/Tx → worker cancel/解除故障/join → 观察连接结束 → DROP本run表 + SELECT对象不存在 → DB.Close`。DB的owner是Run，repository不能关闭它；真实服务与SSH隧道的owner是安装者，课程结束只归还客户端资源和自己的表。`main`等execute返回后才可能os.Exit，保证defer已执行。

`defer`不是块作用域finally，它在当前Go函数返回时执行。`defer func(){ err=errors.Join(err,Close()) }()`闭包读取退出时的具名err槽，才能保留中途错误与Close错误；把`defer closeAndRecord(err)`写成普通参数会在注册时求值，可能捕获当时的nil。本课变量shadowing只在明确局部作用域使用，返回错误仍传回真正的owner。

## 当前版本源码定位与证明边界

实际工具链路径是`D:/Go/src/database/sql/sql.go`；依赖下载后位于`C:/Users/Lenovo/go/pkg/mod/github.com/go-sql-driver/mysql@v1.10.1/`。用下面命令定位当前符号，不依赖网页浮动行号：

```powershell
rg -n 'func Open\(|func \(db \*DB\) conn|waitCount\+\+|waitDuration.Add|func \(tx \*Tx\) awaitDone|func \(rs \*Rows\) awaitDone|func \(rs \*Rows\) Close|func \(r \*Row\) Scan' D:/Go/src/database/sql/sql.go
rg -n 'func init|sql.Register|func \(mc \*mysqlConn\) ExecContext|watchCancel|startWatcher|func \(mc \*mysqlConn\) cancel' C:/Users/Lenovo/go/pkg/mod/github.com/go-sql-driver/mysql@v1.10.1/driver.go C:/Users/Lenovo/go/pkg/mod/github.com/go-sql-driver/mysql@v1.10.1/connection.go
```

源码路径的完整关系是 `sql.Open → mysql.MySQLDriver.OpenConnector → sql.OpenDB`；实际DB调用 `db.conn → mysql connector.Connect → ExecContext/QueryContext → watchCancel → 网络协议`；Rows结束 `Rows.Close → driver.Rows.Close → releaseConn`；Tx取消 `Tx.awaitDone → rollback → releaseConn`；driver取消 `startWatcher观察Done → mysqlConn.cancel → cleanup`。其中对象契约来自标准库/驱动文档，连接请求队列与统计结算位置是当前Go1.27实现，服务端State/精确耗时来自本次运行观察。不要把这三层混成跨版本绝对承诺。

真实SQL与池故障由默认demo/TestMySQL验证；金额与标识符纯函数、提前返回channel收尾由辅助单测验证。默认`go test`会清楚SKIP TestMySQL；只有显式integration测试及真实运行日志才算实库证据。`-race`也需要按M00契约读取主目录`toolchain.json`，只在进程环境设置CC/CXX/PATH/CGO_ENABLED，不安装编译器、不改全局go env；实际结果及完整命令见STATUS。

## 实际输出的阅读方法

以下是Finished修复版默认入口本次真实输出的关键行，表名为`m20_b1569e66c691934c90bc86a6`，不是虚构预期：

```text
[sql.Open] open=0；只构造池，尚未验证网络
[真实会话] server=8.4.11 database=golearn_m20 time_zone=+00:00 max_connections=32
[SELECT] id1 code="o' OR 1=1 --;中文🙂" note.Valid=false amount=99999999.99 cents=9999999999 created=2024-02-29T23:59:59.123456Z shipped.Valid=false quantity=100
[SELECT] id2 note.Valid=true note="" amount=0.10 cents=10 shipped=2024-02-29T23:59:59.123456Z；total=2 exact-match=1
[UPDATE] RowsAffected=1 SELECT quantity=101
[UPDATE] RowsAffected=0 SELECT quantity=101
[Rows正常修复] count=2 quantity-sum=301 Rows.Err=nil；EOF/Close释放连接
[Conn] connection_id=43/43 InUse=1；owner关闭Conn后归还池
[Stats Rows排队屏障] open=1 inuse=1 idle=0 WaitCount=1 delta=1 WaitDuration=0s delta=0s
[Stats Rows借池timeout] open=1 inuse=1 idle=0 WaitCount=1 delta=1 WaitDuration=1.5000467s delta=1.5000467s
[Stats Rows.Close恢复] open=1 inuse=0 idle=1 WaitCount=2 delta=2 WaitDuration=1.50523s delta=1.50523s
[Stats 改用Tx查询] open=1 inuse=1 idle=0 WaitCount=3 delta=1 WaitDuration=2.7052949s delta=1.2000649s
[Tx修复/副作用SELECT] Tx内=777；Rollback后DB SELECT=101；错误DB UPDATE未改变持久值
[SQL屏障] connection_id=44 processlist.State="updating" UPDATE已到MySQL；workerDB InUse=1 WaitCount=0
[SQL取消/副作用SELECT] error=DeadlineExceeded；worker Rollback/Conn.Close完成；processlist连接消失；quantity=101；新连接SELECT 1=1
[Rows取消] 已读取id=1；cancel→Rows.awaitDone→InUse=0→Next=false/Err=Canceled；SELECT quantity=101
[清理] DROP 本 run 表；information_schema SELECT remaining=0
[DB owner] Close 后 open=0 inuse=0 idle=0
```

输出从`[sql.Open] open=0`开始，随后才出现真实会话/独占表。`[SELECT]`两行是业务事实，`[UPDATE] 1/0`要与读回101一起看。`[Stats Rows排队屏障]`的WaitCount先增加、WaitDuration尚不增加；`timeout`后等待耗时计入，Rows仍占InUse=1；Close修复后第二worker读取101、InUse=0。Tx实验的池等待delta=1，Tx查询没有追加等待；最后回滚SELECT恢复101。`[SQL屏障]`必须有服务端连接ID、UPDATE可见、worker池WaitCount=0；最后取消/服务端结束/SELECT101/新连接恢复的记录共同证明该实验成立。

程序的最终清理日志必须有`remaining=0`和`DB owner Close 后 open=0 inuse=0 idle=0`，命令必须退出0。这些行缺失或实际错误非预期时，STATUS保持未完成；不通过修改“预期输出”掩盖故障。随机表后缀、连接ID、精确WaitDuration与服务端State属于本次观察，不保证复跑字面一致。
