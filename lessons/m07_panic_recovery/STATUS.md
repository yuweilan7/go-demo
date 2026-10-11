# M07 交付与真实验证记录

交付日期：2026-10-12，Asia/Shanghai。主 agent 独立验收指出启动后取消缺少 join 边界，本轮已修复并完整复验，现冻结交付，等待重新验收。返工后的正常入口、普通测试、vet、build、race 全部真实通过，启动后两条取消路径及收尾超时有独立同步断言。首次默认 mingw32 C 编译器导致的 race 构建失败已通过进程级 portable amd64 编译器配置解决，原始失败保留为历史。

唯一写入目录：`C:/Users/Lenovo/.codex/worktrees/go-course-m07/go-demo/lessons/m07_panic_recovery/`。未改根 go.mod/go.sum、原 examples、files、共享导航或其他模块，未执行 git add/commit/push，未创建其他 agent。

## 工具链和材料核对

Go 的运行、测试、vet、build 验证命令工作目录为上述模块目录，命令中的 `-C` 也显式使用相同绝对路径。初始 `go version`、`go env` 和材料读取在本 agent 的 worktree 根目录 `C:/Users/Lenovo/.codex/worktrees/go-course-m07/go-demo` 执行；它们没有改模块或工具链配置。

| 实际检查 | 真实值/结果 |
| --- | --- |
| `go version` | `go version go1.27.0 windows/amd64` |
| `go env GOVERSION GOTOOLCHAIN GOOS GOARCH GOROOT` | go1.27.0 / auto / windows / amd64 / D:/Go |
| `go env CGO_ENABLED CC GODEBUG` | 1 / gcc / 空 |
| 本课 go.mod | module=go-demo/lessons/m07_panic_recovery，go=1.27.0，无 toolchain 指令 |
| 依赖 | 仅标准库，无第三方依赖和 go.sum；没有依赖尚未交付的其他 module |
| 材料 | 工作树原 panic-recover/main.go；主目录 files/基础语法/go语言异常捕获，只读未复制/提交 |
| 规则/实现 | 官方 Go 1.27 spec、runtime.PanicNilError、net/http.Handler；本机 panic.go、mutex.go、server.go、debug/stack.go 的直接符号 |
| 本轮复跑时刻 | 初版 run/test/vet/build：2026-10-12 01:11–01:12 +08:00，race：01:19；取消返工后 run/build：01:43–01:44，最终 test/vet/race：01:46 +08:00（shell 本地时间） |
| race 专用环境 | 当前验证进程设置 CC/CXX 为主 agent 提供的 portable GCC/G++，CGO_ENABLED=1，PATH 前置 bin_dir；未写全局配置 |

## 实际执行命令

下表的 `M` 只用于阅读缩写，实际工具调用写的是完整绝对路径 `C:/Users/Lenovo/.codex/worktrees/go-course-m07/go-demo/lessons/m07_panic_recovery`。

| 命令 | 观察点 | 实际结果 |
| --- | --- | --- |
| 对模块内每个手写 .go 文件执行 `gofmt -w` | 返工后全部 9 个 Go 文件格式化 | exit 0 |
| `go -C M run ./cmd/demo`（返工后） | E01–E06 全部执行，不被坏进程截断；Joined 输出读取实际结果字段 | exit 0，末尾 NORMAL_ENTRY_COMPLETED；本轮约 3.51 秒（含构建） |
| `go -C M test -v ./...` | 时间线、失败状态、共享预占、真实 HTTP、进程退出 | exit 0；首次完整通过 |
| `go -C M test -count=1 -v ./...`（初版） | bounds panic 后完整复跑，禁用缓存 | 历史 exit 0，lesson 包 `ok ... 2.882s`；六个原行为测试组均 PASS |
| `go -C M vet ./...`（返工后） | 最终源码及新增同步测试的静态诊断 | exit 0，无诊断 |
| `go -C M build -o M/.local/m07-demo.exe ./cmd/demo`（返工后） | 正常入口构建为独立 executable | exit 0；验证后删除该生成二进制 |
| `go -C M test -race ./...`（首次默认环境） | Windows/amd64 race 构建/检测 | 历史 exit 1，runtime/cgo 编译失败；该轮没有执行 race 行为测试，已解决 |
| `gcc -dumpmachine`、`gcc -v`（首次默认环境） | 历史失败原因 | Target=mingw32，GCC 8.2.0，PATH 命中 D:/MinGW/bin/gcc.exe |
| portable GCC `-dumpmachine` | 修复后的实际 C 编译目标 | `x86_64-w64-mingw32` |
| `go -C M test -race -count=1 -timeout 40s ./...`（初次 amd64 配置） | 初版实际 race 检测，包括真实 HTTP/worker/全部子进程配置 | 历史 exit 0，lesson 包 `ok ... 4.405s`，无 DATA RACE；完整命令约 7.48 秒 |
| 返工 focused：`go -C M test -count=1 -v -run TestWorker -timeout 20s ./lesson` | 启动后两个取消分支、synctest 确认阻塞、独立预算、原 cause、收尾超时/最终清理 | exit 0，lesson 包 0.791s，before-result/after-result/cleanup-timeout/already-canceled 全 PASS |
| 返工最终：`go -C M test -count=1 -v -timeout 40s ./...` | 原行为及取消修复全部测试，禁用缓存 | exit 0，lesson 包 `ok ... 0.379s`；九个行为测试组均 PASS |
| 返工最终：`go -C M test -race -count=1 -timeout 40s ./...`（进程级 amd64 CC/CXX） | 真实 HTTP/worker、全部故障子进程及 synctest 取消断言 | exit 0，lesson 包 `ok ... 4.387s`，无 DATA RACE；完整命令约 7.16 秒 |

