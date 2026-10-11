# M22：MySQL 隔离、行锁、死锁与并发库存

这课从“库存只有一件，为什么两笔订单都成功”开始。你会用真实 MySQL 的两条独立会话固定复现超卖，再对照三种修复；随后看普通快照读、锁定读、索引范围、死锁、锁超时，以及事务里等待慢 HTTP 为什么会把数据库锁一起拖住。正常入口包含有界的危险实验，只有预期错误、持久业务状态和资源收尾都符合断言，整课才返回成功。

实际环境是 Go 1.27.0/windows/amd64、MySQL 8.4.11、`github.com/go-sql-driver/mysql v1.10.1`。每次运行只在安装者已经验收的 `golearn_m20` 创建四张 `m22_<24位随机hex>_{stock,orders,ranges,scan}` 表；结束精确 DROP 并逐张查不存在。不会创建数据库、开启全局诊断或更改其他模块的数据。

## 15 分钟核心实验

程序执行时间用于真实 SQL 和等待预算；15 分钟主要用于顺着源码和输出理解 E01。没有额外 exercise。

| 阅读时间 | 行动与符号 | 需要预测的行为 |
| --- | --- | --- |
| 0～3 分钟 | 运行 stock 入口，读 `badStock` | 两 Tx 普通读都得到1；行锁串行化扣减，库存仍会变成-1 |
| 3～7 分钟 | 读 `place` 的 atomic/locking 分支 | 条件 UPDATE 在当前值判断；FOR UPDATE 将读后检查保护到 Tx 结束 |
| 7～11 分钟 | 读 cas 分支、`safeStock` 重试 | version 是局部读值；影响0行必须退出旧 Tx，再重新读/执行完整事务 |
| 11～15 分钟 | 读 `audit`、唯一键1062、`quantityCase`、owner | 12次请求只成功5笔；qty2守恒/qty0拒绝；结果不是资源退出屏障 |

仓库根目录可以执行：

```powershell
go -C lessons/m22_mysql_locks_isolation run ./cmd/demo -part stock
go -C lessons/m22_mysql_locks_isolation run ./cmd/demo
go -C lessons/m22_mysql_locks_isolation test -v -count=1 -timeout 35s ./...
go -C lessons/m22_mysql_locks_isolation vet ./...
```

默认 `cmd/demo` 执行 E01～E06 全部真实实验，`-part stock` 只执行核心 E01。缺少真实服务时失败，不降级成 fake SQL。默认服务索引是 `D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json`；`GOLEARN_SERVICE_INDEX` 仅覆盖索引路径。程序再内存加载索引中的私有 credential_file，不把密码或 DSN 放进环境命令、README、日志。

默认测试明确 SKIP `TestMySQL` 和真实DDL归属断言，执行原五个资源/错误分类断言及真实loopback HTTP提前关闭断言。完整实库测试要显式开启：

```powershell
$m22SavedIntegration = $env:GOLEARN_INTEGRATION
$m22HadIntegration = Test-Path -LiteralPath Env:GOLEARN_INTEGRATION
try {
    $env:GOLEARN_INTEGRATION = '1'
    go -C lessons/m22_mysql_locks_isolation test -v -count=1 -timeout 110s ./...
} finally {
    if ($m22HadIntegration) {
        Set-Item -LiteralPath Env:GOLEARN_INTEGRATION -Value $m22SavedIntegration
    } else {
        Remove-Item -LiteralPath Env:GOLEARN_INTEGRATION -ErrorAction SilentlyContinue
    }
}
```

本课是独立嵌套 module，根 `go test ./...` 不进入这里。执行实证和最终命名空间见 [STATUS.md](STATUS.md)。

## 从入口读到 SQL 与真实副作用

