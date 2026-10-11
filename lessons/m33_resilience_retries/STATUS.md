# M33 执行者交付状态与实际验证

2026-10-12，Asia/Shanghai。**完整课程交付及主审要求的写响应分类返工已完成，最终版本执行者验证通过；模块目录重新冻结，等待主 agent 独立验收。** 本文件记录本模块实际工作，不替代主 agent 的 ACCEPTED/整层提交；没有继续其他模块、提交或推送。

## 范围、前置、版本

- 唯一写入目录：`C:/Users/Lenovo/.codex/worktrees/go-course-m33/go-demo/lessons/m33_resilience_retries`。开始核验HEAD：`90f7381b495c08f4995dd13400fbc34fca69e8c0`。
- 独立module：`go-demo/lessons/m33_resilience_retries`，`go 1.27.0`；无toolchain、replace、跨模块import、共享go.work修改。所有源码为本模块的手写中文教学实现。
- 实测 `go version go1.27.0 windows/amd64`；GOROOT=`D:/Go`、GOTOOLCHAIN=auto、GOPROXY=`https://goproxy.cn,direct`、CGO_ENABLED=1、默认CC=gcc。运行时依赖锁定 `github.com/sony/gobreaker/v2 v2.4.0`，该库GoVersion=1.22.0；`go mod verify`实际通过。
- gobreaker sum：`h1:g2KJRW1Ubty3+ZOcSEUN7K+REQJdN6yo6XvaML+jptg=`。go.sum中的testify/go-spew/difflib/yaml用于库测试依赖图校验，本课未直接import它们。
- Kitex实际最新元数据为 `v0.16.4`（GoVersion=1.20）；已下载并阅读retry固定版本源码，仅作迁移阅读，未加入运行module、未声称RPC实跑。
- 完整读取M33 prompt、TEACHING_STYLE、ORCHESTRATION、modules.json、根README/go.mod、INFRASTRUCTURE；只读主目录 `files/开发框架`。适用 `C:/Users/Lenovo/.codex/AGENTS.md` 为空；worktree/主仓库未找到额外AGENTS.md。
- 硬前置M13/M17在当前已提交HEAD中存在。已读其实际STATUS/README及M13 budgets/http/suite、M17 client/owned/fixture核心源码；采用README约定复用有效预算、未知写、body与取消join证据，未重跑它们整课、未导入其internal包或复制整项目。
- 真实服务：每组独立loopback动态listener、HTTP/1.1、当前进程合成账本；无固定端口、外部数据库/Redis、生产服务或共享业务数据。只读课程infra-services.json时各服务仍PENDING，本课无需这些中间件，未安装/开隧道。

## 实际命令和结果

以下命令的工作目录均为唯一模块目录。根目录复跑时可用README中的 `go -C lessons/m33_resilience_retries ...`。

| 实际命令 | 真实结果 | 证据/范围 |
| --- | --- | --- |
| `go list -m -versions github.com/sony/gobreaker/v2`，`go list -m -json ...@latest` | 当前版本列表到v2.4.0，实际锁定v2.4.0 | 环境与依赖核验 |
| `go mod download -json github.com/sony/gobreaker/v2@v2.4.0`，Kitex v0.16.4对应命令 | 实际下载/校验元数据成功 | 固定版本源码核对，Kitex只读 |
| `go mod tidy`，`go mod verify` | 成功，verify输出all modules verified | 独立go.mod/go.sum，未修改根模块 |
| `gofmt -w cmd/demo/main.go internal/lab`，随后 `gofmt -l cmd internal` | 成功，最终-l无输出 | 所有手写Go格式化 |
| `go run ./cmd/demo` | 最新exit=0；core/amplify/breaker/isolation/limits五组PASS；末尾M33 PASS | [demo.txt](evidence/demo.txt)，最新总日志约1.753s（观察值），含新增不可信201/空body写 |
| `go -C . run ./cmd/demo -scenario core` | 最新exit=0；真实GET/未知订单/四种不可信201/空body写/取消全部PASS | [core.txt](evidence/core.txt)，最新约1.507s，10～20分钟核心阅读入口 |
| `go test -v -count=1 -timeout 60s ./...` | 最新exit=0；13项顶层测试及新增6个子例通过，lab5.293s | [test.txt](evidence/test.txt)，含独立副作用、协议错误、早退和owner真正等待清理gate |
| `go vet ./...` | 最新exit=0，无诊断 | [vet.txt](evidence/vet.txt) |
| 进程级WinLibs环境中 `go test -race -v -count=1 -timeout 60s ./...` | 最新exit=0；13项顶层及6个新增子例通过，lab6.566s，无race报告 | [race.txt](evidence/race.txt)，实际Windows/amd64并发验证 |
| `go version`、`go env ...`、`go list -m -json all`、compiler `-dumpmachine/--version` | 实测Go1.27.0、锁定依赖、x86_64-w64-mingw32/GCC16.2.0 | [environment.txt](evidence/environment.txt) |
| `git status --short --untracked-files=all` | 仅本模块新增文件；无根/其他模块文件变更 | 未执行git add/commit/push |

