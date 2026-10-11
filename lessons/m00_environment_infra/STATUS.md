# M00 实际交付与验证状态

记录日期：2026-10-12，Asia/Shanghai。Redis 范围修复后的最新远端核验报告时间为 2026-10-11T17:42:15.048285+00:00（北京时间 10-12 01:42:15）。执行者只写 `C:/Users/Lenovo/.codex/worktrees/go-course-m00/go-demo/lessons/m00_environment_infra/`，没有提交、推送、修改主工作目录或其他 module。

**课程源码/解释/只读接入验证已交付，run/test/vet/build 与实际 Windows race 已通过，待主 agent 验收。默认 Windows 32 位 GCC 的历史 race 构建失败已通过主 agent 提供的便携 amd64 CC/CXX 解决。** 本课未安装 MySQL/RabbitMQ/Kafka、未恢复退役 MySQL、未改共享服务/配置或业务数据。

## 交付文件与读取材料

正常入口 `cmd/demo`，独立 `go.mod` 的 module 为 `go-demo/lessons/m00_environment_infra`、go 为 `1.27.0`、没有 toolchain/require/replace。只用 Go 标准库，没有下载依赖需要登记的 `go.sum`。Go 源码带中文调用、对象、等待、错误与清理注释；README 提供完整正常/错误/修复对照和实际输出。

源码文件为 `cmd/demo/{main,label}.go`、`internal/environment/{environment,environment_test}.go`；接入资料为 `infra/{endpoints.json,INSTALLATION_CONTRACT.md}`；可执行只读入口为 `scripts/{check-infra.ps1,check_infra.py}`；辅助范围断言为 `scripts/test_check_infra.py`；后续安装者锁脚本为 `scripts/with-install-lock.sh`。`.gitignore` 在私有产物创建前已提供 `.local/`、`.secrets/` 排除。

实际只读阅读：当前 `learning_prompts/ORCHESTRATION.md`、`TEACHING_STYLE.md`、`INFRASTRUCTURE.md`、完整 M00 prompt；适用 AGENTS（本机 `.codex/AGENTS.md` 为空，本 worktree 根未发现额外 AGENTS）、根 README/go.mod；既有 app/greeting/punctuation 全部 go.mod/源码；`files/go环境搭建/环境搭建.md`、`files/基础语法/依赖管理`；DeepHelp 当前 `docs/LOCAL_SETUP.md`、`infra/README.md`、`infra/OPERATIONS.md`、`infra/postgres/README.md`、`.local/DEPENDENCIES.md`、设置加载源码和本机私有索引的结构/路径。没有将秘密正文或全批 files/ 复制进课程。

## 实际工具链与范围

| 项目 | 当前真实结果 |
| --- | --- |
| Go/gofmt | `D:/Go/bin`；`go version go1.27.0 windows/amd64` |
| GOROOT/GOPATH/GOMODCACHE | `D:/Go`、`C:/Users/Lenovo/go`、`C:/Users/Lenovo/go/pkg/mod` |
| M00 GOMOD | `C:/Users/Lenovo/.codex/worktrees/go-course-m00/go-demo/lessons/m00_environment_infra/go.mod` |
| GOWORK / GOTOOLCHAIN | 空值 / `auto` |
| GOOS/GOARCH 和 host | 默认均 `windows/amd64` |
| CGO_ENABLED / CC / CXX | `1` / `gcc` / `g++` |
| 默认 C compiler | `D:/MinGW/bin/gcc.exe`，MinGW.org GCC-8.2.0-3，实测不能构建 64 位 runtime/cgo |
| race 实际 compiler/runtime | WinLibs tag `16.2.0posix-14.0.0-ucrt-r2`；实际 GCC16.2.0，target=`x86_64-w64-mingw32`，头文件预处理宏确认 mingw-w64 14.0.0 |
| race 工具链接入 | 只读主目录 `.local/course-orchestrator/toolchain.json`；编译器保留主目录 `.local/toolchains/`，未复制进课程 |
| GOPROXY / GOSUMDB | `https://goproxy.cn,direct` / `sum.golang.org` |
| GOPRIVATE/GONOSUMDB/GONOPROXY | 均空；本课没有私有 module 请求 |
| 公司 Go、私有域名、代理/CA、生产 Linux 目标 | 尚未知，不推断公司要求，不升级其代码 |
| Python/真实协议客户端 | 既有 DeepHelp `.venv/Scripts/python.exe`：3.14.7；redis-py 6.4.0；psycopg 3.3.6 |
| SSH/lock tools | Windows OpenSSH；远端 Bash 5.2.21，util-linux flock 2.39.3，setsid/timeout 可用 |

