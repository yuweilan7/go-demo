# M34 交付与真实验证记录

状态：`READY_FOR_REVIEW`，本轮返工交付冻结。主 agent 尚未接受上一版，独立审查发现 driver 所有权缺口后已在原模块修复，并重新完成完整 demo/profile/test/vet/race；等待主 agent 再次独立验收与整层提交。没有自动进入其他模块，没有 git add/commit/push。

验证日期：2026-10-12，Asia/Shanghai。返工后的最终样本时间为 `2026-10-12 02:12:30.7375714 +08:00`，UTC 原值 `2026-10-11T18:12:30.7375714Z`，见 `evidence/report.json`。以下数字是这一轮样本，不把此前运行或下次运行混在一起。旧样本时间曾为 01:21:14 +08:00，旧数字不能证明本次返工已验证。

## 写入范围与交付文件

唯一写入目录：`C:/Users/Lenovo/.codex/worktrees/go-course-m34/go-demo/lessons/m34_observability_diagnosis/`。

已只读核对主目录 ORCHESTRATION、TEACHING_STYLE、完整 M34 prompt、files/开发框架、worktree 根 README/go.mod 和 context 示例；未改主目录、其他模块、根 go.mod/go.sum、共享 go.work、原 examples/files 或统一工具链配置。

交付包含独立 `go.mod/go.sum`、中文 README、9 个带中文解释的 Go 源码文件（含入口、driver owner 与测试）、`scripts/inspect.ps1`、`scripts/verify-race.ps1`、本 STATUS 和 30 份真实文本/JSON/Prometheus 证据。没有布置 exercise。所有实验都直接交付反例、修复、运行过程与解析。

`.local/latest/` 保留本次原始 CPU/block/mutex/heap 二进制、完整 goroutine 栈、runtime trace、转换后的 trace profiles 和完整解析报告；它已经由模块 .gitignore 排除。`evidence/` 只保留有界合成数据和相应文本证据，不纳入编译产物或整批原材料。

## 实际环境和依赖

| 项目 | 实际核对结果 |
| --- | --- |
| go version | `go version go1.27.0 windows/amd64` |
| module / go 指令 | `go-demo/lessons/m34_observability_diagnosis` / `go 1.27.0`，无额外 toolchain 指令 |
| GOROOT / GOTOOLCHAIN | `D:/Go` / `auto` |
| OS / ARCH / CGO | windows / amd64 / 1 |
| PowerShell | 7.6.5 |
| runtime.NumCPU / GOMAXPROCS | 16 / 16 |
| OTel 直接依赖 | otel、otel/trace、otel/sdk、stdout/stdouttrace 均 `v1.47.0`，已实际下载、锁 sum、执行 exporter |
| 下游/指标/pprof 地址 | 每轮独立 `tcp4 127.0.0.1:0`；三个 listener，不占固定共享端口 |
| 业务状态 | 下游独立内存 map，无外部数据库/MQ/云链路 |
| 性能实验环境 | 其他课程 Go agent 同时工作，可能并行构建；MemProfileRate=1、block/mutex 全采样与同步 exporter 也有开销；仅说明当前样本因果 |
| 工作/清理预算 | 工作 ctx 45 秒；每个异步 driver owner 新 Background ctx 3 秒加入；每个 Service 新 ctx 3 秒清理 HTTP/audit；工作 ctx 到期不能跳过 join，也不是进程硬退出上限 |

官方文档与当前源码已经重新核对：slog、runtime/pprof、net/http/pprof、runtime/trace、Go 1.27 发布说明、OTel Go instrumentation/SDK v1.47.0。没有按旧 Go/OTel API 记忆实现。

## 全范围验收证据

