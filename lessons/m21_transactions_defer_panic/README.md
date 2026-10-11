# M21：事务、defer、panic 与提交结果未知

这一课围绕一笔订单：扣一个库存，再插入一条订单。你会亲眼看到三种完全不同的事实：成功后两步都留下；正确失败后两步都不留下；危险写法虽然返回了错误或 panic，却留下半笔业务。最后还会遇到一个更难判断的结果：MySQL 已提交，客户端却收到 `invalid connection`。

课程使用真实 MySQL 8.4.11、Go 1.27.0、`github.com/go-sql-driver/mysql v1.10.1`，没有 fake Commit。每次运行在既有学习库 `golearn_m20` 创建 `m21_<24位随机hex>_stock`、`m21_<同一后缀>_orders`，结束精确删除并查询确认。服务归主 agent 管理，本课只消费 READY 接入、操作自己的合成表。

## 15 分钟主实验

程序本身通常几十秒内完成；10～20 分钟用于按输出读懂这一个“库存+订单”实验，没有额外 exercise。

| 时间 | 做什么 | 必须看懂的事实 |
| --- | --- | --- |
| 0～3 分钟 | 运行正常入口，读 E01 与 `withTx` | Tx 内 SQL 已执行不等于提交；最后显式 Commit |
| 3～8 分钟 | 看 E02 五个危险写法，再读各函数旁注释 | panic 不给 named err 赋值；短声明遮蔽；defer 实参过早求值；遗漏清理；DB 绕过 Tx |
| 8～12 分钟 | 看 E03 两条取消路径 | 取消通知、worker 退出、服务端事务结束是不同事实 |
| 12～15 分钟 | 看 E04 两次通信失败的 SELECT | 同样 Commit error 可对应未提交或已提交；按原 key 查账 |
| 15～20 分钟 | 阅读资源表、测试与来源定位 | 为什么 Result、Finished、独立清理预算不能合并 |

在仓库根目录运行：

```powershell
go -C lessons/m21_transactions_defer_panic run ./cmd/demo
go -C lessons/m21_transactions_defer_panic test -count=1 -timeout 35s ./...
go -C lessons/m21_transactions_defer_panic vet ./...
```

默认索引是 `D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json`。`GOLEARN_SERVICE_INDEX` 可覆盖**索引文件路径**，程序再加载其中的私有 `credential_file`；不要把密码、DSN 放进命令或日志。账号必须限定在 `golearn_m20`，端点必须 READY 且为 `127.0.0.1`。缺少真实服务时入口失败，不能以 mock 补出“通过”。

完整实库测试需显式开启：

```powershell
$savedIntegration = $env:GOLEARN_INTEGRATION
try {
    $env:GOLEARN_INTEGRATION = '1'
    go -C lessons/m21_transactions_defer_panic test -v -count=1 -timeout 100s ./...
} finally {
    $env:GOLEARN_INTEGRATION = $savedIntegration
}
```

默认 `test` 会明确 SKIP `TestMySQL`，只通过五个辅助断言；这不算实库验收。完整实库 race 命令在本文末尾。根 `go test ./...` 不进入本课的嵌套 module。

## 从入口读到真实副作用

