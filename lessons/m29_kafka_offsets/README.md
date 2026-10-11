# M29：Kafka 的 offset 为什么不等于“业务已经完成”

这课围绕订单状态消息：Kafka 保存消息，消费者把订单事件写进本 run 的本机 JSONL 业务文件，再向 Kafka 提交消费进度。真实副作用选文件 `Write + Sync`，便于逐条查看；它不要求前置 MySQL，也不声称具有数据库事务或跨进程幂等保证。M30 再把同一问题组合到真实 MySQL/outbox 中。

核心入口会真实创建 topic、生产、读回、消费、提交与删除。每个消息有稳定 `event_id`，每次运行有随机 `golearn.m29.r<16位hex>` 命名空间。先读源码旁注释、运行一次、按下面时间线解读，约 10～20 分钟能讲清核心；程序本身有两分钟业务预算。Broker 停机/恢复是独立扩展，必须由唯一安装者协调。

## 先把五件事分开

| 阶段 | 本课调用/证据 | 能确认的事实 |
| --- | --- | --- |
| producer 请求 | `ProduceSync` | 调用期间驱动可能等 metadata、连接、发送、响应与重试 |
| broker ack | `ProduceSync(...).FirstErr()==nil`，记录被填写的 partition/offset | Kafka 接受了消息；单副本没有多节点容灾证明 |
| fetch/poll | `PollRecords(ctx,max)`，读回真实 key/value/partition/offset | 消息已交到应用；尚未产生业务效果 |
| processed/业务提交 | `ApplyRecord → Ledger.Apply → Write → Sync` | 本机文件的这一条效果成功；还没承诺 Kafka 消费位置 |
| offset 提交 | `CommitRecords → admin.FetchOffsets` | broker 已保存该 group/partition 的下次位置；它不知道外部业务做了什么 |

Kafka 的 offset 属于 `(topic, partition)`。处理 partition 0 的 offset 7 后，`CommitRecords(record7)` 提交 **next=8**。该 group 重建消费者时从 8 开始，而不是再消费 7。提交 8 表示恢复时把该分区所有 `<8` 的位置视为过去，不是只 ack 一条编号为 7 的任务。不同 partition 都可能有 offset 0，不能把它们放进同一个水位计数器。

消费组是对 topic 分区的分工：同一 group 的同一分区在稳定分配中给一个成员；另一个 group 有自己的进度，仍可从头读同一条消息。两分区最多让两个普通组成员各拥有一个分区，第三个成员可能空闲。组协调期间会暂停/重新分配，瞬时状态不能用“两个client已创建”来证明两个真实成员都拿到分区。

Kafka 按分区组织日志。固定分区数量和固定 key partitioner 下，同 key 通常落同分区；本课显式使用 `StickyKeyPartitioner(nil)`，并在 broker ack 后断言真实分区一致。换 partitioner、手动覆盖 partition、增加分区，都会改变这个条件。E1 自动 key 分区，其他故障实验用 `ManualPartitioner` 精确落入指定分区；手动模式里 key 只是业务元信息，分区以 `Record.Partition` 为准。

还要区分三种位置：后台 fetch 把 broker 批次读进 client 缓冲，poll 把其中部分记录交给应用并推进 client 的已交付位置，业务 owner 才能在处理后声明 processed。`PollRecords(ctx,max)` 的 max 是交给应用的记录数，不是一个 Kafka 网络 batch 的数量；网络 batch 可以包含多条，也可以同时积累多个分区。driver 已 fetch/poll 的最大位置不是已完成业务的连续位置，`DisableAutoCommit` 正是阻止驱动按自己的消费进度自动代替业务承诺。只有 `CommitRecords` 成功并被 admin 读回的位置，才是新 consumer 同 group 的恢复依据。E4 中两条都已经 poll，offset1也已 processed，但offset0没 processed，所以安全位置仍停在0。

## 前置、版本与入口

