# M23：把 GORM 链式代码还原成 SQL、事务和外部事件

本课围绕同一类库存订单业务：库存初始 5，扣 1，再插一条订单。它延续 M21 的业务不变量，使用本课自己的随机表，把 `database/sql` 中明确的连接归属迁到真实 GORM。你会看到能编译、没有 panic、甚至 callback 返回 nil 的程序依然留下错误库存；也会看到 MySQL 全部回滚后，RabbitMQ 仍真实投递出 hook 提前发出的事件。

实际版本为 Go **1.27.0 windows/amd64**、GORM **v1.31.2**、MySQL dialector **v1.6.0**、`go-sql-driver/mysql` **v1.10.1**、MySQL **8.4.11**、`amqp091-go` **v1.15.0**、RabbitMQ **4.3.6**。版本来自本机命令、锁定 module 与真实服务索引/SQL；不把官网示例的 API 签名直接套进另一版本。本课没有 SQLite 实库，没有模拟 broker。

## 先用 15 分钟讲清一个核心实验

这里的核心是“同一笔库存订单，SQL 成功、事务提交、MQ 接受分别在什么时候成立”。代码通常在一分钟内跑完，10～20 分钟用于对照反例和解释，没有另建 exercise。

| 时间 | 阅读/运行位置 | 需要自己说清的事实 |
| --- | --- | --- |
| 0～3 分钟 | 正常入口，E01，`Stock` | `Updates(struct)` 怎么把合法 0/false 丢掉，为什么 map/Select 能修 |
| 3～7 分钟 | E02/E03，`reserve → Transaction → insertOrder` | 句柄是否带 Tx，看连接对象；传统链与泛型链怎么复用 |
| 7～11 分钟 | E05，`Order.AfterCreate → broker.publish` | hook 的 After 是 INSERT 之后、COMMIT 之前；真实 MQ 不接受 SQL Rollback |
| 11～15 分钟 | E06，`cancelCase → waitSQL → join` | timeout 通知、客户端 Done、服务端会话消失、独立查账各证明什么 |
| 15～20 分钟 | E04、源码与测试 | 软删除/Preload/分页的隐含 SQL，失败出口的资源归属 |

在仓库根目录运行，必要参数只有真实服务索引路径，无密码参数：

```powershell
go -C lessons/m23_gorm_semantics run ./cmd/demo
go -C lessons/m23_gorm_semantics test -v -count=1 -timeout 30s ./...
go -C lessons/m23_gorm_semantics vet ./...
```

默认索引为 `D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json`。若在不同路径使用安装者交付的同类索引，设置 `GOLEARN_SERVICE_INDEX` 为**索引文件路径**。程序再读取 `services.mysql/rabbitmq.credential_file`；凭据只在内存使用，不能把整个 config、DSN、AMQP URI 打印出来。

demo 必须连真实 MySQL 和 RabbitMQ，接入失败就非零退出。普通 test 明确 SKIP `TestRealMySQLAndAMQP`，只运行六个辅助断言；SKIP 不能算实库 PASS。完整实库测试显式开启：

```powershell
$savedIntegration = [Environment]::GetEnvironmentVariable('GOLEARN_INTEGRATION', 'Process')
try {
    $env:GOLEARN_INTEGRATION = '1'
    go -C lessons/m23_gorm_semantics test -v -count=1 -timeout 120s ./...
} finally {
    # 原先不存在与原先有值分别恢复；Windows不能把null直接赋回Env当作精确删除。
    if ($null -eq $savedIntegration) {
        Remove-Item -LiteralPath Env:GOLEARN_INTEGRATION -ErrorAction SilentlyContinue
    } else {
        [Environment]::SetEnvironmentVariable('GOLEARN_INTEGRATION', $savedIntegration, 'Process')
    }
}
```

Windows 的完整实库 race 入口复用安装者现有编译器：

```powershell
& ./lessons/m23_gorm_semantics/scripts/verify-race.ps1
```

脚本临时设置 CC/CXX/CGO_ENABLED/PATH/GOLEARN_INTEGRATION，finally 逐项恢复；不安装编译器，不执行 `go env -w`。脚本调用本 module 的 `go test -race -v -count=1 -timeout 120s ./...`，私有记录在本课 `.local/race-final.txt`。根 `go test ./...` 不进入这个嵌套 module。

## 前置证据与文件阅读顺序

已读取硬前置 [M21 README](../m21_transactions_defer_panic/README.md)、[STATUS](../m21_transactions_defer_panic/STATUS.md) 与 `txcourse/transaction.go/config.go/store.go` 的真实接口和逐行断言。M21 由前层主审验收后纳入当前基线，当前其 STATUS 保留当时 READY_FOR_REVIEW 的实施记录；不靠 prompt 存在猜已完成，也没有重跑全部前置。本课不复制 M21 项目、不修改它、不与它循环导入。