| 文件/关键符号 | caller → 职责 → 结果给谁 |
| --- | --- |
| [cmd/demo/main.go](cmd/demo/main.go) `main` | 创建80秒总预算 → 调用 `Run` → 只有全部收尾后才输出成功/Exit |
| [lockcourse/runtime.go](lockcourse/runtime.go) `openPool/readJSON/acquire/session.release` | Run/worker → 只读配置、真实 connector、固定物理会话与session恢复 → DB借给实验，Conn归唯一owner |
| 同文件 `transact/rollback/retryable/code` | 业务 callback → 显式Tx、Commit、失败Rollback、错误树分类 → 正常结果/原失败/UNKNOWN/清理失败 |
| [lockcourse/course.go](lockcourse/course.go) `Run/newStore/create/createWithBoundary/drop/audit/waitSQL/explain` | 入口 → 随机专属表、CREATE尝试预登记、独立查账、PROCESSLIST、计划断言 → 可核对的持久事实与清理 |
| [lockcourse/stock.go](lockcourse/stock.go) `stockCases/badStock/safeStock/place/quantityCase` | E01 → 危险库存算法、三种修复与数量边界 → worker报告与独立订单/库存对照 |
| [lockcourse/isolation.go](lockcourse/isolation.go) `snapshotOne/indexCases/lockProbe` | E02/E03 → 两会话RC/RR、范围/唯一/缺索引 → 读值、1205/成功、EXPLAIN与实际表内容 |
| [lockcourse/deadlock.go](lockcourse/deadlock.go) `deadlockCase/decrement/timeoutCase` | E04/E05 → 首锁屏障、等待环、整Tx重试、statement超时 → 1213/1205和业务守恒/恢复锁 |
| [lockcourse/external.go](lockcourse/external.go) `startSlow/request/externalCase/slowService.close` | E06 → 真实loopback HTTP门控、持锁反例、preflight修复 → 竞争SQL结果、Body语义、HTTP/DB收尾 |
| [lockcourse/owner.go](lockcourse/owner.go) `scope/start/task.join/captureTaskPanic/gate` | 每组实验 → 立即登记任务、解除自有门、取消、最终join → 工作结果/清理错误/Finished分别可见 |
| [lockcourse/course_test.go](lockcourse/course_test.go) | testing → 显式实库开关、结果/退出边界、早退、超时、panic、分类优先级 → 不把辅助测试冒充MySQL效果 |
| [lockcourse/rework_test.go](lockcourse/rework_test.go) | 主审返工证明 → 真实DDL应用确认未知/已有名字拒绝、真实HTTP强制停止后的handler尾部 → 所有权与提前失败收尾 |

核心调用链：

```text
main → Run
  openPool → readJSON(index/credential) → mysql.NewConnector → sql.OpenDB
  newStore → create → 确认精确名字不存在 → 登记本次尝试owner → CREATE
  stockCases
    badStock → scope.start ×2 → acquire → transact → 普通SELECT → ready → gate.wait
      owner收两次ready → gate.open → 无条件UPDATE → INSERT → Commit
      task.join(先Finished，再result) → audit(DB独立SELECT库存/订单)
    safeStock(atomic/locking/cas) → 两worker共12个key
      place → transact → 一种库存方案 → RowsAffected → INSERT → Commit
      CAS确认回滚后重试完整place → 最终audit → 真1062/回滚对照
  snapshotCases → snapshotOne(A普通读/B提交/A普通与锁定读)
  indexCases → explain → lockProbe(holder/另一Conn探针) → 1205或成功 → 独立SELECT
  deadlockCase → 首UPDATE屏障 → A真实等待 → B闭环 → 1213 → 新Tx按30→31
  timeoutCase → 先改41再等40 → 1205 → 同Tx仍读到首步4 → 显式Rollback → 新Tx锁恢复
  externalCase → 真HTTP handler门控 → 持Tx坏版/先preflight修复 → SQL/Body/handler确认
  deferred drop(新Background预算) → 每张表remaining=0
  deferred DB.Close → Stats open/inuse/idle=0
```

`store` 是共享 `*sql.DB` 指针的借用者；`sql.DB` 是连接池，**不是当前事务**。每个 worker 的 `session.conn` 是一条固定连接，`transact` 产生的 `*sql.Tx` 绑定这条连接。callback 中只用 Tx 执行业务 SQL；调用外层 DB 不会因当前 goroutine 已有 Tx 自动加入事务。M21 已验收显式Tx、defer/panic和Commit结果未知，本课按那套接口契约延伸，没有导入其 internal、复制完整前置工程或重跑前置实验。