| 文件/符号 | 调用方 → 职责 → 结果去向 |
| --- | --- |
| [cmd/demo/main.go](cmd/demo/main.go) `main` | 建70秒总预算 → `Run` → 收尾后输出完成或非零退出 |
| [txcourse/config.go](txcourse/config.go) `LoadConfig/openPool` | `Run` → 只读索引与秘密、构建真实 driver connector → 借连接池给实验 |
| [txcourse/course.go](txcourse/course.go) `Run/correctCases/dangerousCases` | 入口 → 建独占表、按次序执行 E01～E04 → 最后 DROP/Close |
| [txcourse/store.go](txcourse/store.go) `reserve/insertOrder/Snapshot/verify` | 业务层 → 参数化 SQL、affected rows、事务内读与外部查账 → 实库事实 |
| [txcourse/transaction.go](txcourse/transaction.go) `Place/withTx` | 订单请求 → 显式传同一 Tx、每步查错、显式 Commit → 成功/原错误/UNKNOWN |
| 同文件 `badNamedPanic/badShadow/badCaptured/badForgot/badMix` | 明确的故障实验 → 复现五个控制流/资源错误 → 独立 SELECT 暴露副作用 |
| [txcourse/cancel.go](txcourse/cancel.go) `cancelCase/cancelIdle` | E03 → idle取消与真实行锁等待取消 → Finished、连接/锁、表内容 |
| [txcourse/task.go](txcourse/task.go) `startOwned/join/stopAndJoin` | E03和断言 → owner取消并以独立预算等待最终退出 → 保留工作/原失败/清理失败 |
| [txcourse/unknown.go](txcourse/unknown.go) `oneUnknown` | E04 → 在代理固定连接执行同样两步 → 原 key 查账 |
| [txcourse/proxy.go](txcourse/proxy.go) `serve/pumpClient/pumpServer` | 本课代理 owner → 原样转发 MySQL packet、仅截断 COMMIT 边界 → 真协议证据+Finished |
| [txcourse/course_test.go](txcourse/course_test.go) | testing → 实库全量、结果已就绪仍必须等清理、超时错误链、packet边界、Accept收尾 → 独立断言 |

正常业务的完整调用链：

```text
main
  Run
    LoadConfig → mysql.NewConfig → mysql.NewConnector → sql.OpenDB
    newStore → create
    correctCases
      Store.Place(ctx, Request)
        withTx(ctx, db, callback)
          db.BeginTx(ctx, nil) → 登记退出 Rollback
          callback(tx)
            reserve(ctx, tx, r)
              tx.ExecContext(条件 UPDATE) → Result.RowsAffected=1
              tx.QueryRowContext(本事务 SELECT).Scan → 未提交库存4
            insertOrder(ctx, tx, r)
              tx.ExecContext(INSERT) → Result.RowsAffected=1
          tx.Commit → mysqlTx.Commit → mysqlConn.exec("COMMIT")
          deferred Rollback → ErrTxDone（按已结束契约忽略）
      verify → Snapshot → DB上的业务 SELECT → 库存4、订单1
    dangerousCases → cancelCase → unknownCase
    deferred drop → information_schema remaining=0
    deferred DB.Close → Stats open/inuse/idle=0
```

`Store` 保存的是 `*sql.DB` 指针，业务函数借用它，不拥有它的关闭权。`Request` 是小结构体值副本；闭包捕获当前请求供同步 callback 使用，没有创建 goroutine。`*sql.Tx` 指向一次事务，它从池中占用一条连接直到结束。`reserve` 和 `insertOrder` **只接受 Tx**，类型签名让读代码的人马上知道 SQL 属于哪个事务。

现在必须理解：显式 Tx 传递、`:=` 的作用域、具名结果、defer 求值/执行两个时刻、error 包装、事务连接归属、Commit 的已知/未知边界、取消后的 owner 收尾。TLS 生产部署、MySQL binlog/复制耐久性、连接池生产定容、大包代理、分布式幂等存储可在相应源码需要时再查；本课没有将它们扩展为验收范围。

## 最小规则：Go 控制流和数据库状态分开看

