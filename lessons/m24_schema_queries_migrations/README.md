# M24：从 Go 查询代码读出表结构、分页与迁移契约

这课已经实现真实 MySQL 实验。你会从“订单金额少了几分、用户 ID 变了、第二页重复、发布新版本后旧程序报错”这些具体结果，反推 SQL 与 Go 调用之间的契约。正常入口包含错误复现和修复，所有故障都被预期并用读回事实核对；不用另做课后练习。

推荐用 15～20 分钟完成核心：运行一次约几十秒以内，随后按四段日志阅读。前 4 分钟看真实值与约束，接着 4 分钟看联合索引，5 分钟看两页之间的写者，最后 5 分钟看兼容迁移、DDL 与资源结束。源码和下面解析提供完整展开，遇到真实仓库时再定位相应段落。

本 module 是 `go-demo/lessons/m24_schema_queries_migrations`，`go 1.27.0`，没有 toolchain/replace/go.work。硬前置 M20 已实际验收；这里复用它的 READY 学习实例、凭据结构、驱动版本和“池不是连接、Rows 要关闭、值用参数绑定”的契约，不把 M20 的表当实验对象。默认学习库 `golearn_m20`，每次运行生成 `m24_<24hex>_users/orders/types/pages/evolve/versions/audit` 七张表。

## 运行和阅读入口

从自己的仓库根执行。若课程被汇总到主目录，根目录是 `D:/IdeaProject/go-demo`；本次执行 worktree 根是 `C:/Users/Lenovo/.codex/worktrees/go-course-m24/go-demo`。

```powershell
go -C lessons/m24_schema_queries_migrations run ./cmd/demo
go -C lessons/m24_schema_queries_migrations test -v ./...
go -C lessons/m24_schema_queries_migrations vet ./...
```

默认 `test` 只运行辅助断言，两个实库测试明确 SKIP；实库开关不会自动安装/重启服务：

```powershell
$savedIntegration = $env:GOLEARN_INTEGRATION
try {
    $env:GOLEARN_INTEGRATION = '1'
    go -C lessons/m24_schema_queries_migrations test -v -count=1 -timeout 120s ./...
} finally {
    $env:GOLEARN_INTEGRATION = $savedIntegration
}
```

入口通过 `GOLEARN_SERVICE_INDEX` 读取路径，默认只读 `D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json`。读取其中 `services.mysql.credential_file`，只在内存加载 username/password/database，禁止打印 Config/DSN。当前实例 MySQL 8.4.11、loopback 127.0.0.1:13306，由主 agent 管理；本课不管理服务、隧道或全库权限。READY 与 TCP 连通不是验收，入口还执行版本/库/时区查询和后续真实业务写读。

阅读顺序和每个符号的职责如下：

| 文件 / 符号 | 谁调用、交付什么、真实副作用在哪里 |
| --- | --- |
| `cmd/demo/main.go`：`main → execute` | 设置信号与 90 秒总预算；等 Run 返回后才能退出进程 |
| `internal/course/config.go`：`LoadConfig → driverConfig` | 读取配置，构造真实 mysql v1.10.1 DSN；无秘密日志，无安装动作 |
| `lesson.go`：`Run → create → 四个实验 → cleanup → DB.Close` | 唯一 DB/表 owner；创建前核对不存在，结束后核对目标不存在 |
| `values.go`：`schemaAndValues → lossyTypes → scaleAndLength` | 用户/订单约束、真实浮点/整数损失、同 Conn 的警告读取 |
| `plans.go`：`plans → explain/rawRows → queryShape` | 有界参数化种子、EXPLAIN、执行只读 ANALYZE、真实查询结果/计数 |
| `pages.go`：`pagination → readPage/readPageQuery → betweenPages → snapshotRepair` | 定义排序与游标，安排独立写者，核对页集合与短 RR 快照 |
| `migrations.go`：`migrations → expand/backfill/contract → implicitCommit → metadataLock` | 结构/版本账本、旧新读写、失败后事实、真实 DDL 提交与锁等待 |
| `task.go`：`startTask → work → result → wrapper defer → finished → join` | 区分结果通知和全部清理完成；owner 负责取消与等待退出 |
| `course_test.go` | 金额/标识符/Finished 独立断言；完整实库与主预算取消后的真实清理 |