现在必须理解：局部读值和数据库当前值的区别、锁的连接/事务归属、条件SQL/影响行数、普通读与当前读、错误码与Tx重试范围、channel通知与Finished、defer的LIFO与具名结果。索引页内部结构、purge实现、生产容量、复制耐久性、跨服务发货协议在需要相关源码时再查，本课不把它们伪装成已完成实证。

## E01：超卖反例与三种修复

库存和订单有两个独立不变量：库存不能为负；成功销售的 qty 总和必须等于初始库存减剩余库存。订单 request_id 是主键，重复请求不能产生第二条同key订单，失败也不能留下第二次扣减。本课并发批量请求均qty=1，观察窗口是两个 worker、12 个请求、初始5件；另有真实qty=2与非法qty=0边界对照，不声称这是生产任意负载证明。

坏例子的时间线由 channel 门控制：

| 顺序 | 参与者/等待点 | 数据或资源 | 为什么进入下一步 |
| --- | --- | --- | --- |
| 1 | A/B `acquire → BeginTx` | 两个不同 CONNECTION_ID，都是RC | owner已登记两个任务与退出清理 |
| 2 | A/B `QueryRow(stock).Scan` | 各自局部 available=1；普通读没锁住库存行 | 每方发送ready后停在 `goUpdate.wait` |
| 3 | owner `receive` 两次 | 确认双方都完成读，不以sleep猜先后 | owner广播open |
| 4 | A/B 无条件 `UPDATE stock=stock-1` | 两条UPDATE仍会互等行锁，但不再检查stock>=1 | 第一笔扣成0，第二笔等锁后扣成-1 |
| 5 | A/B INSERT/Commit/各自defer | 两笔独立订单提交；Tx/Conn结束 | wrapper最后close Finished |
| 6 | owner join/audit | stock=-1，orders=2，qty=2，unique=2 | 真实持久内容证明反例，不能解释成“第二笔只是日志成功” |

关键错误是 check 和 update 没有受同一原子规则保护。行锁只保护 UPDATE 正在做的修改，不会替你重新执行早先 Go 变量上的判断。用 `sync.Mutex` 包在本进程外层也不能保护另一服务实例发来的 SQL。

| 方案与 `place` 分支 | 保护规则 | 本课的业务观察 | 选择边界 |
| --- | --- | --- | --- |
| atomic | `UPDATE ... SET stock=stock-? WHERE id=? AND stock>=?`，两个数量参数都为qty | 竞争者持锁后按当前值判断；0行是NoStock；成功5笔 | 单行可表达的不变量通常最直接；`place`在Begin前校验qty>0，更新依据stock>=qty |
| locking | `SELECT stock ... FOR UPDATE` → Go判断 → UPDATE → INSERT → Commit | 第二方等到锁后读新库存；锁保持到Tx结束；成功5笔 | 多条决策/关联记录可用，但必须缩短持锁窗口与一致加锁顺序 |
| cas | 普通读stock/version → `WHERE stock>=? AND version=?`，成功同时扣qty/version+1 | 固定屏障令两Tx读version0，至少一方affected=0；退出旧Tx重新读；最终成功5笔 | 冲突低/不想预先阻塞的场景；version本身不解决多行不变量，需要有界整事务策略 |

`sql.Result` 是驱动提供的接口结果，`RowsAffected()` 返回的是当前 SQL 影响多少行，尚未证明 Commit 成功。三种方案都把库存扣减与订单 INSERT 放同一个 Tx。独立 `audit` 再确认 stock=0/orders=5/qty=5/unique=5，并将 worker 的 committed_success=5 与它对照。CAS实际冲突次数由调度与负载决定，本课只保证屏障引出至少一次，不固定某个次数。

唯一键测试不是“再打一次看没报错”：查询这次已经持久存在的订单key，在另一个库存1的独占SKU中尝试扣减并插同key，真实 INSERT 返回1062；退出Rollback后该SKU依然库存1、订单0。它证明重复订单错误不会留下扣减，不能把“唯一键阻止第二条订单”单独当成库存守恒。

CAS只重试确定未提交的 ErrCAS。`retryable` 优先排除 ErrUnknown 和 txCleanupFailure；error树含预期原因不能掩盖清理错误。重试16次和请求ctx是上限；预算耗尽要失败，不无限循环。库存不足是业务结果；1062是数据库唯一约束结果；二者不应混作网络故障。

