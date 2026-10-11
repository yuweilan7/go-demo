# M00 基础设施接入与安装协调约定

此文件是 Go 课程的接入说明。M00 仅核验已有服务，不建立远端学习根、不安装中间件、不取得远端安装锁。`endpoints.json` 是本课的无凭据登记基线；实际鉴权和只读结果见 `../STATUS.md`。DeepHelp 文档中出现的历史版本与地址不是当前运行证明。

## 唯一接入地图与证据层

| 项目 | 登记入口 | 当前用途与 owner |
| --- | --- | --- |
| SSH 主机、账号、端口、私钥路径 | `D:/IdeaProject/deephelp/infra/config/connections.env` | 只读加载，M00 沿用已存在 key，不公开主机定位信息 |
| 已核对 SSH host key | `D:/IdeaProject/deephelp/infra/client/known_hosts` | `StrictHostKeyChecking=yes`、`BatchMode=yes`；失败时停止，不跳过校验 |
| Redis 应用口令 | `DEEPHELP_SECRET_FILE`，默认 `C:/Users/Lenovo/.deephelp/.env.secret` | 已有配置的 `REDIS_PASSWORD`；不能打印/复制/提交正文 |
| PostgreSQL 五字段配置 | `D:/IdeaProject/deephelp/.local/m30/live-postgres.json` | 正式事实库，M00 只执行会话元信息 SELECT，不读业务表 |
| Redis 服务 | 服务器 `127.0.0.1:6379`，登记本机 `127.0.0.1:16379` | DeepHelp 共享实例；Go 课只能用独占合成键，M00 不写键 |
| PostgreSQL 服务 | 服务器/登记本机 `127.0.0.1:15432` | 正式库受保护；将来 PG 对照须新建独立学习库 |
| 本课临时转发 | `scripts/check-infra.ps1 -TemporaryTunnel` | 操作系统选两个空闲 loopback 端口，M00 保存自己的 SSH Popen，退出后等待并验证端口关闭 |
| MySQL | `deephelp-mysql` 的旧数据/秘密/冷备是退役恢复源 | M20 首次需要时盘点并建立新实例，不能恢复旧容器作为课堂数据库 |
| RabbitMQ/Kafka | 当前课程索引没有登记学习实例 | M28/M29 首次需要时再盘点与安装，不预装；没有登记不等于扫描证明全服务器不存在 |

证据必须分五层保存：①配置登记；②TCP 可达；③协议鉴权与版本；④合成业务写读与不变量；⑤停止/恢复后的持久事实。M00 覆盖 ①②③ 和会话元信息只读，④⑤留给对应实库课。Redis `PING` 不证明 TTL/原子性/持久化，PG `SELECT server_version` 不证明事务业务正确，容器 healthy 也不等于鉴权被接受。

已有隧道不通时先定位监听与 SSH。M00 临时转发只管理自己创建的进程；端口被占用时 `ExitOnForwardFailure=yes` 拒绝创建，不能杀占用者。探针不调用 DeepHelp `full`、不重启共享服务、不切换发布、不操作恢复卷。

## 后续安装者先取得变更锁

建议学习根为 `/srv/go-demo-learning`。这是拟用根，M00 没有创建它。第一次安装者按授权核对归属后准备 `control/`、`services/`、`data/`、`runs/`；与 `/srv/deephelp-*` 的 Compose project、volume、账号、文件、端口分开。脚本拒绝 `/` 和解析后位于 `/srv/deephelp*` 的根；control 必须由执行者拥有、不是符号链接，install.lock 也不能是符号链接。准备时 control 设 0700，避免其他用户替换 lock/owner 文件。

所有安装、升级、迁移、端口登记和权威服务索引更新使用同一把内核锁：

```bash
# 在已经核对且具有写权限的 /srv/go-demo-learning 下，安装者准备 control。
# with-install-lock.sh 来自本课；install-mysql.sh 由 M20 实施并做完整实库验收。
bash with-install-lock.sh /srv/go-demo-learning m20 r20261012a 900 -- bash install-mysql.sh
```

调用链是 `with-install-lock.sh → realpath/参数校验 → fd 9 打开 install.lock → flock -n → 原子发布 install-owner.json → setsid timeout COMMAND → wait → EXIT cleanup`。`install.lock` 始终保留同一个 inode，不能删除它抢锁。未取得锁退出 75；此时读取 owner、等待该安装者结果，继续本模块的独立讲解或本地实验。

owner 内容包含 `module/run/pid/started_at_utc/scope`，不保存命令正文或密码。正常结束和 TERM/INT 会先停止并等待自己创建的进程组，再移除 owner，最后关闭父进程 fd。timeout 最多 1800 秒，TERM 后给 10 秒才 KILL。清理也有最多 10 秒的进程组观察，仍存在则 KILL；如果组仍存在，保留 owner 诊断。脚本不显式 `flock -u`：异常子进程仍持有继承 fd 时，内核锁会持续到它真正退出，不能提前宣称安装锁已释放。安装命令必须同步等待安装、协议和必要语义核验结束，不能把安装后台化后立即返回。

