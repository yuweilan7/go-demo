# M19 实际交付、自验与冻结记录

记录日期：2026-10-12，Asia/Shanghai。当前状态：**READY_FOR_REVIEW，模块目录冻结，等待主执行者独立验收**。本文件记录本模块实施与实跑事实，不自标 ACCEPTED，不替代主验收/整层汇总/提交。

## 范围、前置与实际环境

- 唯一写入目录：`C:/Users/Lenovo/.codex/worktrees/go-course-m19/go-demo/lessons/m19_composition_architecture/`。worktree基线`5929af4af6d8a599362bdbb20e2d2d3cfa2cf6ba`，包含前两层26课主验收/整层提交成果。
- module路径`go-demo/lessons/m19_composition_architecture`，`go 1.27`，无toolchain/replace/go.work。未修改根go.mod/go.sum、共享索引、其他模块、examples/files；没有add/commit/push、切分支或创建子agent。
- 先读本课完整prompt、TEACHING_STYLE、ORCHESTRATION、modules.json、INFRASTRUCTURE及根README/go.mod。根及逐级祖先未发现其他适用AGENTS；`C:/Users/Lenovo/.codex/AGENTS.md`存在且为空。按go-learning-by-running方法落实中文教学，用户/父执行者范围约束优先。
- M04硬前置实际读STATUS、usecase/service与app/composition；复用小接口、具体指针接线拒绝typed nil与方法集≠语义保证。M18实际读STATUS、protocol/router；复用可信身份、sku-m17/qty1..3、同owner/key幂等、冲突409、完整Envelope、副作用与请求取消范围。未重跑前置全部课；本课对新增Gin组合做实际HTTP验收。前置代码在其internal中，使用文档协议契约+本课最小适配，未非法跨module导入或复制整课。
- 主目录files仅只读：`开发框架`、`工程管理/Go编码规范`、`原理剖析/程序初始化`。README纠正“全局单例=每App容器scope”和绝对文件名init顺序，不依材料宣称框架benchmark。
- 实际工具链`go version go1.27.0 windows/amd64`；GOROOT=`D:/Go`、GOPATH=`C:/Users/Lenovo/go`、GOTOOLCHAIN=auto、GOWORK为空、模块GOMOD为本课路径。实际Go/env与基线见[environment.txt](evidence/environment.txt)，读取时间`2026-10-12 03:39:51 +08:00`。
- Gin锁定`github.com/gin-gonic/gin v1.12.0`，实际go.mod要求Go1.25.0；Sum=`h1:b3YAbrZtnf8N//yjKeU2+MQsh2mY5htkZidOM7O0wG8=`，GoModSum=`h1:VxccKfsSllpKshkBWgVgRniFFAzFb9csfngsqANjnLc=`。间接依赖锁在本课go.mod/go.sum。实际源码`C:/Users/Lenovo/go/pkg/mod/github.com/gin-gonic/gin@v1.12.0`。
- 默认CGO_ENABLED=1、CC=gcc、CXX=g++。race只读主执行者登记的`D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json`，使用WinLibs `16.2.0posix-14.0.0-ucrt-r2`、GCC16.2.0/mingw-w64 14.0.0/amd64 compiler，仅当前PowerShell验证进程设置CC/CXX/CGO/PATH并finally恢复。未安装工具链、改全局PATH或go env。归档SHA核验属于主执行者，本课未重复宣称验过归档。
- 无基础设施安装、重启或隧道操作。每个HTTP App是本进程独占动态loopback端口；每个Repository是独占进程内对象；没有DSN、真实SQL/MQ或共享业务数据。

## 已交付内容与语义事实

