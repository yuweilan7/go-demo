# M16 实际交付与验证记录

状态：**READY_FOR_REVIEW / IMPLEMENTED / SELF VERIFIED**。执行日期：2026-10-12（Asia/Shanghai）。全部本课要求已实现，交主 agent 独立验收；自报结果不等于主验收。本目录从本记录完成后冻结，不再主动追加实现。没有 git add/commit/push、没有子 agent、没有更换分支或修改共享调度状态。

唯一交付目录：`C:/Users/Lenovo/.codex/worktrees/go-course-m16/go-demo/lessons/m16_timers_shutdown/`。工作树基线 `5929af4af6d8a599362bdbb20e2d2d3cfa2cf6ba`。仅该新目录有改动；根 go.mod/go.sum、M15、原 examples/files、共享 go.work/索引未改。最终 `git diff --name-only` 为空，`git status --short` 只有 `?? lessons/m16_timers_shutdown/`。

## 实际环境、依赖和已读取证据

| 项目 | 本轮实际事实 |
| --- | --- |
| Go | `go version go1.27.0 windows/amd64` |
| 本课 module | `go-demo/lessons/m16_timers_shutdown`，`go 1.27.0`，无 toolchain 指令 |
| go env | GOVERSION=go1.27.0，GOROOT=`D:/Go`，GOOS=windows，GOARCH=amd64，CGO_ENABLED=1，GOTOOLCHAIN=auto；GOWORK空，GOMOD准确指向本课go.mod |
| 正常工具链 | CC=gcc，CXX=g++；未用默认工具链宣称race成功 |
| M15 硬前置 | 已读当前worktree的M15 STATUS/README/go.mod和实际pool.go/lifecycle.go/batch.go。M15前层已验收，公开Ticket.Result/Done、Pool.TrySubmit/Close/Abort/Wait/Joined；本课用本地replace直接依赖，不复制worker/队列、不跨internal、不重跑前课整套验证 |
| 依赖图 | `go list -m all`：本课；M15 v0.0.0=>`../m15_structured_concurrency`；`golang.org/x/sync v0.24.0`间接依赖。go.sum真实锁定该版 |
| 适用规范 | 空祖先`C:/Users/Lenovo/.codex/AGENTS.md`；根README/go.mod；完整M16 prompt、TEACHING_STYLE、ORCHESTRATION、modules.json；go-learning-by-running技能 |
| 启发材料 | 原examples/advanced-syntax/timers/main.go；主目录只读files/语法进阶/定时器。纠正旧结构、AfterFunc当前goroutine误解、Stop返回false/旧drain绝对化和Watch缺join |
| 官方与本机证据 | 锁定Go1.27 time/os/synctest、signal.NotifyContext、HTTP Shutdown；官方Go1.27/GODEBUG；本机time/sleep.go、tick.go、internal/godebugs/table.go、HTTP Server.Serve的StateNew before-return、signal.NotifyContext、os.Exit的defer契约 |
| race来源 | 只读`D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json`；portable WinLibs gcc16.2.0 / mingw-w64 14.0.0，实际target=`x86_64-w64-mingw32` |
| race进程变量 | CC=`D:/IdeaProject/go-demo/.local/toolchains/winlibs-16.2.0posix-14.0.0-ucrt-r2/mingw64/bin/gcc.exe`；CXX同目录g++.exe；CGO_ENABLED=1；PATH临时前置bin_dir。在每条PowerShell命令try/finally恢复旧值，无安装/全局PATH/go env -w修改 |
| 网络/数据范围 | 动态127.0.0.1:0真实TCP、HTTP/1.1 POST与JSON；A=12500分/B=1800分；每个fixture独立端口/内存账本。未触碰学习服务器或DeepHelp；没有MySQL/Redis/MQ |

## 最终实际命令与结果

工作目录 A：`C:/Users/Lenovo/.codex/worktrees/go-course-m16/go-demo/`。

工作目录 B：A下的 `lessons/m16_timers_shutdown/`。

stdout日志由Tee-Object保存，Go退出码显式传回shell。最终demo/test/race日志包含E07和全部最终断言；不把早期只测部分内容的通过当最终版本证据。

| 目录 | 实际命令/动作 | 结果与证据 |
| --- | --- | --- |
| A | `go version`、基线`git rev-parse HEAD`、toolchain.json只读 | 0；Go1.27.0和上述基线/工具链 |
| B | `go mod tidy` | 0；M15本地replace与x/sync v0.24.0间接依赖go.sum，未改M15 |
| B/A | `Get-ChildItem -LiteralPath ... -Recurse -Filter '*.go' \| ForEach-Object { gofmt -w $_.FullName }` | 0；所有手写Go源码、隔离错误入口都已格式化 |
| A | `go -C lessons/m16_timers_shutdown run ./cmd/demo` | 0；E01–E07完成，最后SELF VERIFIED；[完整日志](evidence/demo.txt) |
| B | `go test -v -count=1 -timeout=60s ./...` | 0；15个顶层测试、12个子测试通过，包7.786s；[完整日志](evidence/test.txt) |
| A | `go -C lessons/m16_timers_shutdown vet ./...` | 0，无诊断 |
| A | 进程级portable环境后 `go -C lessons/m16_timers_shutdown test -race -v -count=1 -timeout=60s ./...` | 0；同样15个顶层/12个子测试通过，包8.883s，无DATA RACE；[完整日志](evidence/test-race.txt) |
| A | 相同环境后 `go -C lessons/m16_timers_shutdown run -race ./cmd/demo` | 0；E01–E07完成，无DATA RACE；[完整日志](evidence/demo-race.txt) |
| B | `go -C labs/removed_switch run .` | **1，预期编译失败**：removed GODEBUG asynctimerchan=1；隔离反例，不是正常课程失败；[诊断](evidence/removed-switch.txt) |
| B | `go env GOVERSION GOROOT GOMOD GOWORK GOOS GOARCH CGO_ENABLED GOTOOLCHAIN CC CXX`、`go list -m all` | 0；真实输出见环境表 |
| A | `git diff --check`、`git diff --name-only`、`git status --short`、本课文件枚举 | 0；没有已有跟踪文件变化，仅本课新目录；不存在本课临时二进制/.local进程文件 |