`quantityCase`再用初始stock3买qty2：第一笔成功后stock1、订单1、总qty2；第二笔再买qty2影响0行，库存/订单不变；qty0在Begin前被拒绝。`quantity.guard.SELECT stock=1 orders=1 qty=2 unique=1`证明守恒按销售数量而非仅按订单条数计算。qty是函数参数int值副本，SQL中的`?`绑定该值；不能用负数qty把扣减变成加库存。

## E02：RC/RR 普通读与锁定读

`snapshotOne` 使用 `SET SESSION transaction_isolation` 后查询实际 `@@transaction_isolation`。`Conn.BeginTx` 使用默认选项，所以事务确实沿用这项会话设置。直接给 `BeginTx` Isolation 选项时，当前driver会发 `SET TRANSACTION ISOLATION LEVEL` 设置下一事务；不能用“会话变量依然RR”否认那个事务的RC选项。这里显式会话方案避免这项测量歧义，退出会恢复RC。

| 阶段 | A当前函数/状态 | B行为 | RC实际值 | RR实际值 |
| --- | --- | --- | --- | --- |
| 1 | A Tx普通SELECT，stock初始10 | B尚未修改 | 10 | 10；建立普通读read view |
| 2 | A没有结束 | B另一Conn更新20并Commit返回 | 无锁冲突 | 无锁冲突 |
| 3 | A第二次普通SELECT | B提交已确认 | 20；每次一致性读取新快照 | 10；同Tx普通读继续先前read view |
| 4 | A `SELECT FOR UPDATE` | B已结束 | 20 | 20；锁定读读取当前版本并加锁 |
| 5 | A再次普通SELECT，没有自己写该行 | B不再改 | 20 | 仍10；锁定读不会自动替换先前普通快照 |

日志精确输出 `plain=10 -> B COMMIT20 -> plain=20 locking=20 plain_after_lock=20`（RC）与 `plain=10 ... plain=10 locking=20 plain_after_lock=10`（RR）。计划在实际行已存在后取得并断言 `key=PRIMARY/access_type=const`。A和B都是真实独立连接；B提交返回才允许A继续，没有要求A/B由不同OS线程执行。

RR 普通读能在 B 已提交后仍看到旧10，是本课对历史版本可见性的实证。长期保持 read view 可能让 InnoDB 仍需保留这些读者所需的 undo 历史、增加清理压力；本课没有读取purge计数或测磁盘增长，不把这条原理说成已测的容量数据。把一个慢网络调用放进长期Tx，还可能同时拖住锁和连接。

Java经验可帮助你想到JDBC Connection的事务边界，但Spring/JPA不会出现在这段Go调用链里。MySQL的RR普通快照与锁定当前读混合行为要按InnoDB解释，不能将PostgreSQL RR/Serializable的规则或JPA锁枚举直接当成这里的锁覆盖范围。

## E03：gap、唯一索引、缺索引必须带条件解释

初始 ranges 主键是10、20、30；holder在自己的Tx内完成锁定读，随后另一连接尝试INSERT15。`lockProbe` 先读完并Close Rows，只释放结果协议；Tx仍然持锁。探针的最终Finished完成之前，holder绝不正常释放。失败路径有scope清理兜底。

| holder查询与实际条件 | 计划 | holder未释放时探针 | 最后实际表内容 |
| --- | --- | --- | --- |
| RR，`id>=10 AND id<20 FOR UPDATE` | PRIMARY/range | INSERT15真实1205 | id15_count=0 |
| RC，相同范围SQL | PRIMARY/range | INSERT15真实成功 | id15_count=1 |
| RR，`id=10 FOR UPDATE`，已存在唯一主键 | PRIMARY/const | INSERT15真实成功 | id15_count=1 |

第一行符合RR范围搜索的next-key/gap保护：扫描的索引范围会阻止其间隙插入。第三行是唯一索引精确命中已有记录，主要锁记录；它不应被解释为“只要RR FOR UPDATE就锁整个附近间隙”。RC一般不为这类普通范围搜索保留gap锁，但外键检查/重复键检查等仍可能需要gap相关锁；本课没用外键，不推广成“RC从无gap”。找不到的唯一键、复合索引只用部分列、不同范围、不同计划也不在第三行的实证条件中。