初轮demo/test/race已经通过，随后补充共享Reader真实空body反例、Retry-After超总预算拒绝、join owner真实被cleanup gate挡住的证明并重新完成全套验证。初轮成功日志保存在 `initial-demo.txt/initial-test.txt/initial-race.txt`。写分类返工前的10项测试及正常入口日志另存 `pre-write-fix-demo.txt/pre-write-fix-test.txt/pre-write-fix-race.txt`；它们没有覆盖主审指出的提交后不可信201响应，不能代替最新13项测试的证据。

返工中先实际执行受影响测试：`go test -v -count=1 -timeout 60s ./internal/lab -run 'Test(UntrustedSuccessfulWriteResponses|EmptyBodyWriteCannotBypassKey|KnownWriteRejectionsStayDefinite|UnknownWriteReconciliation|ConcurrentIdempotency|AlreadyCanceledAndBusinessRejection)'`，6项顶层及6个新增子例通过，lab0.924s，见 [write-fix-test.txt](evidence/write-fix-test.txt)。随后整理UNKNOWN先返回的循环分支并gofmt，以上表的完整demo/core/13项test/vet/13项race/mod verify验证最终源码。

## 主审发现与本次返工

主agent代码审读发现两个先前未被独立实验覆盖的语义缺口：已派发订单POST收到超4096字节、坏JSON或无有效回执的2xx时，原实现返回普通协议error；而真实server可能先commit再发送这样的响应，不能据此判为确定失败。第二处是安全key guard按 `len(Body)>0` 检查，空body有副作用POST会绕过该条件。旧的成功测试没有证明这两个边界正确；没有声称旧日志发现或复现过失败。

本次新增 `responseFailure/definitiveOrderRejection`：所有本课POST明确为订单写，派发后不可信响应包装UNKNOWN且保留原因；只在合法JSON的状态/code符合提交前拒绝契约时返回确定拒绝。可信成功回执需code=OK、ID>0、SKU/qty与输入一致。Retry遇UNKNOWN立即交回caller查询原key；稳定key、可重放字节和次数余额不会触发盲重放。缺安全key的判断与body长度无关，缺协议且配置更多attempt时保留UNKNOWN并附ErrUnsafeRetry。

新增真实handler在锁内commit后再发送201坏JSON、超过读取上限的合法JSON、没有receipt的OK JSON、qty错误的回执；新的独立测试逐项断言真实账本1行、写handler1次、attempt1、对应原始原因可errors.Is/As，再经原key GET确认ID/参数，账本和写调用数不变。另用空body从server默认参数产生真实订单，再断TCP，分别验证无key停止且ErrUnsafeRetry、有key原key查询确认。有效BAD_ORDER/OUT_OF_STOCK422的独立测试仍要求确定拒绝、一次调用、rows0。README与中文源码注释已解释协议及日志因果，未增加查询POST、练习或共享配置。