race动态通过只证明这些实际路径未检出数据竞争，不证明所有输入、调度或业务原子性。所有普通测试和正常入口都有上限；没有用Sleep猜调度，也没有靠kill回收故障worker。

## 五项要求与业务/资源事实

| 要求 | 完整交付和实际观察 |
| --- | --- |
| 1 当前Timer/Ticker/AfterFunc | E01:Stop=true/false，Reset(stopped/active)=false/true，timer/ticker C.cap=0/0，AfterFunc.C=nil。started门证明Stop=false时第一回调仍在途；Reset=false再启动第二回调，两轮都实际Wait。Go1.27旧开关已移除，显式旧值真实失败 |
| 2 触发与工作策略 | E03:overlap=2/2/0、serial=2/2/0、skip=2/1/1。八触发过载独立测试started2/skipped6；真实ticker100ms延迟接收跨过tick，synctest断言100ms时间戳跨度。真实Service ticker至少执行一轮，再stop/join；定时panic恢复为error，未默默消失 |
| 3 复用M15停机协议 | E04:A真实handler在途/B占队列/C QueueFull；入口停止后D Closed；先producer join、Pool.Close/drain/Wait、Ticket终态，最后client.Close。金额14300分、账本A12500/B1800、2个Ticket正确对应ID，HTTP accepted/closed=2/2、activeHandlers=0 |
| 4 生命周期与独立预算 | NotifyContext.stop可控模拟通知，工作budget与生命周期ctx分开，正常drain不取消A/B。超时/已取消预算先Abort/取消/解除gate，再Background新2秒budget join；Done只有全部资源释放才关闭，finished仅表示coordinator返回。父取消/Ticket完成/Pool.Joined已由E06分开验证 |
| 5 全部边界/错误修复 | 空闲0结果、执行中2结果、队列满拒绝、回调已启动、重复/16caller关闭只一次、25ms超预算unknown[A B]/active1/queued1。Stop-only坏worker真实未退出，再fixture Reset救援join；修复ctx select+Wait。client Close失败保留主/清理错误，Done不伪称完成；kill/os.Exit不保证defer解释有官方证据 |

E05 timeout/canceled-budget辅助内存Fetch最终两个终态且producer/worker/client全部收尾，ShutdownReport仍保留正常drain失败；panic两个PanicError在FinalRows中可见，但服务资源关闭成功不伪装业务成功。

**E07是真实HTTP超预算证据**。最终普通demo和race demo均观察到：到期时unknown=[A B]，客户端成功Ticket合计0分，A返回携带drain DeadlineExceeded的Post错误，B未开始取消；随后真实backend账本实际有A=12500分，B不存在。各自HTTP accepted/closed=1/1，activeHandlers=0，服务workers/producer/client完成收尾。这直接说明取消请求不撤销服务端已发生提交。独立断言允许A账本为空或12500，因为取消传播和提交竞态合法；不把这次A已提交固定成所有运行必然发生。内存账本不承诺崩溃持久性。

## 真实失败与修复

首次 `go test -v -count=1 -timeout=60s ./...` 退出1，失败行 `TestShutdownPaths/timeout: 取消原因丢失: {ID:A Price:0 Err:context canceled}`，包6.153s。当时断言错误地要求所有Row都保留DeadlineExceeded。实际运行中A的业务函数返回ctx.Err()==Canceled，未开始B由M15.worker返回context.Cause()==drain DeadlineExceeded；停机report的主错一直保留。修正独立断言，保留两者含义，没有改业务输出/吞错。首次工具返回片段保存在 [initial-failure.txt](evidence/initial-failure.txt)，明确是失败节选；最终全量普通/race日志均通过。

隔离 removed_switch 的退出1是必须保留的版本纠错，不是未修复的正常入口错误。没有未通过的关键要求。

## 收尾与未验范围

所有本课创建的worker、producer、callback、策略工作、shutdown caller/coordinator、pool joiner、HTTP Serve/已登记连接/handler及辅助通知发布者都通过所属Wait加入；Timer/Context均Stop/cancel，NotifyContext已stop注销，Body读完关闭、Transport在worker后关闭。故障worker靠Reset救援；所有fixture门在提前失败路径先释放。独立清理超时测试只等待未关闭信号，不启动会被遗弃goroutine；client Close报错测试是无实际网络资源的故障函数，actual workers/producer已join，没有遗留socket。

没有启动持久外部服务、新隧道或修改全局环境。未验证真实系统信号投递、外部kill/进程崩溃、HTTP/2/WebSocket/hijack、数据库订单持久化、跨进程恢复、OS实时时钟精度或高频timer性能；README按这些边界解释，不以mock、HTTP200或静态配置替代实证。

交付：独立go.mod/go.sum、完整中文README、详细中文注释手写源码、默认E01–E07实验、15顶层/12子测试、隔离版本失败入口与真实日志。本目录冻结，后续只接受主agent明确返工要求。
