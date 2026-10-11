# M28 实际交付与验证状态

记录日期：2026-10-12，Asia/Shanghai。当前交付已实现并实跑完成，冻结供主 agent 验收。执行者唯一写入目录为 `C:/Users/Lenovo/.codex/worktrees/go-course-m28/go-demo/lessons/m28_rabbitmq_delivery/`，起点 `90f7381b495c08f4995dd13400fbc34fca69e8c0` 含已验收 M00。没有 commit/push、修改共享索引、安装或重启 broker、创建下级 agent。

独立 module、详细中文 README/源码、默认 cmd/demo 和八组真实实验已交付。默认 demo 退出0，八组全部 PASS；启用真实 integration 的 race 再次全部通过，vet/gofmt/diff检查通过。业务效果由独立进程账本读取，confirm/return/delivery/ACK/重投/DLQ 由真实 AMQP 路径和broker状态读取，没有用HTTP健康、mock或Go channel代替RabbitMQ。

## 前置、环境与当前版本

- 已读取根README/go.mod、适用空 `.codex/AGENTS.md`（worktree/主repo根无AGENTS）、M28完整prompt、ORCHESTRATION、TEACHING_STYLE、modules.json、INFRASTRUCTURE、M00 STATUS/README/INSTALLATION_CONTRACT。按 go-learning-by-running 技能生成教学产物。
- 用户 `D:/IdeaProject/go-demo/files` 只读盘点，没有相关RabbitMQ专章；没有复制材料或写入原files。
- 实际 `go version go1.27.0 windows/amd64`；root go=1.27，本课go=1.27.0，无toolchain/replace。`go -C` 实际GOMOD指向本课go.mod，GOTOOLCHAIN=auto、GOWORK为空；共享go.work未修改。
- 官方当前客户端实际锁定 `github.com/rabbitmq/amqp091-go v1.15.0`。go.mod/go.sum保留校验，`go mod verify`通过。实际缓存源码入口取消检查/开始后的I/O边界与README一致；从官方raw下载同tag channel.go，SHA256与module cache完全一致：`25F3ED64C80B591D2D2F03F46DEBB608199A5C0E11A9F104E9D2C2D5346EEE2E`。Recovery=nil显式禁用自动恢复。
- 权威只读索引为 `D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json`，`services.rabbitmq.state=READY`，host=127.0.0.1，port=15673，登记server_version=4.3.6。每组真实AMQP握手均读到Properties.version=4.3.6，和登记相符。凭据另存已忽略JSON，仅内存读取username/password/vhost；vhost实际为 `/golearn_m28`。
- 统一安装者拥有独立RabbitMQ4.3.6实例、固定镜像digest、新课程volume、512MiB/1CPU资源限额及loopback隧道；独立课程用户仅该vhost configure/write/read权限。只读核对其私有报告 `D:/IdeaProject/go-demo/.local/course-orchestrator/install-rabbitmq-stdout.jsonl`、`rabbitmq-persistence-proof.json`、`control-rabbitmq-restart.json`：实际stage=protocol-ready，重启只作用golearn-course-rabbitmq、running=true/oom_killed=false；重启前真实AUTH/confirm/durable/persistent=true、ready=1，重启后Get/正文与MessageId匹配/Ack/queue_deleted=true、DeliveryMode=2，verified=true。这是安装者的持久恢复前置证据；本课复用READY，没有重新安装/重启服务，没有将本课断开consumer连接写成broker重启证明。
- 安装者初始失败 `Error when reading /var/lib/rabbitmq/.erlang.cookie: eacces` 保留于其记录；只读 `rabbitmq-cookie-repair.json` 确认原因为root readiness CLI在cookie创建前竞争启动，诊断改为rabbitmq用户；cookie_value_read_or_changed=false、protected_services_changed=false。该问题已由统一安装者修复，本课没有代修。客户端URI使用官方amqp.URI.String保留vhost前导slash，避免误连名为golearn_m28的另一vhost。
- Windows race使用安装者只读 `D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json` 的WinLibs GCC16.2.0/mingw-w6414.0.0。脚本仅当前进程设CC/CXX/CGO_ENABLED/PATH，finally恢复；没有安装编译器、写go env或改系统PATH。验证后普通shell `go env CC/CXX/CGO_ENABLED` 仍为gcc/g++/1。

