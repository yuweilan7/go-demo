# M15 实际交付与验证记录

状态：**IMPLEMENTED / SELF VERIFIED，全部本课要求已完成，等待主 agent 独立验收**。执行日期：2026-10-12（Asia/Shanghai）。本目录从本记录写入后冻结；没有 git add/commit/push，没有启动下级 agent。课程最终验收和整层提交由主 agent 记录。

唯一交付目录：`C:/Users/Lenovo/.codex/worktrees/go-course-m15/go-demo/lessons/m15_structured_concurrency/`。起点：`90f7381b495c08f4995dd13400fbc34fca69e8c0`。没有改变根 go.mod/go.sum/go.work、前置模块或原 examples/files 的内容；最终 `git diff --name-only` 为空，`git status --short` 只有 `?? lessons/m15_structured_concurrency/`。

## 当前环境与输入

| 项目 | 实际读取/执行事实 |
| --- | --- |
| Go | `go version go1.27.0 windows/amd64` |
| 本课go.mod | `module go-demo/lessons/m15_structured_concurrency`，`go 1.27.0`，无toolchain指令 |
| go env | GOVERSION=go1.27.0，GOOS=windows，GOARCH=amd64，CGO_ENABLED=1，GOTOOLCHAIN=auto，GOROOT=`D:/Go`，GOWORK空，本课GOMOD指向上述目录 |
| 第三方依赖 | `golang.org/x/sync v0.24.0`，真实下载/锁定go.sum；依赖自身go指令=1.26.0；`go list -m all`仅本课+x/sync |
| 默认进程CC/CXX | gcc/g++；没有使用默认32位工具链做race后声称已测 |
| 实际race CC | `D:/IdeaProject/go-demo/.local/toolchains/winlibs-16.2.0posix-14.0.0-ucrt-r2/mingw64/bin/gcc.exe`，CXX同目录g++.exe；实际target=`x86_64-w64-mingw32` |
| race配置来源 | 只读 `D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json`；仅命令PowerShell进程设置CC/CXX/CGO_ENABLED/PATH，未下载/安装、未改全局环境 |
| 适用规范 | 当前空祖先 `C:/Users/Lenovo/.codex/AGENTS.md`、根README/go.mod、完整M15 prompt、ORCHESTRATION、TEACHING_STYLE、modules.json；使用go-learning-by-running技能 |
| 前置证据 | 已读取M11/M12/M13/M14 STATUS与实际核心源码：RequestTask/Lifecycle、Pipeline、context生命周期/SQL adapter、Inventory/锁内I/O；复用前置验收，没有重跑整套前置 |
| 启发材料 | 主目录只读 `files/语法进阶/协程池`；原 `examples/advanced-syntax/goroutine-pool/pool.go/pool_test.go` |
| 原Pool输入 | 每次实际读取原pool.go，以逐字节副本+harness临时编译；SHA256=`059c9a280d9f012566e44c4831f93490bef348f80e156871bac26adf058ec0d8`，原文件未改 |
| Go/依赖查证 | 锁定v0.24.0官方errgroup/semaphore文档与实际缓存源码；本机Go1.27.0的WaitGroup、context、HTTP Serve/Shutdown/conn.serve、database/sql源码；官方内存模型/SQL/HTTP文档 |
| 数据和外部范围 | A=12500分/B=1800分/C缺价/D=4200分；动态loopback HTTP/1.1真实请求/响应与TCP连接；没有MySQL、Redis、MQ、外网或学习服务器修改 |

## 实际命令与结果

工作目录A：`C:/Users/Lenovo/.codex/worktrees/go-course-m15/go-demo/`。
工作目录B：A下的 `lessons/m15_structured_concurrency/`。
表中普通命令均为最终代码执行；原始stdout为Tee-Object保存，Go最终退出码显式传回shell。

| 工作目录 | 实际命令 | 退出码/结果 |
| --- | --- | --- |
| B | `go mod tidy` | 0；下载并锁定x/sync v0.24.0，仅本课go.mod/go.sum变化 |
| A | `Get-ChildItem -LiteralPath 'lessons/m15_structured_concurrency' -Recurse -Filter '*.go' \| ForEach-Object { gofmt -w $_.FullName }`；`gofmt -w lessons/m15_structured_concurrency/legacy_harness.txt` | 0；全部手写Go源码及嵌入的Go harness格式化 |
| A | `go -C lessons/m15_structured_concurrency run ./cmd/demo` | 0，E01–E05全路径完成，最后M15 PASS；[完整日志](evidence/demo.txt) |
| A | `go -C lessons/m15_structured_concurrency test -v -count=1 -timeout=60s ./...` | 0；13顶层、14子测试全部通过，包实际6.307s；[完整日志](evidence/test.txt) |
| A | `go -C lessons/m15_structured_concurrency vet ./...` | 0，无诊断 |
| B | `go test -v -run 'TestRealHTTPResponseBoundary\|TestHTTPShutdownBeforeHandler\|TestRealHTTPBatch\|TestRealHTTPCancel' -count=1 -timeout=30s ./...` | 0；主预审两项HTTP修复的定向验证，包2.811s |
| A | 进程级portable CC/CXX/CGO/PATH后 `go -C lessons/m15_structured_concurrency test -race -v -count=1 -timeout=60s ./...` | 0；相同13顶层/14子测试通过，包19.321s，无DATA RACE；原Pool child也真正 `go build -race`；[完整日志](evidence/test-race.txt) |
| A | 同样进程级环境后 `go -C lessons/m15_structured_concurrency run -race ./cmd/demo` | 0；E01–E05全部完成，无DATA RACE，最后M15 PASS；[完整日志](evidence/demo-race.txt) |
| A/B | `go version`、`go -C lessons/m15_structured_concurrency env ...`、`go -C lessons/m15_structured_concurrency list -m all`、portable `gcc -dumpmachine` | 0；实际值见环境表 |
| A | `git diff --check`、`git diff --name-only`、`git status --short`、本课`.local`只读枚举 | 0；无空白诊断、无已跟踪文件内容变化，仅本课新目录；最终`.local`为空，所有legacy临时目录已移除 |