现在必须理解的是固定类型/NULL、参数与标识符、Rows/Tx/Conn owner、索引查询与覆盖、稳定游标、DDL 和版本账本的边界。复制拓扑、分片、在线迁移工具内部、统计采样算法、多部署者 migration lock/校验和和生产备份恢复可以延后；本课没有把它们伪装成已验收功能。

## 第一段：一张订单表就是一份业务契约

订单表实际 DDL 在 `schemaAndValues`。`T` 在本文表示本 run 的精确 orders 表，程序打印完整表名，并且不会接受任意表名参数。

```sql
id BIGINT NOT NULL PRIMARY KEY,
tenant_id BIGINT NOT NULL,
user_id BIGINT NOT NULL,
request_key VARCHAR(64) COLLATE utf8mb4_0900_bin NOT NULL,
amount DECIMAL(12,2) NOT NULL,
status VARCHAR(16) NOT NULL DEFAULT 'NEW',
note VARCHAR(100) NULL DEFAULT '未填写',
created_at DATETIME(6) NOT NULL,
shipped_at DATETIME(6) NULL,
payload VARCHAR(256) NOT NULL DEFAULT '',
UNIQUE KEY uq_request(tenant_id,request_key),
CHECK(amount>=0),
FOREIGN KEY(user_id) REFERENCES U(id)
```

表用 InnoDB、utf8mb4，默认文本 collation 是 utf8mb4_0900_ai_ci；request_key 单独用 utf8mb4_0900_bin。`U` 是本 run 用户表。外键证明“用户存在”，CHECK 证明“金额非负”，它们不能证明“当前调用者有这个用户的权限”或“订单状态转换合法”。后两项仍需要业务逻辑与正确事务边界。跨租户用户归属约束也没有由当前单列外键自动实现，真实业务若要求它应设计 `(tenant_id,user_id)` 的对应约束/授权校验。

`NOT NULL` 表示不能显式写 NULL；default 表示缺列/DEFAULT 时填默认值。它不表示“空串与 NULL 都会被替换”。实验实际写三行：id1 不提供 note，读到“未填写”；id2 显式 NULL，读到 `sql.NullString.Valid=false`；id3 写空串，读到 `Valid=true` 与空字符串。两行的 `.String` 都可能为空，因此检查 String 不够，必须看 Valid。`sql.NullTime` 对 shipped_at 同理。

Java 可以把 NullString 类比成“值+是否存在”的承载对象，但 Go 不会因为 Scan 目标是普通 `string` 就自动为 NULL 创建一个包装对象。结构体字段是传给 Scan 的地址；Scan 将当前行转换后写入这些变量。`:=` 声明的 `rows` 是 `*sql.Rows` 句柄，它和 DB 是不同资源：循环退出不会自动关闭 Rows，函数注册 defer、检查 Rows.Err 并 Close 才完成拥有者职责。

三行真实输出的关键部分：

```text
id=1 user=9007199254740993 amount=16777217.03 note.Valid=true note="未填写"
id=2 user=1 amount=0.10 note.Valid=false note=""
id=3 user=1 amount=0.00 note.Valid=true note=""
created=2024-02-29T23:59:59.123456Z shipped.Valid=false
```

BIGINT signed 与 Go int64 的明确宽度配合；不能把用户 ID 先转换成 float64，再绑定到 BIGINT。UNSIGNED BIGINT 的更大正数范围需要另行选 uint64/十进制文本和核对驱动绑定，不能把本课 signed 契约直接外推。Go `int` 的宽度随平台变化，本课固定 int64 来表示业务 ID。

金额用非负 DECIMAL(12,2)，最多十位整数与两位小数，Go 读成十进制文本再用 `decimalCents` 转 int64 分。本课最大分是 999999999999，未触及 int64 溢出；真实币种/负数/不同 scale 必须重新定义解析契约。Java 的 BigDecimal 可帮助理解精确十进制，但 Go 标准库没有隐式等价的 BigDecimal 类型，不能把驱动金额直接 Scan 成 float64 后再修补。