## 实际命令与结果

W=`C:/Users/Lenovo/.codex/worktrees/go-course-m28/go-demo`，所有shell workdir显式W；`go -C`进一步选择本课module。原始无秘密证据保存在 [evidence/demo.txt](evidence/demo.txt) 和 [evidence/race.txt](evidence/race.txt)。

| 命令/工作目录 | 实际结果 | 观察范围 |
| --- | --- | --- |
| W：go version；go -C lessons/m28_rabbitmq_delivery env GOVERSION GOOS GOARCH GOMOD GOTOOLCHAIN CC CXX CGO_ENABLED GOWORK | Go1.27.0/windows/amd64，本课GOMOD，auto/gcc/g++/1/GOWORK空 | 当前工具链与module事实 |
| W：go -C lessons/m28_rabbitmq_delivery mod download；首次test/vet | download退出0，首次test/vet退出1，missing go.sum entry for amqp091-go | 真实初始故障，未当成编译通过 |
| W：go -C lessons/m28_rabbitmq_delivery mod tidy；mod verify | 均退出0，依赖完整、all modules verified | 已修复上述缺失校验项 |
| W：gofmt -w 本课cmd/demo与internal/lab；gofmt -l 对同目录；git diff --check | 格式化完成，最终-l与diff-check无输出/退出0 | 手写源码格式与空白检查 |
| W：go -C lessons/m28_rabbitmq_delivery test -v ./... | 退出0，TestLedgerConcurrentCommit和TestTaskJoinAfterCleanup PASS，TestIntegration明确SKIP | 24并发调用同业务键效果1；取消后清理未完成不可假join。该次不证明MQ |
| W：go -C lessons/m28_rabbitmq_delivery run ./cmd/demo，输出经Tee-Object存evidence/demo.txt | 退出0，8/8 PASS；最终ALL SELECTED PASS | 全部真实broker行为与独立业务断言，含清理错误汇总 |
| W：& ./lessons/m28_rabbitmq_delivery/scripts/verify-race.ps1 -Integration，输出经Tee-Object存evidence/race.txt | 退出0；TestLedgerConcurrentCommit PASS；TestTaskJoinAfterCleanup PASS；TestIntegration PASS 30.39s；包32.355s，无race诊断 | 实际子命令go -C 本课绝对目录 test -race -v -count=1 -timeout120s ./...，八组真实AMQP再次全跑 |
| W：go -C lessons/m28_rabbitmq_delivery vet ./... | 最终退出0、无诊断 | 静态检查 |
| W：git check-ignore -v 本课.local/demo.log和.secrets/connection.json；rg --files/秘密内容仅计数扫描；git status --short | 私有目录匹配本课.gitignore；真实password匹配数0；只有本课目录未跟踪新增 | 无秘密进入课程源码/README/证据，无其他模块或共享文件改动 |

早期未READY时只做账本race，退出0且integration SKIP；它没有被用作broker证据。READY后上述默认demo和integration race才完成MQ验收。

## 八组真实验收事实

默认demo run=`golearn.m28.r1791744878782787500`；MessageId实际含这个run/scenario/queue前缀。下表短标签便于阅读，源码和原始记录都是完整ID。