已复用 M00 的实际交付：`../m00_environment_infra/STATUS.md`、`infra/INSTALLATION_CONTRACT.md`、`infra/endpoints.json` 和 `internal/environment/environment.go`。M00 的 `ProbeTCP` 故意只证明 TCP，不能证明 MQ 写读。本课沿用它的唯一安装者、loopback、严格 SSH、秘密独立文件、topic/group 独占与分层证据契约，采用文档约定，未跨 module 导入 M00 的 `internal` 或复制整套项目。

本课的实际工具链为 Go **1.27.0 windows/amd64**，`go.mod` 为 `go 1.27.0`，没有另加 `toolchain` 指令；当前 `GOTOOLCHAIN=auto`、`GOWORK` 为空。真实服务是唯一安装者发布的 **Apache Kafka 4.3.1 单节点 KRaft，127.0.0.1:19192**，metadata读回也是这个地址。独立 module 路径为 `go-demo/lessons/m29_kafka_offsets`。根 `go test ./...` 会跳过嵌套 module，所以务必使用 `go -C`。当前公司 Go/生产 broker 版本未知，本课不能据此替它们升级。

锁定实际下载并读源码的依赖：`franz-go v1.22.1`、`kadm v1.19.0`、`kmsg v1.14.0`，以及 `compress v1.20.0`、`lz4/v4 v4.1.30`。franz-go 和 kadm 自己的最低 Go 为 1.26.0，在本机 1.27.0 编译/测试；精确选择在本课 `go.mod`，校验记录在 `go.sum`。`go.sum` 是内容校验记录，不是仅凭它就能锁定全部行为的文件。

从自己的 worktree 根运行：

```powershell
go -C lessons/m29_kafka_offsets run ./cmd/demo
go -C lessons/m29_kafka_offsets test -v -count=1 -timeout 30s ./...
go -C lessons/m29_kafka_offsets vet ./...
```

默认 `core` 必须读取唯一安装者发布的 READY 索引：

```powershell
# 默认已指向主工作区，不依赖worktree也有一份.local。
$env:GOLEARN_SERVICE_INDEX = 'D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json'
go -C lessons/m29_kafka_offsets run ./cmd/demo -mode core -budget 2m
# 此环境变量只影响当前PowerShell。显式-index也可覆盖。
```

索引格式是 `services.kafka.state/host/port/server_version/credential_file`。必须 `READY`、host=`127.0.0.1`、有效 port、非空版本。凭据文件单独忽略，不打印配置；本轮安装者选 loopback PLAINTEXT，`sasl_mechanism=none`。代码也接受私有 JSON 中的 plain/scram 用户名口令字段。`Config` 含秘密，禁止打印它。SASL/公网TLS分支没有本轮实证，不能当作已验。

`NewHarness` 会发真实 metadata 请求、要求单 broker、比较它返回的 advertised 地址与索引地址。bootstrap 能连不够：驱动拿到 metadata 后会转向 advertised 地址，SSH 转发和 broker advertised 地址必须一致。代码不自行安装、重启或建立竞争隧道。PENDING 会在创建任何 client 前返回失败；这是真实依赖缺口，不能改成 mock。

独立 Go/文件机制模式与真实集成开关：

```powershell
go -C lessons/m29_kafka_offsets run ./cmd/demo -mode offline
$env:GOLEARN_KAFKA_INTEGRATION='1'
go -C lessons/m29_kafka_offsets test -v -count=1 -timeout 3m ./...
Remove-Item Env:GOLEARN_KAFKA_INTEGRATION
# -race只在当前进程设置便携amd64 CC/CXX/PATH，然后恢复原值。
& ./lessons/m29_kafka_offsets/scripts/verify-race.ps1
# 用同一进程内便携工具链运行真实Kafka并发路径：
& ./lessons/m29_kafka_offsets/scripts/verify-race.ps1 -Integration
```

默认 test 的真实集成项是显式 SKIP，不能把它当作 Kafka PASS。启用集成开关后，同一 `Run` 路径会创建全新独占数据，所有业务断言和清理失败都会令 test 失败。`STATUS.md` 记录已经执行的命令与范围。

