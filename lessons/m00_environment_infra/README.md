# M00：工具链、项目地图与共享基础设施入口

本课要解决的场景是：你接到一个 Go 服务仓库，命令看起来跑通了，却不确定测到了哪个 module、用了哪个 Go，或者连接的数据库是不是已经退役的旧实例。先把这些事实证明清楚，再进入业务课。核心实验可在 10～20 分钟读完、运行并对照；这里直接给出反例、修复和解析，没有 exercise 或考核入口。

本课独立 module 为 `go-demo/lessons/m00_environment_infra`，只有标准库依赖。实际验证时间为 **2026-10-12（北京时间）**，本机 Go **1.27.0 windows/amd64**。源码/正常运行/单测/vet/Windows build/Linux 交叉编译已实际运行；Redis/PG 已完成真实鉴权与只读核验。默认 32 位 GCC 导致的 Windows `-race` 失败已通过主 agent 提供的便携 amd64 工具链解决，本课实际 race 测试 PASS；原始失败原因保留以便识别同类问题。

## 先看什么、现在理解什么

阅读顺序是 `go.mod → cmd/demo/main.go → cmd/demo/label.go → internal/environment/environment.go → environment_test.go → infra/endpoints.json → scripts/check-infra.ps1/check_infra.py → infra/INSTALLATION_CONTRACT.md`。`STATUS.md` 保存本次真实结果和未验范围。

现在必须理解：module 与 package 的不同；当前工作目录怎样影响 Go；`go run` 指定文件和指定包的差异；根 `./...` 不穿过嵌套 module；编译器版本和 `go` 指令不同；TCP、鉴权、业务与持久化是不同证据。`context`、`defer`、`error`、`os.Exit` 的本课用法在源码旁解释，先沿调用链读懂它们负责的等待和清理即可。复杂 module 图裁剪、完整 CGO 链接、远端安装恢复和 MQ 配置到相应课程再展开；本课不先安装全套服务。

| 文件 | 在本课中的职责 |
| --- | --- |
| `go.mod` | 声明课程 module 和最低 Go 版本；不修改根 `go.mod/go.sum` |
| `cmd/demo/main.go` | `main → execute → run`；拥有 15 秒演示 ctx，决定是否做 TCP 检查 |
| `cmd/demo/label.go` | 与 main.go 同包的第二个文件，用真实缺失符号展示单文件陷阱 |
| `internal/environment/environment.go` | 读取二进制 build info、等待只读 `go env` 子进程、校验索引、有限 TCP 拨号/关闭 |
| `internal/environment/environment_test.go` | 用真实 loopback listener 证明 TCP 不等于鉴权；断言取消与范围拒绝 |
| `infra/endpoints.json` | 无口令/DSN/token 的接入索引，退休和待安装组件显式禁探测 |
| `scripts/check-infra.ps1` | 使用既有 Python，校验报告必须写本课 `.local/`，转交核验参数 |
| `scripts/check_infra.py` | 真实 AUTH/PING/INFO、PG readonly 会话 SELECT、严格 SSH 只读盘点、临时转发的 ownership/清理 |
| `scripts/test_check_infra.py` | 无网络调用拦截，断言 Redis 越界 host 在客户端/连接池/协议调用前被拒绝；只证明范围，不冒充实库验收 |
| `scripts/with-install-lock.sh` | 后续安装者使用的 flock 协调入口；M00 只做 syntax/tool 可用性核验 |
| `infra/INSTALLATION_CONTRACT.md` | 接入、锁、容量、端口登记、命名空间、秘密与清理约定 |
| `.gitignore` | 在创建私有报告或秘密前排除 `.local/`、`.secrets/` |

## 核心实验 1：证明当前 Go、module 和包

以下命令默认从课程仓库根执行。汇总后根为 `D:/IdeaProject/go-demo`；本次真实执行根为 `C:/Users/Lenovo/.codex/worktrees/go-course-m00/go-demo`。目录变了只会改变日志的绝对路径，不改变 module 身份。