DATETIME(6) 保留微秒而不是纳秒。`ParseTime=true` 决定驱动把 DATE/DATETIME 转成 time.Time；`Loc=time.UTC` 决定 Go 解析位置；服务端 `time_zone='+00:00'` 控制会话。DATETIME 自身存日历字段，不像 TIMESTAMP 那样按会话时区做存取转换；把 UTC 写进 DATETIME 是本课应用约定。代码只写微秒对齐的合法日期，不靠 zero-date 的平台容错。

唯一请求键是 `(tenant_id,request_key)`。id1 的 Req-A 和 id2 的 req-a 已经真实共存；同租户再写 Req-A 失败 1062，随后 SELECT 证明仍只有三行且原金额未变。这说明数据库能挡住重复创建。它不能单独证明“重试成功”：同一键携带不同 payload 应读取原请求摘要/金额，判断重放还是冲突，本课用原金额与 9.99 不同明确记录冲突。幂等语义还需要状态与结果记录，后续 outbox 课程再组合。

字符实验读回“小明🛍️”，并实际查询 `A=a`：ai_ci 为 1，bin 为 0。utf8mb4 决定能表示的字符，collation 决定比较/排序；二者不是同一开关。实际业务若认为请求 ID 大小写不同，应在唯一键上固定相应比较规则，不能只依赖默认表 collation。

本课捕获三种约束故障后都重新 COUNT：外键缺用户1452、负金额 CHECK3819、request_key NULL1048，三次之后行数仍是3。`errors.As` 识别 mysql.MySQLError，只证明错误类型/码；后续 SELECT 才证明没有额外订单。

### 实际写读让错误类型无处藏身

`lossyTypes` 把同一个数同时写进坏类型和好类型。当前真实结果如下：

| 输入 | 坏类型的实际结果 | 正确契约 |
| --- | --- | --- |
| 金额16777217.03 | FLOAT text协议返回16777200.00；在库内CAST到DECIMAL读回16777218.00 | DECIMAL读回16777217.03，再解析为1677721703分 |
| 用户9007199254740993 | DOUBLE读回9007199254740992 | BIGINT→int64读回9007199254740993 |
| 用户2147483648 | signed INT严格模式写失败1264，故障表仍1行 | 根据业务范围使用BIGINT，而不是截断/容忍警告 |
| VARCHAR(32)写33个字符 | 严格模式失败1406，原4行仍4行 | 验证输入范围/选择合适schema；不盲目扩大字段绕过业务规则 |
| DECIMAL(12,2)写1.005 | Exec错误为nil，SHOW WARNINGS=1265，最终值1.01 | 入口先决定金额精度/舍入规则，不能把nil错误当输入无损保存 |

FLOAT 的文本展示与已经存入的近似数需要区分，因此特意增加 CAST 读回，不把16777200.00误解释成原始存储的精确十进制值。FLOAT/DOUBLE 是近似类型，金额和唯一 ID 不应依赖它们。最后一个例子也纠正“严格模式会拒绝所有精度改变”的直觉：当前实际版本仍进行了 scale 转换，警告需要观察。

`SHOW WARNINGS` 是会话信息。`scaleAndLength` 取得一个独占 `*sql.Conn`，INSERT→SHOW WARNINGS→SELECT 都用 conn，最后 Close 将会话归还池；使用三次外层 DB 调用可能借到不同会话并丢掉相关警告。这里 Conn.Close 不等于关闭整个 DB，最终池由 Run 关闭。

## 第二段：读 EXPLAIN 的目的，是对应这条业务查询

1200条种子在12个参数化批次中写入，每批100行。它们仍使用 `?` 和 args，程序只拼固定数量的占位符，不拼业务值。循环里的 args 是每批新建的 slice，append 增加元素；调用 ExecContext 返回后才开始下一批，当前没有并发共享它。准备数据Tx提交后加联合索引：

```sql
ALTER TABLE T ADD INDEX idx_feed(tenant_id,status,created_at,id);
SELECT id,created_at FROM T
WHERE tenant_id=10 AND status='PAID'
ORDER BY created_at,id LIMIT 10;
```