1. `error` 是返回值。`return err` 会先把表达式的值赋给返回结果，再执行 defer。`panic` 直接展开栈，不会自动给具名 `err` 赋值。
2. `defer f(err)` 在注册时计算并保存实参。`defer func(){ use(err) }()` 在退出时运行闭包，届时读取捕获的变量。二者不是同一种“延迟读取”。
3. 多个 defer 后注册先执行。普通 return 和普通 panic 会执行 defer；`os.Exit` 不会。本课入口在 `os.Exit` 前先让 Run 完成全部收尾。
4. `recover` 只能恢复同 goroutine 内展开到直接延迟函数的 panic。恢复后不会返回到 panic 后一行继续业务。本课正确 Tx helper 先回滚再传播，外层边界才生成带栈的失败。
5. `*sql.DB` 是池，`*sql.Tx` 是绑定一条连接的事务。Go 没有自动线程绑定事务：调用 `s.db.ExecContext` 不会因为当前栈上有 Tx 就加入它。
6. `RowsAffected` 是这条 SQL 的影响行数，不是最终提交证据。条件 UPDATE=0 表示库存不存在/不足；本课把它作为 `ErrConflict`，不把它冒充网络故障。
7. Commit 成功后本次 Tx 不能再回滚。Commit 失败后本地 Tx 也可能已结束；`ErrTxDone` 只说明本地对象结束，不能证明服务端没提交。

Java 类比可以帮你定位职责：这里的 `withTx` 类似一个很小的显式 JDBC 事务边界，callback 中每个 DAO 用同一 Connection/Tx。Spring 的事务代理与线程绑定不在 Go 的 `database/sql` 中；把 DB 传进 DAO 不等于把当前事务传进去。`defer` 类似“退出时收尾”的一部分效果，但不是 catch/finally 的语法替身，尤其参数注册求值、panic 展开、具名返回槽要单独理解。

## E01：正确模式的逐行解析

`Place` 先拒绝 qty≤0、空 key、空 SKU，避免负数扣库存变成加库存。`withTx` 在 Begin 成功后立即登记清理。callback 顺序为：条件扣减 → 查影响行数 → Tx 内读自己的未提交库存 → 插入订单 → 查影响行数。任何一步失败都返回原 error，defer 回滚；完整 callback 返回 nil 才显式 Commit。

```go
tx, err := db.BeginTx(ctx, nil)
if err != nil { return fmt.Errorf("BeginTx: %w", err) }
// 实际源码这里还有 panic 分支：rollback 后继续 panic。
defer func() { err = errors.Join(err, rollback(tx)) }()
if err = fn(tx); err != nil { return err }
if err = tx.Commit(); err != nil {
    return errors.Join(ErrUnknown, fmt.Errorf("显式 Commit: %w", err))
}
return nil
```

这是讲解缩略版；运行的是 `transaction.go` 中完整 panic 契约。`errors.Join` 是一棵可遍历的错误树，不自动给错误排序。原 SQL、Commit、Rollback 的原因要分别保留；根据业务阶段决定策略，不能让清理 error 覆盖主失败。

| 步骤 | 参与者/位置 | 实际状态 | 为什么走下一步 |
| --- | --- | --- | --- |
| 1 | owner `correctCases → Place` | success SKU库存5，原 key不存在 | 请求合法 |
| 2 | `withTx → BeginTx` | 一条连接被 Tx 持有 | 已登记 Rollback，提前失败有收尾 |
| 3 | `reserve → ExecContext/RowsAffected/Scan` | Tx 内库存4，影响1行 | 还没有向外承诺成功 |
| 4 | `insertOrder → ExecContext` | Tx 内订单1，影响1行 | callback完整返回nil |
| 5 | `withTx → Commit` | 服务器确认提交 | 返回nil后 deferred Rollback得到ErrTxDone |
| 6 | owner `verify → Snapshot` | 外部SELECT库存4、订单1、qty1、SKU相符 | 两个业务事实一起成立 |

实际输出：`safe.success.SELECT stock=4 orders=1`。相同 key 再调用一次，第二次 Tx 内库存暂时3，订单 INSERT 被真实主键1062拒绝，退出回滚后仍 `stock=4 orders=1`。唯一键负责拒绝重复记录，正确回滚负责撤销第二次扣减，二者缺一不可。