```powershell
Set-Location 'D:/IdeaProject/go-demo'
go version
go -C lessons/m00_environment_infra env GOMOD GOWORK GOTOOLCHAIN
go -C lessons/m00_environment_infra run ./cmd/demo
go -C lessons/m00_environment_infra test ./...
go -C lessons/m00_environment_infra vet ./...
```

`-C` 是 Go 命令先切换的目录；相对包路径和程序默认工作目录随之以本 module 为基准。它不是只改变编译时查找路径。本次正常输出：

```text
M00：证明当前程序来自哪个工具链与 module
[二进制] version=go1.27.0 target=windows/amd64 package=go-demo/lessons/m00_environment_infra/cmd/demo module=go-demo/lessons/m00_environment_infra
[进程] cwd=C:\Users\Lenovo\.codex\worktrees\go-course-m00\go-demo\lessons\m00_environment_infra
[下一条 Go 命令] GOMOD=C:\Users\Lenovo\.codex\worktrees\go-course-m00\go-demo\lessons\m00_environment_infra\go.mod GOWORK="" GOVERSION=go1.27.0 GOTOOLCHAIN=auto CGO_ENABLED=1
[层次] module 管依赖和版本；package 是目录中共同编译的源码；main 包才有程序入口。
[边界] 未连接中间件；需要 TCP 检查时加 -tcp，需要协议鉴权时运行 scripts/check-infra.ps1。
```

第一条事实来自正在执行的二进制。第二条是这个进程的当前目录。第三条来自 `Describe` 同步等待的 `go env` 子进程，证明在此目录下一次 Go 命令会读取哪个 `go.mod`。三条不能混为一条：二进制可以由其他目录编译，也可以在其他目录运行。

`main` 是包名，`go-demo/lessons/m00_environment_infra/cmd/demo` 是包导入路径，`go-demo/lessons/m00_environment_infra` 是 module 路径。一个 module 可以包含多个 package；每个普通目录共同编译为一个 package，同包的文件可以直接引用小写符号。库 package 没有程序入口，不能把 `internal/environment` 单独 `go run` 成程序。`internal/` 还限制导入者所在目录树，不代表 Maven 的公开 jar。

调用和资源时间线：

| 步骤 | 参与者/符号与等待点 | 当前状态与下一步 |
| --- | --- | --- |
| 1 | Go 运行时 → `main` → `execute` | flag 创建 bool/string 指针；`*tcp`/`*index` 取其命令行值 |
| 2 | `execute` → `context.WithTimeout` | 本入口拥有 deadline/timer；注册 `defer cancel` |
| 3 | `run` → `Describe` | 读 cwd 和当前二进制 build info，无数据库副作用 |
| 4 | `Describe` → `exec.CommandContext(...).Output()` | 同步等待只读 `go env` 子进程结束，解码其安全白名单 JSON |
| 5 | 默认 `tcp=false` | 返回 nil；没有创建数据库连接、服务或后台任务 |
| 6 | `execute` 返回 0 | 先执行 `defer cancel`，再回到 main 的 `os.Exit(0)` |

出现读取/JSON/命令错误时，`%w` 保留下层 error 原因并返回 `run → execute`；入口输出错误，返回 1，清理 timer 后由 main 报非零退出状态。不把 error 换成“健康通过”，也不需要 recover。Java 可以把这一层看作返回成功/失败状态的 CLI wrapper，但 Go 的 error 是普通返回值，不会自动沿异常机制抛出；`os.Exit` 不执行当前栈上的 defer，所以必须让拥有资源的 `execute` 先返回。

## 核心实验 2：工作目录反例、单文件反例与修复

从仓库根执行这条**错误命令**：

```powershell
go env GOMOD
go run ./lessons/m00_environment_infra/cmd/demo
```

本次第一条输出根 `.../go-demo/go.mod`，第二条真实失败，退出 1：

```text
main module (go-demo) does not contain package go-demo/lessons/m00_environment_infra/cmd/demo
```

包路径看起来在根目录下面，实际 `lessons/m00_environment_infra/go.mod` 已形成新 module 边界。修复是明确进入它：

```powershell
go -C lessons/m00_environment_infra run ./cmd/demo
```