## 实际业务、协议和清理事实

| 范围 | 已观测/断言的真实结果 |
| --- | --- |
| 适用GET重试 | /flaky真实503、503、200，handler3次；/always503最大次数3，错误保留 |
| 预算没有重置 | 每个普通attempt deadline与总deadline相等；150ms总预算max10实际2次，退避中DeadlineExceeded；慢HTTP单次70ms、总190ms，child deadline均不晚于总deadline |
| equal jitter | 实际前两wait约9.280192ms、13.798966ms；base10ms/cap40ms，每轮新RNG固定教材seed；不宣称生产同步seed可用 |
| body危险/修复 | server原文危险两次为`stock-query`与空串；正常重试两次收到完整相同JSON；不可重放标记阻止第二派发，只有一次新增handler |
| Retry-After | 真503 header=1，实际第二attempt至少1s后；建议1s/总预算80ms时handler1次并deadline；HTTP-date/非法/负数由辅助解析断言 |
| 明确业务拒绝 | OUT_OF_STOCK422只请求1次；同幂等key换参数409只1次，账本不变；业务拒绝不污染breaker故障计数 |
| cancel | 真503后的backoff通知后取消，attempts=1、handler=1；先读Result再join Done，owned=0。已取消入口attempts=0/handler=0 |
| 写UNKNOWN/危险重复 | /lost-order先真实append再断TCP，client EOF/UNKNOWN、rows1；自动策略无key停一次；显式危险盲重试真实rows2 |
| 查询+幂等 | 带稳定key丢响应后rows3；真GET查到ID3/SKU/qty；同key同payload返回Duplicate/ID3、rows仍3；冲突409无新行 |
| 提交后201坏JSON | /corrupt-order真实commit后截断JSON；UNKNOWN且errors.As保留json.SyntaxError，errors.Is保留ErrInvalidOrderResponse；原keyGET查实订单，无额外POST |
| 提交后201超限 | /large-order真实commit后合法JSON加6000字节padding；UNKNOWN+ErrResponseTooLarge；原keyGET确认，未把超过4096读取上限当确定业务失败 |
| 提交后201缺/错回执 | /no-receipt-order只有OK，/mismatched-order的qty被改坏；均UNKNOWN+ErrInvalidReceipt；GET原key拿到账本中实际参数 |
| 不可信写没有盲重放 | 上述四种各自新fixture独立rows1/POST1/attempt1，MaxAttempts4且key/Idempotent/Replayable均满足；GET后rows及POST计数不变，/orders0；demo独立组顺序rows1～4 |
| 空body也有写副作用 | /empty-lost-order从默认参数创建server-default-m33/qty1再断TCP；无key，即使Idempotent=true/Replayable=true/max3仍attempt1/handler1/rows1并UNKNOWN+ErrUnsafeRetry；有key也先停1次、GET原key确认 |
| 有效写拒绝保留确定性 | 422/BAD_ORDER与422/OUT_OF_STOCK均合法协议、commit前返回；不是UNKNOWN，attempt1/handler1/rows0；409参数冲突仍确定拒绝 |
| 并发幂等 | 八个同时释放gate的真实POST：handler8、账本1行、回执ID全1、Duplicate7；Mutex临界区查key+append+回执原子 |
| 三层放大/修复 | 真实application2 × SDK3 × server2 → gateway6/leaf12；只保留application重试后2×1×1 → gateway2/leaf2 |
| breaker正常恢复 | 真实503两次closed→open；第三调用ErrOpenState、handler不增加；过期half-open第一真实probe等gate，第二ErrTooManyRequests，server probe1；放行后closed、真实healthy1 |
| breaker补充故障 | fixed统计窗口实际清计数；half-open再收到真实503后重新open；没有把库计数当HTTP自动分类 |
| fallback | INVENTORY_UNAVAILABLE + 原error，Receipt.ID=0，没有伪造库存、订单或支付成功 |
| 资源隔离 | slow handler1占active1，第二queue1，第三ErrQueueFull且不派发；slow Release关闭时healthy1已真完成；分别cancel/join后active0/queue0 |
| 速率限制 | 第一HTTP已完成，第二仍在开始间隔内被拒绝且不发HTTP；窗口后第三成功，handler总2；不同于完成时release的并发槽 |
| Result非Done/失败清理 | 真实HTTP结果成功已消费，worker仍在cleanup gate；cancel后owned1、Done未关；join owner到达JoinWaiting仍未结束；Open后worker Done、owner Done相继确认，主/清理error都可errors.Is |
| 早退出 | 真实slow Entered后caller不先读结果，直接cancel/unblock/独立join；Task.Done和handler.Done确认，最终owned0；所有t.Fatal路径先任务清理再fixture清理 |