scan 表初始 `(id,bucket)=(10,1),(20,2),(30,3)`。没有bucket索引时，RR holder查询 `WHERE bucket=1 FOR UPDATE` 的实际 `access_type=ALL`；即使WHERE不匹配id30，扫描锁范围仍使另一会话 `UPDATE id30` 返回1205。随后本课在自有表创建 ix_bucket，并在教学查询显式 `FORCE INDEX(ix_bucket)`：计划为 `key=ix_bucket/access_type=ref`，相同holder持锁时id30更新实际成功，最后value=1，索引元数据为ix_bucket,PRIMARY。

小表优化器可能仍选全扫，因此这里明确用hint固定修复实验条件，再断言EXPLAIN。生产中应以真实数据分布、计划和业务SQL核对索引，并非一律加hint。锁作用在扫描的索引记录/范围，而不是Go里一个抽象“业务对象”。本课没有开启performance_schema，也没有直接dump data_locks；gap和全扫范围的结论来自官方规则、实际计划、holder事务未释放、真实错误/成功与持久表内容的组合证据。

## E04：稳定死锁与整事务重试

本实验有30/31两SKU，初始各5。每笔订单包含两SKU各1件，订单qty=2；orders.sku=30是本实验的分组标识，第二SKU也单独查询库存，不能只查订单分组那一行。

| 顺序 | 参与者/具体等待点 | 状态/证明 |
| --- | --- | --- |
| 1 | A Tx `decrement(30)`，B Tx `decrement(31)` | 首UPDATE都返回后ready；A持30/B持31 |
| 2 | owner仅放A门；A `decrement(31)` | owner从本A的PROCESSLIST看到带随机marker的实际UPDATE；B仍持31 |
| 3 | owner放B门；B `decrement(30)` | B等A的30，A等B的31；构成真实等待环 |
| 4 | InnoDB选择victim | 某一worker得到真实1213/SQLSTATE40001；victim身份不固定 |
| 5 | `transact`退出清理；重试worker开新Tx | 整个旧Tx已被InnoDB回滚；应用也结束本地Tx；原request_id保持不变 |
| 6 | retry按30→31扣两行并插订单 | 不再使用相反顺序；最多3次，UNKNOWN/清理失败立即停止 |
| 7 | 两worker Finished，owner独立audit | 每SKU剩3；两订单/总qty4/unique2；初始总10−剩6=销售4 |

实际日志保留完整driver错误，例如 `Error 1213 (40001): Deadlock found ...`，不是自己构造“deadlock”字符串。本课固定只制造一轮环，所以断言两次最终提交、恰一次1213重试。victim第一次成功的首扣也已回滚，必须重做 `Begin → 首扣 → 次扣 → 订单 → Commit`；只重试第二条UPDATE可能漏首扣或漏订单，总量审计会失败。

一致加锁顺序是所有调用方约定共同遵守的规则；本课重试使用30→31，而首次相反顺序是明确反例。生产中按稳定业务/主键顺序获取多行锁，缩短锁窗口、准备必要索引，并保持重试有上限。竞争复杂时通常需要有界退避/jitter；本课两worker确定性实验不引入sleep猜调度。

整Tx重试的条件是确认旧Tx没有成功提交，且业务动作可幂等。数据库唯一key保证不会多插一条同key订单，事务回滚保证库存不重复扣；RPC发货、扣款、发送消息并不随MySQL Rollback撤销。本课重试体内没有外部副作用。若Commit通信错误，M21说明它可能已经提交，必须以原key查权威业务事实，不能因“像临时故障”直接再卖一次。

## E05：1205锁等待超时不同于1213死锁

每个物理会话的 `innodb_lock_wait_timeout=2`，总请求ctx80秒、driver read timeout10秒是不同层预算。2秒是服务端等行锁预算；ctx能取消客户端等待；read timeout是socket读预算。日志耗时受隧道/服务负载影响，本课不保证精准2.000秒，但所有等待都有上限。