## 阅读顺序与真实调用链

| 文件 | 先看哪些符号 | 职责 |
| --- | --- | --- |
| `cmd/demo/main.go` | `execute` | 命令行、signal、总预算、清理后退出 |
| `internal/course/experiments.go` | `Run`、`KeyOrder`、`OffsetWindows` | 总 owner、正常读写、四类错误/修复实验 |
| `internal/course/kafka.go` | `NewHarness`、`Produce`、`ConsumerOptions`、`Poll`、`Commit` | 实际 driver/protocol 与 broker offset 验证 |
| `internal/course/state.go` | `Ledger.Apply`、`Watermark.Complete`、`joinDone` | 本机业务文件、连续水位、独立清理预算 |
| `internal/course/rebalance.go` | `StartConsumer`、`process`、`callbackBlocked`、`revoking`、`Stop` | 真正组重分配与 goroutine 资源所有权 |
| `internal/course/fault.go` | `BrokerFault`、`WaitMarker` | 唯一安装者协调的 broker 停止/恢复 |
| `internal/course/state_test.go` | `TestCanceledGateHasNoLateEffect` 等 | 独立事实断言和显式真实 Kafka 集成 |

主链：`main → execute → course.Run → ReadConfig → NewHarness → BrokerMetadata → KeyOrder → OffsetWindows → Rebalance`。生产链是`Produce/KeyOrder → ProduceAndWait → 辅助goroutine里的kgo.ProduceSync → 所有promise结果 → join Done`。每个业务链是 `ReadN/Poll → Decode → ApplyRecord → Ledger.Apply → file.Write/Sync → Harness.Commit → kgo.CommitRecords → kadm.FetchOffsets → require`。错误以返回值反向回到调用者；只有明确的 `errInjected` 属于已设计的故障，其他错误都失败退出。

当前锁定的 franz-go 源码定位：

- `pkg/kgo/producer.go`：`ProduceSync` 380 行创建 WaitGroup、`Produce` 583 行入队；等待所有 promise 后返回。Record 的 key/value 在发送期间由驱动借用，教学代码不再修改它们。
- `pkg/kgo/consumer.go`：`PollRecords` 547 行给应用至多 max 条；`AllowRebalance` 731 行开放被 poll 挡住的回调。fetch后台任务与业务处理不是同一个goroutine。
- `pkg/kgo/consumer_group.go`：`CommitRecords` 3094 行把 `r.Offset+1` 放入 `EpochOffset`，同一分区有多条时选较大位置；`CommitOffsetsSync` 3304 行等真实commit响应与回调，再检查每分区错误。本课另用 admin 读回提交位置。
- `pkg/kgo/config.go`：`BlockRebalanceOnPoll`、`OnPartitionsCallbackBlocked`、`DisableAutoCommit`、`WithContext`；`pkg/kgo/client.go`：`CloseAllowingRebalance` 1363 行、`Close` 1386 行等待组离开/连接和任务收尾。

这些行号来自本次实际 module cache 的版本源码，不是跨版本API保证。入口与调用关系比行号更耐版本变化。

## E1：正常 key 分区、读取、业务、手动提交

`KeyOrder` 创建两分区 topic，给三个订单各发两步，共6条。fixture用锁定驱动的公开partitioner API挑选各落一个分区的两个合成key，再加`order-extra`；本轮实际是`order-0→p0`、`order-2/order-extra→p1`。不同key可以碰撞到同分区，不能推断“不同key就不同partition”。首次真实运行的`order-a/b/c`全部落p1，两分区覆盖断言拒绝了它，所以改为明确选fixture，而没有删掉该断言。producer一次提交这些 Record 指针，等 `ProduceSync` 全部返回，打印 `PRODUCE_ACK key/value/p/offset`。同 key 的后一步必须仍在同分区且 offset 更大；仍依据实际broker ACK确认覆盖两个分区，本地散列预测不代替实证。