最新每组fixture日志最终为 `active=0 conns=0 Serve=joined listener=closed err=<nil>`，客户端 `bodies=0`，每组 `owned_tasks=0`。所有startTask caller在创建后立即登记完整收尾；result交付不等同join。server自建Serve goroutine用独立完成关闭广播，Shutdown完成之后另等Serve并检查连接/handler归零。Hijack连接由handler显式Close，不误认为由Shutdown管理。没有外部长驻进程、故障代理或数据库对象需清理。

## 工具核验中的错误与处理

- 首次PowerShell源码定位误用了shell brace expansion路径，实际诊断 `Missing argument in parameter list.`；之后采用显式路径/Select-Object读取。另一次rg直接传Windows `internal/lab/*.go`得到 `os error 123`，改为 `rg ... internal/lab -g '*.go'`。这两个是只读定位命令错误，不是Go程序失败。
- 一次在仓库根读取模块evidence相对路径得到路径不存在，随即从显式模块workdir读取已有证据。没有把这个错误当成实验失败或覆盖业务日志。
- 返工收尾定位prompt时从模块workdir误用根目录相对路径，rg返回os error 3；随即改为已核验的绝对路径读取。这也是只读路径错误，未影响最终源码和业务验证。
- 官方 `https://pkg.go.dev/net/http@go1.27.0` 当次web工具返回Internal Error；之后打开官方net/http当前文档，并实际go doc与本机Go1.27源码核对GetBody/Transport/CancelFunc。固定context、gobreaker、Kitex和RFC页面正常可读。
- 本课实际执行中没有Go编译、业务断言、vet或race失败；主审发现的是旧测试未覆盖的写协议语义缺口，已按上节修复并新增真实副作用证明，最新全套通过。没有把未执行的默认32位compiler race失败复制成自己的失败记录。

## race环境与独立验证边界

主agent统一索引：`D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json`。实际race使用该文件的CC/CXX/bin_dir，`CGO_ENABLED=1`，WinLibs `16.2.0posix-14.0.0-ucrt-r2`、GCC16.2.0、mingw-w64 14.0.0。设置仅在执行命令的PowerShell进程，finally恢复原CC/CXX/CGO/PATH；没有go env -w、全局环境改动或重复下载安装。编译器归档SHA是主agent索引事实，本模块只核对实际compiler输出，未声称重新核验归档散列。

官方核验与本机源码位置：Go `context.CancelFunc`、`net/http.Request.GetBody`/Transport；`D:/Go/src/net/http/transport.go:844 shouldRetryRequest`、`request.go:1561 isReplayable`；gobreaker `gobreaker.go:83 Settings/:217 Execute/:237 beforeRequest/:304 currentState`；Kitex固定版本failure.go/failure_retryer.go/util.go。行号属于本次版本，升级后需重新定位；README提供官方链接和真实调用关系。

未验证范围：公网/DNS/TLS故障、HTTP/2并发stream、生产压力、持久数据库/事务、数据库锁取消、跨实例或重启幂等、Redis分布式限流、Kitex RPC/backup request。总预算、body、retry、breaker与隔离的验收均在本地真实HTTP/1.1；内存账本不冒充实库。race通过只说明此次测试调度未检测到数据竞争，不能替代业务语义/副作用/资源Done断言。