## 已执行命令、工作目录和结果

以下 `W` 代表**本次唯一 shell 工作目录** `C:/Users/Lenovo/.codex/worktrees/go-course-m00/go-demo`。表内所有命令都显式以 W 作为工具 workdir；`go -C` 进一步选择嵌套 module。错误命令没有修改 root。工具退出码和观察范围分别记录，不用最终 shell 命令成功遮盖前面故障。

| 工作目录 | 实际命令 | 观察与真实结果 |
| --- | --- | --- |
| W | `go version`、`go env ...`、`go -C lessons/m00_environment_infra env -json ...` | Go1.27.0，根/M00 GOMOD 分别确认；上表环境实测 |
| W | `gofmt -w` 本课四个 Go 文件；后续 `gofmt -l` 本课 Go 目录 | 格式化执行成功；最终 -l 无输出 |
| W | `go -C lessons/m00_environment_infra run ./cmd/demo` | 退出0；build info package/module=M00，cwd/GOMOD=M00 |
| W | `go -C lessons/m00_environment_infra test -v ./...` | 退出0；三个证据/取消/范围断言 PASS；cmd/demo 无测试文件 |
| W | `go -C lessons/m00_environment_infra vet ./...` | 退出0，无诊断 |
| W | `go -C lessons/m00_environment_infra build -o .local/demo.exe ./cmd/demo` | 退出0；`go version -m` 确认 Windows/amd64、Go1.27.0、M00 module |
| W | `& ./lessons/m00_environment_infra/.local/demo.exe` | 退出0；二进制=M00，但 cwd/GOMOD=根 module，真实展示两个事实来源不同 |
| W | `go run ./lessons/m00_environment_infra/cmd/demo` | **预期反例**退出1：`main module (go-demo) does not contain package ...`；root GOMOD 已独立确认 |
| W | `go -C lessons/m00_environment_infra run ./cmd/demo/main.go` | **预期反例**退出1：`undefined: lessonLabel`；指定文件遗漏 label.go |
| W | `go -C lessons/m00_environment_infra run ./cmd/demo/main.go ./cmd/demo/label.go` | 退出0；package=`command-line-arguments`、Main.Path 为空；GOMOD 仍为 M00 |
| W | `go list ./...`；`go test ./...` | root 选中36个 package，测试退出0；既不含 M00，也不含既有嵌套 modulelab app |
| W | `go -C lessons/m00_environment_infra list -f '{{.ImportPath}} => {{.Dir}}' ./...`、`list -m all` | 两个课程 package 和一个标准库依赖课程主 module；边界确认 |
| W | `go -C examples/basic-syntax/go-modules/app run .`、`list -m all` | 退出0：`你好，Gopher！`；app→greeting→punctuation，两个本地 replace |
| W | 仅该 shell 进程设 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0` 后 `go -C lessons/m00_environment_infra build -o .local/demo-linux-amd64 ./cmd/demo` | 退出0；`go version -m` 确认 Linux/amd64/CGO=0；finally 恢复原环境。没有在 Linux 执行产物 |
| W | `gcc --version`、`gcc --print-file-name libsynchronization.a` | GCC8.2.0；后者只回显文件名，没有可用库路径 |
| W | `go -C lessons/m00_environment_infra test -race ./...`（初次默认 CC） | **历史故障**退出1；精确原因如下，后续便携 amd64 CC 已解决 |
| W | 进程级设置索引 `CC/CXX/CGO_ENABLED=1/PATH` 后 `go -C lessons/m00_environment_infra test -race -count=1 -timeout 40s ./...` | **最终 PASS**退出0；cmd/demo 无测试，internal/environment 三项断言通过，2.667s；finally 恢复原环境 |
| W | `& $record.cc --version`、`-dumpmachine`、`--print-file-name libsynchronization.a`、`-dM -E -include _mingw.h -` | GCC16.2.0、x86_64-w64-mingw32、库真实路径、mingw-w64版本宏14.0.0，均来自当前工具实际输出 |
| W | `go -C lessons/m00_environment_infra run ./cmd/demo -tcp` | 观察流程退出0；登记 Redis/PG TCP 均 connected=false，authenticated/business=false；退休/待安装组件跳过 |
| W | `& ./lessons/m00_environment_infra/scripts/check-infra.ps1` | 初次退出1；登记端口无监听，Redis TimeoutError、PG ConnectionTimeout；初次远端 docker 无权限 |
| W | `& ./lessons/m00_environment_infra/scripts/check-infra.ps1 -TemporaryTunnel` | 最终退出0；真实 Redis/PG 鉴权/版本/readonly 元信息 PASS，SSH 只读盘点 PASS，自己的转发关闭 |
| W | 既有 Python `-B -m unittest discover -s ./lessons/m00_environment_infra/scripts -p test_check_infra.py -v` | Redis 范围修复后退出0；1个测试、4个host子场景，constructor/PING/INFO/close/pool.disconnect/tcp/socket调用均为0；无网络/秘密加载，不作为实库验收 |
| W | 直接用合成非127.0.0.1配置调用 `redis_check` | `NonLoopbackRejected`，tcp.executed/resources_created/protocol_attempted 均false；明确 client/pool 未创建，无需清理 |
| W | Redis guard 修复后再次 `check-infra.ps1 -TemporaryTunnel` | 退出0；Redis8.2.8 AUTH/PING/INFO与PG18.6 readonly SELECT真实通过，SSH与锁syntax核验仍通过；owner进程与两个临时端口均关闭 |
| W | 精确查询修复后 owner PID699556 和临时端口10408/10409 | owner_process_exists=false，remaining_listener_count=0；未停止或清理他人的进程 |
| W | `D:/Git/bin/bash.exe -n .../with-install-lock.sh`；严格 SSH `bash -n`（脚本字节经 stdin） | 最终本机/远端均退出0；语法/依赖可用性确认，**没有运行远端锁/安装命令** |
| W | `git check-ignore -v` 本模块 `.local/infra-current.json`、`.secrets/example.env` | 退出0，各匹配本模块 .gitignore；未创建秘密示例正文 |
| W | `git -C D:/IdeaProject/deephelp check-ignore -v infra/config/connections.env .local/m30/live-postgres.json infra/client/known_hosts` | 退出0；既有私有配置与 host key 路径都被该库忽略 |
| W | 精确查询本课 SSH PID和临时端口；按已核对绝对路径删除两个编译产物 | 最终该 PID不存在，两端口无监听；只删除本课 `.local/demo.exe`、`demo-linux-amd64`，保留必要无秘密报告 |
| W | 本机内存中加载既有 secret 值，逐字节比对本模块14个交付文件和9个私有JSON报告（不输出值） | 检测退出0，secret_value_matches=[]；没有匹配的秘密正文 |

默认 race 失败原文：

```text
# runtime/cgo
cc1.exe: sorry, unimplemented: 64-bit mode not compiled in
FAIL go-demo/lessons/m00_environment_infra/cmd/demo [build failed]
FAIL go-demo/lessons/m00_environment_infra/internal/environment [build failed]
```

主 agent 已在主工作区提供便携 WinLibs；本执行者没有安装/复制编译器，实际用它补验 race PASS。只有当前 shell 的 CC/CXX/CGO/PATH 发生短暂变化，finally 后 `go env CC CXX CGO_ENABLED` 回到 gcc/g++/1。没有改系统环境、写 go env 或关闭校验。普通 test 没有被当成 race 证据，原始32位编译器失败仍保留。

## 真实只读基础设施结果

最终报告是本模块 `.local/infra-current.json`；初次失败 `.local/infra-initial.json`、stdin 修复前 `.local/infra-authenticated-before-syntax-fix.json` 和 Redis guard 修复前 `.local/infra-before-redis-scope-fix.json` 保留历史证据。最终锁脚本独立语法复核 `.local/lock-syntax-current.json`、实际 race `.local/race-current.json`，辅助范围回归记录 `.local/redis-scope-unit-current.json` 和 `.local/redis-scope-rejection-current.json`，修复后的独立进程/端口清理复核 `.local/redis-scope-cleanup-current.json`。报告只含无秘密结果、localhost 临时端口、owner PID及本机编译器路径，不含口令、DSN、私钥、SSH 主机定位信息。

| 服务/证据层 | 本次结果 | 明确未证明的内容 |
| --- | --- | --- |
| Redis 登记 `16379` | 当时本机无 listener，TCP/鉴权失败；不把旧报告当当前事实 | 该登记端口未来是否有隧道，不做保证 |
| PG 登记 `15432` | 当时本机无 listener，TCP/鉴权失败 | 同上 |
| 本课 own SSH tunnel | 仅使用既有 key + strict known_hosts，修复后最终临时 Redis=10408、PG=10409，owner PID=699556 | 端口为一次性结果，不可作为稳定配置复用 |
| Redis 实际协议 | TCP=true，AUTH 接受，PING=true，INFO server_version=8.2.8；finally client/pool closed | 键值写读、TTL、原子性、一致性、持久恢复未运行 |
| PG 实际协议 | TCP=true，既有凭据连接成功；server_version=`18.6 (Debian 18.6-1.pgdg12+2)`；transaction_read_only=`on`；cursor/connection closed | 未读正式业务表，未写表，事务业务/恢复未验 |
| SSH 只读 inventory | uname/nproc/free/df/ss/sudo -n docker ps/stats 通过 | 未改服务，未安装，未扫描全服务器所有可能的非容器组件 |
| 退役 MySQL | `deephelp-mysql` image=`mysql:8.4.11`，`Exited (0) 6 days ago` | 旧端口/DSN/秘密不足以证明当前连接；本课未连接或启动它 |
| 新学习 MySQL/RabbitMQ/Kafka | 当前课程没有登记，当前 Docker 清单未看到对应独立学习容器；按 M20/M28/M29 首次需要时再核对 | 不声称服务器所有部署方式绝对不存在这些软件；本课未预装 |

最终只读资源快照：Linux x86_64；nproc=4；RAM total=7685 MiB、available=3347 MiB；swap total=1987 MiB、used=416 MiB；`/srv` 所在磁盘可用=28666 MiB、used=51%。这些是该时刻观察，不保证后续安装额度。Docker stats 确认正式 PG 当前 memory limit=1 GiB，与旧资料的 512 MiB 不同；资源以实时核对为准。本课没有全量分析 host cgroup quota/未来峰值，首次安装者仍须核对。

初次 SSH inventory 失败由 direct docker 权限引起，修复为非交互 sudo 的固定只读 Docker 命令。第一次远端锁脚本 bash -n 退出2由 Windows text stdin 的 CRLF 转换引起，修复为 UTF-8 bytes 后退出0；后续锁脚本清理语义补强后也再次只做独立 bash -n 验证。真实报告/文档区分失败与修复，不使用模拟代替实库。

主 agent 验收还发现 `redis_check` 的范围缺口：旧版 `tcp_check` 拒绝非127.0.0.1之后，外层仍继续创建 Redis client/pool 并执行 PING/INFO。现在 guard 在驱动导入、资源创建和拨号前直接返回，拒绝记录明确 `resources_created=false`、`protocol_attempted=false`、`tcp.executed=false`，cleanup 是未创建资源、无需清理。新辅助测试独立拦截 constructor/协议/socket 调用，使用无真实秘密的合成输入；它证明“没有调用”，与重跑真实协议探针的鉴权 PASS 分开。Go 功能与锁脚本未变，本轮没有重复无关 Go 构建/测试，也没有运行远端锁变更。

## 未验范围、资源结局与主 agent 验收重点

- Windows race：已用进程级便携 amd64 工具链实际 PASS；只覆盖本次执行的课程测试，默认旧 gcc 本身没有被系统替换，新机器须提供兼容工具链。
- Linux：纯 Go 交叉编译已证实，远端运行、Linux CGO/race 未执行；公司目标版本/私有依赖入口未知。
- 锁：脚本内容、参数/owner/等待/清理约定已交付，本机/远端语法与 tools 可用性已验；M00 远端权限是只读，所以没有创建学习根/control、运行锁竞争/timeout/signal 或安装命令。首次安装者需验证这些运行路径。
- 业务：Redis/PG 只读鉴权与元信息，没有合成业务写读/持久恢复；未安装/复活 MQ/MySQL；未触碰 Milvus/etcd/正式业务/发布/预算/恢复卷。
- 清理：三个单测自己的 socket 在 test cleanup 关闭；Go env 子进程被同步等待；PG cursor/connection 与 Redis 专用连接池均关闭；本课临时 SSH Popen 已等待退出，两临时端口无监听；编译产物已精确删除，无故障代理/后台进程遗留。秘密原件和必要报告保留。
- 主 agent 应重点核对：Redis guard 在客户端/连接池创建之前返回，失败记录没有把“未创建”写成“已关闭”；辅助调用拦截不冒充实库证据；模块边界反例不是故意删除 go.mod；文件列表临时包不等于无 module；TCP demo 的退出0不等于服务 PASS；最终 protocol PASS 来自真实临时转发且清理完成；race 来自进程级便携 amd64 工具链的真实执行；锁的运行范围未被夸大；未来安装索引/额度是约定而不是已部署事实。

本课没有硬前置。所有课程代码、日志解析、接入说明都在本模块内；最终整层验收与统一提交由主 agent 执行。