M21 的直接契约保留在这里：显式传同一 Tx；所有 SQL 查错误和行数；callback 完整成功才提交；panic 先清理再传播；取消后 owner 真 join；独立 SQL 查账；Commit error 不能武断断言未提交。M21 的 `withTx` 迁成这里的 `manualTx`，M21 的库存和订单独立 SELECT 迁成这里的 `verify`。本课写的是 GORM 适配和新增行为，不代做前置故障代理实验。

| 文件/符号 | 调用方 → 职责 → 结果去向 |
| --- | --- |
| [cmd/demo/main.go](cmd/demo/main.go) `main` | 建 80 秒总预算 → Run → 清理完成后退出码 |
| [gormcourse/config.go](gormcourse/config.go) `loadConfig/openPool` | Run → 只读 READY/凭据、driver connector → 一个最多 4 连接的池 |
| [gormcourse/lab.go](gormcourse/lab.go) `Run/remaining/drop/verify` | 入口 → 登记随机对象、按 E01～E06 执行 → 精确 DROP/Close，独立 SQL 查账 |
| [gormcourse/models.go](gormcourse/models.go) `Stock/Order/AfterCreate` | GORM callback → model、hook 当前 Tx、audit/outbox/MQ副作用 → callback error |
| [gormcourse/crud.go](gormcourse/crud.go) `crudCases/owned/chainCases` | Run → 传统/泛型 API、零值/归属/复用反例 → 行内容断言 |
| [gormcourse/transactions.go](gormcourse/transactions.go) `reserve/insertOrder/manualTx/transactionCases` | 业务入口 → 连接归属、手动/回调事务、危险与修复 → 两表 SELECT |
| [gormcourse/hidden.go](gormcourse/hidden.go) `hiddenCases/page/hookCases/relayOne` | Run → 默认隐藏 SQL、未提交事件与修复 → 查询计数、broker证据 |
| [gormcourse/cancel.go](gormcourse/cancel.go) `cancelCase/waitSQL` | Run → holder/worker/observer → 真实锁等待、timeout、服务器退出、回滚 |
| [gormcourse/task.go](gormcourse/task.go) `startOwned/join/stopAndJoin` | cancelCase/test → 登记 Result/Done/取消/清理 → 真正 owner 收尾 |
| [gormcourse/netbudget.go](gormcourse/netbudget.go) `boundedConn/resetBudget` | AMQP库/owner → 真实socket deadline上限 → 库握手/heartbeat不能取消owner预算 |
| [gormcourse/sqltrace.go](gormcourse/sqltrace.go) `Trace/count` | GORM processor → 合成 SQL 展示、SELECT次数 → 读者及关联断言 |
| [gormcourse/course_test.go](gormcourse/course_test.go) | testing → 实库整课、参数分离、非法分页、Result/Done、错误树、提前返回 → 独立断言 |

现在必须理解：GORM 句柄和 Statement、0/false 的业务含义、返回 error 与异常不同、连接归属、callback 的提交条件、hook 时机、显式归属条件、context 与清理边界。复杂关联的反射解析、内部 callback 注册缓存、生产批量迁移、公司旧版 API、完整 outbox relay 幂等/恢复可在阅读对应源码时深入；本课不会用这些扩展冲淡核心。

完整调用链如下。括号说明当前是谁的连接，箭头是实际调用，不暗示框架会加入一个没有传入的 Tx：

```text
main → Run
  loadConfig → mysql.NewConnector → sql.OpenDB
  gorm.Open(mysql.New(Config{Conn: pool}))             一个池，不是两个池
  remaining=0 → 登记owned/defer drop → AutoMigrate    仅本run五张教学表
  openBroker → passive404 → declare → confirm        本run一个队列
  E01/E02 → GORM chain → processor.Execute
    ConvertToAssignments/BuildQuerySQL
    Statement.ConnPool.ExecContext/QueryContext(ctx, SQL, Vars...)
    driver → 真MySQL → Error/RowsAffected/Scan
    Trace(展示插值SQL) → 独立database/sql SELECT
  E03 → manualTx/Transaction → Begin
    root.Statement.ConnPool=*sql.DB
    tx.Statement.ConnPool=*sql.Tx
    reserve(tx) → UPDATE数量 → insertOrder(tx) → INSERT
    callback nil → Commit → verify外部SELECT
    error/panic → Rollback → catchPanic/verify
  E05 → Transaction → tx.Create(Order)
    create INSERT → AfterCreate(tx的NewDB session，仍*sql.Tx)
      tx.Create(HookAudit)
      危险: AMQP publish → confirm Ack → callback ErrBusiness → SQL Rollback
      修复: tx.Create(Outbox) → callback nil → SQL Commit → relayOne
    broker.Get → body/id核对 → Ack → 下一Get为空
  E06 → holder Begin/FOR UPDATE
    startOwned → worker Conn/CONNECTION_ID/Begin/INSERT/UPDATE等行锁
    owner PROCESSLIST屏障 → timeout → worker清理 → Result → Done → owner join
    holder Rollback → PROCESSLIST原ID消失 → verify库存5/订单0
  defer broker.close → queue.delete/被动404/连接关闭
  defer drop → 精确五表DROP/remaining0
  defer pool.Close → open/inuse/idle全部0
```

