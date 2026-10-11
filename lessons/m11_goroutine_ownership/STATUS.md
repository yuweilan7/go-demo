# M11 实际交付与验证记录

记录日期：2026-10-12。实现、讲解、正常实验、隔离反例、独立断言已完成；**gofmt、普通 run/test/vet、test -race 和完整 run -race 均通过**。默认 32 位 GCC 导致的首次 race 构建失败已通过主 agent 提供的 portable amd64 编译器解决，历史失败与补测事实均保留。此交付已冻结，供主 agent 独立验收。

本文件仅记录本模块实现者的事实；主 agent 的整层验收、汇总、提交另行记录。实现者未 git add/commit/push，未改其他模块、原 examples/files、根 go.mod 或全局 go.work。

## 实际环境与输入

| 项目 | 本次事实 |
| --- | --- |
| 唯一实现目录 | `C:/Users/Lenovo/.codex/worktrees/go-course-m11/go-demo/lessons/m11_goroutine_ownership/` |
| 命令工作目录 A | `C:/Users/Lenovo/.codex/worktrees/go-course-m11/go-demo/` |
| 命令工作目录 B | 上述模块目录 |
| Go | `go version go1.27.0 windows/amd64` |
| go env | GOVERSION=go1.27.0，GOOS=windows，GOARCH=amd64，CGO_ENABLED=1，GOTOOLCHAIN=auto，GOROOT=`D:/Go` |
| 当前 GOMOD | 指定模块目录中的 `go.mod`；module=`go-demo/lessons/m11_goroutine_ownership`，go=1.27，无 toolchain 指令 |
| 依赖 | 标准库，无外部 module / go.sum / 服务依赖 |
| 默认 C 编译器（首次失败） | CC=gcc，CXX=g++，`D:/MinGW/bin/gcc.exe`；MinGW.org GCC 8.2.0，`gcc -dumpmachine`=mingw32 |
| race 实际 C 编译器 | `D:/IdeaProject/go-demo/.local/toolchains/winlibs-16.2.0posix-14.0.0-ucrt-r2/mingw64/bin/gcc.exe`，实际 `--version`=16.2.0，`-dumpmachine`=x86_64-w64-mingw32；CXX 同目录 g++.exe |
| 合成数据 | O-101=200，O-102 价格失败，O-103=500；后台 ledger 仅本次内存中的 O-101 |
| 命名空间与端口 | 本模块内存对象/child 进程；没有监听端口、数据库表、MQ topic 或服务器修改 |

已读取编排与教学规范、M11 完整 prompt、根 README/go.mod、适用祖先 AGENTS（`C:/Users/Lenovo/.codex/AGENTS.md` 当前为空），以及指定原示例和主目录只读 Goroutine 材料。Go 语句、程序退出、内存模型与 sync WaitGroup 官方文档已在本次查询；本地 `D:/Go/src/sync/waitgroup.go` 也实际阅读。README 对规范、公共契约、运行时实现作了区分。

## 实际命令、观察点与结果

| 工作目录 | 实际命令 | 实际结果 / 观察点 |
| --- | --- | --- |
| B | `Get-ChildItem -Recurse -Filter '*.go' \| ForEach-Object { gofmt -w $_.FullName }` | exit=0，全部手写 Go 文件格式化 |
| A | `go -C lessons/m11_goroutine_ownership run ./cmd/demo` | exit=0；E01–E04 完整运行、E05 四项故障有界；最后 M11 PASS，完整 stdout 保存 `evidence/demo.txt` |
| B | `go test -timeout 30s ./...` | exit=0；初次测试通过 |
| A | `go -C lessons/m11_goroutine_ownership test -v -count=1 -timeout 30s ./...` | exit=0；最终独立测试全部通过，lesson 包实际 `1.952s`；完整 stdout 保存 `evidence/test.txt` |
| A | `go -C lessons/m11_goroutine_ownership vet ./...` | exit=0，无诊断 |
| B | `go test -race -count=1 -timeout 45s ./...`（默认 GCC，首次） | exit=1，构建阶段失败；精确历史诊断见下文，该次没有进入动态 race 测试 |
| B | 进程级 portable CC/CXX/CGO/PATH 后 `go test -race -count=1 -timeout 40s ./...` | exit=0；补测初次 10.972s，最终 9.733s，无 race 诊断；stdout 保存 `evidence/race-test.txt` |
| B | 同样进程级环境后 `go run -race ./cmd/demo` | exit=0；正常 E01–E04 与全部有界 E05 均通过，无 race 诊断；stdout 保存 `evidence/race-demo.txt` |
| B | `go version`、`go env GOVERSION GOOS GOARCH CGO_ENABLED GOTOOLCHAIN GOROOT GOMOD`、`gcc --version`、`go env CC CXX`、`gcc -dumpmachine` | 实际值见环境表；没有改 go env/PATH |

`test` 的 `TestFaultChild` 普通调用显示 SKIP，因为它只作为 `TestIsolatedFaults` 的 child 入口；四个 child 子测试均真实运行通过，这个 SKIP 不是少跑了一种故障。测试不拿课程内部 require/PASS 作为唯一证据，另外核对业务金额、错误归属、部分顺序、真实接收值和资源结局。

## 每项实验的真实事实