为什么这个次序？请求先用租户与状态两个等值条件定位范围；这个固定范围内部已经按 created_at、id 排序。只读取 id/created_at，索引中都有，所以本次 `Extra=Using index`；InnoDB 二级索引也携带主键，此处把 id 显式写在排序后缀，方便直接读分页意图。索引不是把每个查询字段随意堆起来，范围条件、可选过滤、排序方向和选择性变化都需要重新看真实计划。

当前计划对照如下；估计行数每run可能从1165变1216，因为统计采样，不是实际行数错误。实际COUNT/无界遍历始终1203。

| 查询变化 | 当前访问方式/索引 | 当前观察与影响 |
| --- | --- | --- |
| tenant/status等值，读id/created_at，按created_at/id | ref，idx_feed，估计20 | Using index，无filesort；覆盖且顺序匹配 |
| 只按status过滤，缺tenant | index，idx_feed，估计约全表 | 扫整个索引并Using where/Using index/Using filesort；用了索引不等于做了高效查找 |
| tenant/status不变，改按amount/id | ref，idx_feed，估计20 | 过滤仍可用前缀，但amount不在所需排序后缀，Using filesort |
| CAST(tenant_id AS CHAR)后过滤 | index，idx_feed | 当前普通索引无法按原tenant值直接查找，扫描并filesort |
| SELECT*，过滤/排序不变 | ref，idx_feed | Extra不再Using index，需要读payload等聚簇记录字段 |
| text_id='1' | ref，idx_text，估计1 | 字符串类型匹配，精确索引查询，结果1行 |
| text_id=1 | index，idx_text，估计5 | 列转数字后扫描，'1'/'01'/'001'都匹配，结果3行 |

最左前缀描述的是按索引前导列进行查找的能力，不是“缺第一列永远绝不出现这个索引”。本次缺tenant仍选idx_feed全索引扫描，已经是反例。函数索引、生成列、skip scan、优化器转换和参数类型会改变具体计划，因此不要背“所有函数/所有隐式转换都让索引失效”。当前函数与字符串列比数字的实际计划、结果都写在日志中。

`type=ref`、`type=index`、`key`、`key_len`、估计`rows`、`Extra`一起看。`key_len=74`不是“读取了74行”；它表示本次用作查找的索引字节长度，受字段/字符集/可空性等影响。`Using index`表示当前列能由索引覆盖，不表示只访问一行。`filesort`是额外排序的名称，不自动证明数据已落磁盘；不要由一个词宣称IO故障。

`EXPLAIN FORMAT=TRADITIONAL`没有执行这些SELECT的完整结果。`EXPLAIN ANALYZE FORMAT=TREE`则会真实执行：本课只对独占合成表的两个有界只读SELECT使用它。覆盖版本打印 `Covering index lookup` 与实际返回10行；读取payload的版本打印普通 `Index lookup` 与实际20行。精确毫秒只是本次观测，不是性能SLA，也没有包装任何真实业务写语句做ANALYZE。

### 查询形状比一个耗时数字更可核对

`queryShape` 实际观察了四件事：

1. SELECT* 有10列，目标投影只用id/user_id两列。字段不是越多越安全：迁移后新增字段还可能破坏按位置Scan，payload也增加传输与回表需求。
2. 无LIMIT的实际遍历返回1203行，有LIMIT的页面返回10行。这里只用小数据演示；真实无界读会让Rows/连接、网络、Go slice内存与请求预算一起扩张。
3. 先查10个订单，再每单查用户姓名，明确11次SQL；JOIN一次返回同顺序/同姓名。测试比较结果而不是看屏幕“似乎快一点”。一对多JOIN可能膨胀结果和改变分页，应按真实关系考虑批量IN或先分页主实体。
4. 非覆盖SELECT*真实取20行，单payload就带回4000字节。需要列表两列时用投影可以保持覆盖、避免为不用的字段取聚簇记录；没有声称测到了生产磁盘读取次数，计划与实际数据搬运是本课证据。