consumer 的 `ReadN` 累计6条。`PollRecords` 一次只保证“至多 max 条”，不保证一次收齐所有分区，所以循环用剩余数量再 poll。读回消息必须与对应 event_id 的 broker ack 在 key、原始 value 字节、partition、offset 上完全一致；验证每分区 offset 递增。之后**串行**执行每条 `ApplyRecord` 和 `Commit`。主线的全局串行是简单可读的每分区串行特例；高吞吐时可按partition设置一个串行owner，让不同分区并行。

日志里的 `FETCH` 不是 processed。只有 `BUSINESS applied=true` 后才到 `COMMIT_VERIFIED next=...`。反过来看，producer出现 `PRODUCE_ACK` 时，Ledger还没有任何业务效果。跨分区的 `Records()` 遍历顺序只是client当前fetch容器顺序：partition0 的 offset2 与partition1 的offset2没有一个可比较的全局消费位置。

本课每条提交便于观察，不是吞吐调优模板。生产中常按小批次提交最后一个**连续完成**位置；批次越大，重投窗口也越大。`CommitUncommittedOffsets` 取的是poll进度，异步业务还没完成时不能把它当处理进度自动提交。

## E2：提交进度早于业务，恢复时丢处理

`LossWindow` 使用真实单分区的offset0/1/2。

| 步骤 | 参与者/函数与等待点 | 状态/证据 |
| --- | --- | --- |
| 1 | consumer A，`ReadN → PollRecords` | 真正读到offset0，但业务次数0 |
| 2 | A，`Harness.Commit` | broker `next=1`，`COMMIT_VERIFIED`读回1 |
| 3 | A，故意 `return errInjected` | `INJECT_FAILURE before_business`；没执行Apply |
| 4 | `WithConsumer` 的defer | `AllowRebalance → Leave → cancel lifetime → Close → join Done` |
| 5 | 新client B，用同group | 从offset1读起，稳定event_id0在业务文件仍为0次 |

这是真正 group offset 恢复下的业务丢处理，不是消息从Kafka文件被删除。日志 `E2_PASS business=0 restart_offset=1` 的含义是坏策略已经被确定性复现。修复应先业务成功再提交，转到E3看它带来的重投窗口。

## E3：业务成功、进度未提交，至少一次与幂等

`DuplicateWindow(false)`：A读取offset0，文件 `Write/Sync` 成功，随后故意退出，不提交offset。B同group重建后再次读到相同event_id和offset0，再做一次文件效果，最后commit next1。断言 `attempts=2/effects=2`。

`DuplicateWindow(true)` 重复完全相同的窗口，但 `Ledger.seen[scope:event_id]` 避免再次应用，文件仍记录第二次尝试且 `Applied=false`。断言 `attempts=2/effects=1`。所以至少一次通常要求**允许重复投递，并在业务侧守住唯一效果**，而不是假设client重试从不重复。

这次“重启”是关闭并重建真实Kafka consumer/client，broker group状态真实保留；同一课程进程的Ledger存活并把效果Sync到文件。代码没有重启操作系统进程、没有把seen从磁盘重建，也没有跨进程唯一约束。断电、JSONL半条写入、多进程争抢的幂等恢复不在保证范围。M30 的数据库唯一键/事务会解决另一个层面的原子性。

## E4：高 offset 的异步 worker 不能自行推进整分区进度

`OutOfOrder(false)` 先读取offset0/1，再启动两个本课worker。低worker在未关闭的gate接收等待；高worker立即写业务，**从自己的goroutine同步调用CommitRecords(record1)**。它虽然只是完成一条消息，broker却保存next2。owner等高worker真正Done后，取消低worker并join，关闭consumer。恢复client的首条是offset2，而低offset0业务次数仍0。

这个反例让异步完成者在不掌握整分区连续进度的情况下commit；CommitRecords内部同步/线程安全都无法修复语义。另一类危险是高worker先commit1000、低worker后commit5：本版API允许把提交位置回退到6，造成重投。用多个异步提交回调相互竞速，也不能通过“最后完成的任务”推断安全进度。本课真实验收覆盖高位置跳过未完成低任务，回退行为按本版源码契约解析，未额外做网络乱序实验。

