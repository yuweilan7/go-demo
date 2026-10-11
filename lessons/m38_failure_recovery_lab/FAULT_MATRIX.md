# M38 冻结的不变量与故障矩阵

冻结日期：2026-10-12。先按 M37 已交付的唯一公开业务契约选择风险，随后实现、真实运行并独立核对；本表的预期不是运行结果，实际结果另见 STATUS。

## 不变量与事实 owner

- 每组初始库存10；每个合法创建数量1；`stock >= 0`，`stock + SUM(orders.quantity) = 10`。只有 `Store.Create` 同一个 MySQL/GORM Tx 扣源库存。
- `(owner, request_key)` 唯一：同 key 同载荷只能返回原 order，冲突载荷不能变成第二单。HTTP `request_id` 是每次调用身份，order/event 不因网络重试换号。
- 一单一个冻结 outbox event；单价299分，CNY，`ACCEPTED/v1`。`sent=true` 必须在真实 Kafka ACK 之后，ACK 不代表消费者已完成。
- 一 `event_id` 一 inbox/receipt，`effect_count=1`；consumer 不再次扣库存。先 MySQL Apply Tx，后 Redis 失效，再 commit group next offset；任何后段失败不撤销前段事实。
- `RECORDED` 必须有真实持久 receipt。Redis 可以短暂 PENDING；真实 GET 200、日志 PASS、取消通知、协议 ACK、Serve 返回都不能替代权威内容和最终 Done。
- `UNKNOWN` 可按原 owner/key查；提交结果不明不换新 key重放。cleanup/UNKNOWN 原因优先保留，不能因 error 树还含1062就归一化为成功。
- 原 owner 预检精确对象不存在 → 登记 attempt → 创建；原账本拥有清理权。fresh/child 的合法名字不是授权，恢复只借原精确缓存键。旧进程 Wait 或旧 App Done 后才启动一个恢复业务写者。

## 独立风险与预期恢复

