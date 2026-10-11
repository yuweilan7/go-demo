# M12 实施状态

记录日期：2026-10-12。实施者只写 `lessons/m12_channels_select/`，未提交、推送或修改共享目录。主 agent 独立验收、汇总和整层提交另行记录。

当前状态：**IMPLEMENTED_AND_VERIFIED — 本模块全部要求已交付，普通与正常 race 验证通过，显式指针故障实际检出竞争，交付已冻结等待主 agent 独立验收。** 默认32位GCC的历史编译错误已解决；下方保留准确诊断，不再是未完成项。

## 环境与隔离

实际工作目录：`C:/Users/Lenovo/.codex/worktrees/go-course-m12/go-demo/lessons/m12_channels_select`。

| 检查 | 实际结果 |
| --- | --- |
| `go version` / `go env GOVERSION` | `go version go1.27.0 windows/amd64` / `go1.27.0` |
| `go env GOTOOLCHAIN GOOS GOARCH CGO_ENABLED CC` | `auto` / `windows` / `amd64` / `1` / `gcc` |
| 默认编译器（历史错误） | `D:/MinGW/bin/gcc.exe`，`gcc -dumpmachine` 为 `mingw32`，不支持当前amd64 cgo/race |
| race实际CC/CXX | 主 agent 已核验的 WinLibs portable 16.2.0：`D:/IdeaProject/go-demo/.local/toolchains/winlibs-16.2.0posix-14.0.0-ucrt-r2/mingw64/bin/gcc.exe` / 同目录 `g++.exe`；`-dumpmachine` 实际为 `x86_64-w64-mingw32` |
| module / go 指令 | `go-demo/lessons/m12_channels_select` / `go 1.27.0` |
| toolchain 指令 | 与 go 相同的冗余 go1.27.0 经真实 `go mod tidy` 去除；实际仍为 Go1.27.0 |
| 第三方依赖 | 无，只用标准库，无 go.sum |
| 合成业务 / 副作用 | O-01..O-06 订单；消费者 append 报告，没有外部 DB、MQ、网络、端口或持久数据 |
| 资料核对 | ORCHESTRATION、TEACHING_STYLE、完整 M12 prompt、根 README/go.mod、现有 channel/select 示例、主目录 files 的 Channel/Select、Go1.27 官方规范/pipelines/内存模型 |

## 已实际执行的命令

所有下列命令的 workdir 均为上面的模块目录，`-C .` 指向该模块。只有产物与实验证据写在本模块。

| 命令 | 观察点 | 实际结果 |
| --- | --- | --- |
| `gofmt -w <本模块全部手写 Go 文件>` | 语法格式 | 成功，无格式诊断 |
| `go mod tidy` | 独立模块契约、无第三方依赖 | 成功；去除冗余 toolchain，不改根 go.mod |
| `go -C . run ./cmd/demo` | 全部正常/受控反例/修复段落 | 退出0，5段 PASS，最终 PASS M12；[原始完整日志](evidence/demo.txt) |
| `go -C . test -v ./...` | 状态、实际业务数据、结束/取消和错误传播 | 退出0，13个顶层测试及17个子测试全部通过；[详细日志](evidence/test.txt) |
| `go -C . test -v -run 'TestMemoryFinalPublisherCancellation\|TestSelectAndMemory' ./internal/course` | 最后发布取消屏障和成功语义 | 退出0；取消后owner进入清理、未放行不能返回，放行后FinalCount=6且context.Canceled保留，成功路径100/9/200/6不变 |
| `go -C . test -count=20 ./...`（较早饱和修复） | 针对新发现饱和屏障窗口的复验 | 修复 Produced=3 屏障后退出0，全部20轮通过；并非以重复次数证明调度顺序 |
| `go -C . vet ./...` | 标准静态诊断 | 退出0，无诊断；[记录](evidence/vet.txt) |
| `go test -race -count=3 ./...`，默认 CC（历史） | 默认编译器失败 | 编译期退出1，runtime/cgo报32位gcc不支持64位目标，当时测试未执行；后续x64CC已解决 |
| `go run -race ./cmd/fault pointer-race`，默认 CC（历史） | 默认编译器失败 | 编译期退出1，故障当时尚未运行；后续64位CC已解决，[精确历史诊断](evidence/race-default-compiler-failure.txt) |
| `go -C . test -race -count=3 -timeout=60s ./...`，进程级x64CC | 正常路径的数据竞争检测与全部协议断言 | 退出0，全部3轮通过，无WARNING: DATA RACE；最终发布收尾修复后实际course包耗时8.255s，[真实记录](evidence/test-race.txt) |
| `go -C . run -race ./cmd/demo`，进程级x64CC | 全部正常/受控故障/修复入口带竞争检测 | 退出0，5段和最终M12均PASS，没有竞争报告；[完整日志](evidence/demo-race.txt) |
| `go -C . run -race ./cmd/fault pointer-race`，进程级x64CC（先前有效证据，故障源码未改） | 真实危险指针竞争 | 预期go run退出1，显示WARNING: DATA RACE、Found 1 data race(s)、exit status 66；read第39行/write第47行同一AmountCents，[原始记录](evidence/pointer-race.txt) |

