# M29 实际交付与验收状态

日期：2026-10-12，Asia/Shanghai。唯一写入范围 `C:/Users/Lenovo/.codex/worktrees/go-course-m29/go-demo/lessons/m29_kafka_offsets/`；基线HEAD=`90f7381b495c08f4995dd13400fbc34fca69e8c0`。没有提交、推送、修改根配置、其他模块或原有files，没有创建下级agent。Kafka安装/容量/镜像/容器控制由主agent持锁执行；本模块只使用登记端点，没有自行安装、重启或建立竞争SSH隧道。

当前状态：**实施完成，全部规定真实实验自验通过，READY_FOR_REVIEW。** 中文文档、详细注释、独立module、离线机制、真实Kafka core、真实集成test、真实并发race及最终故障窗口C已通过。B协调超时失败与完整清理、C安装者实际时间偏差均已保留；最后标准入口退出0。目录冻结，等待主agent独立验收；子agent通过不代替主agent ACCEPTED。

## 实际前置与版本

完整阅读本模块prompt、TEACHING_STYLE、ORCHESTRATION、modules.json、INFRASTRUCTURE、M00完整prompt与实际STATUS/安装契约/端点登记/core environment/main源码，以及根README/go.mod。适用`.codex/AGENTS.md`为空，没有找到额外适用AGENTS。原有 `D:/IdeaProject/go-demo/files/开发框架` 只读定位，未复制原材料。采用go-learning-by-running教学方式；手写Go旁解释职责、caller、参数/结果、值与别名、等待点、取消/错误/panic/defer与资源owner。没有额外exercise，没有跨module internal导入或复制前置整项目。

本机实际 **Go1.27.0 windows/amd64**；module为`go-demo/lessons/m29_kafka_offsets`，`go 1.27.0`，无toolchain指令，GOTOOLCHAIN=auto，GOWORK空。依赖实际下载和锁定：franz-go1.22.1、kadm1.19.0、kmsg1.14.0、compress1.20.0、lz4/v4 4.1.30；前两者自身最低Go1.26.0。实际核对Kafka4.3官方设计/配置/KRaft文档、kgo官方API、下载锁定源码与Go1.27发布说明；源码符号/行号见README。公司生产版本未知。

主agent发布READY：**真实Apache Kafka4.3.1，单节点KRaft，127.0.0.1:19192，单副本，loopback PLAINTEXT/none**。镜像digest、独立volume、768MiB/1CPU、heap与安装证据由主agent掌握。服务索引是 `D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json`，独立凭据文件只读且没有打印正文。每个run实际metadata返回broker_id1、advertised=127.0.0.1:19192，与登记一致。版本来自安装者实际镜像核验；本课metadata核验地址，不假称metadata协议返回服务器版本。

## 实际命令与结果

默认工作目录为 `C:/Users/Lenovo/.codex/worktrees/go-course-m29/go-demo/lessons/m29_kafka_offsets`；注明worktree根的命令在它的`go-demo`目录执行。无秘密日志在模块`.local`，Go test业务文件在`internal/course/.local`，两者都已忽略。