首次 race 构建失败的原始关键诊断（已解决历史）：

```text
# runtime/cgo
cc1.exe: sorry, unimplemented: 64-bit mode not compiled in
FAIL go-demo/lessons/m07_panic_recovery/cmd/demo [build failed]
FAIL go-demo/lessons/m07_panic_recovery/lesson [build failed]
FAIL
```

首次只读检查中 PATH 没有 clang/clang-cl，常见 C:/msys64、D:/msys64 的 mingw64/ucrt64 GCC 路径和 C:/Program Files/LLVM/bin/clang.exe 没找到；这不是全盘软件清单。主 agent 随后统一准备 portable WinLibs，并通知其已核验官方发布与 SHA256。本 agent 读取 `D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json` 的 cc/cxx/bin_dir，不自行安装或修改全局配置。实际执行用的路径是：

```text
CC=D:/IdeaProject/go-demo/.local/toolchains/winlibs-16.2.0posix-14.0.0-ucrt-r2/mingw64/bin/gcc.exe
CXX=D:/IdeaProject/go-demo/.local/toolchains/winlibs-16.2.0posix-14.0.0-ucrt-r2/mingw64/bin/g++.exe
bin_dir=D:/IdeaProject/go-demo/.local/toolchains/winlibs-16.2.0posix-14.0.0-ucrt-r2/mingw64/bin
CGO_ENABLED=1
```

仅在该次验证 PowerShell 进程中设置 CC/CXX/CGO_ENABLED 并前置 PATH，实际编译目标为 x86_64-w64-mingw32，race 测试真实通过。没有通过关闭 CGO 省略检测；默认 shell 的原 mingw32 配置仍在，所以复跑 race 需按 README 加载此进程级配置。

## 主验收指出的取消资源缺口及返工

初版 RunWorker 两个 select 的 ctx.Done 分支直接返回。任务有限、results 有缓冲只说明 worker 不会永久卡在发送，不能证明 owner 返回时清理已完成；此前普通成功路径 PASS 没有覆盖这项取消资源事实。

返工把两个分支统一交给 finishCanceledWorker。它保留 ctx.Err 与 context.Cause，从 Background 创建独立三秒预算等 done，清理完成才读取共享库存并标 Joined=true；调用取消仍返回失败。预算耗尽则同时保留原取消、ErrWorkerCleanupTimeout、独立 DeadlineExceeded，标 CLEANUP_INCOMPLETE/Joined=false，禁止读取活跃共享状态。启动前拒绝取消只是优化，不替代启动后的 join。

新增 worker_cancel_test.go 对真实实现设置清理/退出 gate，分别在任务已启动但尚未清理和结果已接收但外层尚未退出时取消。synctest.Wait 先确认其他测试 goroutine 到达阻塞或已经返回，随后检查 owner 没有提前返回；独立观察者在 API 返回瞬间检查真实 done/cleanup channel，避免仅断言实现的 Joined 标志。25ms 预算耗尽的故障测试明确验证未确认退出，然后自行释放故障 gate 并等清理/退出。没有 sleep 或 goroutine.NumGoroutine 猜完成。

README E04、入口 Joined 输出、源码旁的 ownership/结果/预算解释已同步，最终全部验证记录如上。

## 按要求核对真实行为