| 实验 | 实际broker/协议事实 | 独立业务事实与修复 | 结果 |
| --- | --- | --- | --- |
| confirm/prefetch/manualack | 两条persistent消息分别publisher ACK；没有consumer时真实ready=2；prefetch1取得tag1后ready=1且DeliveryMode=2 | 此两次观察effects=0；再各Commit→Ack(false)→同channel屏障，A/B效果各1 | PASS |
| mandatory return | existing exchange+missing-binding得到同ID/key的真实return312，同时该条publisher ACK；work ready=0 | effects=0；return与confirm各自读取，不能推导消费成功 | PASS |
| PublishWithContext | 已取消ctx返回Canceled、publish序号不增长、ready=0；真实底层Write屏障内取消后Publish仍未返回；放行后返回nil，另一个owner实际读到NotifyPublish ACK | 取消后真实消息仍被deliver、commit并产生effects=1；sendCtx与Session ctx分开，不把owner关闭socket冒充API取消 | PASS |
| earlyack/连接恢复 | 先Ack且屏障通过，关consumer Connection后ready=0；提交后未Ack直接关自己socket，等原channel关闭，旧Delivery.Ack报错；全新Connection/Channel重声明topology/Qos/consumer实际收到同ID redelivered | earlyack effects=0；重投Commit applied=false且effects仍1；恢复后新消息after-reconnect继续处理，效果1 | PASS |
| 相同message_id两次发布 | 两次真实发布/confirm，消费前ready=2，两次真实delivery均被Ack，第二次也可Redelivered=false | 第一次applied=true，第二次false，effects=1；broker不因MessageId去重 | PASS |
| 有限retry/DLQ | transient一次Nack(requeue=true)，实际第二次redelivered；poison attempt0/1通过真实TTL150ms retry队列回work，attempt2 Nack(false,false)；实际basic.get取得dead同ID和x-death | transient总2次/效果1；poison总3次/效果0；循环最多5次。retry新发布confirm后才Ack旧消息，重复窗口已说明 | PASS |
| multiple并发误确认/修复 | 同channel A=tag1/B=tag2，owner在B完成后Ack(true)并协议屏障，关连接真实ready=0；改Ack(false)后观察ready=1，新consumer实际收到redelivered A | 坏例A0/B1，A未处理却无重投；修复A/B各1。两个业务worker真实join，AMQP操作由owner串行执行 | PASS |
| channel局部delivery tag | 空foreign channel ACK原tag写帧返回nil，NotifyClose实际406；原channel仍开放且Connection存活，原Delivery正常ACK | 异常前effects=0，原channelCommit→Ack后effects=1；没有把相同数字tag当全局身份 | PASS |

## 所有权、失败与清理

每组最多3个课程queue/3个exchange，durable classic、有界长度32；work/retry overflow拒绝发布。八组串行，消息/consumer/tag名称属于本run，最多同时3条Connection、6个逻辑channel。课程没有扫描/清空他人对象，也没有故障其他服务。默认demo和真实integration race两轮各创建并精确删除24queue/24exchange，共48queue/48exchange；每组PASS仅在自己的defer清理无错误后打印。删除等待真实delete-ok，清理缺对象仅容许准确协议404，其他失败保留。

主run工作预算90s、每组9s；owner的CloseDeadline/join均使用独立3s收尾预算，拓扑清理以新的background context给5s。即使业务ctx已取消，收尾仍可执行。NotifyClose/Return/Cancel有缓冲；Next显式处理数据流关闭、channel错误、broker取消和ctx，提前失败不会永远等delivery。socket写屏障也由Close释放。

本课goroutine只由Session或ownedTask拥有。创建业务/发送任务前注册cleanup；失败先cancel/释放自己的屏障/必要时关闭自己的socket，再等finished。ownedTask.finished只在work及其内部defer全部结束后关闭，result在观察finished后读；首次join超时不标joined。TestTaskJoinAfterCleanup真实卡住worker defer，证明cancel通知与完成不是同一事件，放行后保留工作取消错误。Session.Close停止watcher、关闭连接/socket，再等待done；账本并发单测的WaitGroup也实际Wait。主错误和close/delete/join错误经errors.Join汇总。

两轮前台进程已退出，没有课程后台消费者或publisher留存；全部本run拓扑已删除。安装者持有的broker/volume/隧道继续保留，课程不停止服务或删除安装者数据根。私有凭据未输出、未写入课程；官方源核对文件只在本课已忽略.local中。

## 明确未验范围

账本仅本进程内原子查重；没有验收进程崩溃后的持久幂等、跨服务事务或exactly-once。没有额外运行broker集群、断电、网络分区、生产流控/吞吐或自动恢复配置。观察到单节点classic队列TTL/DLX迁移，不等于验收目标队列失效时的可靠死信。本课连接故障与父安装者的单节点重启持久前置证据分别陈述。未继续M29或其他模块。