| 实际命令 | 真实结果与范围 |
| --- | --- |
| `go version`；`go env GOVERSION GOTOOLCHAIN GOOS GOARCH CGO_ENABLED GOPROXY GOMOD GOWORK`（根与module） | Go1.27.0 windows/amd64；独立GOMOD，CGO1，未改全局环境/代理 |
| `go list -m -json ...@latest`；`go get ...@v1.22.1 .../kadm@v1.19.0`；`go mod tidy` | 精确版本下载、实际校验成功，没有关闭sum验证 |
| `go test ./...`（源码首次组装） | 初次失败`undefined: BrokerFault`，扩展当时尚未落盘，补齐后通过 |
| `gofmt -w cmd internal`；`go test ./...`；`go vet ./...` | 最终各轮退出0，无vet诊断 |
| `go run ./cmd/demo -mode offline` | 退出0：Complete1→safeNext0，Complete0→safeNext2；没有Kafka连接 |
| `go test -v -count=1 -timeout 30s ./...` | 7项Go/文件/lifecycle测试及2个guard子场景PASS；未开真实开关时Kafka集成明确SKIP |
| `go run ./cmd/demo`（最初服务PENDING） | 退出1：guard在client创建前拒绝未READY，没用mock/Redpanda代替 |
| `go run ./cmd/demo -mode core -budget 2m`，`.local/core-run-1.log` | 真实退出1：order-a/b/c全散列到p1，两分区断言拒绝；topic删除并核验不存在，两个client Done；未创建group的多余清理得到COORDINATOR_NOT_AVAILABLE并聚合返回 |
| 同一core命令，`.local/core-run-2.log` | **真实退出0**：run=`golearn.m29.r0995b9eeaef7ae70`；E1–E5、3topic/7group清理读回全通过，153条日志 |
| `go -C lessons/m29_kafka_offsets run ./cmd/demo`（worktree根），`.local/core-final.log` | **最终默认入口退出0**：run=`golearn.m29.r1eee772ac8e699e9`；包括新增low.Started屏障的E4和E1–E5全通过，3topic/7group absence、全部client/worker Done确认 |
| 进程内`GOLEARN_KAFKA_INTEGRATION=1`后 `go test -v -count=1 -timeout 3m ./...`，finally恢复 | **真实退出0**：run=`golearn.m29.rc18a05100d5ac90a`；TestRealKafkaCore22.50s、包22.735s；`.local/integration-run.log`有完整协议/业务/清理事实 |
| `& ./scripts/verify-race.ps1` | Windows amd64实际race机制/文件/lifecycle PASS；当时真实Kafka未开，明确SKIP |
| `& ./scripts/verify-race.ps1 -Integration`，`.local/race-integration-run.log` | **真实退出0**：run=`golearn.m29.r7c4312e6ac90d328`，包24.572s；core真实网络/业务并发路径race通过 |
| 最终源码 `& ./scripts/verify-race.ps1 -Integration`，`.local/race-final.log` | **最终真实退出0**：run=`golearn.m29.r8f970ef55ab2809e`，包24.745s；包括low.Started因果屏障的完整core并发路径；环境恢复后仍gcc/g++/1。此轮在主agent停止重复验证通知到达前已启动，通知到达时已结束，此后没有重跑 |
| `go env CC CXX CGO_ENABLED`（race恢复后） | gcc/g++/1，与原值一致；脚本只读root私有toolchain.json，把CC/CXX/bin_dir/CGO用于自身进程，finally恢复，没有全局go env写入 |
| `go run ./cmd/demo -mode broker-fault -budget 5m -control .local/fault-m29-20261012-a` | **真实退出0**：run=`golearn.m29.r7480a92a540f5c6a`；实际停/恢复由主agent执行；before p0/offset0、after p0/offset1读回，unknown=false；group Dead、topic不存在、client Done |
| 同一故障命令，control改为`.local/fault-m29-20261012-b` | 真实退出1：run=`golearn.m29.rb8414221dcdc95b0`；5m等待stopped的deadline到期，此次broker没有停动；topic删除并读回不存在、admin Done，未创建consumer/group；日志`.local/broker-fault-run-final.log` |
| 同一故障命令，control改为`.local/fault-m29-20261012-c` | **真实退出0**：B进程与清理结束后才启C；run=`golearn.m29.ra81b474f17a7175b`；真实错误正文、before/after原始ACK的key/value/partition/offset逐项匹配、commit next1/2、unknown=false、group Dead/topic不存在和全部client Done；日志`.local/broker-fault-run-c.log` |
| `go -C lessons/m29_kafka_offsets test -v -count=1 -timeout 30s ./...`；`go -C lessons/m29_kafka_offsets vet ./...`（worktree根） | 最终源码退出0：7个机制/文件/lifecycle测试及2个子场景通过，未开真实开关的集成明确SKIP；vet无诊断 |
| `go -C lessons/m29_kafka_offsets run ./cmd/demo -mode offline`（worktree根）；`go mod verify`；`gofmt -l cmd internal` | 退出0，离线水位符合预期，all modules verified，格式检查没有输出 |
| PowerShell只读解析五次成功core与最终C实际JSONL | 每次core16条attempt、15条Applied=true，scope/event_id一致，幂等重复第二次Applied=false；最终C真实2条attempt、2条Applied=true |
| `git check-ignore -v .local/example.json .secrets/connection.json`；`git diff --check` | 私有文件命中模块.gitignore，diff无空白问题；没有输出秘密正文 |

## 真实业务与协议事实

E1：实际fixture `order-0→p0`、`order-2/order-extra→p1`，各两步。六条producer结果都broker ACK，consumer真实key/value原始字节/partition/offset与ACK逐项相同；同key后一步offset更大，每分区读序递增。串行业务成功后才手动提交，再用admin FetchOffsets确认next=record.Offset+1。跨分区遍历顺序没有全局offset保证。

E2：先commit0→next1，再故意业务前失败；完整关闭旧consumer后同group重建，首条offset1，event0业务次数0。消息未消失，是进度跳过业务处理。

E3：先文件Write/Sync，故意不commit退出；完整关闭后同group重建，再读同event_id/offset0。非幂等attempts/effects=2/2；幂等=2/1，JSONL第二次Applied=false。seen在同一课程进程存活，文件真实Sync；没有跨OS进程幂等或SQL事务保证。

E4坏例：low0卡在未释放gate，高worker完成后自行commit1→broker next2；owner读结果并join高worker，取消并join低worker，恢复从2开始，低业务0。修复：高1完成safeNext仍0不commit，入口释放gate，低0完成且Done后safeNext2，再commit1并读回next2；恢复从2，低业务1。Watermark仅覆盖无事务/compaction的连续fixture，生产数字缺口应按已投递队列的完成前缀推进。