| prompt 内容 | 实际完成内容与验证 |
| --- | --- |
| 1. 真实 HTTP/下游、结构化日志与必要栈 | frontend POST /checkout、downstream POST /reserve 为真实协议；request_id/operation/outcome/duration/trace_id；panic 在同 goroutine defer/recover 保留 debug.Stack；权威 map 独立查询 |
| 2. 请求/错误/取消/in-flight/队列/依赖指标与 histogram | 真实 `/metrics` 输出有限 variant/operation/outcome/le；requests、known errors、cancellations、unknown、in-flight、queue-waiters，request/queue/dependency histogram；成功、409、503、派发前取消、响应丢失未知、panic 均实测 |
| 3. 当前 OTel SDK 传播、owner/End、异步关联与真实 exporter | v1.47.0 TraceContext Inject/Extract 经两次真实 HTTP；driver 独立 Done join 与 Service HTTP/audit join 都完成后读取 capture/exporter；42 个真实本地 exporter JSON span，18 条精确 remote parent 边，6 个不同 trace 新 root Link；元数据断言和文件流解析对应；不声称云导出 |
| 4. goroutine/heap/CPU/block/mutex/runtime trace 与 leak | E01 栈+block+mutex 定位锁跨 I/O；CPU 重复 hash 追到 digestOnce；heap 8 MiB slice 保留追到 retainBad；runtime trace 实际解析并转 sync/net profile；Go1.27 leak 不可达 total=1、仍可达 total=0，显式 owner 修复 |
| 5. 受控 pprof、从慢请求到源码、修复对照与未知范围 | debug listener 仅 loopback、独立 mux、无 cmdline；CPU/trace HTTP 采样固定上限；bad-fast queue/IO 时间与栈关联；fixed fast 在 holder 门关闭时实际提交，最终两个订单不丢失；完整 README 调用链/owner/时间线/清理/限制 |

## 最终实际命令与结果

以下命令的显式工作目录均为：

`C:/Users/Lenovo/.codex/worktrees/go-course-m34/go-demo/lessons/m34_observability_diagnosis/`。

| 命令 | 观察点与真实结果 |
| --- | --- |
| `gofmt -w cmd internal` | 全部 Go 源码格式化；最后 `gofmt -l cmd internal` 无输出 |
| `go mod tidy` | 当前版本依赖实际下载，模块 sum 已锁定 |
| `go -C . run ./cmd/demo` | 退出 0，全部 core/outcome/CPU/heap/leak/trace 断言通过；见 evidence/demo-output.txt |
| `.\scripts\inspect.ps1` | 退出 0；pprof 10 份 top/source/diff 报告，runtime trace parsed、sync/net 转换和 pprof 解析均通过 |
| `go test -count=1 -timeout 60s -v ./...` | 9 个顶层独立测试全部 PASS；internal/lab 最终一次 0.968s；包含确定性工作超时+清理 gate 与 cleanup 超时后最终 join；见 evidence/tests.txt |
| `go vet ./...` | 退出 0，无诊断 |
| 进程级统一 CC 后 `go test -race -count=1 -timeout 60s ./...` | 退出 0，无 race；最终一次 internal/lab 2.207s，见 evidence/race.txt |
| `.\scripts\verify-race.ps1` | 脚本本身实际复验退出 0；target=x86_64-w64-mingw32，internal/lab 1.321s，退出恢复原 CC/CXX/CGO/PATH；见 evidence/race-wrapper.txt |
| `go mod verify` | 退出 0，`all modules verified` |
| `git diff --check` | 退出 0；最终 Git 未跟踪条目仅本模块授权文件，未暂存/提交 |

实际执行的 pprof/trace 命令包括：

```powershell
go tool pprof -top -nodecount=12 .local/latest/cpu_bad.prof
go tool pprof -list=digestOnce .local/latest/cpu_bad.prof
go tool pprof -top -nodecount=12 .local/latest/cpu_fixed.prof
go tool pprof -top -nodecount=12 .local/latest/block-bad.prof
go tool pprof -list=badCheckout .local/latest/block-bad.prof
go tool pprof -top -nodecount=12 .local/latest/mutex-bad.prof
go tool pprof -top -nodecount=12 -diff_base .local/latest/mutex-bad.prof .local/latest/mutex-fixed.prof
go tool pprof -top -nodecount=12 .local/latest/heap-bad.prof
go tool pprof -list=retainBad .local/latest/heap-bad.prof
go tool pprof -top -nodecount=12 .local/latest/heap-fixed.prof
go tool trace -d=parsed .local/latest/runtime-trace.out
# 脚本用固定 cmd 重定向保留以下二进制 stdout，然后真正用 pprof 解析。
go tool trace -pprof=sync .local/latest/runtime-trace.out
go tool trace -pprof=net .local/latest/runtime-trace.out
go tool pprof -top -nodecount=12 .local/latest/trace-sync.prof
go tool pprof -top -nodecount=12 .local/latest/trace-net.prof
```

trace 转换后的 profile 没有主 binary 文件名，pprof 写了 `Main binary filename not available.` 提示，但实际解析退出 0且已有函数符号/调用栈；未把提示隐去，也未误记为解析失败。实时 HTML 查看器没有启动并留驻，验收使用 parser/实际 profile 输出。

## 最终样本观察

默认入口最终输出：