修复 `OutOfOrder(true)`：worker只交业务结果，由一个owner管理该partition的Watermark。

| 步骤 | 当前等待/操作 | safeNext |
| --- | --- | --- |
| 1 | low挂在gate；high业务已成功并Done | 初值0 |
| 2 | owner `Complete(1)`，pending包含1 | 仍0，**不commit** |
| 3 | owner唯一一次close(gate) | low才进入ApplyRecord |
| 4 | owner读low结果并join Done，再`Complete(0)` | 连续跨过0和1，到2 |
| 5 | owner `CommitRecords(record1)`，admin读回next2 | 此时低/高业务均完成 |
| 6 | 同group恢复从offset2读起 | 低offset0业务次数1 |

Watermark的Next是值，pending是该owner私有map，无并发写。每个partition各要一个，不能拿不同partition的最大offset来补缺口。本课没有事务控制记录或日志压实（compaction），offset0/1连续，所以实现按数值+1推进。消息编码的压缩格式不会凭空造成这种缺口；日志压实是删除旧key版本的另一件事。真实日志可能因事务过滤、控制记录或compaction出现数值缺口，企业实现应按“已经投递到业务的有序record队列”推进已完成前缀，并用前缀最后一条的`Offset+1`提交；不能等一个永远不会交到业务的整数。初读真实消费者代码时，优先辨认每分区串行owner，这种方案不需要猜不存在的位置。

## E5：第二消费者真实加入与退出，旧 owner 必须有边界

`Rebalance` 的两个client确实使用相同group/topic。第一个old先得到两分区，再读取一条挂在gate的业务。只有worker自己发布Started后，主入口才开始第二个new。使用本版 `RangeBalancer` 的eager分配，真实分区从old的2个变成old/new各1个、不重叠；new退出后old恢复2个。分区编号归哪个名字并不保证固定，日志和断言读取真实callback结果。

`ConsumerOptions` 关闭自动提交，`PollRecords(max=1)` 约束批大小，`BlockRebalanceOnPoll` 约束提交与owner变化。业务deadline12s、rebalance15s、session6s、heartbeat1s来自教学小消息/可取消gate范围；heartbeat是驱动独立任务，本课不中断它。实际服务若有不同group配置或阻塞外部I/O，必须重新核对预算，不能照搬这些数字。

| 先后 | 参与者 | 当前函数/真实阻塞点 | 状态变化与证据 |
| --- | --- | --- | --- |
| 1 | old的poll goroutine | `PollRecords → process` | 拿到一条消息；暂挡revoke |
| 2 | old.gN的worker | `select gate/ctx.Done` | gate没开，无业务效果；`OLD_WORK_INFLIGHT` |
| 3 | new的client | 组加入/协调网络 | 真正请求重分配，不是仅创建对象 |
| 4 | kgo signal goroutine | `callbackBlocked` | 记录blocked、`active.Cancel()`，只通知，不join |
| 5 | 旧worker | 观察ctx.Done，返回Canceled；defer送Result、cancel、close Done | 未Apply/未commit |
| 6 | old poll owner | `ResultBeforeDeadline → Worker.Stop → joinDone` | 新Background清理预算等待Done；`WORKER_JOINED` |
| 7 | old poll owner | `active=nil → AllowRebalance` | 才允许真正revoke执行 |
| 8 | kgo组协调goroutine | `revoking` | `old_worker_joined=true`，旧assignment清空 |
| 9 | old/new callbacks | `assigned`，入口等change广播/重新查谓词 | 真实各1个、不重叠；`REBALANCE_VERIFIED` |
| 10 | 主入口 | 关闭旧gate、向new拥有的partition生产marker | gate不能复活已join的旧worker；new真实业务+commit |
| 11 | 主入口/ new owner | `new.Stop`取消poll/任务→join→Leave/Close→join | 实际离组；old再次revoke/assign得回2个 |
| 12 | old新generation | 消费tail、文件Sync、commit读回 | 入口等`WaitCommit`，之后old.Stop/join |