N+1计数是当前repository明确发出的调用数，不包含驱动可能的prepare/协议内部包数。`rawRows`会把sql.RawBytes当前行立即复制成string；RawBytes引用驱动缓冲，不能把它存起来等Next后再解释。读取函数在返回纯map/slice之前检查Rows.Err并Close，后续实验不会握着未结束的Rows占用池。

## 第三段：分页是跨时间的集合契约

初始pages表只有6行，id1/2时间0，id3/4时间1，id5/6时间2。排序定义为 `created_at DESC,id DESC`，每页3行，所以第一页 `[6 5 4]`，游标是id4的时间1与ID4。

错误下一页条件 `created_at < cursorTime` 返回 `[2 1]`，同时间的id3被漏掉。正确条件：

```sql
SELECT id,created_at FROM P
WHERE created_at < ? OR (created_at = ? AND id < ?)
ORDER BY created_at DESC,id DESC LIMIT 3;
```

两个时间参数绑定同一个cursor.Created，最后参数绑定cursor.ID。`cursor`结构体是上一页最后键的值副本，不是数据库行指针或长期打开的Rows。使用严格小于，与降序排序一致；升序应相应改大于。游标还应绑定租户/过滤条件/排序版本，服务端不能接受“任意游标+任意过滤”组合。列表公开API通常将这些字段编码并验证；这课只实现内部明确类型。

`ORDER BY created_at`只定义时间之间的顺序，同值的相对顺序没有保证。某次运行恰好按主键排列，不构成SQL契约；因此不人为断言“坏SQL必在每次运行乱序”，而用id3必漏的时间-only反例证明游标不完整。唯一ID补全tie-breaker，但排序字段也必须适合跨页稳定性要求。

真实页间变化与结果：

| 页间真实写入 | offset下一页 | keyset下一页 | 业务影响 |
| --- | --- | --- | --- |
| 新id7插入最前 | [4 3 2] | [3 2 1] | offset重复4，原1被推到后续页；keyset继续原游标，新头部7留给刷新 |
| 删除第一页最前id6 | [2 1] | [3 2 1] | offset漏3；keyset按值仍能继续 |
| 已读id5改到游标后，未读id3改到游标前 | 本段关注keyset | [2 1 5] | keyset也重复5、漏3，排序键可变破坏跨页集合稳定 |

所以keyset解决行位置漂移，不自动提供整个列表的时间快照。订单feed宜用不可变创建序号/不可变创建时间与唯一ID；按会持续改变的updated_at排序时，需要接受实时列表的重漏契约、做客户端去重与刷新，或者为导出等严格场景提供受控快照。即使用不可变键，删除、权限变化与过滤状态变化也会改变集合，不能把游标包装成全局不变承诺。

### 写者和读者的实际时间线

`betweenPages`只在`readPageQuery`返回纯值后被调用。下面是每个插入/删除/更新阶段共有的机制，只有SQL内容不同。

| 次序 | 参与者 / 当前函数与等待点 | 状态与下一步证明 |
| --- | --- | --- |
| 1 | owner：readPageQuery | 第一页Scan完、Rows.Close，保存cursor；还未启动写者 |
| 2 | worker：startTask内work | close(ready)，在select等待permit或work.Done；尚未发SQL |
| 3 | owner：betweenPages | 收到ready后close(permit)，授予页间写入许可 |
| 4 | worker：DB.ExecContext | 借池连接，真实autocommit写；错误返回给task，不写共享日志 |
| 5 | worker：result发送与wrapper defer | 结果进入容量1缓冲；after/cancel仍可能未完成 |
| 6 | worker：close(finished) | wrapper最后defer关闭广播，全部自身收尾完成 |
| 7 | owner：task.join | 等finished后读取result/清理错误，再打印writer finished、读取第二页 |

ready仅表示准备好，permit仅表示许可，SQL返回表示调用结果，finished表示wrapper真正结束。取消Done只通知worker在select/驱动处观察；owner仍必须join。它们不能互换。输出顺序是屏障保证的第一页面→写者完成→第二页面，不要求runtime固定调度先运行某个goroutine。

### 有界快照修复与它的限制