库存0路径：条件 UPDATE影响0行 → `ErrConflict` → 没有订单INSERT → Rollback → `stock=0 orders=0`。panic路径：Tx内扣成4 → 订单代码panic → helper先Rollback → 原panic继续展开 → `catchPanic`得到 `PanicFailure`（带现场栈）→ 外部SELECT `stock=5 orders=0`。恢复是控制流，不是“把已经做过的操作倒放”；回滚是我们明确写的资源行为。

## E02：五种危险写法，逐个看副作用与修复

每个 case 有自己的 SKU，准备时库存5。shadow/capture/forgot case 还预置一条同 key订单，专门触发第二步重复主键；这是故障准备数据，不是业务下单。

### 1. named err==nil 不能证明没有 panic

`badNamedPanic` 中 `tx, err := BeginTx` 在函数同层复用具名 err，成功后值为nil。defer 注册的闭包退出时读它。`reserve` 成功也仍为nil。随后 `panic(...)` 没有执行 `return error`，err没有变化。defer据nil错误Commit，然后panic才到外层边界。

```text
body: Begin → err=nil → Tx扣库存4 → panic
展开: deferred closure读named err=nil → COMMIT库存
边界: catchPanic恢复为失败
SELECT: 库存4、订单0（部分业务已提交）
```

危险版外层确实收到panic失败，依然不能猜“肯定没写”。修复 `withTx` 不把 Commit 放在 defer，panic分支只Rollback后传播。E01相同故障点得到库存5、订单0。

### 2. := 遮蔽错误，吞掉后还能错误提交

`if err := insertOrder(...); err != nil` 的 `err` 属于 if 内层作用域，与具名结果是两个变量。坏版本只打印后离开 if，再在外层裸 `return`，具名 err依然nil。defer于是Commit扣减。caller甚至收到nil，而表中 `stock=4 orders=1`：旧订单仍只有一条，但库存又少一个。

修复既可以写 `err = insertOrder(...)`，也可以保留内层变量但明确 `return err`。后者先读取内层err，赋给返回槽，再执行闭包。更易审查的本课模式是 callback显式返回 error、统一 owner只在callback完整返回nil后Commit。`fixed.shadow.SELECT stock=5 orders=1`。

### 3. defer 保存旧 err，caller失败仍提交

`badCaptured` 写 `defer func(oldErr error){...}(err)`，此处实参立即求值为nil接口副本。第二步失败后，具名err保存1062并交给caller，但延迟函数的oldErr永远是注册时nil，因此Commit库存。

```text
注册: err=nil → 保存oldErr=nil
body: 扣库存4 → INSERT失败 → err=1062
return: 返回槽已是1062
defer: 读oldErr=nil → 错误Commit
caller/SELECT: error!=nil，仍库存4、订单1
```

把参数改成退出时读err的闭包能修正“过早捕获”，但仍没有解决上一个panic陷阱。完整修复采用显式Commit、无条件退出Rollback和panic契约。`fixed.capture.SELECT stock=5 orders=1`。

### 4. 提前返回忘 Rollback，外部SELECT暂时正常也仍泄漏

`badForgot` 内部没有任何清理，扣到4后订单1062失败直接return。实验 harness在调用前拥有这个Tx，保留保护defer；**观察前没有帮坏函数回滚**。返回后真实 `pool.InUse=1`，同一个Tx还能SELECT到4；另一个连接看到库存5、旧订单1，因为未提交写入被隔离。

这说明“外部表暂时没变化”不能证明Tx结束或锁/连接已释放。harness随后显式Rollback，确认InUse=0并再次SELECT库存5、订单1。正常业务应在Begin成功后一开始就登记Rollback；不要依赖外围偶然有兜底。实验没有真正留下无限期事务，也没有耗尽共享服务。

### 5. 用外层 DB 写订单，Rollback管不到另一连接

`badMix` 用Tx扣库存，却 `s.db.ExecContext` 插订单。主池允许第二条连接，INSERT在另一连接的autocommit下立即提交；随后业务返回 `ErrStepTwo`，defer只回滚Tx内扣减，得到 `stock=5 orders=1`。