最终普通test、race test、race demo覆盖主agent预审后的完整代码，不把预审前的通过记录当作最终版本证据。正常run/test/vet并非只验证HTTP200；测试独立核对业务金额、SKU对应、错误链、终态数量、真实连接关闭与资源计数。

## 五项范围与真实观察

| 要求/实验 | 实际事实与验收点 |
| --- | --- |
| E01 已有Pool持RLock阻塞关闭 | 原Pool(1,1)唯一worker在门内、B填满队列、第三Task原atomic submitted=true；Close writer pending导致TryRLock=false，Submit/Close均未完成。放gate后队列排空、RUnlock、写锁关闭、全部worker/owner真实Wait |
| E01 递归提交 | 原Pool(1,0)，outer占唯一worker并Submit(inner)，接收者不能运行；原Close等写锁。fixture额外接收inner仅作救援，随后outer/worker/closer都加入；不称原Pool有取消能力 |
| E02 顺序/有界fan-out/部分业务失败 | 真实HTTP完成固定B/C/D/A，返回顺序A/B/C/D；A12500/B1800/D4200/C缺价，成功合计18500；HTTP active/max/finished=0/2/4，所有listener/连接/handler清理 |
| E03 errgroup | 真实v0.24.0 WithContext/Go/Wait/SetLimit；取消已被首项观察且其cleanup门未放时，第二Go仍不能接纳；首错ErrBackend已触发兄弟取消但Wait仍未完成，放清理门后Wait保留原错、2工作真实退出 |
| E04 semaphore | 真实Weighted Acquire在g.Go之前，Aweight2+Bweight1占3；Dweight2停在单一派发者中，未启动Fetch。取消后D ErrNotStarted，实际maxUnits=3、maxActive=2、started=finished=2、units=active=0 |
| E05 bounded queue关闭/背压 | 修复Pool短锁内只做立即可完成的send；1worker/1queue饱和时D等待，Close唤醒且拒绝D ErrClosed/nil Ticket，不被持RLock发送阻塞；A/B按已转移所有权成功drain |
| E05 提交取消 | 饱和D的提交ctx取消返回Canceled/nil Ticket，任务未转移；A/B仍成功，开始/完成均2，D没有执行 |
| E05 panic/部分失败汇总 | 当前worker内recover A panic→PanicError，C业务缺价，D成功4200；三项均有终态且worker继续执行；errors.Join同时保留panic和缺价 |
| E05 首错 | 两worker执行A/B、D排队；A ErrBackend先保存并cancel，B观察后等清理门，D不调用Fetch但发布首错原因；未放门joined=false，放门后started=finished=2、三Ticket都有终态 |
| E05 消费者退出 | owner不读Ticket先Abort/Wait；运行A与排队B都Canceled，始终只一项业务启动，active=0；发布槽无无人接收send，不因消费者放弃而泄漏worker |
| E05 递归修复 | outer.TrySubmit(D)在满队列立即ErrQueueFull，outer先返回，外部owner随后Submit(D)成功；A/B/D价格12500/1800/4200，started=finished=3；worker不Wait自己 |
| 128 SKU独立不变量 | limit1/3/8各次128项稳定ID，18项业务缺价；价格`825600-119700=705900`分，maxActive<=limit、active=0、started=finished=128 |
| 主错/清理错 | 独立清理预算解除gate后真正Wait，保留ErrBackend；未关闭信号的纯错误链测试保留ErrBackend+DeadlineExceeded，没有为制造超时启动遗留goroutine |
| 库边界 | 成功errgroup.Wait仍取消派生ctx；SetLimit(0)+TryGo拒绝；非法并发数、重复/空ID、weight>capacity和本修复queue0都在工作开始前拒绝 |