```text
core variant=bad committed=2 in_flight=0 queue_waiters=0 proof=PASS
core variant=fixed committed=2 in_flight=0 queue_waiters=0 proof=PASS
outcomes business_error=1 dependency_error=1 canceled=1 unknown=1 internal_error=1 dependency_samples=3 unknown_authority_committed=true proof=PASS
cpu same_checksum=true digest_calls=400000->1 response_header_ms=704.611->0.503 (sample only)
trace exported_local=42 remote_parent_edges=18 linked_async_roots=6 proof=PASS
artifacts=.local/latest cleanup=PASS
```

核心 bad-fast 的最终日志原值为 duration 186.4019 ms、queue 185.3986 ms、dependency 1.0033 ms。block 的 `badCheckout` Lock 样本约 185.80 ms，mutex 的对应持锁者调用栈约 185.80 ms，Unlock 总项另含其他锁争用为187.15 ms。时间来自当前 profile 的不同测量口径，不断言数值必须完全一致。修复由独立先后断言证明：fixed-fast 已成功且实际提交时，fixed-holder 尚未提交且下游 gate 仍未释放；释放后 holder 也提交，两个版本最终都保持 2 个提交。

CPU bad profile total 690 ms，`digestOnce` cum 690 ms，其中 SHA256 指令实现 flat 680 ms。profile 显示 Duration704.61ms、total/Duration约97.93%；total 是多线程采样累计，不是单核百分比，也不要求一定大于墙钟。fixed CPU 样本为 0，窗口太短，不能解释为没有 CPU。checksum 均为 `9793410642751206272`；独立参考公式与调用次数测试通过。

`Result.Elapsed` bad/fixed 原值为704.6107/0.5033ms，从 driver span Start 之后构造前端请求到 client.Do 返回响应头/错误，不包括 Body JSON 解析、Body.Close、span.End；构造失败的早退没有计时、Elapsed 默认零。CPU profile 的 Start/Stop 则包住整个 request 调用，范围不同。report 新增 `cpu_timing_scope`，stdout 改为 `response_header_ms`，避免称为完整请求耗时。`callDependency` dependency duration 同样截止响应头/错误、排除后续 Body 读取；build/body 错误用 %w 保留各自原错误，Do 错误在 ctx.Err()!=nil 时保留该取消/截止原因，ctx.Err()==nil 时固定分类 cause 替换原 *url.Error 与底层网络错误链，未声称完全保留所有网络错误。

heap-bad 的 `retainBad` 活对象样本为 8192 KiB，占该 profile 约 97.92%；切换独立复制的 16 字节 header 后，heap-fixed 中该 8 MiB 分配不再存活。header 字节相同，cap 由 8388608 变 16。未宣称 RSS 即刻回落。

结果指标 known errors_total=3，cancellations_total=1，unknown_total=1。五类 outcome 各1，dependency_count=3，request_count=5，in-flight/waiters 最终0。派发前取消不增加 dependency 样本，日志记录 dependency_observed=false、dependency_ms=null；未知写入取消后权威状态为已提交。

goroutineleak 两种子进程最终样本：unreachable `live_wait_stack=true detected_target=true leak_count=1 debug=1`；reachable `live_wait_stack=true detected_target=false leak_count=0 debug=1`。reachable 在修复 close(gate)+await(done) 后 worker 退出得到确认。原始不可达坏 worker 只存在有界子进程，子进程实际退出。

## 首次失败和修复历史

1. 未完成编写时首次构建缺少尚未添加的 `Digest/DigestIterations`；添加 CPU 工作负载后重新构建和运行，完整 demo 通过。没有把该次仅编译阶段当成实验验收。
2. 第一次完整 demo 的 reachable leak 判断失败：对 `goroutineleak.WriteTo(debug=2)` 仅按函数名出现判断“检出”，把未泄漏的 reachableWaiter 误算进来。读取本机 `D:/Go/src/runtime/pprof/pprof.go:writeGoroutineLeak`，确认 debug>=2 写全部 goroutine 栈，包含 non-leaked。修复为 debug=1 的专用泄漏记录与 Count，再同时读取普通存活栈。后续两种子进程实际为1/0且正常结束；README/源码就地解释该误判边界。
3. 默认 race 首次失败原文：

   ```text
   # runtime/cgo
   cc1.exe: sorry, unimplemented: 64-bit mode not compiled in
   FAIL .../cmd/demo [build failed]
   FAIL .../internal/lab [build failed]
   ```

   默认 CC 为 `D:/MinGW/bin/gcc.exe`，target=mingw32。主 agent 统一安装/配置兼容64位编译器，本课仅只读其 `D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json`，使用：

   `D:/IdeaProject/go-demo/.local/toolchains/winlibs-16.2.0posix-14.0.0-ucrt-r2/mingw64/bin/gcc.exe` 与同目录 g++.exe。

   进程级 CC/CXX、CGO_ENABLED=1、PATH 前置 bin_dir 后，目标为 x86_64-w64-mingw32，实际 race 测试通过。没有全局写 go env、PATH 或注册表；脚本退出恢复原进程值。