Run只读核验 `@@innodb_rollback_on_timeout=0`、`@@innodb_deadlock_detect=1`，没有改全局参数。holder锁40；waiter同Tx先把41从5扣成4，再尝试扣40，真实返回1205。**1205后就在同一Tx里查询41，仍是4**，证明本服务配置下仅失败statement回滚，先前步骤还没有被撤销。应用随后明确Rollback，waiter Finished后独立查询41恢复5、订单0。holder也结束，新的Tx依次FOR UPDATE锁40/41都成功且值5，证明没有遗留锁。

死锁1213会让InnoDB回滚整个Tx；锁等待超时在当前配置只回滚等待失败的statement。若服务以innodb_rollback_on_timeout=ON启动，1205效果会不同，本代码读取配置并检查对应首步值，但**本轮没有启用或验证ON分支**。不会为了教学改变共享服务全局选项。应用选择每次失败都明确退出Rollback，让业务边界一致；分类策略也仍需知道错误发生在SQL还是Commit，以及清理是否成功。

`PROCESSLIST`只按自己的connection ID查询，Info必须含本run随机marker。State本次可见“updating”或“update”，名字不是语言/锁契约；Info命中先证明SQL已进入服务器，固定holder与后续真实1205再证明锁等待。没有将“线程睡眠几秒”或“HTTP返回200”当数据库事务结束证据。

## E06：事务中等慢外部调用会扩大等待窗口

`startSlow` 创建一个动态loopback端口的真实HTTP服务。handler收到请求发entered后停在gate上，响应有固定语义body；owner放行之前不会完成。不是sleep延迟或纯函数替身。

坏路径：worker先Begin Tx，FOR UPDATE锁SKU50，再调用 `/bad` HTTP。owner收到handler entered后，另一个会话UPDATE50实际进入PROCESSLIST并返回1205；此时HTTP门仍未开。owner放门，worker读完整body并Close，再扣库存/Commit。外部查询50=4，只有坏worker扣了一次，竞争者超时没写入。代价是外部服务整个等待窗口都占着库存锁和DB连接。

修复：worker先调用只读 `/fixed` preflight，没有借Conn/开Tx。owner在handler仍等门时独立确认DB InUse=0，并让另一条UPDATE51实际成功1行，然后才放HTTP门。修复worker取得Conn后在短Tx扣一次；最终51=3，证明两次实际扣减。HTTP的200和body仅证明外部校验成功，库存结果仍看独立SQL。

这种移动适用于只读校验/可重做准备，随后Tx还必须重新检查会变化的数据库约束。不可幂等发货/扣款不能简单搬到Tx之前或之后就说“原子了”；需要相应的业务协议，例如持久待办/outbox、幂等外部请求与补偿。本课只教持锁窗口，不构造跨系统事务承诺。

HTTP资源也有owner：request拥有Response.Body；externalCase拥有两client任务和业务门；slowService拥有http.Server/Serve task/Transport，以及mu保护的有限接纳账本。handler进入fixture资源区之前先在 `admit` 登记自己的record，然后才能发entered、等待业务门；每个/bad、/fixed最多一次。关闭无条件先 `seal` 禁止新的fixture接纳并快照实际record，解除门，Shutdown/Close/CloseIdleConnections，然后分别以新的Background预算join Serve、实际接纳record的最后done。正常末尾不再承担唯一的handler等待职责，任何提前return也走这一条close。没有启动的/fixed没有record，因此不能等一个永远不会关闭的channel。

`Shutdown`超时、TCP被Close、client返回错误、Serve返回都不是handler最终defer的证明。`TestHTTPForcedCloseJoinsAcceptedTail`真实发一个/bad请求，把handler尾部defer挡在独立门上：Shutdown的20ms预算真实耗尽，Close使client I/O结束、Serve结束；关闭owner仍在一个新Background2秒的handler等待点，不能报告完成。放尾部门后handlerDone才关闭，owner真正join，并保留原业务失败、Shutdown DeadlineExceeded、尾部清理失败。该门只用于有限fixture断言，没有sleep猜调度。证明范围是本fixture实际接纳的最多两个handler及应用owner，不承诺join任意标准库内部goroutine。

## Go并发与资源：谁等待谁，最后哪一行才算结束