M20已经证明池max1时类似误用会等自己的连接造成有界阻塞；本课max3让它“能跑完”，但会留下不一致。连接够用没有修复事务归属。

修复把订单INSERT也传同一Tx，再在订单之后同样返回业务失败：两步一起回滚，`fixed.db-tx-mix.SELECT stock=5 orders=0`。Go不会从当前goroutine/调用栈推断“当前事务”，正确函数签名应清楚传递 `*sql.Tx`。

## E03：取消不是回滚确认

先有三个位置不同的取消事实：

| 路径 | 当前等待点 | 本课实际观察 |
| --- | --- | --- |
| Begin前已取消 | `DB.BeginTx` 本地检查 | `Canceled`、nil Tx，没有可供caller回滚的事务 |
| Tx已写两步，但此刻idle | `cancelIdle` 取消后调用 `Tx.Commit` | 本地拒绝为Canceled或ErrTxDone；Rollback/Conn归还之后SELECT无副作用，新Tx FOR UPDATE成功 |
| Tx写了订单，UPDATE等行锁 | worker `Tx.ExecContext` 真实SQL I/O | PROCESSLIST命中标记UPDATE后取消；driver返回Canceled、wrapper Finished；释放holder后原服务端连接消失，SELECT库存5/订单0 |

第二条 idle路径中，BeginTx接收的ctx控制整个事务生命周期。`database/sql` 创建 `awaitDone` 观察ctx；取消后它和caller清理可能竞争。`Commit`先查ctx：自动回滚尚未把done改掉时可返回Canceled，已结束时可返回ErrTxDone。这里断言“二者之一”，不把一次调度顺序宣传为语言保证。实际表内容和新事务锁定读才是回滚/锁释放的证据。

锁等待路径的完整时间线：

| 步骤 | 参与者 → 当前函数/等待点 | 状态/资源变化 → 证明点 |
| --- | --- | --- |
| 1 | owner → holder `BeginTx/SELECT FOR UPDATE` | 本run cancel SKU被holder锁住，库存5 |
| 2 | owner → `startOwned` | 创建workerCtx、Result容量1、Finished广播；立刻登记stopAndJoin |
| 3 | worker → `Conn/CONNECTION_ID/BeginTx/insertOrder` | worker独占Conn和Tx，订单已插但未提交；ID发给owner只用于诊断 |
| 4 | worker → 带 `m21_cancel_lock` 的UPDATE | 等holder行锁；不是等池，已经拿到真实连接 |
| 5 | owner → PROCESSLIST查询 | 实际Info含本表+标记UPDATE才继续；State本次观察updating，名字不保证每次一致 |
| 6 | owner → `worker.cancel()` | 发取消通知，不能由此宣布SQL停止/服务器回滚 |
| 7 | worker → Exec返回/Tx Rollback/Conn.Close | driver I/O watcher关闭这一连接；数据库/sql自动回滚可能竞争；fn先清自己的资源 |
| 8 | worker wrapper → 发Result，再执行cancel/附加cleanup，最后close Finished | 结果已交付与wrapper全结束是两件事 |
| 9 | owner → 新Background预算 `join` | 先等真实Finished再读取结果；确认Canceled原因、没有join超时 |
| 10 | owner → holder.Rollback | 解除自己的服务端行锁，让原worker会话继续检测断连并结束 |
| 11 | owner → PROCESSLIST直到原ID消失 → Snapshot | 服务器原事务退出后确认库存5、订单0、workerDB.InUse0 |

为什么还要释放holder并观察服务端连接？客户端关闭socket不保证MySQL此刻已结束等待，服务端可以仍在等锁。普通一致性SELECT也可能暂时看不到未提交写入。因此本课不拿ctx.Err、客户端Finished或单次普通SELECT当全部服务端回滚证据。

