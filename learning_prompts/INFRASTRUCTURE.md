# 真实基础设施与并行实验约定

这份文件由需要中间件或服务器实验的模块读取，普通语法课不需要全量读。用户已明确授权：需要 MySQL 或消息队列时直接在现有学习服务器安装独立实例；可以在服务器运行课程代码。这些工作用于准备和验证真实实验，按实际需要执行。最初修订 prompt 时没有安装；第 1 层实施时已由主 agent 完成真实安装和验收，以下记录区分当前实证与原配置基线。

## 第 1 层已核验的学习入口

2026-10-12，主 agent 实际登录授权服务器盘点、持锁安装并做真实写读及课程实例重启持久性核验。权威私有索引为主目录 `.local/course-orchestrator/infra-services.json`，其四个服务均为 READY；凭据从各自 `credential_file` 读取，不复制或打印。课程专属 SSH 隧道由主 agent 持有，消费模块只使用登记端点，不另建隧道或控制容器。

| 服务 | 实际版本与本机入口 | 归属、实证与当前限制 |
| --- | --- | --- |
| Redis | 8.2.8，`127.0.0.1:16389` | 复用既有 DeepHelp Redis，M25 实际 AUTH/键值/TTL/并发/响应失败验收；不得停机、改全局配置或 broad delete。 |
| MySQL | 8.4.11，`127.0.0.1:13306` | 新容器 `golearn-course-mysql`、新 volume `golearn-course-mysql-data`；512MiB、1CPU、max_connections=32、InnoDB128MiB。课程账号只在 `golearn_m20` 有权限；后课可用本库的独占模块/run 表，不复用 M20 数据。真实重启写读保持，M20 已验收。 |
| RabbitMQ | 4.3.6，`127.0.0.1:15673` | 新容器/volume，512MiB、1CPU、独占 vhost `/golearn_m28`；M28 已验收真实 confirm、投递、重投、DLQ、业务效果和精确清理，安装者已核验重启持久读回。 |
| Apache Kafka | 4.3.1，`127.0.0.1:19192` | 新单节点 KRaft 容器/volume，768MiB、1CPU、heap256MiB；metadata 返回同一 loopback 地址。当前为课程专用 PLAINTEXT，M29 已验收分区/offset/rebalance/真实 stop/start 恢复；非多节点或生产鉴权证明。 |

学习根为 `/srv/go-demo-learning`，Compose project=`golearn-course`，容器和 named volume 的 owner labels 已实际核对。镜像 tag/digest、资源配置、安装失败/修复、运行锁竞争/超时与持久性报告保存在主目录 `.local/course-orchestrator/`。初选 `mysql:8.4.12` 的 manifest 实际不存在，已保留失败并改用可拉取的 8.4.11，不能把运行版本写成 8.4.12。

固定 inode 的安装锁已实跑：竞争者返回75，超时 owner 返回124且所属子进程结束，后续 owner 正常取得锁；所有变更仍由主 agent 唯一持锁。日志轮转限制为每容器5MiB×2，合成数据/retention 有界，但 named volume **没有硬磁盘 quota**。实测三课程实例 Running、OOMKilled=false；安装前17个 DeepHelp 容器的名称、镜像 tag、State、既有健康状态及 StartedAt 早于安装基线均已复核，退役 MySQL 仍退出。原盘点未记录容器 ID/digest，不能据此宣称前后 ID/digest 比对。

后续并行实库模块按自己的表/键/topic 命名空间隔离；建议单课最多4个活跃数据库连接，跨课总量必须低于32并留恢复余量。只读索引一次即可，不重复完整安装验收；版本、归属或端点变化后再核验受影响部分。模块不得停课程实例做并发故障；需要真实进程故障时向主 agent 请求独占窗口，由它核对所有使用者、唯一 stop/start 并恢复协议。

## 当前可依据的事实