## 把 Go 与 GORM 的对象分清

`*gorm.DB` 是一个带配置、Error、RowsAffected、Statement 的句柄。名字虽然叫 DB，它可以包装池，也可以包装一个事务连接，甚至在 E06 包装一条专属 Conn。`l.db` 和 `pool` 借同一个数据库池；复制一个 `*gorm.DB` 指针不会复制数据库，也不会自动创建 Tx。

`:=` 在当前作用域声明新变量。本课 `result := db...Updates(...)` 把返回句柄保存在新变量，`result.Error` 是该次操作的错误；`if err := ...; err != nil` 的 err 只在 if 内有效。Go 的 error 是显式返回值，调用失败不会像 Java 异常那样跳过下一行。丢掉 error 后继续 `return nil` 就是向 Transaction 声称整个业务成功。

`Stock{Quantity:0, Active:false}` 是 struct 值。`map[string]any` 的 any 是接口：这里动态值分别是 int 和 bool，GORM 可以判断 map 的 key 确实出现过。结构体字段的零值不能区分“未填”与“主动设成零”，因此更新输入一般需要显式字段选择、map，或上层可表示 presence 的 DTO，而不是把 ORM model 直接当 patch 请求。

Java 类比可以借 JDBC Connection 理解 Tx：这里的 Statement.ConnPool=*sql.Tx 类似 DAO 明确收到同一个事务 Connection。Spring 线程绑定和代理事务不在 Go/GORM 中；处于 callback 的调用栈并不会让另一个外层 db 自动加入 Tx。goroutine 也不会继承“当前事务”。context 是显式传播的取消/截止信息，不能替你传 Tx 或撤销外部副作用。

## E01：更新成功到底更新了什么

运行前，zero 是 alice 的数量 9、active=true；selected 是数量 6、true；foreign 是 bob 的数量 8、true。先看参数式：

```sql
UPDATE `m23_<run>_stocks` SET `active`=?,`quantity`=?
WHERE (sku = ? AND owner = ?)
  AND `m23_<run>_stocks`.`deleted_at` IS NULL
-- Vars: [false 0 zero alice]
```

这是 DryRun 的实际 Statement.SQL/Vars，用来解释 builder，没有发送服务器。随后 map 的同样条件真实执行，database/sql 读取到 0/false 才算该实验通过。`[SQL展示]` 是 GORM `Dialector.Explain` 为人阅读插值后的显示，它不是 packet 抓包，更不能把显示字符串复制拼接回线上 SQL；真正执行使用 SQL 与 Vars 分离，driver 的 `InterpolateParams=false`。

| 调用与规则 | 本课实际 SQL/行内容 | 修复意义 |
| --- | --- | --- |
| `Updates(Stock{Quantity:0,Active:false})` | 未生成 UPDATE，RowsAffected=0；zero 仍9/true | 不能把这个0误判为SKU不存在 |
| `Updates(map[string]any{"quantity":0,"active":false})` | UPDATE两列，影响1；zero=0/false | map的key明确出现，零值也写 |
| `Select("Quantity","Active").Updates(Stock{...})` | UPDATE指定两列，影响1；selected=0/false | 明确字段presence，不乱覆盖Owner/SKU |
| 再 `Update("active",false)` | Error=nil、影响0，行确实存在 | 本连接`ClientFoundRows=false`，统计实际改变；不用UpdatedAt掩盖 |
| 缺WHERE `Update("quantity",1)` | ErrMissingWhereClause，foreign仍8 | 框架拦发送，错误需要caller检查 |
| `Where("1 = 1").Update("quantity",7)` | 本run三行都真实变7 | GORM不理解“1=1没有业务限制” |
| `Where("sku=?","foreign")` | alice路径真实把bob行改6，影响1 | 有WHERE不意味着有归属授权 |
| `Scopes(owned("alice")).Where(sku)` | 影响0，bob行仍6 | 归属条件与记录条件一起过滤 |

这里 soft-delete 自动条件不会独自被算成足够的业务 WHERE。锁定 `callbacks/helper.go:checkMissingWhereConditions` 在存在 `soft_delete_enabled` 时另查条件数量。因此“模型有软删除，缺WHERE也安全”不是事实。`AllowGlobalUpdate:true` 或 Raw SQL 可以绕过框架保护；它们只应在审查明确的批处理接口使用，本课危险全表反例只写自己的三行合成数据。