`startOwned` 的owner是cancelCase。`fn`拥有Conn/Tx，wrapper拥有附加清理与Finished。`taskResult.Err/CleanupErr` 分开保存工作与资源失败；owner必须同时确认预期Canceled和清理错误nil，不能因error树里有Canceled就忽略别的关闭失败。`result`容量1避免无人接收时卡发送；`finished`只关闭、不发送，任何观察者可重复等完成。`join`先等finished，再读pending/result和wrapperCleanup，确保不会提前读仍在写的共享字段。

`Conn.Close` 和 `Tx.Rollback` 的重复调用契约不同。idle路径为了在查账前确认归还连接，先显式Close，再用本函数专属 `connClosed` 标志阻止保护defer重复Close；否则第二次会返回ErrConnDone，制造已经清理成功却整课失败的误报。这个标志由单一caller读写，没有并发数据竞争。

`TestResultReadyStillWaitsForFinished` 在Result已交付后把wrapper卡在清理gate，借 `enteredContext.Done` 的求值通知确定owner已进入join等待，然后独立确认owner尚未返回。放门后才Finished/返回；原owner失败、工作失败、Canceled和cleanup失败都通过errors.Is保留。`TestJoinTimeoutRetainsOriginal` 故意用20ms独立预算耗尽：返回ErrJoin+DeadlineExceeded+原失败，明确未Joined；测试随后放门并用新预算真join，不留下任务。

`TestEarlyReturnStopsAndJoins` 直接走与生产相同的保护defer：owner在未读Result时return，仍取消worker并等Finished；fn和wrapper两层清理失败与原失败全部保留，返回后独立确认真实完成channel与清理哨兵。

## E04：Commit error 可以对应相反的表内容

我们没有停MySQL或改全局网络。每个case启动一条本课loopback TCP代理，clientDB只允许一条连接，认证/SQL/事务全部交给真实MySQL。代理原样转发小packet，只识别 `COM_QUERY` 的文本 `COMMIT`；本课关闭TLS/compression，生产TLS/大包/多连接代理不在范围内。

| 本课故障 | 协议仪器看到什么 | 客户端看到什么 | 原 key 权威SELECT |
| --- | --- | --- | --- |
| `commit-not-forwarded` | COMMIT已到代理，未发服务器，断双方连接 | `invalid connection`，业务状态UNKNOWN | stock5、order0；原会话已退出，实验确认未提交 |
| `commit-response-lost` | COMMIT已发服务器，收到真实成功OK包，主动不发给client | 同样 `invalid connection`，业务状态UNKNOWN | stock4、order1、qty/SKU一致；恢复COMMITTED |

已提交但响应丢失的调用/状态时间线：

```text
owner: newProxy → 单连接proxyDB → Conn → BeginTx
Tx:    UPDATE库存4 → INSERT订单1（两步尚未提交）
client→server pump: 看COM_QUERY COMMIT → 原包发给MySQL
MySQL: 真实COMMIT → 生成OK packet
server→client pump: 读到OK → 保存ServerOK/Dropped → 不转发 → 返回
serve: 收第一方向结果 → 关双方socket → 收第二结果 → 等两方向Done
       所有defer结束 → close proxy Finished
client Tx.Commit: 读不到完整响应 → 驱动 invalid connection
owner: UNKNOWN + request_id + 原错误，绝不自动再次扣库存
owner: stopAndJoin独立预算等Finished → 原server ID消失 → 原key SELECT
查账: 订单1且payload匹配、库存4 → 返回原订单，恢复COMMITTED
```

本机标准库在调用driver Commit之前就把Tx.done从false改成true。driver即使返回通信error，本机Tx仍结束，所以随后Rollback得到ErrTxDone。这个error无法撤销服务器已经提交的事实。代理的ServerOK是课堂仪器，不是生产caller能获得的额外信息；生产边界应保留UNKNOWN，不能根据错误文案猜“未提交”。

安全恢复以 **同一个 request_id** 查业务权威库：