| 项 | 实际触发点 / 协调者 | 必查观察与允许暂态 | 错误恢复 | 正确恢复 |
| --- | --- | --- | --- | --- |
| F01 请求ctx到期 | 真实HTTP请求在 `AfterOrderWrites` 等门，客户端2秒到期；hook读到server ctx取消 | caller原DeadlineExceeded/UNKNOWN；hook取消观察；handler空闲；SQL库存10、订单/outbox0 | cancel客户端就放门，或看到404马上重下单；服务端可能尚未观察取消 | hook实际观察取消；caller/handler最终退出，再查原key及SQL |
| F01 应用停止在途 | 本课 `App.Stop` 同步取消根ctx，在Tx writes门内观察取消 | Stop不是Done；App所有handler/task、caller最终Done；SQL0效果 | Stop后立即关池/删表，或先开fresh形成两个写者 | 停入站→root cancel→观察→放门→Close/join→查账 |
| F02 Tx内提前error/panic | 真实 `Store.Create` 完成未提交SQL后hook返回error或panic | Gin错误协议；真实库存/订单/outbox回滚；panic在handler goroutine展开 | recover返回nil，或defer凭旧err Commit；订单/outbox半成品 | M37 withTx回滚后继续panic传播，错误链保留；正常创建作对照 |
| F03 池饱和 | 持有business池全部3条真实Conn，第4借用者等 `DB.conn` | Stats InUse3/WaitCount增加；真实pprof栈；等待ctx到期；持有者仍拥有Conn | 加无界goroutine/重试、盲增池导致共享库超限 | caller有限预算；持有者归还Conn；observer max1，与business合计≤4 |
| F04 服务已接受HTTP响应丢失 | 本课代理完整读取真实201、COMMITTED和订单，再断下游TCP | caller EOF/UNKNOWN；服务端原key订单1、库存9；outbox最终sent，receipt可PENDING | 只看到网络错就换key重POST，或把代理日志当业务事实 | 原key GET权威API；继续消费；完整API变RECORDED；效果仍1 |
| F05 COMMIT wire响应丢失 | MySQL代理捕获实际COM_QUERY COMMIT和server seq1 OK，丢回复 | driver invalid connection+UNKNOWN；observer SQL订单/outbox已提交；broker0、next-1 | 按invalid connection判断“未提交”，新key扣第二次 | 等caller/代理Done→close旧client→fresh同namespace查原key→relay/consumer→next1 |
| F06 依赖慢 | Redis代理在真实GET完整reply处等门，caller 500ms期限耗尽 | GET已发/实际reply已收，但caller无内容；读取无新消费效果 | 把client错误解释成server未收到；无限重试读冲垮依赖 | 关本课代理/等待callerDone，保留已有业务状态；新client继续 |
| F06 Redis不可用回执 | Apply真实DB commit后EVAL reply被本课代理截断 | SQL inbox/receipt1；EVAL已执行、data不存在/generation2；offset仍-1；错误UNKNOWN | 按Redis error回滚旧业务，或先commit offset后补缓存 | join/close旧consumer/client→fresh同group重投→Apply去重→重复失效→next1 |
| F07 提交后Go取消错误传播 | ACK及消费DB提交真实hook内取消自己的ctx并实际观察Done/Err | UNKNOWN与原Canceled同时保留；ACK后sent0/raw1、消费后effect1/next-1 | 正常停机只看Canceled而吞掉已发生效果/UNKNOWN | 不按取消归一化，旧event恢复重投与去重，effects1/next2；此项是Go错误路径，不冒称网络故障 |
| K01 DB提交后publish前 | child仅 `Store.Create`，relay尚未启动；真实AfterOrderCommit阶段+SQL审计后OS Kill/Wait | 订单/outbox1、sent0、broker0、next-1；没有消费效果 | 新建一套订单/清旧状态后宣称恢复 | fresh原key/API PENDING；relay真实发旧event；consumer effects1/next1 |
| K02 publish后sent前 | child `RelayOne.AfterRelayACK`，真实broker raw offset0+sent0后OS Kill/Wait | broker已经1条，outbox仍pending，offset-1 | 以sent0认定未发过，重新生成event_id；双效果 | fresh重发同event/同字节→broker2条→inbox/receipt1、next2 |
| K03消费DB提交后offset前 | child `ProcessRecord.AfterConsumeCommit`，SQL1/offset-1/缓存旧PENDING后OS Kill/Wait | receipt事实已RECORDED，cache PENDING，inbox首次offset0 | 提前offset或只去重不再次失效，永久旧cache | fresh原key/API仍旧cache；重投同event→效果不增加→失效→next1/API RECORDED |
| D01 duplicate | 真实原event相同Key/Value再发布两次 | 原始offset0/1/2，inbox首次0，业务effect1，next3 | 以消息offset当业务ID，重复加库存/扣款 | 冻结event_id+payload，持久inbox与receipt同Tx去重 |
| R01 rebalance | 已处理完消息后加入同group只读standby并退出 | broker Stable成员1→2→1；实际generation改变；assignment；standby不写业务、不commit，next3 | 新owner出现就允许旧处理者继续迟到写/commit | BlockRebalanceOnPoll安全阶段；standby poll Done后Leave/Close；随后继续原消费者 |
| C01 旧读回填 | 真实AfterRead冻结PENDING；consumer提交/失效；旧读再继续 | 旧caller可返回PENDING；错误UnsafeFill真实覆盖成旧命中；修复Lua拒旧，下一API RECORDED | SET旧snapshot覆盖新失效，或定时双删当严格保证 | M37真实generation+Lua原子比较；坏例最后精确Invalidate恢复 |

## 不适用的机制

M37 只有同聚合不可变创建v1，没有状态更新v2、版本冲突处理或多版本投影。同聚合乱序演练不适用；重复v1不是乱序，两个不同order的事件也不是同聚合版本顺序。不制造畸形v2毒化topic并删除消息/重置offset后声称恢复。

M37 没有Redis lease、自动续租或下游fencing token。旧lease owner过期不适用；`BorrowCacheKeys`是可信caller的一次能力授予，不是租约/分布式锁。相关知识前置M27有专属机制证据，本课不复刻另一业务以凑项。

本课选择真实终止应用/relay/consumer进程满足宕机风险；没有停止共享Redis、MySQL或Kafka服务。单节点Kafka ISR1 ACK、当前SQL读回，不证明多节点故障耐久性或生产SLA。