`go` 创建独立goroutine，不自动加入调用方的返回/异常边界；`context.CancelFunc`只发通知，不等待。`gate` 内的 `chan struct{}` 用close广播；`sync.Once`让正常路径/错误收尾都可放门。`chan<-`/`<-chan`限制函数能发送还是接收，传channel仍共享同一个通道对象。for-loop里的worker变量按本课Go1.27的逐次迭代语义捕获，不是所有闭包共享最后一次的值。

`task.result`是容量1的单次结果，让owner早退时worker也不会卡在发送；`task.finished`是wrapper最后一项defer关闭的广播。start内最早登记close(finished)，LIFO导致内部清理、after清理、cancel全部早于它。join先等finished，再读result和wrapperCleanup；channel关闭同步之后才能读取worker写的字段。收到result、通知cancel、observed handler进入均不能宣布已经退出。

| 资源 | owner/实际等待点 | 停止与最终证明 |
| --- | --- | --- |
| 总ctx timer | main → Run SQL/任务等待 | Run收尾后cancel，再决定Exit |
| DB | Run | 表DROP后Close，Stats全0 |
| 四张表尝试 | store，preflight不存在后、CREATE之前登记 | 新Background8秒精确DROP IF EXISTS，information_schema逐名remaining0；未知确认也恢复清理 |
| 固定Conn | acquire成功后的caller/worker | Tx结束后session恢复RC、Conn.Close；cleanup失败单列 |
| 正常Tx | transact | callback失败Rollback；完整成功才Commit；panic先Rollback再传播 |
| 手动holder/timeout Tx | snapshotOne/lockProbe/timeoutCase | Begin成功立即登记Rollback，早退也执行 |
| Rows | lockProbe | Scan循环、Rows.Err、Close，锁仍由Tx持到结束 |
| worker/门/result/Finished | 每组scope | 创建后立刻登记；提前失败先打开自有门，再cancel、Background新预算真join |
| HTTP handler/Serve/Body/Transport | externalCase/slowService/request | 先seal接纳账本，门解除，Shutdown/Close，独立预算Serve Finished与实际handler最后done，Body.Close/idle Close |
| observe ticker/清理timer | 创建函数 | defer Stop/cancel，周期只用于复查真实状态 |

拥有资源的worker函数最先 `defer captureTaskPanic(&r)`，后面再登记Tx/Conn清理；最后recover时，这些defer已把清理错误写进具名 `r.Cleanup`，于是panic原因与清理失败都能返回。recover后是整个fn返回r，不会回到panic点继续Commit。wrapper也有最后安全边界，但业务资源错误要在fn自己的具名结果中保留。

`scope.close`保留owner原错误，解除所有自有门并通知取消，再用不继承业务取消的新Background预算join尚未完成的任务。错误用 `errors.Join`组成可遍历树；工作错误、资源错误、join超时均保留。正常路径已join/已分类的1205不会在退出defer里再次被当成意外失败；所有未分类失败会向上传播。join超时明确未joined，不能写“清理成功”，必须继续定位实际仍被阻塞的资源。

五个辅助测试是这些边界的独立证据：result已入buffer却wrapper门未开时，owner实际进入join等待仍不能返回；提前return仍保留原/工作/清理三原因并确认Finished；故意20ms预算耗尽不会标joined，放门后再真join；panic与清理两个原因可追溯；error树含UNKNOWN/清理失败即使也含1213/CAS仍禁止重试。它们不是实库替代品。

DDL也有确认未知边界。`createWithBoundary`精确preflight发现已存在名字时拒绝且不登记；不存在的随机名字在实际发送CREATE之前进入owned尝试清单。Exec返回error不能据此猜服务器没有执行DDL，因此drop用精确DROP IF EXISTS并查询不存在。`TestCreateAttemptOwnsUnconfirmedEffect`显式实库运行：应用执行包装实际CREATE成功，再故意向上层返回确认错误；独立查询先看到真实表1个，随后drop恢复到0。另一个被测store遇到准备owner已创建的表时清单必须空，其drop不能删旧对象，准备owner最后精确删除。这是**应用边界丢弃确认注入**，没有截断MySQL wire，不冒称真正响应丢失实验。

## 版本、来源与实证边界

