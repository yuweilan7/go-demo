# M10 课程交付与实际实验状态

- 记录日期：2026-10-12（Asia/Shanghai）。
- 模块状态：**IMPLEMENTED / SELF-VERIFIED**。本模块要求的课程、正常实验、明确故障、竞态、fuzz 与实际调试已全部完成；交付已冻结，待主 agent 独立验收。
- 唯一写入范围：`lessons/m10_testing_debugging/`。未改根模块、原示例/资料、其他课程，没有 git add/commit/push。
- module：`go-demo/lessons/m10_testing_debugging`；`go 1.27.0`；无第三方依赖、无 `toolchain` 指令、无 go.sum、无硬前置。
- 实际 Go：`go1.27.0 windows/amd64`；`GOTOOLCHAIN=auto`；`CGO_ENABLED=1`；`GOWORK` 为空。完整运行环境见 [environment.txt](evidence/environment.txt)。
- 调试工具：Delve **1.27.1**；实际断点/参数/栈输出成功，见 [delve-version.txt](evidence/delve-version.txt) 和 [delve.txt](evidence/delve.txt)。
- 初次 race 的 CC：`gcc`，PATH 解析到 `D:/MinGW/bin/gcc.exe`，GCC 8.2.0、target `mingw32`，不具备 amd64 编译支持；版本/解析路径见 [toolchain-initial.txt](evidence/toolchain-initial.txt)。
- 最终 race 编译器：主 agent 提供的 `D:/IdeaProject/go-demo/.local/toolchains/winlibs-16.2.0posix-14.0.0-ucrt-r2/mingw64/bin/gcc.exe`，实测 GCC **16.2.0 MinGW-W64 x86_64-ucrt-posix-seh**。CXX 为同目录 g++.exe，bin 目录只前置到验证脚本进程 PATH，CGO_ENABLED=1；结束恢复原环境。
- 真实 `libsynchronization.a` 路径已由编译器返回；配置/版本/库检查分别见 [race-environment.txt](evidence/race-environment.txt)、[race-compiler.txt](evidence/race-compiler.txt)、[race-runtime-library.txt](evidence/race-runtime-library.txt)。没有在本模块下载/安装工具链或更改共享配置。

## 真实命令、观察点和结果

下表全部从 `C:/Users/Lenovo/.codex/worktrees/go-course-m10/go-demo/lessons/m10_testing_debugging/` 运行。脚本在每条记录里保存了 `go -C` 的绝对目录、完整命令、退出码和真实输出。普通通过和预期故障分别标注，非零退出只有匹配目标原因才算故障复现。

| 验证 | 命令（相对于模块目录） | 实际结果与证明点 | 证据 |
| --- | --- | --- | --- |
| 格式化 | `gofmt -w` 本模块所有 `.go` 文件 | 成功；标准 Go 格式 | 手写源码 |
| 默认入口 | `go run ./cmd/demo` | exit 0；错误金额坏版 paid、修复 pending；取消后 cleaned=false，加入后 true；真实 HTTP 拒绝/成功/重放；实际栈 `[chan receive]` | [demo.txt](evidence/demo.txt) |
| 正常测试 | `go test -v -count=1 -shuffle=1729 -timeout=30s ./...` | exit 0；lifecycle/order/protocol 全 PASS；金额表/错误链/业务状态/一次入账/夹具/落盘/清理/环境/真实 HTTP 均覆盖 | [tests.txt](evidence/tests.txt) |
| 正常静态检查 | `go vet ./...` | exit 0，无诊断 | [vet.txt](evidence/vet.txt) |
| 坏提交静态检查 | `go vet -tags=faults ./faults` | exit 0；证明 vet 没有识别本例业务错误或 counter race，不能把它当业务正确性证书 | [vet-faults.txt](evidence/vet-faults.txt) |
| 错误提交复现 | `go test -tags=faults -run=^TestWrongCommit -count=1 -timeout=30s ./faults` | 预期 exit 1；reject 后 `State:paid CreditedCents:0 CreditCount:0`，违背待支付不变量 | [fault-logic.txt](evidence/fault-logic.txt) |
| 共享夹具复现 | `go test -tags=faults -run=^TestSharedFixture -count=1 -parallel=2 -timeout=30s ./faults` | 预期 exit 1；屏障之后 sibling 看到 `paid count=1`；修复夹具在普通测试 PASS | [fault-shared.txt](evidence/fault-shared.txt) |
| 后台 Fatal 静态诊断 | `go vet -tags=fatalfault ./faults` | 预期 exit 1；`call to (*testing.T).Fatal from a non-test goroutine` | [vet-fatal.txt](evidence/vet-fatal.txt) |
| 后台 Fatal 运行后果 | `go test -vet=off -tags=fatalfault -run=^TestFatalInWorker$ -v -count=1 -timeout=30s ./faults` | 预期 exit 1；worker defer 执行，owner 仍到达最后一行；正常回传错误版本在普通测试 PASS | [fault-fatal.txt](evidence/fault-fatal.txt) |
| 有界 fuzz | `go test ./internal/order -run=^$ -fuzz=^FuzzParseCents$ -fuzztime=3s -parallel=2 -timeout=30s` | exit 0；最终记录为两个 worker、244,821 次执行、baseline 93、14 个新 interesting input，未找到失败；oracle 为独立 math/big.Rat | [fuzz-amount.txt](evidence/fuzz-amount.txt) |
| 缓存对照 | 连续两次 `go test ./internal/order -run=^TestParseCents$` | exit 0；第二次 `(cached)`；不当成本次重新执行证据 | [cache-first.txt](evidence/cache-first.txt)、[cache-second.txt](evidence/cache-second.txt) |
| 调试构建 | `go build -gcflags='all=-N -l' -o .local/m10-demo.exe ./cmd/demo` | exit 0，二进制位于忽略目录 | [debug-build.txt](evidence/debug-build.txt) |
| Delve 实际定位 | `dlv exec .local/m10-demo.exe --allow-non-terminal-interactive=true --init=scripts/delve.commands -- -experiment=logic` | exit 0；命中 ApplyPayment；打印 `ID: "order-42", Cents: 1300`；栈为 ApplyPayment ← runLogic ← run ← main；继续后进程 exit 0 | [delve.txt](evidence/delve.txt) |
| 首次 race 环境故障 | `go test -race -count=1 -timeout=60s -parallel=2 ./...` | exit 1，runtime/cgo 构建失败：`cc1.exe: sorry, unimplemented: 64-bit mode not compiled in`。检测器未执行，不算 race PASS/复现 | [race-toolchain-initial.txt](evidence/race-toolchain-initial.txt) |
| 最终正常竞态检查 | `go test -race -count=1 -timeout=60s -parallel=2 ./...`（最终私有 CC/CXX） | exit 0；lifecycle/order/protocol 全 PASS，包含原子计数修复、synctest、隔离、真实 HTTP | [race-safe.txt](evidence/race-safe.txt) |
| 真实 data race 复现 | `go test -race -tags=faults -run=^TestRaceSharedCounter$ -v -count=1 -timeout=30s ./faults` | 预期 exit 1；两段 DATA RACE 读/写栈均定位 race_test.go 的 counter++；计数恰好 20000，仍有 race | [race-fault.txt](evidence/race-fault.txt) |
| race 下错误提交 | `go test -race -tags=faults -run=^TestWrongCommit -count=1 -timeout=30s ./faults` | 预期 exit 1；业务状态 paid 导致失败，没有 DATA RACE 输出 | [race-logic.txt](evidence/race-logic.txt) |
| race 下夹具污染 | `go test -race -tags=faults -run=^TestSharedFixture -parallel=2 -count=1 -timeout=30s ./faults` | 预期 exit 1；共享业务状态 paid/count=1 导致失败，没有 DATA RACE 输出 | [race-shared.txt](evidence/race-shared.txt) |