| 要求/实验 | 实际运行与直接证明 |
| --- | --- |
| root/handler/service/repository | cmd/demo→Config.Load→lab.Protocol→app.Build→api.NewRouter→真实net/http/Gin→Service.Create→memory.Commit；接口由业务使用者定义，root显式接线，闭包Config不走context.Value |
| 配置/版本/非敏感输出 | 默认<文件<已出现env；验证必需项、未知字段、单JSON、4096字节上限、预算、loopback与端口；启动Safe JSON与GET/version只暴露listen/version/budget/storage，无token |
| E01真实订单协议与副作用 | 1+8同key请求全部201/OK/committed=true/ID1，真实rows=1；qty冲突409，非法qty422，未知字段400，未认证401，均无新增；独立测试检查8个不同requestIDs、可信owner/qty、快照不别名、缺key400/KEY_REQUIRED与超限413/INPUT |
| E02 typed nil与过早调用 | 接口!=nil但动态nil指针，直接业务方法确实panic；同步边界recover有界观测；root具体指针拒绝。new资源直调用返回ErrNotReady且rows0，root同样拒绝；显式Start后业务写1行 |
| E03全局client范围 | 加锁的包全局client无data race仍串线：请求到AppA，A0行/B1行；显式构造修复后A1行/B0行；四个实际HTTP实例分两轮独占创建/收尾 |
| E04中途失败与原原因保留 | Listen factory取得真实TCP listener后返回错误，Build回滚，Accept实报net.ErrClosed、repo closed、App=nil；errors.Is同时识别构造与清理原因。独立测试另验非法Config零factory调用、repo资源+error、未Start关闭、构造panic回滚 |
| E05真实取消 | 等真实Commit.Entered后cancel客户端；Call实报context.Canceled；gate保持未释放，独立预算root cancel/Shutdown/handler join/Serve join后rows0、repo closed；observer取消不伪造完成 |
| 真实请求deadline | 80ms请求预算到达真实仓储gate；504/BUDGET/committed=false、rows0；Entered关闭证明走到等待点，未靠sleep调度 |
| E06两级Result与Done | Shutdown后Serve Result=nil可读但App tail仍关闭，App.Done=false、Close调用未返回、repo ready；解除App tail后关闭仓储。Close caller结果可读后自己的尾部清理仍有门，caller.Done=false；owner开两道门后用全新预算Join caller.Done，再读取最终原因。独立测试验证owner在caller tail关闭时不能完成，并保留原失败/Close失败/清理失败；原30msApp关闭超时/重试断言保留 |
| 强制Close仍须等handler | 真实HTTP handler停在显式屏障；两次20ms关闭预算都超时，第一次断TCP后handler仍active1、repo ready；解除屏障并实际退出后Close成功，active0、repo closed。独立handlerOwner账本避免用空连接表代替函数退出 |
| E07 init反例/修复 | 独立子进程init真实绑定端口，并看到before-main版本；main配置为main-configured，证明读取过早；坏例run owner显式Close且输出closed=true。正常入口先配置再Build，无badinit导入 |
| 完整教学 | 中文README给10～20分钟读跑路径、七段反例/修复/业务影响、真实符号地图、正常/失败/取消/清理时间线；源码解释caller、局部值/共享指针、等待、作用域、错误链及close owner；未留额外exercise |
| DI与生成来源 | 未安装Wire/Fx、无生成文件；README基于官方资料做手写/Wire/Fx阅读比较，指明现有项目须定位provider/生成脚本/锁定版本，不杜撰本课重生成命令 |

## 最终命令与结果

下表所有PowerShell工具调用的workdir均显式为`C:/Users/Lenovo/.codex/worktrees/go-course-m19/go-demo`，Go命令用`-C lessons/m19_composition_architecture`进入本独立module。没有使用默认共享cwd写文件。

| 实际命令/过程 | 结果/观察 | 证据 |
| --- | --- | --- |
| `gofmt -w`本课全部实际Go路径 | 退出0；末尾gofmt-l无输出 | 格式化源码 |
| `go -C lessons/m19_composition_architecture mod tidy` | 退出0，只写本module依赖锁；无浮动latest | go.mod/go.sum、environment.txt |
| `go ... run ./cmd/demo` | 最终退出0；E01–E07全部PASS，额外真实initbad二进制构建/执行/Wait完成 | [demo.txt](evidence/demo.txt) |
| `go ... test ./... -count=1 -v -timeout 120s` | 返工后最终退出0；14项顶层测试+7配置子测试+2 caller子测试全部PASS；各包实测耗时保留原日志，不作为语义契约 | [test.txt](evidence/test.txt) |
| `go ... vet ./...` | 最终退出0，无诊断 | [vet.txt](evidence/vet.txt)为空表示实际无输出 |
| `go ... test -race ./... -count=1 -v -timeout 120s` | 返工后进程级WinLibs临时环境，最终退出0，无race诊断；各包耗时保留日志 | [race.txt](evidence/race.txt) |
| `go ... env ...`、`go ... list -m -json github.com/gin-gonic/gin` | 实际工具链、模块、依赖最低版本/sum已读取 | [environment.txt](evidence/environment.txt) |
| `go doc net/http.Server.Shutdown`、`go doc context.CancelFunc`、Gin源码rg | 核对Shutdown/Close与cancel契约，定位POST/handle/ServeHTTP/pool/Next真实版本符号 | [source-symbols.txt](evidence/source-symbols.txt)、README |
| 进程临时`M19_REQUEST_TIMEOUT_MS=not-an-integer`后`go ... run ./cmd/demo` | **有意失败**，Go命令退出1；报字段integer错误，没有启动实验/输出配置，finally恢复env | [invalid-config.txt](evidence/invalid-config.txt) |
| Git status、gofmt-l、自有.local检查、README本地链接检查 | 只有本模块新文件；无其他目录修改；.local最终空，无故障二进制；本地文档链接已复核 | 模块当前文件范围 |

开发期间普通run/test/vet/race没有发生未解决失败。源码复核发现两处更严格的生命周期边界，已修复并新增实际断言：构造panic时不能只看err==nil跳过回滚；强制Server.Close后不能只靠再次Shutdown连接列表判定handler退出。因此增加transferred交接状态与handlerOwner独立账本后，实际重新跑最终run/test/vet/race。最后badinit关闭用errors.Join保留原原因，并精确对齐M18缺key/超限协议，新增直接HTTP断言后重新run/test/vet/race；配置故障是预期演示，不算默认入口失败。以本文件和最终evidence记录为准，不把旧PASS替代新路径验收。