empty 查询的两种语义：`First(&one)` 实际加主键排序和 LIMIT 1，0行时返回 `ErrRecordNotFound`；`Find(&many)` 0行返回 nil/RowsAffected0/empty slice。First 表达“我要一个”，Find 表达“我要集合”；不是 MySQL 的失败与成功之别。旧目的对象若有非零主键，还可能自动加入主键条件；本课每次空查询用新的零值目标，避免另一种隐藏条件干扰当前教学。

`RowsAffected` 的含义也必须看操作：UPDATE 在本连接统计 changed rows；SELECT 是扫描行数；全零 struct 根本没发UPDATE；soft Delete 是 UPDATE；以上都不能独立证明整段业务已经 COMMIT。`Error==nil` 与 `RowsAffected==1` 需要按业务契约一起检查。reserve 的条件扣减要求恰好1行，0行转换成 ErrConflict，不伪装成网络错误。

## E02：传统链会污染，Session 的语义要看源码

链的最小问题不是“GORM一律线程不安全”，而是你复用了一个已经初始化、会继续修改同一 Statement 的传统句柄：

```go
base := db.Model(&Stock{}).Where("owner = ?", "chain")
base.Where("quantity = ?", 1).Find(&a)
base.Where("quantity = ?", 2).Find(&b)
```

第二条真实 SQL 含 `owner='chain' AND quantity=1 AND quantity=2`，a 长度1、b长度0。这里 `Where` 在 `clone==0` 的句柄上改同一 Statement，finisher 不会把它自动恢复成最初的owner条件。

修复用一条干净的基础链，末尾 `Session(&gorm.Session{})`，随后每次派生查询复制已有条件。结果1/1，且都保留owner。`Session(NewDB:true)` 则选择新 Statement：本课真实查询获得当前五条所有owner的记录，而不是chain两条。它仍借同一ConnPool，既没有新库，也没有新事务；清条件修复如果连认证归属一起清掉，就换成了数据泄漏。

`WithContext(ctx)` 源码就是 `Session(&Session{Context:ctx})`：复制所需Statement、存ctx，最终传给ExecContext/QueryContext/BeginTx。它有新session的可派生语义，但不是自动归属过滤，不是开始Tx。traditional `Scopes(owned(owner))` 接收函数 `func(*gorm.DB)*gorm.DB`，闭包捕获owner字符串值；函数返回带WHERE的当前句柄。认证值必须由业务层给它，SQL模型不会自己知道当前用户。

泛型 API 的 T 固定 model 类型。本课实际版本返回约定如下，不混用传统的 `.Error`：

| API v1.31.2 | 返回值 |
| --- | --- |
| `gorm.G[Order](tx).Create(ctx,&order)` | `error` |
| `gorm.G[Stock](db).Where(...).First(ctx)` | `(Stock,error)` |
| `...Find(ctx)` | `([]Stock,error)` |
| `...Update(ctx,"active",false)` | `(int,error)`，这里int是影响行数 |
| `...Select(...).Updates(ctx,Stock{...})` | `(int,error)` |

`chainG.with` 复制 ops slice、追加新操作；`apply(ctx)` 用 NewDB session依次应用操作。实验复用泛型基础owner链，两个quantity分支各1条；Select Updates把chain2写成0/false，Update把chain1的active改false。**泛型不替你修补零值规则，也不补owner条件。** 它改变类型与链的构建方式，而不是改变数据库事务原子性。