`snapshotRepair`明确开启 `sql.LevelRepeatableRead` 的只读Tx，第一次一致性SELECT建立当前Tx快照。外部writer在另外连接将id5/id3的时间真实提交，Tx内第二页仍返回 `[3 2 1]`；Tx结束后的新读取返回 `[3 6 4]`，证明更新实际存在，没有用应用锁把写者偷偷挡住。

这个修复适用于短而有界的导出，实验六行两页。跨HTTP请求每次BeginTx产生的新快照不等价；长时间握住Tx也会保留版本、占连接并干扰DDL，所以不能为普通无限滚动一直保存事务。Java事务经验有助于理解快照，但Go代码必须把两页都发送给同一个 `*sql.Tx`。接口queryer的动态值若是外层 `*sql.DB`，同样函数名会走新会话，无法延续Tx快照。

## 第四段：迁移要兼容正在运行的代码，而不是只让 DDL 成功

独占evolve表一开始是 `id,amount,note`，versions记录v1。目标是让note_v2非空并删除旧note。实验不直接rename/drop；按下面顺序验证旧/新程序的同时存活窗口。

| 阶段 | 真实结构与应用调用 | 实际验证 |
| --- | --- | --- |
| v1 | note可空，两行旧数据 | NULL/“放门口”是历史事实 |
| expand v2 | ADD note_v2 VARCHAR(100) NULL | 旧程序不认识新列仍能写；重复expand核对现有列与ledger |
| 恢复窗口 | 保留已新增列，删掉本run的v2账本记录后重进expand | 读结构发现列存在，补记版本，没有第二次ADD；这是有界恢复状态实验，未杀进程 |
| 第一次回填 | UPDATE note_v2=COALESCE(note,'') WHERE note_v2 IS NULL | RowsAffected2，并SELECT确认NULL0 |
| 滚动兼容写 | 旧代码只写note，产生id3；新代码双写两列产生id4 | 旧读note与新读COALESCE(note_v2,note,'')都返回“旧版刚写” |
| 错误收紧 | ALTER note_v2 NOT NULL | 失败1138；实际NULL1、nullable=YES、ledger2，不能标迁移完成 |
| 发布门禁 | contract(false) | 仍有旧程序时返回errOldWriters，没有发DDL |
| 再回填/停旧代码 | 第二次回填后NULL0 | 本课明确不再调用旧写函数；生产需发布/消费者证据 |
| contract v3 | MODIFY新列NOT NULL，再DROP旧列 | 重新读结构后才记v3，数据4行，ledger三行 |
| 新版最终读取 | SELECT note_v2 | 原id3值保留；重跑v2/v3无额外DDL |
| 旧SQL反例 | SELECT note | 实际1054，证明删列会破坏旧调用，不能提前删 |

先新增兼容列→部署fallback读/双写→有界回填→确认所有旧读写者退出→新代码只读写新列→核对再收紧→最后删旧列，是这里的应用兼容顺序。旧SQL还有后台job、报表、消费者、重试队列和旧版本回滚包等消费者；前端发布完不能自动证明它们都退出。contract里的布尔量是教学调用方提供的条件，不是一个可以代替生产发布证据的“安全开关”。

`expand`先查ledger，再核对列是否存在；未记账且列已存在的情况下补记账。`contract`先核对NULL0、nullable和旧列，再按实际状态跳过已完成DDL，最后记账。v2已记账且v3收紧后新列不可空仍是合法演进：v2重入只要求其新增列还存在，v3另外核对不可空与旧列不存在。代码没有声称验证任意篡改后的完整schema签名、checksum或多个迁移者互斥；本实验只有一个owner。

版本表是证据索引，不是数据库结构的替身。DDL完成后网络错误、DDL完成后ledger写失败都可能留下结构/账本不一致；不能从“没有v2行”猜“从没ADD”。本课真实制造了结构在、账本不在的窗口并恢复。其他无法读回的未知结果应保留精确目标和失败，不能盲重试后宣称原子成功。

### `sql.Tx.Rollback()` 为何没有撤销这里的 UPDATE

`implicitCommit`的两个阶段都在同一`*sql.Tx`上做UPDATE再ALTER，随后Rollback：