- 找到一条完整、payload与原请求相同的订单：返回原结果，恢复COMMITTED，不再扣库存。
- 找到相同key却payload不同：明确冲突/人工核查，不能把另一个请求误认为本次成功。
- 暂时无记录：仍可能在执行或读到副本延迟，不立即认定未提交；保留UNKNOWN并在有界预算内继续查权威库/原请求状态。
- 本实验的未转发case额外确认原会话已经退出、查询真实主库无记录，才说“实验确认未提交”。这项确定性来自仪器和结束状态，不能泛化到任意NoRows。

`oneUnknown` 没有重放Place，没有新建request_id，没有发货/MQ外部副作用。错误树同时保留 `ErrUnknown`、带原key的包装以及原始Commit error；查账报告是单独事实，不擦除原通信失败的历史。

## 谁拥有资源，最后在哪里确认结束

| 对象 | owner | 当前等待/使用 | 最终边界 |
| --- | --- | --- | --- |
| 总ctx timer | main | 整课70秒预算 | Run返回后显式cancel，再决定Exit |
| 主DB | Run | 一般1连接，混用/holder观察时至多2，最大设置3 | 表清理后DB.Close，Stats全0 |
| 两个表 | Store由Run登记清理 | 每run独占后缀、各case独立SKU | 精确DROP、information_schema remaining0 |
| 正确业务Tx | withTx | callback同连接两步SQL | 失败Rollback；成功显式Commit；panic先Rollback再传播 |
| 漏清理反例Tx | forgotCase harness | 观察时仍持池/锁 | 观察后Rollback；defer保护任何早退 |
| holder Tx | cancelCase | 本run SKU的FOR UPDATE锁 | 正常显式Rollback；defer兜底 |
| worker Conn/Tx | worker fn | 真正UPDATE等锁 | fn退出defer先Rollback后Conn.Close |
| worker/Result/Finished | cancelCase owner | Result交值，Finished广播最后收尾 | cancel通知+独立budget join；早退也同一路径 |
| idle取消Conn/Tx | cancelIdle | 两步SQL完成后取消 | Rollback/Conn.Close，再SELECT与新锁定读 |
| proxyDB/Conn/Tx | oneUnknown | 一条固定连接，真实Commit | Tx结束、Conn.Close、DB.Close |
| proxy listener/socket/pumps | oneUnknown → faultProxy | Accept、ReadFull、Write | Close解除等待，等两pump Done，再总Finished，owner独立budget join |
| Rows | QueryRow.Scan内部协议 | 本课均单行/聚合查询 | Scan管理返回行关闭，无裸Rows逃逸 |
| 所有observe ticker/清理ctx | 创建它的函数 | 重看实际状态/独立期限 | defer Stop/cancel |

共享实例的活跃连接按本课设计最多3：holder1+observer1+worker1；未知提交case是主池1+proxy连接1。每个池只保留至多1闲置连接，worker/代理实验结束马上Close；MySQL全服务max32由安装者管理，本课不改变它。

默认日志只报告无秘密版本、表名、合成key、错误和SELECT。私有本地报告在本模块 `.local/`，`.gitignore`排除；凭据继续留在安装者私有文件，没有复制。正常或错误实验结束没有本run表、后台代理/worker；服务和课程隧道由安装者继续持有。

## 版本、前置与官方源码核对

模块 `go-demo/lessons/m21_transactions_defer_panic` 的 `go` 为1.27.0，没有toolchain/replace/go.work；实际本机Go1.27.0/windows/amd64，GOROOT `D:/Go`。第三方锁定mysql v1.10.1，其自身go.mod为1.24.0，间接依赖edwards25519 v1.2.0。本课没有凭旧教程记忆混用Logger API，配置经 `mysql.NewConnector` 保留真实NopLogger而不是把对象编码成DSN后丢失。

硬前置已读取真实交付：M20的READY库/Config/openPool/sqlWait/Finished修复，M05 `Classify` 的未知优先级与Join，M06 `captureTooEarly/shadowResult/explicitReturn`，M07 `RunEffects/reportFailure` 的panic边界与收尾。它们在前两层已主验收。本课不导入其他课internal包、不复制整套前置工程，也不重跑全部前置；用实库把前置的语言/内存模型升级为业务证明。