Ledger的Owner包含generation，如 `old.g1`。同一client在新generation重新拿到原分区，合法重投与旧generation迟到副作用必须分开：本课保存确切gatedOwner，断言它的业务尝试一直为0。被join后再释放gate，旧任务不可能回来；`E5_PASS canceled_owner=... late_effects=0` 是这个证据。新generation或new完成原消息是预期的至少一次恢复，幂等effects不会增加到两次。

本轮`.local/core-run-2.log`的关键事实如下，序号只是本进程日志的观察顺序：

```text
[091] BROKER_GROUP_VERIFIED state=Stable members=1 partitions=2
[094] OLD_WORK_INFLIGHT owner=old ... p=1 gate_open=false
[096] CALLBACK_BLOCKED owner=old active=true cancel_not_join=true
[097] BOUNDED_ABORT owner=old ... 未commit，等待新owner重投
[098] WORKER_JOINED owner=old ...
[099] REVOKED owner=old partitions=[0 1] old_worker_joined=true
[100] ASSIGNED owner=old generation=2 partitions=[1]
[101] ASSIGNED owner=new generation=1 partitions=[0]
[104] BROKER_GROUP_VERIFIED state=Stable members=2 partitions=2
[107] BUSINESS scope=rebalance owner=old.g2 ... applied=true
[122] BROKER_GROUP_VERIFIED state=Stable members=1 partitions=2
[131] E5_PASS ... attempts=3 effects=3 canceled_owner=old.g1 late_effects=0
```

这次p1仍给old，但generation已从1变2；旧g1确实被join，g2合法重投并完成原消息。另一次运行可以把p1给new，断言不依赖成员名字的偶然排序。E5的`FETCH`有时早于producer线程打印`PRODUCE_ACK`，因为broker接受后consumer就可读取，而producer caller尚未获得CPU来打印；这不能解释为consumer读取了未被Kafka接受的数据，也不改变E1中先等producer结束再创建consumer的因果。

回调不能同步调用 `Close/LeaveGroup`：组协调循环正在等回调返回，Close又等组循环结束，会死锁。本课回调只更新少量状态/发cancel；外层owner收尾。`OnPartitionsLost` 单独记录并失败：成员已失去身份时不再假设commit能成功，不能把lost日志当普通revoke PASS。

保护也有边界：BlockRebalanceOnPoll不是分布式fencing token。外部I/O若忽略ctx、在不可控线程/远端已接受请求，取消不能撤销请求；需要下游幂等、版本/fencing或事务来守住副作用。这里的gate可取消、文件副作用有短临界区、callback在worker join之后才开放，所以课程能给出确定的旧任务停止证据。不能扩大到任意阻塞DB驱动或远程服务。

## goroutine、返回值和资源 owner

| 任务/资源 | 创建者与owner | 真正等待点 | 早退收尾 |
| --- | --- | --- | --- |
| driver连接/fetch/heartbeat/group任务 | `NewClient`的ClientOwner | metadata、网络、组协调、内部channel | Allow→独立3s Leave→cancel lifetime→Close→独立8s join |
| ProduceSync辅助任务 | `ProduceAndWait` | 所有record的promise/WaitGroup | ctx到期cancel client lifetime→Close解除缓冲/网络→独立8s join任务Done |
| 一次性consumer | `WithConsumer` | `ReadN → PollRecords`；同步fn | fn返回前join它的workers；defer ClientOwner.Close |
| 异步低/高业务worker | `OutOfOrder`/`process` | gate、文件锁/Write/Sync、坏例中的commit网络 | 所有返回分支defer Cancel+新预算join Done |
| old/new poll owner | `StartConsumer`返回的ManagedConsumer | poll、Result、commit、callbackgate | Stop cancel→join poll Done→关闭driver |
| Close执行goroutine | ClientOwner.once | Leave/驱动Close | 取消lifetime解除驱动等待；调用者等待独立done；超时保留失败 |
| Ledger文件 | Run | mutex、Write、Sync | 所有consumer Done之后才Close，Close错误聚合 |
| admin、topics/groups | Run/Harness | metadata/create/commit-offset查询/delete | 每项独立预算，仅精确成功创建/本run group名单 |