4. 上一版独立审查未通过 driver 所有权验收。原 `launch` 只返回容量1结果 channel，`take(workCtx)` 可能工作预算到期先返回；正常/意外 select 消费结果后也不能复用结果接收证明 join。核心 Await/fetchSample/waitingStack 的失败路径、outcome 的 admission/unknown Await 失败以及测试 t.Fatal 路径都可能未加入已启动 driver，原 README 把 take 描述为 join 也不准确。Service.Close 只拥有 HTTP/audit，不能证明 caller goroutine 退出。

   本轮新增 `internal/lab/drivers.go`，每个 driver 立即登记独立 child cancel、只关闭的 ResultReady、只在全部 worker defer 后关闭的 Done 与所属 gate release。结果等待和收尾等待分开；全部生产 launch caller（holder/fast/admission/unknown）与测试 launch caller（holder/pre/post/log-cancel）都归 owner。每条 trial 返回路径使用同一 defer，先 cancel 全部任务并释放 gate，以新 Background 3秒预算逐个 join Done，再关闭 Service，errors.Join 保留原失败与 cleanup 失败；core/outcome PASS 只在 join/Close 全成功后输出。测试 Cleanup 按 driver → Service → SDK → workCtx 的顺序负责已启动任务，structured-log 测试也先 join driver 再读取 buffer。

   两个新增独立断言用实际工作 deadline、显式结果门、清理门、owner进入select的屏障证明先后。工作ctx先到期且take先返回时，清理阻塞仍不许owner返回；放行、真实Done后才能返回原失败。cleanup deadline到期必须给任务名+incomplete错误并保留原失败，Done尚未关闭；测试最后放行并再次真实加入。没有sleep或NumGoroutine猜退出。构造中途fail helper也修复为errors.Join原构造错误与s.Close错误。本次完整demo、九测试、vet、race和profile均重新运行，未把旧样本当返工通过证据。

## 独立断言、资源结局和剩余范围

9 个独立测试分别验证：不相关订单在其他订单等待真实 I/O 时可以完成；取消/未知的提交边界；两层 HTTP remote parent 与异步 Link；三条真实 listener 关闭并且 Close 幂等；参考 checksum 和 slice 复制后不别名；histogram 累计 bucket 与无样本/无 ID 标签；真实 JSON 中缺失观察为 null+false以及 panic 栈保留；工作deadline与结果可读均不替代driver cleanup Done；清理超时明确失败且最终放行再join。race 执行这些测试，不声称覆盖所有可能调度或能证明无泄漏。

程序与测试已经分别加入全部已启动异步 driver 的独立 Done；服务 listener/Serve 已停止，HTTP handler 经 Shutdown 等待完成，audit 经 WaitGroup join，Transport 关闭 idle connection，CPU/trace Stop 后关闭文件，profile 设置退出恢复，两个 child process 已结束。两项清理测试展示的暂时阻塞/超时worker最终也放行并加入，测试owner包装goroutine另有Done并在Cleanup加入。只有源码/模块依赖、被忽略的本地样本和合成证据文件被保留。没有留驻 HTML 查看器、故障代理、服务器、云配置、外部数据库/消息或安装锁。

未验证范围：真实数据库持久化/库存事务、跨进程幂等、消息可靠性、远程 OTLP/Collector/Prometheus/Grafana、生产采样/告警、RSS 回收时机、长期内存稳定性、稳定性能倍数与生产 SLA。Go goroutineleak 空报告不能排除可达对象上的业务生命周期泄漏。若需要稳定性能结论，待其他模块构建结束后在固定机器/输入/并发下串行重复 CPU/heap 样本；这不是当前机制验收缺口。

主 agent 建议独立复验默认入口、test/vet/race 和 inspect 脚本，核对所有launch caller已登记、take只等结果、Done在worker收尾后关闭、新预算join/error合并及两项确定性取消清理断言；再沿 `bad-fast → queue observed → live Lock 栈 → block/mutex → badCheckout` 核对因果，确认 fixed 的先后不变量以及 unknown 的真实 map 提交。主 agent 的 ACCEPTED 与整层提交 SHA 由统一调度记录，不由本模块自行代写。