2026-10-12已核对本机Go版本、GOMOD/GOWORK、driver v1.10.1源码的BeginTx/Commit/Rollback/watchCancel，以及前置M21实际README/STATUS/txcourse契约。module为 `go-demo/lessons/m22_mysql_locks_isolation`，go=1.27.0，无toolchain/replace/go.work；driver自身go=1.24.0，间接依赖edwards25519 v1.2.0。主目录files/开发框架只读参考的是CRUD学习入口，CRUD成功不能证明并发事务正确，本课补的是实际隔离/锁/守恒证据。

优先来源：

- [InnoDB事务模型](https://dev.mysql.com/doc/refman/8.4/en/innodb-transaction-model.html)与[隔离级别](https://dev.mysql.com/doc/refman/8.4/en/innodb-transaction-isolation-levels.html)：RC/RR一致性读、当前读与范围条件。
- [不同语句设置的锁](https://dev.mysql.com/doc/refman/8.4/en/innodb-locks-set.html)：扫描索引记录、唯一命中、范围/gap与缺索引锁覆盖。
- [InnoDB死锁](https://dev.mysql.com/doc/refman/8.4/en/innodb-deadlocks.html)与[错误处理](https://dev.mysql.com/doc/refman/8.4/en/innodb-error-handling.html)：死锁全Tx回滚、锁超时按rollback_on_timeout配置区分。
- [Go1.27.0 database/sql](https://pkg.go.dev/database/sql@go1.27.0)与[执行事务](https://go.dev/doc/database/execute-transactions)：Tx/Conn/Context契约、不要混用外层DB。
- [driver v1.10.1](https://github.com/go-sql-driver/mysql/tree/v1.10.1)：本地模块缓存的connection.go/transaction.go与官方版本入口对应，不凭旧教程混用API。

Windows完整实库race只临时使用安装者已提供的amd64编译器，不安装或改系统PATH/go env：

```powershell
$tc = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' | ConvertFrom-Json
$m22EnvNames = @('CC','CXX','CGO_ENABLED','PATH','GOLEARN_INTEGRATION')
$m22Saved = @{}
foreach ($m22Name in $m22EnvNames) {
    $m22Saved[$m22Name] = @{
        Exists = Test-Path -LiteralPath ('Env:' + $m22Name)
        Value = [Environment]::GetEnvironmentVariable($m22Name, 'Process')
    }
}
try {
    $env:CC=$tc.cc
    $env:CXX=$tc.cxx
    $env:CGO_ENABLED='1'
    $env:PATH=$tc.bin_dir + ';' + $env:PATH
    $env:GOLEARN_INTEGRATION='1'
    go -C lessons/m22_mysql_locks_isolation test -race -v -count=1 -timeout 110s ./...
} finally {
    foreach ($m22Name in $m22EnvNames) {
        if ($m22Saved[$m22Name].Exists) {
            Set-Item -LiteralPath ('Env:' + $m22Name) -Value $m22Saved[$m22Name].Value
        } else {
            Remove-Item -LiteralPath ('Env:' + $m22Name) -ErrorAction SilentlyContinue
        }
    }
}
```

两个finally都同时保存“是否存在”和原值：原来不存在必须Remove-Item Env，不能把null赋回后变成存在的空串。原来存在（含空串）则精确Set-Item恢复。返工时已在当前PowerShell进程分别以不存在/已存在（非空与空串）轻量检查两种恢复写法，实际结果见STATUS。

本课连接池MaxOpen4、闲置最多1；一般两业务连接+一观察连接，不使用共享全服务的32连接额度做压力。正常入口80秒、独立清理8秒、服务端锁等待2秒/driver读10秒。真实错误与失败路径保持非零返回；不关闭断言或更换mock来生成PASS文案。

已测范围是当前单机MySQL8.4.11/隧道窗口、固定合成行和有界两worker业务。race通过仅代表执行过的Go路径未检测到数据竞争。未测生产吞吐、多节点/复制/binlog耐久性、服务重启、回滚超时ON、所有索引形态/外键/隔离级别、purge容量、网络分区全集、公司旧Go/driver、生产TLS、真实发货或跨系统原子协议。performance_schema保持OFF，没有读取data_locks，也没有启用general log；资源恢复/服务安装复用主agent已验收基础设施证据。本课交付等待独立review，不自称ACCEPTED。