`Result` 是缓冲1的error channel，任务可把结果放进去后继续执行defer；`Done` 在最末关闭。测试 `TestResultDoesNotMeanJoined` 用另一个cleanup gate确定性证明读结果与任务完成不同。`context.CancelFunc` 关闭通知，不强杀goroutine；`Worker.Stop`才组合通知与等待。

`StartWorker` 在自己的goroutine用defer recover把panic交error，父goroutine的recover不能跨过来救它。`Run/WithConsumer/process`的具名error与defer闭包把业务失败和清理错误 `errors.Join`，不会被“Close成功返回nil”覆盖。`errCleanup`类别让故障扩展拒绝把“produce失败但join/Close也失败”算PASS，独立测试检查各错误身份均保留。`main`在execute返回后才os.Exit。任何无法join/删除/关闭的路径都会失败，不能输出成功后忽略资源残留。

`RecordDeliveryTimeout(5s)`在本版是rough timeout：幂等producer为了保持序列，在结果未知时可能延迟失败。不能把这个选项或传入ctx解释为严格的五秒goroutine寿命。本课额外配置produce请求3s、request overhead2s、有限retry；`ProduceAndWait`还拥有辅助任务，在总ctx取消时关闭自己的client来解除promise等待，然后以新的有限清理预算join真正Done。结果未知仍按未知报告，不因为强制关闭就声称broker没写消息。

Java中熟悉的Executor/Future有助理解owner和完成，但goroutine启动没有自动Future、没有固定线程池，也不会随调用函数返回自动停止。Go的channel close建立通知与可见性，不能把它直接等同于Java BlockingQueue关闭；map/slice/`*Ledger`传参会共享底层对象，Mutex不能按值复制。这里闭包捕获的`rs`是只读record指针slice，而Ledger是共享指针，用mutex保护实际可变状态。

## Broker 故障扩展：由安装者停/恢复真实学习实例

此扩展不在默认core里停止服务。先与唯一安装者确认没有其他模块正在用课程Kafka，然后运行：

```powershell
go -C lessons/m29_kafka_offsets run ./cmd/demo -mode broker-fault -budget 5m -control .local/fault-unique-run
```

`-control`必须是当前module `.local` 的新独占子目录，拒绝旧标记。实验先生产停机前消息得到ack，写`ready.json`；安装者实际停止独立课程broker后创建`stopped`。代码在真实停机状态发Produce，要求失败，Close并join失败producer，写`failure-observed.json`。安装者随后恢复相同Kafka volume并创建`resumed`。代码向同topic生产恢复消息，同group消费读取并commit，直到停机前和恢复后两条稳定event_id都读回，输出 `BROKER_FAULT_PASS`。

标记文件是协调证据，ticker反复Stat只是观察实际状态，不用sleep猜停机已经完成。安装者仍要保留真实容器退出/恢复与版本/volume证据，marker本身不能冒充broker状态。故障producer返回error说明未收到成功，不能在任意网络失败下一概断言消息绝不在Kafka；代码会报告失败尝试的event_id恢复后是否实际读到。本轮受控“broker先停，再produce”的无ack结果与更复杂响应丢失窗口要分开。

实际A窗口按完整握手通过；B没有等到stopped，五分钟预算到期，程序退出1并用独立清理预算删除topic、核验不存在、关闭admin，没有停broker。确认B进程与清理结束后，才以新目录运行C。C最终对停机前/恢复后ACK的原始key/value/partition/offset逐项断言通过：before为p0/offset0、after为p0/offset1，业务效果各1，commit读回next1/2，unknown尝试本轮未出现。C的真实错误是`records have timed out before they were able to be produced`，last err为loopback TCP被远端关闭，约5000ms；这只是本轮观察，rough timeout没有变成语言保证。