```text
UPDATE=20 DDL-code=0    Rollback=nil persisted-value=20 stamp-exists=true
UPDATE=30 DDL-code=1060 Rollback=nil persisted-value=30 stamp-exists=true
```

第一轮 `UPDATE audit SET value=20`，`ALTER ADD stamp`成功。第二轮UPDATE=30，再ADD已有stamp失败1060。MySQL的此类DDL在执行前隐式结束当前事务，因此第二轮即使DDL失败，前面的DML也已提交。两个阶段最后Rollback都返回nil，独立SELECT却读到20/30，stamp列一直存在。

Go的`sql.Tx`对象没有把服务端DDL规则改写成“Spring事务包住所有schema操作”。Rollback返回只说明驱动的ROLLBACK命令结果；MySQL已提交的事务事实不能撤销。InnoDB atomic DDL描述支持范围内单个DDL的原子性/恢复，不是让任意DML+DDL+版本表变成用户可Rollback的一组操作。不是每个语句都同样隐式提交，temporary表等有特例；判断应按真实版本文档与具体SQL，本课只实测上述ALTER。

### DDL 的 metadata lock 真正在哪里等待

`metadataLock`先开启holder事务并SELECT audit，使该表metadata lock保持到事务结束；另一个worker借独占Conn，设置本会话 `lock_wait_timeout=2` 后ALTER ADD lock_probe。observer用同一课程用户按worker的 `CONNECTION_ID()`读information_schema.PROCESSLIST，当前实际状态是 `Waiting for table metadata lock`。

| 次序 | 参与者 / 等待点 | 资源变化与结果 |
| --- | --- | --- |
| 1 | owner/holder Tx：SELECT audit | 读回value30，持有表metadata lock；Rows已消费但Tx未结束 |
| 2 | worker Conn：SET/SELECT ID→ALTER | 占第二连接；服务器等表metadata lock，2秒会话预算开始作用 |
| 3 | observer：按ID重复查询状态 | 第三临时连接观察实际等待，ticker每10ms触发观察，1.5秒总观察预算 |
| 4 | worker：ALTER返回1205 | 仅本会话锁等待超时；Conn.Close，wrapper结束后finished广播 |
| 5 | owner：join→holder.Rollback | 确认worker完成，释放holder；失败路径也先释放holder，再取消/join |
| 6 | owner：结构/数据/Stats读回 | lock_probe不存在、value30、InUse0，资源和数据都保持准确事实 |

日志中观测完状态时`pool-inuse=2`，因为observer的QueryRow已经Scan完归还连接；MaxOpen=4是配置上限，不是宣称此时有4个活跃连接。本段没有更改全局lock timeout、没有stop/start共享服务、没有KILL他人连接。在线DDL也可能在开始/结束阶段等待metadata lock，不能把 `ALGORITHM=INSTANT/INPLACE` 或 `LOCK=NONE` 当成绝无等待承诺；它们还受具体操作支持范围限制。

## 错误、取消和清理也是课程结果

Run按顺序拥有DB与表。create先SELECT确认精确随机目标不存在，不用IF NOT EXISTS认领既有对象；标识符只接受本课24位hex前缀与七个固定suffix，quote后才放进SQL。值始终参数绑定。CREATE返回错误时独立预算读回存在性，已确认本次创建的目标才登记清理；读回也失败则保留目标名字和主错误供owner恢复，不假称没有副作用。

所有Rows在自己的函数关闭并检查Err，所有Tx用自己的defer结束。Tx已Commit/Rollback后再次Rollback返回ErrTxDone可作为Go对象已结束，但实际提交/回滚数据仍由SELECT判断。seedTx在进入索引阶段前已Commit，分页快照Tx在返回新读前已Commit，holderTx在DDL核验前已Rollback。

分页任务和DDL任务由对应实验函数创建、取消并join。`task.result`容量1让交付结果不阻塞，但没有读取结果就释放整个课程资源的捷径；`finished`是在wrapper最后defer关闭。`TestResultDoesNotMeanFinished`用清理gate证明结果已缓冲但Finished仍不成立，释放gate后join合并work/cleanup两个错误。任务内不写out，日志只由owner写，避免输出竞争。