## 主验收返工 R1：Close caller 也需要最终 Done

主执行者发现旧E06的真实所有权缺口：caller向缓冲closeResult发送a.Close结果后，才执行自己的defer cancel；owner只接收closeResult，所以旧报告“Close worker已join”超出了实际完成证据。返工前全部evidence与当时README/STATUS、experiments.go快照已经原样保存至[evidence/review-r1-before](evidence/review-r1-before)，未覆盖历史证据。

修复只增加`internal/lab/closecaller.go`并改E06、必要独立断言和文档；`internal/app`、真实HTTP handlerOwner、其余13项已验路径保持原实现。CloseCaller在最终defer先cancel，再做尾部清理、Join原因，最后关闭caller.Done。E06从创建起登记退出defer，所有正常/提前失败路径先解除两道自有门，再从Background建立新的4秒预算实际Join caller.Done；只有Done后读取finalErr。缓冲Result只保留为预览，不被叫作join。

`TestCloseCallerFinalDone/normal`与`early-observer-failure`确定性控制预览结果、tailEntered、Joining和两级Done。tail未释放时owner没有完成/最终结果；释放后先等caller.Done及owner.Done，最终失败组errors.Is同时保留原观察失败、Close失败与尾部清理失败。还实跑实际ResultBeforeDone，检查新增caller_result/caller_done/owner_joined状态。已复跑受影响demo、完整本模块test/race/vet；新增测试使当前14项顶层+9项子测试全部通过，旧13项证据保留供对照。没有把观察超时当完成，也没有将结果channel换名伪装最终等待。

## race 复跑的临时环境

从同一仓库根目录执行；脚本读已登记编译器，恢复调用前变量，不安装或全局更改。其他平台有合适本机编译器时直接执行go test -race即可。

```powershell
$tc = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' -Raw | ConvertFrom-Json
$savedCC = $env:CC
$savedCXX = $env:CXX
$savedCGO = $env:CGO_ENABLED
$savedPath = $env:PATH
try {
    $env:CC = $tc.cc
    $env:CXX = $tc.cxx
    $env:CGO_ENABLED = '1'
    $env:PATH = $tc.bin_dir + ';' + $savedPath
    go -C lessons/m19_composition_architecture test -race ./... -count=1 -v -timeout 120s
    if ($LASTEXITCODE -ne 0) { throw 'M19 race verification failed' }
} finally {
    $env:CC = $savedCC
    $env:CXX = $savedCXX
    $env:CGO_ENABLED = $savedCGO
    $env:PATH = $savedPath
}
```

## 资源最终状态、资料与未验范围

每个成功实验Cleanup在App.Close返回后直接验证activeHandlers=0、Repository.state=closed，失败也errors.Join原原因与关闭原因。Close停止新的业务handler进入、cancel root、Shutdown/必要时强制Close本App连接、等独立handler账本、等Serve Done、最后关闭仓储。未启动App不创建Serve goroutine，由Close直接回收listener/仓储；构造错误/异常展开由Build在交接前回滚。

Call每个响应体都有defer关闭且保存关闭原因，Client.Transport归实验owner，关闭闲连接池；并发HTTP worker用WaitGroup，取消Call从创建起登记停止/屏障解除/独立预算join。Close caller另有最终Done，cancel/尾部清理在Done广播前完成，正常与早失败owner开门后真正Join再读finalErr。E07的child由CombinedOutput等待真实退出，run defer关闭真实listener，父进程确认绝对临时目录属于本module.local后删除。最终.local为空、无生成exe、无持久实验服务/故障代理；没有杀其他进程、重启外部实例或动共享数据。

实际访问Go modules、Effective Go、Go1.27说明、Go spec初始化、net/http、context，以及Wire/Fx官方资料；Gin tag tree网页首次取回Internal Error后使用本机已下载的v1.12.0源码做确切定位，并用官方仓库/版本blob链接作查证入口。Wire官方README已确认项目不再维护，README只作阅读比较，未安装。资料链接在README相应主张旁。

未验：生产身份/鉴权、反向代理公网部署、HTTP2/3、真实SQL/MQ/事务、重启持久性、跨进程/跨实例幂等、驱动完全不可取消故障、业务提交后进程崩溃/恢复、性能与生产容量。ctx检查与cancel/外部副作用不原子，本课只严格证明已进入写前屏障且观察取消的窗口内rows0。父进程race不覆盖普通build的initbad子进程；没有将编译、HTTP200或当前内存账本冒充实库证明。

返工交付目录重新冻结，主执行者待验重点：R1新CloseCaller最终defer/Done/Join、两道门的全部退出路径、确定性tail阻塞与三项错误保留；其余真实HTTP handlerOwner与调用链保持原实现。本模块等待主验收，不主动推进下一模块。