安装者的C记录还明确保留一次协调偏差：首次读取failure文件恰好早于文件生成，shell未立即停止，start请求已开始；随后匹配的failure文件和producer超时/Done都在broker协议恢复成功之前观察到。C证据支持“失败producer已结束，再收到协议READY并开始恢复写读”，不能声称本轮安装者严格先读失败文件再发start。A保留严格握手的完整证据，C保留最终更强字节断言及实际时间边界，两者不互相覆盖。详细命令、失败、资源结果与安装者报告位置见STATUS。

## Kafka事务、RabbitMQ比较与当前保证范围

Kafka的幂等producer在Kafka日志侧去重重试；Kafka事务可把Kafka输出记录与消费offset原子关联，配合read_committed覆盖Kafka内的读→处理→写链。它不会自动包含本课文件、MySQL或Redis的提交。外部DB先成功Kafka后失败的重复窗口仍需业务幂等/outbox等设计。M29未实施Kafka事务，不声称外部业务exactly-once。

只围绕消费者代码比较RabbitMQ：RabbitMQ `Ack(deliveryTag)` 针对当前channel的交付，可以配置单条/多条；Kafka `CommitRecords` 针对group/partition的恢复位置，对更小位置有连带影响。RabbitMQ redelivery与Kafka同group从未提交offset重读，都要求应用辨认重复；Kafka增加consumer是分区重分配，不是每条消息自动找空闲worker。两者的publisher confirm/produce ack都不证明消费者业务已完成。

现在必须掌握五阶段、next offset、稳定event_id、每分区连续进度、Block/Allow与Done的关系。压缩格式、批内协议字段、合作式rebalance、Kafka事务API、跨集群容灾与吞吐调优可在相应源码出现时再查。这里直接给出全部反例/修复解析，不另布置exercise。

## 官方查证与隔离清理

2026-10-12 先核验4.1文档，安装者选定4.3分支后又实际查证：[Kafka 4.3设计/投递语义](https://kafka.apache.org/43/design/design/)、[advertised.listeners配置](https://kafka.apache.org/43/configuration/broker-configs/#advertised.listeners)、[官方KRaft快速入口](https://kafka.apache.org/43/getting-started/quickstart/)、[锁定franz-go kgo API](https://pkg.go.dev/github.com/twmb/franz-go@v1.22.1/pkg/kgo)、[franz-go锁定源码](https://github.com/twmb/franz-go/tree/v1.22.1)、[Go1.27发布说明](https://go.dev/doc/go1.27)。Go语言的协作取消/共享对象是语言和标准库语义，rebalance/commit是库契约，broker版本/分配/写读是当前运行证据，三者不互相替代。

每次只创建3个core topic、7个group（group只是逻辑命名/offset，不创建外部账号），所有名字写日志。topic replication=1、retention=10分钟、每分区retention.bytes=1MiB，小消息不做负载；retention不是立即硬磁盘配额，根安装者负责heap/容器/卷上限。只把CreateTopics确认成功的名字放入删除名单；同名已存在不会被误删。`Group`只构造名字，`UseGroup`在真正创建consumer、准备启动poll时才登记该名字，重建或第二成员不重复登记；producer阶段早退时没有创建group，就不发多余DeleteGroups。关闭所有consumer后逐个DeleteGroups/DeleteTopics并检查broker响应。删除接受与broker异步回收segment文件不是同一时刻；`VerifyTopicGone`以本版不会自动创建topic的metadata查询等待`UnknownTopicOrPartition`，`VerifyGroupGone`读回精确group的不存在/Dead事实。E5还以broker `DescribeGroups`核验Stable、成员数1→2→1及两分区不重复分配。默认清理保留本run `.local/*.business.jsonl` 和无秘密日志供复查，它们被.gitignore排除；CLI在module工作目录写入，Go test在`internal/course/.local`写入，因为测试进程工作目录是包目录。不扫其他课数据，不改其他group offset。