## 已完成的关键竞态验收与复跑

主 agent 已接管并提供统一私有工具链，本模块只读配置，没有重复下载/安装。以下命令已在本模块实际完成：

```powershell
$compiler = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' -Raw | ConvertFrom-Json
pwsh -NoProfile -File ./scripts/verify.ps1 -RaceOnly -CCompiler $compiler.cc -CxxCompiler $compiler.cxx -CompilerBinDir $compiler.bin_dir
```

脚本仅在其进程内设置 CC/CXX/CGO_ENABLED/PATH，退出时恢复；外层 PowerShell 退出码为 0，表示四项实验的预期结果及指定原因均匹配。正常 `-race` PASS；共享计数器出现实际 `WARNING: DATA RACE`；错误提交和共享夹具只因业务断言 FAIL、无 data race 警报。保留初次编译器故障作为修复前后的环境证据，当前无待补关键实验。

## 实验与证明边界

- 全部普通测试独立、本机、无云端依赖。Store 为内存业务，TempDir 是真实本机临时文件；HTTP 是真实 loopback TCP/HTTP。
- 没有运行 MySQL/MQ，不把替身/内存支付 PASS 宣称为事务或 ack 正确。响应读取、业务状态、资金/次数分别核验。
- synctest 只覆盖 bubble 中 context/channel 的虚拟截止与退出，不模拟真实网络、数据库或生产调度。
- 正常入口、故障入口、测试和 fuzz 都有限；测试总超时 30 秒，race 正常集上限 60 秒，真实 client 和诊断等待 2 秒，fuzz 两个 worker/3 秒。
- benchmark 是可选内容，本课未做性能结论；正式性能验收留给 M35。

## 清理与遗留

正常 worker owner 发送停止、释放清理屏障、加入 Done；栈实验 owner 关闭 gate、等待 done；测试 Cleanup 即使 Fatal 也执行释放与加入。HTTP body 逐请求关闭，client 的空闲连接关闭，server listener 关闭。TempDir 结束后删除，并有实际不存在断言；环境变量恢复原值/存在性。所有故障测试 goroutine 都加入完成，fuzz/Delve 已退出。

`.local/m10-demo.exe` 为可删除/可重新生成的调试产物，被本模块 `.gitignore` 忽略；没有持久监听进程、云端数据、故障代理、秘密或全局环境修改。Go 的编译/fuzz cache 属工具缓存，本课不清理共享缓存。主 agent 负责整层验收提交，当前 STATUS 只报告本模块事实。

主 agent 源码验收补充：`runWorker` 的独立清理预算等待失败，现在通过具名结果与 `errors.Join` 保留给 caller；此前只打印清理错误，可能丢失收尾诊断。正常/失败主路径仍先 Stop、放行清理屏障，再独立等待 Done。下述主 agent 实际复验另存编排验收记录。

主 agent 完整独立复验：修正清理错误返回后，实际运行 `scripts/verify.ps1 -IncludeRace -IncludeDelve`，外层 exit 0。正常测试、vet、三包 race、244,821 次有限 fuzz 与 Delve 1.27.1 实际断点/参数/调用栈均通过。错误提交、共享夹具、counter race 与 worker Fatal 的真实失败和指定诊断均匹配；正常数量 20,000 仍同时检出 DATA RACE。缓存第二次明确为 cached。上述 evidence 文件是本次主验收重新生成的实际记录，旧子 agent 的 187,768 次 fuzz 仅属历史样本。