核对日期2026-10-12，优先来源：

- [Go官方执行事务](https://go.dev/doc/database/execute-transactions)：显式Tx、Begin后清理、末尾Commit与不要混用外层DB。
- [Go1.27.0 database/sql契约](https://pkg.go.dev/database/sql@go1.27.0)：BeginTx ctx贯穿事务；Tx结束后操作ErrTxDone。本课刻意使用带版本地址，pkg.go.dev默认页面当时已是更新补丁。
- [Go语言spec的defer规则](https://go.dev/ref/spec#Defer_statements)：实参注册求值、退出LIFO；panic/recover直接延迟调用的规则与M06/M07核对。
- [driver v1.10.1源码](https://github.com/go-sql-driver/mysql/tree/v1.10.1)：`transaction.go` 的 `mysqlTx.Commit/Rollback` 调 `mysqlConn.exec("COMMIT"/"ROLLBACK")`，`connection.go` 的 `watchCancel/startWatcher` 在I/O取消时关闭连接，`errors.go` 的Logger/NopLogger实际API。
- 本机 `D:/Go/src/database/sql/sql.go`：`BeginTx`、`Tx.awaitDone`、`Tx.Commit`、`Tx.rollback`、`Conn.Close`；driver缓存 `C:/Users/Lenovo/go/pkg/mod/github.com/go-sql-driver/mysql@v1.10.1/` 的上述符号。语言保证、库契约、本机实现与一次运行State分别说明，没有假定所有版本完全相同。

`files/基础语法/go语言defer` 与 `go语言异常捕获` 只读参考。材料说defer在return/panic执行是普通控制流规则，不等于自动事务回滚；把error统称“异常”、只打印recover后继续，也不足以证明失败结果和业务状态。M21用具名err未改变、真实错误提交、原key查账纠正这些过度简化。

## 完整实库 race 与证据范围

Windows默认gcc曾在前置课程被证实是32位目标，不适合amd64 race；安装者已提供portable工具链。本课只在当前PowerShell进程临时加载并finally恢复，不改全局PATH或go env：

```powershell
$tc = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' | ConvertFrom-Json
$m21Saved = @{ CC=$env:CC; CXX=$env:CXX; CGO_ENABLED=$env:CGO_ENABLED; PATH=$env:PATH; GOLEARN_INTEGRATION=$env:GOLEARN_INTEGRATION }
try {
    $env:CC=$tc.cc
    $env:CXX=$tc.cxx
    $env:CGO_ENABLED='1'
    $env:PATH=$tc.bin_dir + ';' + $env:PATH
    $env:GOLEARN_INTEGRATION='1'
    go -C lessons/m21_transactions_defer_panic test -race -v -count=1 -timeout 100s ./...
} finally {
    $env:CC=$m21Saved.CC
    $env:CXX=$m21Saved.CXX
    $env:CGO_ENABLED=$m21Saved.CGO_ENABLED
    $env:PATH=$m21Saved.PATH
    $env:GOLEARN_INTEGRATION=$m21Saved.GOLEARN_INTEGRATION
}
```

最终真实命令、失败修复、run命名空间与清理见 [STATUS.md](STATUS.md)。race通过说明本机这些被执行路径没有检测到数据竞争，不保证生产任意路径。业务正确性分别由库存/订单SELECT、主键1062、PROCESSLIST/新锁定读、真实COMMIT包和最终清理证明。

未验证生产负载、网络分区全集、硬断电/binlog/副本耐久性、MySQL服务重启、公司旧Go/旧driver、其他OS、TLS/压缩/大包代理、分布式发货/MQ事务或生产幂等存储。本课服务重启/资源限制/安装锁等基础设施证据复用安装者，不停共享服务重复环境验收。