最初 prompt 编排时读取的是 DeepHelp 仓库文档、Compose 与本机接入索引，当时没有登录服务器。下表保留原配置基线；当前课程端点以以上实际验收与私有索引为准。

| 组件 | 现行材料事实 | Go 课程怎样使用 |
| --- | --- | --- |
| Redis | DeepHelp 基础 Compose 仍管理 Redis；文档为服务器 loopback 6379 经 SSH 映射本机 16379 | 复用其真实协议，使用课程专属 key，不修改 Redis 全局参数或停服务 |
| PostgreSQL | DeepHelp 已使用独立 PG 根，文档本机/服务器入口 15432；活动业务事实在正式库 | 本路线主数据库选 MySQL；不得写正式 PG 业务库，未来 PG 对照另建独立学习库 |
| Milvus/etcd | DeepHelp 的检索组件 | 本路线不需要安装、重建集合或做容量负载 |
| MySQL | M30 已退役旧 MySQL；原数据/密码/冷备/旧源码保留供恢复，已退出默认隧道/health | M20 首次需要时新建 go-demo 学习实例，绝不恢复旧数据目录当课堂数据库 |
| RabbitMQ | 本次所读现行配置没有登记 | M28 首次执行时安装独立 broker |
| Kafka | 本次所读现行配置没有登记 | M29 首次执行时安装独立单节点 KRaft broker，后续 M30/M37 复用 |

“未登记”不是扫描全服务器后的不存在证明。先查真实容器、服务、端口和资源；发现现有独立学习实例就核对版本/归属并复用，不重复安装。

定位入口：DeepHelp 的 `docs/LOCAL_SETUP.md`、`infra/README.md`、`infra/OPERATIONS.md`、`.local/DEPENDENCIES.md`、`.local/dependency-access.json`。本机仓库默认 `D:/IdeaProject/deephelp`；其他机器按用户提供位置定位。

秘密由已有配置/凭据路径加载，不能复制秘密正文进 prompt、README、日志或 Git。现有 SSH key/known_hosts 继续使用严格校验，不更改系统代理或重建密钥。

## 一个安装者，多个实验者

共享服务器上的安装、升级、Compose/端口登记和基础设施迁移只能由一个执行者持锁操作。建议独立根 `/srv/go-demo-learning`，使用与 DeepHelp 无关的 Compose project、容器名、volume、账号和 loopback 端口；实际选择先核对当前占用。

锁位于学习根的 control 目录，由持有者记录模块、run、进程、创建时间和操作范围。应使用远端原子创建或 flock 等可靠机制，定义正常释放和失败定位。其他执行者先读取已交付端点，正在安装则等待该安装者结果，同时继续不依赖连接的讲解/设计。不能删一个尚有活跃 owner 的锁抢装。

这是一把基础设施变更锁，不锁所有 Go 文件、正常数据库连接或模块独占数据。MySQL/Redis/Kafka 的正常隔离实验可按容量并行。

M00 交付接入地图/锁和命名规范；M20/M28/M29 各自首次安装需要的组件。索引一旦变化，仅由安装者更新其权威版本；消费模块把使用的版本记在自己的 STATUS，不共同改根课程索引。

## 首次安装要做的具体工作

1. 通过既有 SSH 入口核对主机架构、内存、CPU/cgroup、磁盘、监听端口、现有容器与 volume 归属。不要把旧文档中的 4 核/8 GB 当当前空闲资源。
2. 为当课必需组件选可运行稳定版本并固定镜像 tag/digest；MySQL 优先有官方完整文档的 LTS 分支，Kafka 用该版本支持的 KRaft。先核验 Go driver/runtime/工具兼容性。
3. 建独立账号、数据根与本课 loopback 端口；秘密写私有文件，执行前验证 Git ignore。已有 DeepHelp 容器/活动数据/恢复卷/发布指针/预算/备份都不是 Go 课的实验对象。
4. 设置基于实际容量的内存/CPU/连接/磁盘/日志上限和保留策略；当前不需要的新增 MQ 可以正常停机保留数据。不要为装课堂服务停 DeepHelp，也不要同时启动两套新 MQ 占满资源。
5. 建课程专属隧道或准确复用对应转发；只管理自己启动的 SSH 进程，不用端口占用就杀任意进程。
6. 分别验证进程/协议鉴权/合成写读/持久化以及启动停止入口。安装成功、端口开放、PING/PingContext 都不是完整课程验收。
7. 保存无秘密的端点/版本/资源/启动停止/恢复说明和当前 owner。失败保留必要诊断，继续在允许范围修复，不能改用 mock 声称实库完成。