不能通过改根 module 路径、删除子 `go.mod` 或改共享 go.work 来隐藏这个边界。根 module 与课程 module 是不同依赖图。

第二条**错误命令**已经正确选中了 module，却只提交一个源码文件：

```powershell
go -C lessons/m00_environment_infra run ./cmd/demo/main.go
```

本次真实编译错误：

```text
# command-line-arguments
cmd\demo\main.go:47:14: undefined: lessonLabel
```

`lessonLabel` 在同目录的 `label.go`。指定文件列表时 Go 不自动加上剩余同包文件。`go run ./cmd/demo` 按包选择源码，会包含两者，修复后成功。显式列出两文件也能成功：

```powershell
go -C lessons/m00_environment_infra run ./cmd/demo/main.go ./cmd/demo/label.go
```

但本次它的 build info 为 `package=command-line-arguments module=`，而 `go env GOMOD` 仍是 M00 的 go.mod。显式源码列表产生临时包；空 `Main.Path` 不能推出“没有使用 module 解析 import”。真实项目优先按包运行，既包含同包源码，又保留易识别的包/module 构建身份。

还有一个成功但容易误读的对照：

```powershell
# 输出只属于本模块 .local，先由调用者创建这个已忽略目录。
New-Item -ItemType Directory -Force lessons/m00_environment_infra/.local | Out-Null
go -C lessons/m00_environment_infra build -o .local/demo.exe ./cmd/demo
& ./lessons/m00_environment_infra/.local/demo.exe
```

第二条在仓库根运行二进制。实际日志仍显示二进制 module 为 M00，但进程 cwd 和下一条 GOMOD 都是根 `go-demo`。二进制身份由构建写入，后续 Go 命令身份由当前目录/工作区决定；修改工作目录不会重新编译已经存在的程序。

## `go test ./...` 为什么漏掉课程

`./...` 是当前 module/工作区的 package 匹配，不是遍历硬盘上每一个 go.mod。本次从根 `go list ./...` 选中 36 个 package，里面既没有 M00，也没有既有 `examples/basic-syntax/go-modules/app`。根 `go test ./...` 成功只覆盖这些根 package。M00 的 `go list ./...` 单独选中 `cmd/demo` 与 `internal/environment`，本次三个有意义的断言均 PASS：

```text
TestTCPDoesNotProveAuthentication  PASS
TestCanceledDial                 PASS
TestIndexRejectsScopeExpansion    PASS
```

第一个测试拥有真实 `127.0.0.1:0` listener，`net.Listen` 返回就已是就绪屏障，不用 sleep；对它只握手，断言 `Connected=true` 且 `Authenticated/Business=false`。第二个由 caller 先 cancel，拨号观察取消并返回失败。第三个拒绝退休 MySQL 的启探测配置和公网 Redis 地址。它们证明本课的接入证据/范围契约，不冒充实库业务测试。

这和 Maven 多模块 reactor 的聚合不同：根 go.mod 没有 Maven `<modules>` 那种隐式课程清单；嵌套 go.mod 会建立边界。每课独立执行 `go -C lessons/mXX_slug test ./...`，整层验收由主 agent 汇总。本课不创建共享 go.work。

## module、依赖选择、校验与 Maven 类比

现有只读参照为 `examples/basic-syntax/go-modules/{app,greeting,punctuation}`，实际调用链是 `app.main → greeting.Hello → punctuation.Finish`。运行与查看真实选中模块：

```powershell
go -C examples/basic-syntax/go-modules/app run .
go -C examples/basic-syntax/go-modules/app list -m all
go -C lessons/m00_environment_infra list -m all
```

已有 app 本次输出 `你好，Gopher！`，依赖为 greeting、punctuation 的 `v0.0.0 => ../目录`；M00 只有自己的主 module。app 的两个本地 replace 都在 app.go.mod 中：依赖 module 内部的 replace 不传递给主 module。该示例源码一直留在原处，本课不复制或修改它。