race共享工具链由主agent统一下载、核验和管理。本模块从主目录已忽略的 `.local/course-orchestrator/toolchain.json` 读取 cc/cxx/bin_dir，只有当前命令进程的环境改变。本模块没有下载/安装GCC，没有执行go env -w或修改系统/用户环境变量。

以下是最终race运行前的实际PowerShell设置，可在同一台机器的课程命令进程内复用：

```powershell
$m12Toolchain = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' -Raw | ConvertFrom-Json
$env:CC = $m12Toolchain.cc
$env:CXX = $m12Toolchain.cxx
$env:CGO_ENABLED = '1'
$env:PATH = $m12Toolchain.bin_dir + ';' + $env:PATH
# 在本模块目录运行；PATH/CC设置只影响这个PowerShell进程及它的Go子进程。
go -C . test -race -count=3 -timeout=60s ./...
go -C . run -race ./cmd/demo
go -C . run -race ./cmd/fault pointer-race
```

其他机器需先有匹配GOARCH的C编译器，再设置自身CC/CXX；私有toolchain.json不随课程提交。普通demo/test/vet不需要C编译器。显式故障的go-run非零退出是预期结果，不能与正常race通过混为一项。

## 行为与独立断言

| 必须交付项目 | 当前实证 |
| --- | --- |
| nil/open-empty/buffered/closed-drained 的收发关闭 | 7行真实状态表；close(nil)、重复close、sendclosed 在同一goroutine recover，闭包 factory 独立构造每列 |
| 合法零值 vs 通道结束 | 真实 `0,true → 7,true → 0,false → 0,false`，并有独立预期表断言 |
| range 不因来源返回结束 | 来源发送后返回，consumer 收值后仍不能完成；owner 同步确认无后续发送后close并join |
| 无缓冲会合与内存同步 | 发送前屏障证明不能完成；实际42交接；consumerState=77在receive前写，sender在send返回后读，done同步后owner断言77 |
| default/closed 忙循环 | 固定100,000次，默认运行有界；blocking receive与nil禁用均实际运行 |
| 双ready及取消非优先 | 两组4096轮；仅断言总数，不断言比例、worker顺序或每次都能选到特定case |
| 有界订单pipeline正常与空输入 | 1/3/8个worker × 0/1缓冲 × 正常/空，共12组合；逐ID/金额/拒绝原因核验，owner返回后实际接收ok=false |
| 消费者提前停止 | 无缓冲results，真实只消费2单，最终3个worker退出；不对产生/校验的偶然总数设固定断言 |
| 裸发送阻塞反例与修复 | 错误版本第二次send没有接收者不能结束，夹具真实接收O-02解开；pipeline每个发送同时等取消 |
| 来源错误 | failAt=0/3/6各次返回ErrSource并排空已交付前缀；来源错误与提前停止同时发生时Join保留两者 |
| 饱和恢复和取消 | 冻结consumer，等result-send失败与Produced=3，再观察稳定jobs=1/results=1；恢复收完整六单；独立test直接Cancel、不读结果也能Wait |
| 接收者随意关闭的panic | gate保证close之后才第二次send，实际recover为send on closed channel；修复是唯一关闭者 |
| 多生产者关闭 | worker只Done不close；协调者等完整WaitGroup和sourceDone后唯一close(results)，所有正常/停止/错误路径核验 |
| 指针/值所有权 | 正常值副本100/原变量9，指针独占回传200，close发布finalCount6；最后发布任务在正常/取消路径均独立cleanupAwait，屏障测试确认取消后仍收尾且保留Canceled；危险指针入口先前实际检出1处竞争，源码未改 |
| 中文注释/README/调用链 | 全部手写函数有职责与关键中文说明；README含完整状态表、owner表、正常/空/停止/错误/背压时间线、select和内存边界、Java类比失效点 |