本修复Pool明确只支持正容量队列1..16，README解释锁内非阻塞接纳为什么不能直接设queue0。原反例仍实测它自身支持的queue0；未悄悄删除该反例。Queue大小、活跃工作数、加权资源数与数据库连接池等待分开说明；没有声称semaphore是每秒限流器。

## 主 agent 预审修复与独立证据

预审指出价格JSON的 `LimitReader(4096)` 会将人为截断当EOF，可能接受合法对象后的超限空白/垃圾后缀。现已先实际读取最多4097字节、>4096拒绝ErrResponseTooLarge，再用完整bytes.Reader严格解码一个对象。真实HTTP测试覆盖合法、恰4096合法、4097空白、对象+超限垃圾、第二对象，普通/race均通过。Body Close错误仍与主错合并。

预审指出HTTP Close后立即handlers.Wait可能看到0，却仍有已接纳但未进handler的连接随后Add。现已在ConnState StateNew登记每一连接、StateClosed完成；本机Go1.27源码Serve同步StateNew在Serve可返回前，HTTP/1.1 conn.serve退出defer的StateClosed在handler返回后。Close有界Shutdown、失败则server.Close，然后独立预算真实join Serve停止新增连接，再加入所有已接纳连接，最后handler Wait/finisher Wait。主Shutdown错与后续清理错都保留。

`TestHTTPShutdownBeforeHandler`实际只建立TCP、不发送请求；观察accepted连接1、handler started0，graceful两秒预算真实DeadlineExceeded；fallback Close后accepted=closed=1/active0，handler仍0，客户端读取EOF。普通/race均通过，不能用“handler数0”代替连接回收。没有WebSocket/hijack，本实现明确不承担外借连接生命周期。

## 历史失败与修复过程

首次普通独立测试的最终常量误写705000，而逐项独立期望与实际结果均为705900；三个limit子测试真实失败。已把常量修正为705900，业务代码和价格输出未据此改动；最终普通/race所有断言通过。

首次race已使用正确portable amd64编译器，但原Pool child构建+运行超过测试原10秒总预算：实际TestActualLegacyPool=12.91s、child `exit status 1`，该轮整体退出1，未出现race诊断。[原始失败](evidence/race-initial-parent-budget.txt)。只把此构建测试总预算改为40秒后，child运行/退出仍触发旧6秒监督：[该轮日志](evidence/race-child-exit-budget.txt)显示两反例及救援日志后child非零退出。

随后加入进程时长与ctx诊断，真实 `race=true build-and-run=13.58s child=6.352s child-ctx=context deadline exceeded`，明确是监督预算耗尽，不能当成Pool新故障或race通过。[原始诊断](evidence/race-legacy-diagnosis.txt)。将进程监督改为20秒，harness内部4秒观察、独立2秒清理不变；child只设进程级GORACE atexit_sleep_ms=0，仍启用检测且保留错误退出码66。[定向补验](evidence/race-legacy-final.txt)真实child=6.432s、ctx=nil、两反例/救援完成，exit0。

最终完整race test的原Pool child真实13.197s构建+运行、child5.848s、ctx=nil；最终完整race demo的child217ms、ctx=nil，均无DATA RACE。时长差异仅是本次环境观察，不设为同步断言。

另有一次最终命令cwd切换失误：格式化递归枚举到了本worktree全部Go文件，裸legacy_harness.txt在根cwd报不存在。我立刻核对git diff --name-only为空、status仅本课新目录，原examples和前置模块没有内容变化；随后采用显式本课LiteralPath格式化（含harness）并退出0。已向主agent说明，未回滚或清理任何他人文件。

## 最终资源与未验范围

本课请求工作、errgroup工作、加权工作、固定worker、提交者和join发布者都实际Wait。每条提前失败路径先cancel/Abort、放所有gate，再使用独立两秒清理预算；结果通知与取消通知均不代替join。已转移给Pool的Ticket恰有终态，排队取消项不遗失，所有Tracker最终active/units=0。

原Pool child由exec.CommandContext直接执行，Run已Wait回收；临时stage路径由本课.local内MkDirTemp创建，RemoveAll的错误会合并成命令失败。最后只读枚举`.local`为空，没有临时二进制、故障代理或遗留子进程。所有工具返回的exec session都已结束。HTTP仅动态loopback端口，Body/Transport/listener、已接纳连接、handler和Serve均收尾；没有全局PATH/go env写入。

实证为Go1.27.0 Windows下本进程合成状态、真实loopback HTTP/TCP和锁定x/sync，不是MySQL、事务取消、跨实例幂等、MQ、生产性能或所有调度证明。SQLPriceFetcher仅编译/阅读，没有真实driver、DB、表、Rows取消或实库连接池等待验收。README明确说明批次预算/队列/permit/DB等待和后续M20/M21实库责任，没有用内存Fetch或HTTP冒充实库。

交付包含中文README、全部手写源码注释、五组默认可运行实验、13项顶层/14项子测试、独立go.mod/go.sum、原始无秘密证据和本记录。没有额外exercise，没有本课关键未通过项。后续仅等待主agent复审及本层统一交付。