| 你已有的 Maven 经验 | Go 中应使用的判断 |
| --- | --- |
| group/artifact/version 坐标 | module path + version；import 指向其中的 package，不能把 import 和 module 名完全等同 |
| dependencyManagement / 最近定义选择 | require 表达最低要求；MVS 从实际需求图为同一 module path 选择所需最高版本，不套用 Maven nearest-wins |
| lock 固定最终依赖清单 | go.mod + 依赖模块需求生成 build list；go.sum 是下载内容校验记录，既可能保存多版本 hash，也不能决定当前选中版本 |
| 标准库也可能在依赖库中 | fmt/runtime/net 是 Go 标准库，版本随工具链，不写 require |
| 本地组件替换 | 当前 main module 的 replace；替换本地目录不需要远程下载 hash，因此本课和既有本地示例可没有 go.sum |

`go.sum` 应随适用的依赖变更审查/提交，不能把“通常不手改”误解为“不用管”。内容 hash 校验也不等于依赖代码没有漏洞、当前业务一定正确。[官方 modules 参考](https://go.dev/ref/mod) 是 module/require/replace/MVS 的查证入口；本课的依赖图证据来自上面的真实 `list -m all`。

## `go`、`toolchain`、`GOTOOLCHAIN` 与当前平台

本课 `go 1.27.0` 声明最低工具链要求，同时约束本 module 源码语言版本；不是自动安装语法，也不是一行纯注释。本课没有显式 toolchain 指令。toolchain 是主 module/工作区建议使用的工具链版本，与 go 最低要求分开。`GOTOOLCHAIN=auto` 允许 Go 根据要求选择/下载可用工具链；`local` 则只用捆绑版本，不满足最低要求会报错。因此 PATH 指向的 go 与实际选出的编译器不能只靠猜测，要同时看 `go version`、go.mod/go.work、GOTOOLCHAIN 和二进制 build info。[官方 toolchains 文档](https://go.dev/doc/toolchain)

本次实际状态：

| 字段 | 当前值/观察 |
| --- | --- |
| `go` / `gofmt` | `D:/Go/bin/go.exe`、`D:/Go/bin/gofmt.exe` |
| `GOVERSION` / `GOROOT` | `go1.27.0` / `D:/Go` |
| `GOHOSTOS/GOHOSTARCH`、默认 `GOOS/GOARCH` | `windows/amd64` |
| `GOTOOLCHAIN` / `GOWORK` | `auto` / 空值（没有激活共享工作区） |
| `CGO_ENABLED` / `CC` / `CXX` | `1` / `gcc` / `g++` |
| 实际默认 C 编译器 | `D:/MinGW/bin/gcc.exe`，MinGW.org GCC 8.2.0，不能编译此次 64 位 runtime/cgo |
| 本课实际 race 使用的编译器 | WinLibs GCC 16.2.0 / mingw-w64 14.0.0 / UCRT，目标 `x86_64-w64-mingw32`，仅进程级设置 |
| `GOPROXY` / `GOSUMDB` | `https://goproxy.cn,direct` / `sum.golang.org` |
| `GOPRIVATE/GONOPROXY/GONOSUMDB` | 均为空；本课未访问私有依赖，没有真实公司配置可核验 |

CGO_ENABLED=1 表示允许使用 CGO，不代表已导入 C 的所有构建都可用。标准库纯 Go 正常命令通过，实际 `go test -race ./...` 触发 runtime/cgo 后失败：

```text
# runtime/cgo
cc1.exe: sorry, unimplemented: 64-bit mode not compiled in
FAIL ... [build failed]
```

旧 `gcc --print-file-name libsynchronization.a` 也只回显文件名，未给出路径。Go 的 Windows/amd64 race 支持需要合适的 C 编译器和 mingw-w64 runtime；“GCC 主版本是 8”不能推断它就是 mingw-w64 runtime 8，更不能推断是 amd64 编译器。[官方 race requirements](https://go.dev/doc/articles/race_detector#requirements)

主 agent 已把可用工具链准备在主工作区 `.local/toolchains/`，权威本机入口为 `D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json`。本课没有复制/安装它；只读加载其 `cc/cxx/bin_dir`，进程级使用，结束后恢复原环境。复跑方式：

```powershell
$record = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' -Raw | ConvertFrom-Json
$savedCCForRace=$env:CC; $savedCXXForRace=$env:CXX
$savedCGOForRace=$env:CGO_ENABLED; $savedPathForRace=$env:PATH
try {
    $env:CC=$record.cc; $env:CXX=$record.cxx; $env:CGO_ENABLED='1'
    $env:PATH=$record.bin_dir+';'+$env:PATH
    & $record.cc --version
    & $record.cc -dumpmachine
    & $record.cc --print-file-name libsynchronization.a
    go -C lessons/m00_environment_infra test -race -count=1 -timeout 40s ./...
} finally {
    $env:CC=$savedCCForRace; $env:CXX=$savedCXXForRace
    $env:CGO_ENABLED=$savedCGOForRace; $env:PATH=$savedPathForRace
}
```

本次实际 `--version` 为 GCC 16.2.0，`-dumpmachine` 为 `x86_64-w64-mingw32`，库查询给出真实 `libsynchronization.a` 路径。`-dM -E -include _mingw.h -` 的预处理版本宏进一步确认 mingw-w64 **14.0.0**。本课 race 退出0：`cmd/demo [no test files]`，`internal/environment 2.667s`；这证明本次执行的三项测试及 Windows race 运行可用，不证明未执行路径没有数据竞争。该本机索引/编译器是私有环境资产，新机器需要提供兼容工具链及自己的索引，不能仅凭课程 Git 文件假设它存在。本课没有关 CGO、关闭校验或写全局 go env。

Linux 目标用纯 Go 交叉编译，当前已成功生成并由 `go version -m` 检查 `GOOS=linux/GOARCH=amd64/CGO_ENABLED=0`：

```powershell
$savedGOOS=$env:GOOS; $savedGOARCH=$env:GOARCH; $savedCGO=$env:CGO_ENABLED
try {
    $env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
    go -C lessons/m00_environment_infra build -o .local/demo-linux-amd64 ./cmd/demo
} finally {
    $env:GOOS=$savedGOOS; $env:GOARCH=$savedGOARCH; $env:CGO_ENABLED=$savedCGO
}
go version -m lessons/m00_environment_infra/.local/demo-linux-amd64
```

这只证明 Windows 工具链能生成这个 Linux 纯 Go 产物。本课未部署/运行它，未证明 Linux CGO/race 或目标机器所有驱动可用。公司使用的 Go 版本、Linux 架构、私有 module 域名、内部代理与 CA 目前尚未知；拿到公司仓库时按其实际 go/toolchain 指令解释，不擅自升级。

## 代理与私有依赖的边界

本课只读取现状，不执行 `go env -w`。`GOPRIVATE` 为匹配的 module path 前缀提供私有判定，并在没有独立覆盖时影响 GONOPROXY/GONOSUMDB。GONOPROXY 控制是否绕过代理；GONOSUMDB 控制是否联系公共 checksum database。这三者既不交付 Git 登录凭据，也不保证网络通；仅设置 GONOSUMDB 仍可能把模块路径发给公共代理。配置公司内部代理时要按真实域名与内部协议决定覆盖关系，不能把所有模块都设为私有或关闭 GOSUMDB 来省事。[官方 private modules 边界](https://go.dev/ref/mod#private-modules)

资料里“下载失败就从 direct 回退”需要修正：逗号分隔 GOPROXY 通常只在 404/410 回退，竖线分隔的回退范围更宽；不能把超时/403 都当同一结果。代理 URL 可能包含口令，因此正常 demo 只输出安全白名单，不打印全量环境。[Go 命令文档](https://pkg.go.dev/cmd/go)

## 扩展实验：真实 Redis/PG 只读核验与清理

有配置且已有转发时：

```powershell
go -C lessons/m00_environment_infra run ./cmd/demo -tcp
& ./lessons/m00_environment_infra/scripts/check-infra.ps1
```

第一条只打开/关闭 TCP socket，没有 AUTH 或 SQL；连接失败作为观察结果打印，程序退出 0 只表示观察流程完成，不表示服务通过。本次登记的 16379/15432 没有本机监听，Go TCP 输出 `connected=false authenticated=false business=false`，Python 的真实鉴权尝试分别 `TimeoutError` / `ConnectionTimeout`，脚本退出 1。没有用历史 `.local/DEPENDENCIES.md` 的 PASS 覆盖失败，也没有启动别人的隧道。

接入范围由 `redis_check` 自己先校验，不靠 TCP 结果间接阻断。验收曾发现旧代码在 `tcp_check` 返回拒绝后仍继续构造 Redis 客户端并执行 PING/INFO；已修正为在驱动导入、client/连接池创建和拨号之前，对非显式 `127.0.0.1` host 直接返回。失败记录是 `NonLoopbackRejected`、`tcp.executed=false`、`resources_created=false`、`protocol_attempted=false`，cleanup 明确说明“未创建客户端/连接池，无需清理”，不能称为“已执行然后关闭”。`localhost` 别名与 IPv6 `::1` 不属于本课已验证的显式 IPv4 转发入口；拒绝它们不表示 `::1` 不是 loopback。

这项边界的独立断言使用合成配置和调用拦截，不建立连接，也不加载真实秘密：

```powershell
& 'D:/IdeaProject/deephelp/.venv/Scripts/python.exe' -B -m unittest discover -s ./lessons/m00_environment_infra/scripts -p test_check_infra.py -v
```

本次 1 个测试、4 个 host 子场景均通过，Redis constructor/PING/INFO/close/pool.disconnect、tcp_check、socket.create_connection 的调用次数都为零。TCP 拦截即使返回 `pass=false` 也不会被当成 guard；独立的 constructor/协议调用拦截才证明客户端没有被创建或访问。这是无副作用的范围回归证据，后面的真实协议结果单独验收。

本课自己的完整修复路径为：

```powershell
& ./lessons/m00_environment_infra/scripts/check-infra.ps1 -TemporaryTunnel
```

它读取既有严格 SSH 配置，创建自己的一条 SSH 进程，选两个动态 loopback 端口转发到现有 Redis/PG。没有变更服务、认证、数据或共享配置。真实结果关键段：

```text
Redis: TCP=true authenticated=true server_version=8.2.8
PostgreSQL: TCP=true authenticated=true
server_version=18.6 (Debian 18.6-1.pgdg12+2), transaction_read_only=on
SSH inventory=true, lock_script_bash_n_exit=0
owned tunnel: owner process waited; both temporary local ports no longer listening
business_write_read=not run; persistent_recovery=not run
```

本次客户端来自既有 DeepHelp Python 3.14.7 环境，redis-py 6.4.0、psycopg 3.3.6；本课没有创建新 Python 环境、增加 Go driver 或改变 DeepHelp 锁。Python 的 `-B` 避免导入时在外部环境写 pycache。

完整调用链为 `check-infra.ps1 → check_infra.main → [owned_tunnel] → redis_check/postgres_check → ssh_inventory → owned_tunnel finally → 写本模块 .local 报告 → 退出码`。对应时间线：

| 顺序 | 参与者/等待点 | 证明事实与清理 |
| --- | --- | --- |
| 1 | `owned_tunnel` 绑定两个 `127.0.0.1:0` 临时 reservation socket | 保存选中的端口后释放 reservation，交给 SSH；被抢占由 ExitOnForwardFailure 拒绝 |
| 2 | `Popen(ssh -N -T -L ...)` | owner 保存自己的进程对象；严格 known_hosts，不接管已有隧道 |
| 3 | 有截止/次数上限的 TCP 状态观察 | 实际握手成功才是就绪屏障；50ms 重试间隔不是就绪保证 |
| 4 | `redis_check → host guard → ping/info` | host 超出显式127.0.0.1范围就立即返回，不创建资源；通过 guard 后 AUTH 被接受才称 authenticated；只读 PING/INFO；finally close + disconnect 私有池 |
| 5 | `postgres_check → psycopg.connect → cursor.execute` | 连接启动设 readonly；只 SELECT 两项会话设置，随后先退出 cursor、再关闭 connection |
| 6 | `ssh_inventory → subprocess.run` | 固定 uname/free/df/ss 和 sudo -n docker 的只读命令；SSH 最多 20 秒；bash -n 最多 10 秒 |
| 7 | `owned_tunnel finally` | terminate 自己的 Popen，wait 3 秒，必要时 kill 并 wait；验证两个端口不再监听 |
| 8 | 写报告、返回状态 | 两个 authenticated 都 true 且 SSH 未失败才退出 0；报告/异常不含口令/DSN/私钥正文 |

Redis 连接/命令超时各 2 秒，无自动重试；PG connect_timeout=3、statement_timeout=2 秒、lock_timeout=1 秒；TCP 单次 2 秒；核心 Go 演示 deadline=15 秒。没有不受控 goroutine、无限重试或故障服务重启。

第一次远端 inventory 失败原因是 SSH 账号直接 docker 无权限；改用允许的 `sudo -n docker` 固定只读操作后成功。第一次 remote bash -n 失败原因是 Windows `text=True` stdin 把 LF 转成 CRLF；改为 UTF-8 字节管道，远端 syntax 核验通过。两次失败均保存在本课 `.local/`，STATUS 保留原因，不把修复前输出改写为 PASS。

修复 Redis 范围后已重跑 `-TemporaryTunnel`，真实 Redis/PG 鉴权仍通过，自己的 SSH 进程已等待退出，两临时端口关闭。该次远端只读盘点为 Linux x86_64、4 CPU、RAM 7685 MiB / available 3347 MiB，`/srv` 所在磁盘 available 28666 MiB、51% 已用。这是一次快照，不是未来容量保证。Redis/PG 容器 running，旧 `deephelp-mysql|mysql:8.4.11|Exited (0) 6 days ago`；当前容器清单没有登记 Go 学习 MySQL/RabbitMQ/Kafka，也没有因看到其他监听端口就猜组件归属。旧 MYSQL_DSN/密码/恢复卷仍存在不证明当前可连接，更不表示有权恢复双写。

端点地图、后续安装者的内核 flock、资源额度提案、端口登记和隔离名在 [安装协调约定](infra/INSTALLATION_CONTRACT.md)。M20/M28/M29 到首次需要时再核对并安装自己的实例；本课没有创建远端学习根/锁、运行安装命令、写业务数据或重启恢复实验。

## 启发材料中需要纠正的结论

已只读核对 `files/go环境搭建/环境搭建.md` 和 `files/基础语法/依赖管理`，它们帮助定位主题，不能当版本事实。对应修正是：

- 安装示例的下载版本/架构和解压文件不一致；实际安装须匹配当前目标，不能照抄先 `rm -rf` 再解压的旧命令。本课已有工具链可用，没有重装。
- “次新版本即可”不适用于维护公司旧服务；先读该仓库最低 go/toolchain 和兼容约束。本课按当前授权用 Go 1.27.0，公司版本未知。
- `go` 指令在 Go 1.21 起具有最低版本约束，不能继续说只是标识；现代 Go module 默认行为也不能套用旧 GO111MODULE auto 规则。
- `vendor` 不无条件优先；要看当前 module 的 go 版本、vendor 状态及 -mod 模式。本课没有 vendor，不用全局开关修依赖。
- `indirect` 的说明不只有“依赖缺 go.mod”一种；现代图裁剪/延迟加载也影响主 module 的显式 requirements，当前选中版本以真实 build list 为准。
- replace 的语法是 `=>`，不是资料中的 `==>`；正确指令是 `retract`，不是 `restract`；`+incompatible` 是对某些旧非 module 版本的兼容表示，不能给任意违反主版本路径的现代 module 自动补一个标签。
- go.sum 校验下载内容，不是版本锁，也不应无条件忽略；`go clean -modcache` 会清掉共用缓存，本课无需执行它。

本课没有硬前置，也没有组合其他课程的代码。交付完成与真实验证范围在 [STATUS](STATUS.md)；统一提交由主 agent 在整层验收后执行。