E5：old真实先有[0 1]，g1业务卡gate；new加入触发callbackBlocked→Cancel→BOUNDED_ABORT→WORKER_JOINED→AllowRebalance→REVOKED(old_worker_joined=true)→分配。broker DescribeGroups确认Stable成员1→2→1、两分区无重复归属。CLI这次old g2得p1、new g1得p0，old g2合法重投完成原消息；new退出后old g3恢复两分区并完成tail。总attempt/effect3/3，精确旧gatedOwner=`old.g1`一直0次，join后开gate也无迟到效果。lost=0、真实revoke≥2；分区归属名字不作固定假设。

五次成功core都在consumer/worker完成后，精确DeleteGroups7个→DescribeGroups Dead/不存在，DeleteTopics3个→不自动创建的metadata查询UnknownTopicOrPartition，最终admin CLIENT_DONE leave_error=false。每个删除独立8s预算，Close先独立3s Leave，再cancel/Close与独立8s Done join。读取结果或发cancel没有冒充join，清理错误保留返回。

## 失败、修复与边界

首次真实运行纠正“不同key必不同分区”的fixture假设：用锁定库公开partitioner API选key，仍保留真实ACK覆盖两分区断言。资源登记分开：Group只命名，UseGroup在创建consumer、准备poll时登记，同group重建/第二成员只登记一次；producer早退没有group资源，不再误删。COORDINATOR_NOT_AVAILABLE没有吞成成功，原失败日志保留。

源码显示RecordDeliveryTimeout在默认幂等producer下是rough timeout，不保证ctx到期就结束promise等待。因此ProduceAndWait拥有辅助goroutine，在ctx取消/错误时cancel client lifetime、真正Close以解除缓冲/网络，再用全新有限预算join Done。ManagedConsumer.Stop即使poll join失败也继续关闭驱动，再独立重试join并聚合错误；loopErr只在Done后读取，避免超时路径数据竞争。单测确定性证明取消gate无迟到效果、Result与Done不同、goroutine内panic转error、业务/清理错误身份均保留。

窗口A的ready→stopped→failure-observed→resumed来自独占新目录；主agent实际确认课程容器running=false/oom_killed=false，恢复同一容器/volume并核验broker协议握手才发布resumed，主目录证据`.local/course-orchestrator/m29-fault-window-a.json`。本课失败producer约5000ms返回非nil、Close/join后才写failure-observed；恢复读before/after并核验commit，unknown尝试未读到。A日志只记错误动态类型，复核后补真实错误正文与before/after原始ACK字节/partition/offset逐项比对。B等待安装者实时协调时超过5m，没有收到stopped，程序真实返回deadline失败但独立预算清理成功；安装者明确未停过期B的broker。C只在B进程实际退出与topic不存在核验之后启动，不让新窗口与旧清理竞争，也不覆盖A/B日志。

C最终退出0，实际error=`records have timed out before they were able to be produced`，last err为loopback TCP wsarecv被远端关闭，elapsed5000ms、非context deadline/ErrClientClosed；Close/join通过后发布failure，收到resumed后才生产after与消费。安装者C证据`.local/course-orchestrator/m29-fault-window-c.json`实际只读核对：首次failure文件读取与文件生成竞速，shell未立即停止，start请求已先发出；随后匹配failure和失败producer超时/Done都在broker协议READY之前确认。没有声称C严格先读failure再发start。A保留完整严格握手证据，C核验最终更强原始字节断言与失败/关闭事实。实际读取before/after p0/offset0/1，文件两条Applied=true，commit next1/2，unknown=false，精确group Dead、topic metadata不存在、admin Done，broker Running=true/OOM=false。

未验多节点容灾、吞吐/长压测、合作式rebalance、Kafka事务实现、外部MySQL/Redis exactly-once、SASL/TLS故障或无视context的外部I/O取消。本课consumer重启是完整关闭重建真实client，未强杀课程OS进程；broker扩展则是真实容器进程stop/start。幂等producer不代替业务幂等，BlockRebalanceOnPoll不是分布式fencing，单副本AllISRAcks只有一个ISR确认。清理证实逻辑资源不存在，不假称segment空间立即全部回收。原始files只读，业务JSONL和无秘密日志供主agent独立复验。

最终冻结：14个公共交付文件、8个手写Go文件。Git只显示本模块新增，HEAD保持起点，没有add/commit/push。私有凭据机制为none，实际非空username/password值0个，14个公共文本的该凭据值匹配0；没有把这项零值扫描扩大成其它秘密的证明。所有前台实验/测试进程均返回，业务worker/driver owner清理已在Run返回中核验；broker与原volume恢复且由主agent继续持有。文件SHA256冻结清单保存在本模块忽略的`.local/delivery-manifest.json`。等待主agent独立core/race及课程内容验收，子agent停止写入。