| 实验 | 真实观察 | 独立断言 / 证明范围 |
| --- | --- | --- |
| E01 启动与完成 | 函数值/实参求值先于 body，started 后 done=false；金额=200，清理=true；result→cleanup→done 通知→caller joined | `TestLifecyclePublication`；不声称 caller go 后一行一定先于 worker，不声称 runtime 栈已被回收 |
| E02 Add/Done/Wait | classic 输出 `[200 0 500]` 一项业务错误，兄弟完成 | 同样输入独立合计成功金额 700、失败订单 O-102 |
| E02 WaitGroup.Go | go 风格同样 `[200 0 500]` 一项错误 | 使用实际 Go 1.27 API；不传 error、不取消兄弟、f 不得 panic |
| E02 late Add 反例与救援 | `Wait-returned; task-unfinished=true`，随后 `released-and-joined` | 屏障确保 Add 尚未发生；救援后工作已清理，修复入口为正常 classic/Go |
| E03 单结果邮箱修复 | cap=1，无接收者时 send accepted；owner Join 后 mailbox-values=1 | 独立真实接收测试；邮箱值最后排空，不说成消费者完成业务 |
| E03 取消发送修复 | cap=0，无接收者；cancel notified→observed→resources released→joined；active=0/resources=0/delivered=false | 32 次 owner 登记/Cancel/Join 各自核对归零；不用 NumGoroutine |
| E04 服务作用域 | 请求返回+取消时 service-active=1/resources=1；之后 ledger=[O-101]，Shutdown 后归零 | 独立测试请求结束不结束 service、真实内存记录唯一 |
| E04 服务关闭 | 一项运行、一项排队，两项 context.Canceled；第三项队列满拒绝，关停后拒绝新任务 | ledger 为空、所有接纳的句柄发布终态、worker 退出、令牌归零 |
| E05 early-main | child exit=0/timeout=false，EARLY_READY 有，worker cleanup/completed 无 | 真正 main 返回结束进程；没有用偶然没调度来解释 |
| E05 no-join | request-returned outstanding=1；600ms supervisor deadline，child exit=1/timeout=true | 真实服务保持存活，worker 句柄被遗忘；child 已回收；强制退出不等于 graceful cleanup |
| E05 no-receiver | child exit=2/timeout=false，runtime deadlock，owner `[chan receive]`/worker `[chan send]` | 真实阻塞堆栈与未完成 marker；修复对应 E03 |
| E05 self-wait | child exit=2/timeout=false，owner/worker 都是 `[sync.WaitGroup.Wait]` | 自己在计数中，Done 需返回；修复对应 E02 外部 owner 等待 |

## race 的历史失败与最终通过

默认 GCC 下实际命令的原始失败为：

```text
# runtime/cgo
cc1.exe: sorry, unimplemented: 64-bit mode not compiled in
FAIL go-demo/lessons/m11_goroutine_ownership/cmd/demo [build failed]
FAIL go-demo/lessons/m11_goroutine_ownership/internal/lesson [build failed]
FAIL
```

这是编译器 target=mingw32 与 Go windows/amd64 不匹配，不是 detector 报告的数据竞争。只读核对过常见 MSYS2/MinGW64/LLVM 已安装路径，未找到可用 amd64 compiler；随后按主 agent 约定停止重复定位。

共享 portable amd64 编译器由主 agent 准备。模块读取 `D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json` 的 cc/cxx/bin_dir，仅在当前 shell 进程设置 CC/CXX、CGO_ENABLED=1、PATH 前置 bin_dir；未自行下载、安装或修改全局配置。实际 GCC `--version` 为 `MinGW-W64 x86_64-ucrt-posix-seh ... 16.2.0`，target 为 x86_64-w64-mingw32。test -race 与完整 run -race 都已 exit=0，当前没有待补的 race 工具链缺口。

带 race 的 child 观察结果：early-main exit=0/timeout=false；no-join exit=1/timeout=true（600ms）；no-receiver 和 self-wait 均 exit=1/timeout=true（3s），全部 child 已回收。普通构建仍出现真实 runtime deadlock exit=2。这是本次不同 runtime 配置的观察差异，未把检测文本或具体耗时当语言保证。CheckFault 明确拒绝 child 输出中的数据竞争警告，防止预期坏例子掩盖新的 race 缺陷；最终 race 测试覆盖了这条检查。动态 race 通过证明本次执行未检出竞争，不声称覆盖所有可能输入或调度。

## 资源结局与保留内容

正常路径每个 request task 都通过 Join/Stop 等待 WaitGroup.Go 的工作函数返回，Service 通过 Shutdown 停止/完成所有接纳工作并 Wait；两种 Tracker 的最终 active/resources 均为 0。E01/批量/late-add 的屏障全部释放且 Done/Wait 完成；缓冲结果教学邮箱已排空。上下文 cancel/父进程 timer 已释放，demo watchdog 正常结束时被通知并 join。

四个危险 child 均由 `exec.CommandContext` 启动同一个二进制，无 shell/go run 孙进程；`cmd.Run` 返回即已等待并回收 child。早退、runtime deadlock 和超时 child 的退出均不被记作 worker 优雅清理。整体 demo 有 20s watchdog；其他 child 有 3s 监督上限，no-join 为 600ms；测试设 30s 总上限。业务等待用 channel 屏障控制，没有用 Sleep 猜调度。

没有常驻实验进程、监听器、故障代理、外部服务或凭据文件由本模块创建；Go 构建/测试临时二进制由工具清理。保留的是源码、README、STATUS 与有意保存的四份验证 stdout。无其他模块副作用；不据此宣称整个 Go runtime 的 goroutine 数为零或栈内存已回收到某个值。