官网更新页仍有把泛型 Update 接成单 error 的示例，和本机 v1.31.2 的接口/实现不同。课程以 `generics.go` 的 `CreateInterface.Update/chainG.Update` 的 `(rowsAffected int,err error)` 为准。公司代码若锁老版，先读它自己的 module 与源码再解释，不能为适配本文擅自升级。[GORM更新文档](https://gorm.io/docs/update.html)、[v1.31.2泛型源码](https://github.com/go-gorm/gorm/blob/v1.31.2/generics.go)。

## E03：从 M21 的明确事务迁到 GORM

正常手动路线 `manualTx` 是 `Begin → reserve(tx) → insertOrder(tx) → Commit`。`Begin` 得到的新句柄的 Statement.ConnPool 是真实 `*sql.Tx`；退出Rollback立即登记。SQL 只接受当前tx，callback没有Commit权。错误路径返回原error并回滚；panic路径在defer直接recover到原值、先Rollback、再panic，外层catchPanic转换成非nil失败。M21 里逐行说明的“panic不自动给err赋值”在这里同样成立。

`rollback(rawTx)` 忽略已结束的 `sql.ErrTxDone`，其他清理失败 Join 到原失败。这里取底层Tx避免 `gorm.DB.Error` 已有业务错误导致 `tx.Rollback().Error` 混进同一个旧Error，读者知道清理本身的结果是什么。Commit 放在完整body成功后的普通语句，不藏在defer里猜named err是否nil。

第二条正常路线是现网常见 `root.Transaction(func(tx *gorm.DB) error {...})`，里面第二步再展示 `gorm.G[Order](tx).Create(ctx,...)`。传统事务容器与泛型操作可以组合，但始终传同一个tx。两种正常路线的外部SELECT都为数量4、订单1。

| 路径 | 关键控制流/连接 | 外部真实SELECT |
| --- | --- | --- |
| manual提交 | 两步sql.Tx，显式Commit | 4/1 |
| manual普通error | 两步未提交 → ErrBusiness → deferredRollback | 5/0 |
| manual panic | 扣库存 → panic → Rollback → 外层恢复失败 | 5/0 |
| Transaction正常 | callback nil，GORM Commit | 4/1 |
| callback里混root | 库存tx；订单root另一连接先提交；callback错误只回滚库存 | **5/1** |
| 同tx修复 | 两步同tx，后续ErrBusiness | **5/0** |
| 吞真实1062 | 预置旧订单；扣库存；INSERT主键重复；只打印后返回nil | **4/1**，旧订单只有1条，库存再少1 |
| 传播1062修复 | 同样输入；把Create的Error交回callback | **5/1** |
| 内层recover返回成功 | 扣库存；panic；recover未写具名err；callback收到nil | **4/0** |
| 外层recover修复 | panic越过callback，GORM先Rollback；外层再转失败 | **5/0** |
| 两次默认单写 | root扣库存的默认事务已提交；root订单1062失败 | **4/1** |

正常、普通失败、panic的时间线分别要这样读：

| 步骤 | 参与者/当前函数 | 正常 | error / panic |
| --- | --- | --- | --- |
| 1 | owner → Begin | 一条连接绑定Tx | Begin失败无body可执行 |
| 2 | callback → reserve | 条件UPDATE影响1，库存暂时4 | Error/0行原样返回，Tx退出回滚 |
| 3 | callback → insertOrder | INSERT影响1但未提交 | 1062必须return；panic展开callback栈 |
| 4 | Transaction或manualTx | callback完整nil → Commit | error/Panic时Rollback；内层吞掉会把路径错误变正常 |
| 5 | owner → verify | 外部两表4/1才证明完整业务事实 | 正确5/0或旧订单5/1；危险版半笔结果暴露 |

锁定源码 `Transaction` 用 `panicked:=true`，defer在panicked或err非nil时Rollback；只有 `fc(tx)` 返回nil才将panicked=false并Commit。它不分析callback里面发生过什么。`Create` 的返回结果携带Error，caller丢弃了它，不能指望最初的tx句柄自动追踪所有派生结果错误。

v1.31.2 的原生Transaction对Rollback返回值未加入返回error，这是一项实际库实现边界；不能把它宣称成M21式“清理错误全保留”。本课manualTx演示需要保留该事实时的明确写法，所有原生回调反例另以真实SQL读回证明回滚结果。[锁定事务源码](https://github.com/go-gorm/gorm/blob/v1.31.2/finisher_api.go)、[事务文档](https://gorm.io/docs/transactions.html)。

默认 create/update/delete 的事务只包**一条操作及其callbacks/关联**，不自动包下一条业务调用。不要因两条各有默认写事务，就说“库存订单整个事务已经成立”。也不要在callback里用root寄希望于“同goroutine自然参加”。Commit出错仍可能是响应丢失后的UNKNOWN，M21已用真实协议截断验证；GORM包装没有消除那个数据库边界，本课不重复停服务/代理故障。

## E04：软删除、关联与分页隐藏的 SQL

`gorm.DeletedAt` 给model加入默认过滤。对soft SKU调用Delete，真实 SQL 是 `UPDATE stocks SET deleted_at=? WHERE sku=? AND owner=? AND deleted_at IS NULL`。随后普通Find长度0，但原始SQL物理COUNT1、deleted_at非NULL1、Unscoped长度1。删掉的记录仍占主键；不能把默认看不见理解成已释放唯一键。`Unscoped().Delete`会物理删，使用前仍要检查归属条件；本课最终DROP自有表完成清理。[软删除文档](https://gorm.io/docs/delete.html)。

关联实验有三条assoc订单，每条一个合法Line；第一条另有一个Owner=foreign的合成Line，用来证明父查询owner不会自动传进子关联。

N+1版先查三订单，再for循环逐订单 `WHERE order_id=?`，真实SELECT数是1+3=4。这里 `for i := range orders` 的i是索引，`&orders[i].Lines` 指向当前slice元素，ORM填充它，不是把range值副本的修改丢掉。Preload版用两条SELECT：父订单和 `WHERE order_id IN (...)` 的批量子查询；数量4→2，子Line仍有4个，错误归属没有因为批量读取而消失。

修复用父 `Scopes(owned("assoc"))` 并在 `Preload("Lines","owner = ?","assoc")` 再加子WHERE。实际仍两条SELECT，合法Line数3，独立逐项验证每个Line.Owner。Preload是实际另一条查询，不是一个JOIN；父/子查询间可以有并发写入，不把它宣传成默认快照一致性。当前实验没有并发改关联；需要一致读时要明确同Tx/隔离级别。[Preload文档](https://gorm.io/docs/preload.html)。

GORM的父查询Trace在processor执行完整callbacks后记录，因此日志中子SELECT可能先显示、父SELECT后显示；父Rows早已读出用于构造IN。hook的audit/outbox日志也可以早于订单INSERT的Trace，订单INSERT实际已经执行才会进入AfterCreate。log显示顺序不等于服务端SQL发送顺序，更不能凭logger行的位置说“先查子表再查父表”。本课计数统计完成的SELECT，业务正确性看实际关联结果。

分页必须显式确定顺序：`ORDER BY id ASC LIMIT 2 OFFSET 0/2`，ID唯一；total另用不带Limit/Offset的COUNT得到3。第一页assoc01/02，第二页assoc03，合计3且没有重叠。`page>=1,size∈[1,100]` 在SQL前检查，避免负数被GORM当取消限制；辅助测试使用nil lab.db证明非法输入根本没到数据库层。

OFFSET分页在跨请求期间有并发插删仍可能移位，唯一排序只能定义当次页序，不能保证多次请求快照。大OFFSET开销、游标分页、线上索引/迁移与查询计划详见M24，不拿本课三行实验当生产性能证明。AutoMigrate只在先核对不存在的本run五表使用，无外键DDL；GORM关系仍能Create/Preload，数据库外键约束需要另行设计。

## E05：hook 成功、SQL 回滚，真实 MQ 仍存在

`Order.AfterCreate(tx)` 是方法接收者为 `*Order` 的hook：GORM反射拿刚保存的model，判断是否实现 AfterCreateInterface，再在同一goroutine调用。Lab/Mode标记为 `gorm:"-"`，只是本次请求的进程依赖，不写SQL列，也不会在Find后恢复。

`callMethod` 给hook一个 `Session(NewDB:true)` 句柄，这里只是没有当前INSERT/WHERE条件，仍传同一 `Statement.ConnPool=*sql.Tx`。本课实际输出并断言该动态类型。HookAudit写当前tx，因此它和订单一起回滚；改用l.db去写audit会绕过这个原子边界。[Hook文档](https://gorm.io/docs/hooks.html)。

危险路径完全使用真实AMQP：

| 顺序 | 参与者/函数/等待点 | 已知状态/真实证据 |
| --- | --- | --- |
| 1 | owner → Transaction → reserve | Tx库存暂时4，未提交 |
| 2 | GORM Create → Order.AfterCreate | 订单INSERT已执行，COMMIT尚未执行；hook收到*sql.Tx |
| 3 | hook → tx.Create(HookAudit) | audit也未提交，同一连接 |
| 4 | hook → publish/WaitContext | 真broker接收，publisher confirm Ack=true；MySQL仍未提交 |
| 5 | callback返回ErrBusiness | Transaction回滚库存、订单、audit |
| 6 | owner → SQL查账 | 库存5、订单0、audit0、outbox0 |
| 7 | owner → AMQP basic.get | 真投递id/body=hook-unsafe，消息没有被SQL回滚撤掉 |
| 8 | owner → Delivery.Ack → 下一Get | Ack已发送；同channel后续basic.get-empty，消息已消费 |

这不是内存slice、打印日志或健康检查。本课使用一个独占随机**durable队列**，已有默认交换机，不创建新exchange/group；两条有限合成消息。RabbitMQ4.3.6默认禁止transient非exclusive队列，首次运行真实收到541拒绝后改用durable定义，没有改变服务器feature配置。队列durable不等于消息persistent；本课只验证当前broker的confirm/投递/清理，没有宣称重启耐久性。

修复hook只写Outbox事件意图，同一个tx同时保存业务和意图。失败版真实SQL为库存5、订单0、audit0、outbox0，同时broker basic.get-empty；成功版先Commit，真实库存4、订单1、audit1、outbox1，但队列仍为空，因为Commit不自动调用relay。然后 `relayOne`读取已提交unsent事件 → publish/confirm → 标Sent=true → 真Get/Ack，SQL读回Sent=true。

“提交后直接发MQ”仍有业务已经提交但publish失败的漏事件窗口；outbox将意图保存为可恢复数据库事实。这里relay只是同步单次教学入口，confirm后标Sent前仍可能失败造成重复事件，消费者需按eventID幂等；完整后台relay/恢复由M30组合课程负责。MySQL Tx与AMQP channel没有分布式原子提交，GORM hooks也不提供这个能力。

AMQP v1.15.0 `PublishWithDeferredConfirmWithContext` 只在publish开始前检查ctx，不能打断在途socket写入；`WaitContext`等待confirm时能观察取消。我们用socket deadline保证无ctx的Get/Delete和在途I/O有上限，失败收尾用新的8秒socket预算。这里不能只在Dial时设一次deadline：`connection.go:openComplete`会清握手deadline，heartbeater又会延长read deadline。`boundedConn`装饰真实net.Conn，把库的clear/extend截到owner的75秒绝对上限，仅owner能reset为独立清理预算；没有替换AMQP数据或模拟协议。辅助测试使用真实net.Pipe证明已过期限不会被库式clear/extend抹掉。confirm丢失时存在已发未确认的未知结果，不能随便换eventID再发；本课没有注入MQ响应丢失，但准确保留该边界。[锁定AMQP源码](https://github.com/rabbitmq/amqp091-go/blob/v1.15.0/channel.go)。

## E06：WithContext 的真正等待和取消清理

取消case只有一个应用worker goroutine；Run/cancelCase是owner。AMQP客户端内部reader/heartbeat由库连接管理，本课没有异步consumer。SQL worker用专属Conn，让当前服务器ID可以被owner独立观测。

| 顺序 | 谁 → 当前函数/实际等待点 | 状态/资源变化 → 为何下一步 |
| --- | --- | --- |
| 1 | owner → holder.Begin/FOR UPDATE | 持住cancel SKU行锁，数量5；立即登记退出Rollback |
| 2 | owner → WithTimeout/startOwned | 创建3秒worker预算、Result/Done/cancel登记；立即保护defer |
| 3 | worker → pool.Conn/CONNECTION_ID | 专属Conn；ID发容量1 channel，不会因owner退场卡发送 |
| 4 | worker → Session/Begin/insertOrder | 私有Statement.ConnPool=Conn，再Begin=*sql.Tx；订单已插但未提交 |
| 5 | worker → tx.Exec UPDATE | 在driver ExecContext等真实服务端行锁，尚无完成结果 |
| 6 | owner → waitSQL(PROCESSLIST) | INFO同时含自有表与m23_cancel_lock，才确认worker到了SQL等待 |
| 7 | context deadline到达 | Done关闭通知；driver观察后关闭这条连接，Exec返回DeadlineExceeded |
| 8 | worker → manualTx defer/Conn.Close | 事务Rollback与sql自动回滚可能竞争；CleanupErr必须nil；Conn归还 |
| 9 | wrapper → Result → cleanup/cancel → close Done | 结果到达与最终结束不是同一事实；Done最后关闭 |
| 10 | owner → 独立Background join | 等真实Done后读取结果/清理；已取消业务ctx不能耗掉清理预算 |
| 11 | owner → holder.Rollback | 解除自己的服务端行锁；客户端Done可能早于服务端检测断连 |
| 12 | owner → waitSQL(gone=true) | 原服务器ID确实消失，再进行独立查账 |
| 13 | owner → verify/Stats | 库存5、订单0、InUse0，不能只凭ctx.Err猜回滚 |

`WithContext`最终把ctx送给底层BeginTx/ExecContext，而不是一个同步“强制杀死业务”按钮。本课锁定driver的取消支持通过真实SQL证明；换其他驱动要查其契约。`database/sql.BeginTx` 的ctx也控制事务生命周期，自动Rollback与我们显式Rollback可竞争，ErrTxDone只是本地事务结束；最终表/服务器证据另读。[Go1.27.0 database/sql](https://pkg.go.dev/database/sql@go1.27.0#DB.BeginTx)、[GORM Context](https://gorm.io/docs/context.html)。

3秒是deadline机制，不是调度sleep。owner先读取可观察SQL屏障，ticker20ms只是查询频率；没有“睡1秒断言worker肯定在等锁”。正常跑时观察屏障2秒内成立；超时也会取消任务、释放holder，用独立预算join后精确清理，不把没进入真实等待的路径算PASS。

提前失败的defer先task.cancel，再主动释放holder，然后stopAndJoin；不能让服务端屏障永远留着。正常已join则不重复消费Result。worker具名res分别存Err/CleanupErr，Conn.Close的defer先执行，同函数边界recover后保留两个字段。wrapper最终清理错误只在Done之后读取，避免数据竞争。任务fn如果自身panic，wrapper只能恢复panic值，不能替fn还原丢失的返回槽，因此资源错误需要在资源owner函数内先保存。

## 断言与资源结束事实

六个默认辅助测试各有独立目的：

| 测试 | 明确证明 | 不能证明 |
| --- | --- | --- |
| TestOwnerIsBoundParameter | 恶意owner字符串只进入Vars，不进入SQL语法 | 真实归属授权已由builder自动完成 |
| TestInvalidPageRejectsBeforeSQL | page0/size0/size101在SQL前拒绝 | 数据库分页性能 |
| TestLibraryCannotExtendSocketBudget | 真net.Pipe在owner期限已过时仍返回deadline错误，库式clear/extend无效 | broker断线重连/耐久性 |
| TestResultIsNotFinalDone | Result已就绪、清理门未开时owner真join不返回；放门后读完整错误 | server事务已经结束 |
| TestJoinBudgetRetainsFailure | 已取消join预算返回ErrJoin+Canceled+原失败；新预算最终真join | 第一次join失败后任务已经结束 |
| TestEarlyReturnCancelsAndJoins | 原失败、工作Canceled、两层清理错误都保留；返回时Done关闭 | 任意第三方API取消都立即响应 |

测试用明确门和进入join的通知context，不用sleep断言调度。测试中的join观察者也登记独立ownerFinished，错误断言出口放门后先等观察者真正退出，再执行原任务保护join，避免两个owner并发修改task状态。门无论测试成功失败都由owner释放，随后新预算join；不存在断言失败后卡住的测试goroutine。`close(done)`建立完成观察同步点，`sync.Mutex`保护日志计数和共享writer；不会在持mutex时等待SQL。精确耗时和connectionID是本次观察，不是语言保证。[Go规范defer/panic/recover](https://go.dev/ref/spec#Defer_statements)。

每run资源只有：课程库golearn_m20中的五张 `m23_<24hex>_stocks/orders/lines/hook_audits/outboxes`，同后缀 `_events` 队列，一个最多4连接的池，一条AMQP连接，一个有界SQL worker。随机名字在建对象前已登记，建表前确认五个名字不存在，建队列前被动声明确认404。没有共享go.work、全局服务/代理/隧道变更；不操作DeepHelp数据或其他课对象。

清理顺序是worker/Tx先结束 → 精确queue.delete → passive404读回 → Close AMQP → 精确五表DROP → information_schema remaining0 → pool.Close/Stats全部0。失败同样执行已登记defer，清理使用Background/新socket预算，Join保留原失败与清理原因。GORM查询内部关闭Rows，独立查账用QueryRow.Scan，不把裸Rows交给caller；本课不拥有安装者课程容器和隧道的关闭权。

## 锁定源码入口与材料纠正

本机 module cache 根为 `C:/Users/Lenovo/go/pkg/mod`；按下面路径/符号读，网络页面只是入口。源码版本由go.mod/go.sum、`go list -m all`、`go mod verify`重核对，不使用“最新版应该这样”的记忆。

| 真实路径/符号 | 沿源码该看到的调用 |
| --- | --- |
| `gorm.io/gorm@v1.31.2/gorm.go:Session/WithContext/getInstance` | clone1建新Statement、clone2复制Statement、clone0复用；ConnPool/Context保留方式 |
| `finisher_api.go:Updates/First/Find/Transaction/Begin/Commit/Rollback` | Dest→callback；First置RaiseErrorOnNotFound；Begin替换Statement.ConnPool |
| `callbacks.go:processor.Execute` | 执行注册callback → Trace/Explain；SQL显示晚于某些hook/preload日志 |
| `callbacks/update.go:ConvertToAssignments/Update` | struct零值筛选或Select；最后ConnPool.ExecContext(ctx,SQL,Vars...) |
| `callbacks/helper.go:checkMissingWhereConditions` | 软删除过滤不能充当业务WHERE；真实ErrMissingWhereClause |
| `callbacks/query.go:Query`、`scan.go:Scan` | QueryContext → Rows扫描 → 空First错误 |
| `callbacks/transaction.go:BeginTransaction/CommitOrRollbackTransaction` | 默认单条写事务，内部操作已在Tx时不另开一个池事务 |
| `callbacks/callmethod.go:callMethod`、`callbacks/create.go:AfterCreate` | NewDB session仍当前ConnPool → 调model方法 → Error参与提交/回滚 |
| `generics.go:G/apply/chainG.with/Update/Updates/First/Find` | 类型T、ops副本、每次应用上下文、直接返回rows/error |
| `gorm.io/driver/mysql@v1.6.0/mysql.go:Initialize` | 使用Config.Conn既有池，并读版本 |
| `github.com/rabbitmq/amqp091-go@v1.15.0/channel.go/confirms.go/connection.go` | deferred publish与WaitContext的取消差异；basic.get/delete/passive协议；openComplete/heartbeater deadline行为 |

原 `files/开发框架` 提供“GORM与MySQL CRUD”的方向，但仅CRUD成功不够判断复杂后端的安全性。本课补出数据库行内容、Tx范围、归属过滤、真实MQ边界，修正文档/版本差异；没有复制整批材料。传统API在现网仍需能读，泛型API自v1.30加入，当前v1.31.2不是“类型安全就事务安全”。

真实结果与历史失败见 [STATUS.md](STATUS.md)。范围边界包括：单节点学习服务、少量合成数据、当前进程协议；没有生产负载/权限体系评估，没有服务重启/宕机/硬断电耐久性、生产TLS、多节点MQ、所有网络失败排列、完整分布式事务或公司旧版本兼容证明。