主ctx取消或到期后，Run的defer仍创建新的15秒cleanup ctx，逆序DROP精确created表并SELECT information_schema确认remaining0，最后Close DB。worker提前失败的收尾则用新的12秒join预算，保留原始工作/主错误与清理错误`errors.Join`，不覆盖主因。cleanup失败会让整个Run失败；日志中的PASS行出现在cleanup之前，必须同时检查进程退出0与最终表/DB日志，不能只搜索PASS。

`TestMySQLCanceledOwner`在种子commit后、打印covering-feed SQL但尚未发Explain时取消主ctx。它不替换驱动、不伪造SQL结果；实际创建的三张表仍用新cleanup预算DROP并读回0，最终pool0。这个证明范围是“SQL调用前取消与真实资源清理”，没有声称模拟服务崩溃或丢失Commit响应。

所有实验有总预算、DDL锁预算、固定行数和固定任务数。共享实例的安装/持久化/锁/资源证据复用安装者与M20 STATUS；本课没有停止实例或重新跑安装验收，没有写DeepHelp活动/退役库。

## 版本与材料修正的依据

实际运行版本/命令/失败修复/命名空间/清理在 [STATUS.md](STATUS.md)。本次已核对Go1.27.0与当前module/env，读取M20 Config/Run/STATUS，以及本机真实标准库/驱动入口。`files/开发框架`只提供MySQL CRUD方向；`files/工程管理/Go编码规范`的循环defer示例应理解成资源作用域问题，不能绝对化“循环内所有defer违法”：本课DDL每轮使用一个短函数，defer在该轮退出时运行。示例中`db.Query(v)`不应用业务输入当SQL，本课所有业务值绑定参数。

官方资料各自支持不同契约，不用一个总链接替代版本事实：

- [Go 1.27发布说明](https://go.dev/doc/go1.27)与[database/sql](https://pkg.go.dev/database/sql)：语言/标准库版本、DB/Tx/Conn/Rows资源边界；公司旧Go源码应按实际版本另核对。
- [mysql驱动v1.10.1文档](https://pkg.go.dev/github.com/go-sql-driver/mysql@v1.10.1)：ParseTime、Loc、会话参数和真实driver API；依赖固定在本课go.mod/go.sum。
- [DECIMAL](https://dev.mysql.com/doc/refman/8.4/en/fixed-point-types.html)、[FLOAT/DOUBLE](https://dev.mysql.com/doc/refman/8.4/en/floating-point-types.html)、[整数范围](https://dev.mysql.com/doc/refman/8.4/en/integer-types.html)、[DATETIME/TIMESTAMP](https://dev.mysql.com/doc/refman/8.4/en/datetime.html)：类型选择应同时核对精度/范围/时间转换，再与真实写读比较。
- [联合索引](https://dev.mysql.com/doc/refman/8.4/en/multiple-column-indexes.html)、[类型转换](https://dev.mysql.com/doc/refman/8.4/en/type-conversion.html)、[EXPLAIN](https://dev.mysql.com/doc/refman/8.4/en/explain.html)：前缀查找与扫描、转换后的比较、ANALYZE真实执行；本文估计行数/时长只代表本run。
- [LIMIT与同值排序](https://dev.mysql.com/doc/refman/8.4/en/limit-optimization.html)：ORDER BY同值没有完整顺序保证，应补唯一tie-breaker。
- [隐式提交](https://dev.mysql.com/doc/refman/8.4/en/implicit-commit.html)、[在线DDL并发与锁](https://dev.mysql.com/doc/refman/8.4/en/innodb-online-ddl-performance.html)：具体DDL、提交边界与metadata锁不能由应用事务名字推断。

本课的必要验收是：金额/ID/NULL/字符读回，唯一/FK/CHECK/长度错误后的数据状态，覆盖和非覆盖/转换计划及结果，两页集合的重漏与修复，旧新应用兼容、失败迁移的真实结构/账本、重复执行和缺账恢复，成功/失败DDL前DML已提交，元数据锁等待/超时以及资源最终结束。生产在线迁移、备份恢复、跨节点故障、未知Commit响应与更大规模容量没有由这组小数据实验覆盖。