SIGKILL 或断电不能执行 trap。内核锁随最后持锁 fd 的进程退出而释放，但 owner 文件可能遗留。恢复者先用 `flock -n` 判断锁、核对 owner PID/进程和原 run 的容器/端口/报告；不能仅因文件旧就删除活动锁，也不能盲重装。owner 文件不单独充当互斥锁。

M00 的实际验证限于本机与远端 `bash -n`、远端 `flock/setsid/timeout` 可用性。远端锁竞争、超时和信号路径没有实际运行，因为它们需要写远端 control；首次安装者应在准备好的独立根内，用无业务副作用命令先验证锁竞争/释放，再进入安装。这里不会把语法检查声称为运行验收。

## 资源额度和端口登记

M00 的当前只读快照确认 Linux x86_64、4 个可见 CPU、7685 MiB RAM；实际 available 和磁盘见 STATUS，不能沿用历史“4 核/8 GB”当空闲额度。Docker 实例运行额度也存在过度承诺风险，`MemAvailable`、峰值、CPU/磁盘使用和现有 cgroup 上限都要在安装时重读。

以下是首次学习实验的上限提案，只有安装者核对容量、固定版本并实际应用后才成为事实：

| 新增组件 | 起始 memory/CPU 上限提案 | 连接/数据/日志上限提案 | 端口处理 |
| --- | --- | --- | --- |
| M20 MySQL | 768 MiB / 0.75 CPU | `max_connections=25`；合成数据 100 MiB 内；日志 10 MiB×3 | 持锁核对后选空闲 loopback 端口；禁止复用退役 13306 语义 |
| M28 RabbitMQ | 512 MiB / 0.5 CPU | 实验连接≤4、channel≤8、队列≤10、消息≤1000；日志 10 MiB×3 | broker/管理端口均 loopback，只有需要管理 API 才登记它 |
| M29 Kafka 单节点 KRaft | 1024 MiB / 1 CPU；JVM heap 512 MiB | 实验 client≤4；每 topic≤3 partition；保留 1 小时/100 MiB；日志 10 MiB×3 | 同时核对 bootstrap 与 advertised.listeners 的客户端可达地址 |

这些不是容量承诺，版本实际配置项需由该课验证。如果安装后 available 会低于 1536 MiB 的学习保护余量，或当前其他组件已产生压力，应减少并发/正常停机保存未用的新 MQ，再核对；不能为课堂服务停 DeepHelp。保护余量是课程的操作约定，不是内核硬保证；磁盘先核对实需/可用并设置可观察上限，bind 目录本身不会因文档写了“100 MiB”就具备硬配额。

唯一权威服务索引由持锁安装者写 `/srv/go-demo-learning/control/endpoints.json`，先写临时文件再原子替换。至少记录：service、owner/module/run、镜像 tag/digest、服务端/Go driver 版本、loopback 监听、隧道入口、advertised 地址、Compose project、volume 归属、memory/CPU/连接/日志上限、启动/停止/恢复命令、当前验证层和秘密文件路径。不能写 password/DSN/token 正文。消费者只读并将索引版本和所用端点记入自己的 STATUS；不能几个模块一起改索引。

## 每课独占数据与清理

| 资源 | 名字示例 | 清理边界 |
| --- | --- | --- |
| MySQL 数据库 | `golearn_m20_r20261012a` | 只清理本 run 自建库；先核对连接目标和实际对象 |
| Redis | `golearn:m21:r20261012a:order:1`，小 TTL | 只按已记录精确键清理；禁止 FLUSHDB/FLUSHALL 和全库 scan-delete |
| RabbitMQ | vhost `/golearn_m28_r20261012a`，独立 user/queue | 只删本课自建 vhost/queue/binding；先确认无消费者 |
| Kafka | topic `golearn.m29.r20261012a.orders`、group `golearn.m29.r20261012a.consumer` | 不删其他 topic，不重置他人 offset；M30/M37 另建自己的业务名 |
| HTTP/RPC/metrics | `127.0.0.1:0` 动态分配或索引登记端口 | 仅关闭 owner 保存的 listener/process，不按占用端口杀任意进程 |
| 私有配置/报告 | 每课 `.secrets/` 与 `.local/` | 报告和秘密分开；保持连接配置/必要失败证据，编译产物可按精确路径清理 |

先建 `.gitignore` 并执行 `git check-ignore -v lessons/mXX_slug/.secrets/connection.env`，确认匹配后才写秘密文件；远端目录 0700、秘密文件 0600。写了 ignore 不等于防止终端泄漏，不能打印整个配置、`docker inspect` 的 Env、展开后的 Compose、带密码的 DSN 或原始异常 traceback。

版本/端点/归属改变时只复验受影响证据。M20/M28/M29 首次安装必须证明进程、鉴权、合成写读、持久化与启动停止入口；Kafka metadata 返回地址也要真实连通。M00 的只读 PASS 不替代它们。