| 范围 | 实际证据 |
| --- | --- |
| 三层展开、defer LIFO、恢复返回点 | panic 时间线有 L3.second → L3.first → L2.observer → L2.recovered → L2.cleanup → L1.after-service；没有 L2.after-leaf；error 分支有 L2.after-leaf |
| 正常 error 对照 | success err=nil；业务校验分支返回 ErrInvalidOrder，未转成 panic |
| recover 调用位置 | ordinary=nil、indirect.helper=nil，outer 能恢复；named deferred function 能恢复；defer recover() 未截断，仍由外层有效 closure 收住 |
| defer 参数/闭包/结果 | 实际 `[closure=finished argument=registered]`，具名结果在 defer 后交付 |
| 普通 runtime panic | slice bounds 动态值为 runtime.boundsError，实现 runtime.Error，同 goroutine 可恢复并保留现场栈 |
| 恢复却返回 nil | swallow external=1、committed=1、err=nil；caller 真实继续提交 |
| 恢复后 defer 仍提交 | commit-defer external=1、committed=1、err 非 nil，故障已记录仍发生错误提交 |
| 修复失败结果与状态 | safe external=1、committed=0、staged=0、rollback=true，PanicFailure 包含任务/阶段/值/现场栈 |
| worker 的自身恢复 | FAILED + failure stack；owner 等 results 和 done；未修复 reserved=1，修复后 shared inventory reserved=0 |
| 返工：启动后取消有界收尾 | 两个 ctx.Done 分支均转入 Background 建立的独立预算；真实屏障命中结果前/结果后取消，owner 返回时实际 done 与 cleanup 均已关闭，cause/Canceled 保留 |
| 返工：独立预算超时 | worker 清理 gate 未放行，25ms 实际耗尽，返回 CLEANUP_INCOMPLETE/Joined=false，保留 cause/Canceled/ErrWorkerCleanupTimeout/DeadlineExceeded；随后放行并观察真正清理退出 |
| 真实本地 HTTP/1.1 | builtin 无头 + EOF + server 栈；middleware 完整 500 + 失败体 + retained stack；partial 已发 200/accepted-prefix 后读 body 得 unexpected EOF |
| 未恢复主调用链 panic | 子进程实际 exit=2，CHILD_DEFER_RAN 存在；没有超时 |
| 父 goroutine recover 反例 | 子进程 exit=2，WORKER_DEFER_RAN 存在，PARENT_RECOVERED/PARENT_DEFER_RAN 均不存在；没有超时 |
| runtime fatal | unlock 从未 lock 的 mutex，实际 exit=2；fatal 诊断存在，FATAL_DEFER_RAN/FATAL_RECOVER 均不存在 |
| os.Exit 进程终止 | 实际 exit=17，EXIT_DEFER_RAN 不存在 |
| panic(nil) 当前默认 | 显式 panicnil=0：recover 得 *runtime.PanicNilError，非 nil；子进程 exit=0 |
| panic(nil) 兼容模式 | 显式 panicnil=1：recover 结果 nil，仍使本次 panic 停止；子进程 exit=0 |

`TestChildEntry` 在父测试中 SKIP 是设计的入口分流，不是内容未验：`TestActualProcessExitCodes` 已分别启动五种故障加 nil 兼容模式，共六个真实子进程配置，逐项验证退出码、诊断和哨兵。正常 demo 也重复验证了这六个配置。

## 交付内容、清理和证据范围

- README 提供 15 分钟核心阅读、所有错误/修复的直接解析、实际日志、调用链、展开图、goroutine owner/等待点、HTTP/worker/事务策略、Java 类比边界和材料错误纠正。没有布置 exercise。
- 手写入口、业务逻辑、defer、并发协调、进程 harness、资源清理及测试关键断言有具体中文注释；源码与最终日志已逐项核对。
- 本模块下 `.local/` 被独立 .gitignore 排除。最后复跑的 run/test/race 输出留在 `.local/run.log`、`.local/test.log`、`.local/race.log` 供本机审查；vet 没有诊断输出，Tee-Object 未生成日志文件。不把这些本机日志汇总为课程文件。生成二进制验证后删除。
- 子进程由父进程逐个启动、有三秒上限、CombinedOutput 等待退出并回收，六个配置均未超时。HTTP listener、response body、transport 收尾，context timer 已释放；正常和取消收尾成功的 worker 均在 owner 返回前等 done。预算故障分支明确报告尚未退出，测试随后放行故障 gate 并等真实 done，完整实验结束没有遗留 worker。没有外部服务/持久数据变更。
- 证据范围：语言控制流和本机进程行为已实跑，race 检测已通过；HTTP/1.1 使用真实本地 TCP 请求；订单外部效果、事务提交/回滚、库存是合成内存模型。未证明实库/真实发货/MQ 的事务一致性，未单独验 HTTP/2；这些边界没有伪装成成功。

主 agent 验收重点：优先核对两个 ctx.Done 分支共用的独立收尾预算、原 cause 与清理超时分类；对照屏障/synctest/独立返回观察者证明。其余三层、helper/named defer、危险提交、真实 HTTP、六个子进程行为保持原范围，已一并复验。保留 portable amd64 race PASS 与旧失败历史、实库/HTTP2 范围限制。