Kafka 还必须验证 broker 返回的 advertised.listeners 地址可从客户端到达。bootstrap 地址能 TCP 连接不代表 producer/consumer 能完成 metadata 后的通信；SSH 转发、broker advertised 地址、DNS/端口映射要一并核对。

服务器容量确实不足时，可以使用本机已有 Docker/WSL 的**真实组件**或另一台已获授权的学习主机；依旧记录实际拓扑。没有可用容量就报告准确缺口，保留实证未完成并推进独立课，不替用户购买资源，不把单节点兼容服务冒充未运行的 Kafka。

## 并行模块的独占范围

| 资源 | 命名/隔离建议 | 允许的正常清理 |
| --- | --- | --- |
| Go 代码/依赖 | 每课 `lessons/mXX_slug` 独立 module | 只处理自己生成且已无消费者的产物 |
| 私有配置/报告 | 每课自己的 `.local/`、`.secrets/`，本目录 .gitignore 排除 | 报告与秘密分开；保留有效连接配置和必要失败证据 |
| MySQL | `golearn_mXX_run` 独立库，或核对后的课程库内独占表前缀 | 只删本课本 run 自建对象，先核对实际目标 |
| Redis | `golearn:mXX:run:*`，合成键设置 TTL | 仅按记录精确删除自己的键；禁 FLUSHDB/FLUSHALL 和 broad scan-delete |
| RabbitMQ | 独立 vhost/user；队列带模块/run | 只删自己的队列/binding 或已退出的实验 vhost |
| Kafka | 独立 topic/group/client id，保留小数据 | 不删除其他课 topic；不重置别人的 offset |
| HTTP/gRPC/metrics/pprof | 动态 loopback 端口或登记后的专属端口 | 只停止自己创建的进程/监听器 |
| 服务器实验进程 | 课程独立运行目录、进程/容器和端口，记录实际 owner | 只管理本课进程，不切换 DeepHelp 单元或覆盖受引用安装 |

M30/M37 用自己的业务库/topic/缓存前缀，复用服务器实例不等于复用 M20/M29 实验数据。数据库唯一约束和幂等记录属于该课业务状态，不随服务重启清零。

## 故障和证据边界

共享 Redis/PG 的故障用课程独占网络代理、连接隔离或独立实例实现；不重启共享服务。课程自己的 Kafka/RabbitMQ/MySQL 也只在 owner 明确、其他实验没有使用时做进程故障；停止后恢复并核对状态。

每个命令要记录工作目录、服务端/客户端版本、命名空间、超时/并发限制、结果和清理。证明点分别是：

- 语言/资源实验：对象与执行时间线、真实阻塞及退出。
- SQL：表内容、守恒/唯一/回滚与 commit 结果，不只看调用返回。
- Redis：真实键值/TTL/并发结果及一致性窗口。
- MQ：broker 确认/投递、业务效果、ack/offset 与重投，不混成一个成功。
- 应用故障实验：本课进程的中断/退出、实际业务状态、恢复后的处理与资源收尾；明确实际平台和触发方式。

不要求每课重新搭全套环境或反复跑全部健康检查；同环境证据仍适用时复用，版本/端点/归属变了再核验受影响部分。