## 发现过的真实故障与修复

饱和实验早期只等两条“发送曾不能完成”的通知。后续真实普通测试捕获 `TestSaturationCancelAtSend` 失败，实际 `jobs=0 results=1`：旧 jobs 通知可以在worker稍后取走订单之前产生，不能证明此刻jobs满。修复加入feeder成功交付第三单的Produced屏障；结合唯一worker被results阻挡且consumer被冻结，证明第三单在jobs、第二单在worker、第一单在results。修复后20轮普通测试通过，保留[原始诊断与机制说明](evidence/saturation-barrier-diagnostic.txt)。

主验收发现最后的最终值publisher独立于先前指针worker：业务await(ctx, finished)取消后，原代码只等前面的done，缺少finished的独立完成确认。已在启动最后任务前注册deferred cleanupAwait(finished)，收尾成功后再读取最终状态，Join保留业务context错误。新增TestMemoryFinalPublisherCancellation，真实屏障顺序为finalStarted → 取消业务ctx → cleanupStarted → 未放行时不能返回 → releaseFinal → finished → 返回FinalCount=6且errors.Is(Canceled)。测试没有sleep/NumGoroutine，不宣称runtime栈已回收。修复仅涉及memory.go及该测试/文档，pipeline/来源错误/饱和/误关闭的生产源码未变。

普通module初次run提示 `go: updates to go.mod needed`，已在模块内真实tidy；构建发现memory.go未使用fmt导入，已移除并复跑。两项均已解决，不影响当前普通运行。

## 资源清理与未验证范围

正常、空、来源失败：feeder关闭jobs并留终态，全部worker执行defer/Done，唯一closer关闭results/Done，owner完成Wait。提前停止/外部取消：每次可能阻塞的业务send/receive均能观察ctx，Collect用独立3秒cleanup context等待终态。控制示例的range/裸send均有真实close或接收救援，最后等待完成；误关闭panic在sender自身recover并返回owner。MemoryOwnership分别等待指针worker的done与最后publisher的finished；最后业务await取消后仍以独立3秒cleanupAwait确认finished，并保留取消错误。取消测试gate自身有界，驱动defer在断言失败时放行并等待返回；完成信号证明工作段没有后续写入/清理已确认，不声称runtime栈物理回收。timer随段落/测试退出cancel，观察channel不会阻塞业务且不要求close。

没有后台服务器、监听端口、子服务、长驻goroutine或编译二进制产物；Go标准缓存由工具管理。pointer-race固定两条goroutine各20,000次循环，是单独显式入口，两者实际WaitGroup结束后go-run以预期失败状态结束，进程资源已回收。正常各路径已用race验证，未发现额外竞争。未验证范围：没有持久业务副作用、重试、顺序消费、断点恢复或跨进程消息语义，也不作这些承诺；race检测覆盖实际执行路径，不宣称能证明所有未来输入/调度的程序正确性。
