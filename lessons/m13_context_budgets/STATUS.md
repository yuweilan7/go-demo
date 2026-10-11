# M13 实际交付与验证状态

记录日期：2026-10-12，Asia/Shanghai。**本模块交付和执行者验证完成，已冻结待主agent验收。** 中文讲解、详细注释源码、反例/修复、真实本地HTTP实验和独立断言已完成；默认入口、test、vet、使用64位工具链的race均通过。旧GCC失败已解决并保留历史。主agent尚未做整层验收/提交，本文件不代替该验收。

## 独占交付范围与环境

- 唯一写入目录：`C:/Users/Lenovo/.codex/worktrees/go-course-m13/go-demo/lessons/m13_context_budgets/`。
- module：`go-demo/lessons/m13_context_budgets`；`go 1.27.0`；无 toolchain 指令、第三方依赖、go.sum、go.work、replace。
- 实测：`go version go1.27.0 windows/amd64`；GOVERSION=go1.27.0；GOTOOLCHAIN=auto；GOOS=windows；GOARCH=amd64；CGO_ENABLED=1；CC=gcc；GOROOT=D:/Go。
- 读取主目录的 ORCHESTRATION、TEACHING_STYLE、M13 prompt、INFRASTRUCTURE 和指定 files 资料，读取自己 worktree 的根 README/go.mod/context 示例；适用的 `C:/Users/Lenovo/.codex/AGENTS.md` 长度0，未新增限制。
- 官方文档核验使用 Go1.27.0 固定版本 context/net/http/database/sql/driver；本机源码核验 D:/Go/src/context/context.go、net/http/request.go/client.go/transport.go。
- 没有改原始 materials、共享索引、根 module、其他课程，没有 git add/commit/push；`.local/` 实验日志被本模块 .gitignore 排除。

## 真实执行命令

以下每条命令的工作目录均为上面唯一模块目录。命令直接在 PowerShell 执行，结果来自本次运行。

| 命令 | 真实结果 | 证明范围 |
| --- | --- | --- |
| `gofmt -w cmd/demo/main.go internal/lesson faults/lostcancel/main.go` | 成功，无诊断 | 所有手写 Go 源码格式化 |
| `go run ./cmd/demo` | exit=0，六组 PASS + 最后 M13 PASS | 所有业务/取消/失败/清理断言；真实 loopback HTTP/1.1 |
| `go -C . run ./cmd/demo -scenario http` | exit=0，核心五个HTTP子例完整PASS | 直接复跑10～20分钟核心阅读路线 |
| `go test -count=1 ./...` | exit=0；cmd/demo无测试；internal/lesson第一次9.319s、最终整模块9.972s | 四项测试，课程端到端、未知结果、并发幂等、caller已取消 |
| `go test -v -count=1 ./internal/lesson` | exit=0；TestScenarios=3.40s、独立timeout=0.25s、并发幂等=0.00s、已取消caller=0.00s；包=3.814s | 为解释实测耗时差异做一次分项复验，均通过；不承诺固定时延 |
| `go vet ./...` / 最终 `go -C . vet ./...` | exit=0，无诊断 | 正常模块静态检查；不包含显式lostcancel tag |
| `go vet -tags=lostcancel ./faults/lostcancel` | **预期exit=1**，main.go:17:7报告遗失cancel | 故意静态故障成功复现，错误入口由build tag隔离 |
| 旧默认CC：`go -C . test -race -count=1 ./...` | **历史exit=1，构建失败**；没有执行测试 | 已解决的默认32位C编译器限制，非代码行为失败 |
| 隔离64位CC：`go -C . test -race -count=1 ./...` | **最终exit=0**；cmd/demo无测试，internal/lesson=10.787s，无race报告 | 四项测试在Windows/amd64实际运行；本次未检测到数据竞争 |

最后一次完整demo的无秘密原始日志在 `.local/demo.log`；核心复跑在 `.local/core-http.log`；最终整模块测试在 `.local/test.log`；分项测试在 `.local/test-verbose.log`；默认race失败在 `.local/race-initial.log`、修复后的race成功在 `.local/race.log`。这些是本机辅助证据，课程关键结果已归纳在下表与README，交付不依赖私有日志被Git提交。

## 实测关键业务事实

| 路径 | 本次实际结果 | 机制保证/未知边界 |
| --- | --- | --- |
| tree | A先取消后保留自己的Cause；B继承rootCause；owner看见observed时done未关闭，放行cleanup才收到done | 通知、观察、清理、join是分别观测的事实 |
| HTTP成功 | JSON回执Order正确、Total=1、真实count=1；入站原ctx无deadline，header重建自己的期限 | 不以HTTP200代替回执语义和状态查询 |
| HTTP主动取消 | client错误链Canceled；服务端观察取消后退出；count=0 | 本次gate没开放，因此效果未发生；不保证已提交效果可撤回 |
| HTTP超时后晚效果 | 400ms客户端DeadlineExceeded；handler仍等待；随后放行，count从0变1 | 超时后的客户端结果UNKNOWN，后续独立状态查询确定已执行 |
| 无防重重试 | retry先count=1，原请求后count=2 | 盲重试写操作会重复效果 |
| 固定键重试 | 最终count=1；原请求晚执行得到Duplicate=true | 只证明本进程Mutex/账本原子防重，不证明持久化或多实例 |
| 继承总预算 | deadline三层=t0+900ms；queue=300ms、execution=599ms、total=900ms；effects=0、DeadlineExceeded | deadline相等和副作用为零是断言；整数耗时是本次观察 |
| 每层重置预算 | gateway deadline=t0+1200ms；原request到期时gateway仍活跃；晚150ms放行；queue=300ms、execution=750ms、total=1050ms；effects=1 | 原预算已结束后才放行成功，违反caller整链契约 |
| 排队预算到期 | queue=60ms、execution=0ms；DeadlineExceeded；HTTP started未关闭 | 没派发下游，整个预算已消耗在队列 |
| 不观察ctx | timeout后done仍未关闭；gate放行后effects=1、清理完成 | timeout不强杀函数/goroutine |
| Background循环/修复 | 危险版3个效果；修复版3个Canceled、0效果；两组均join3任务 | 每个worker都有Done监听，但parent选择决定传播范围 |
| 迟cancel/修复 | 一分钟child在业务结束后仍活跃；恢复owner调用cancel；修复callee返回前执行defer cancel | 没有刻意遗留timer；vet不证明cancel一定及时调用 |
| 取消后发送 | 危险版observed后卡在out，owner drain收到42才能退出；修复版Done分支退出 | 若发送与取消同时可用，select仍可能选发送 |
| WithoutCancel | raw无deadline、Done=nil、Err/Cause=nil，保留requestID；自有90ms任务到期、defer完成、owner join | 请求外任务另有owner/期限；无后台持久完成保证 |
| AfterFunc | 尚未开始stop=true；已开始后stop=false且done未完成；gate放行后done关闭 | stop不是join；明确屏障证明回调开始/结束 |
| 独立并发HTTP测试 | 同一订单/键的6个真实请求，最终效果1、Duplicate回执5 | 补充串行重试没覆盖的并发原子性 |

## race 编译器历史与已解决验证

默认 `CC=gcc` 实际来自 `D:/MinGW/bin/gcc.exe`。真实race构建诊断：

```text
# runtime/cgo
cc1.exe: sorry, unimplemented: 64-bit mode not compiled in
FAIL go-demo/lessons/m13_context_budgets/cmd/demo [build failed]
FAIL go-demo/lessons/m13_context_budgets/internal/lesson [build failed]
```

随后主agent提供统一的Windows/amd64工具链索引 `D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json`。本模块只读索引，只在执行测试的PowerShell进程设置CC/CXX、CGO_ENABLED=1和PATH前置bin_dir，未改Go版本、系统PATH或全局环境。

- CC：`D:/IdeaProject/go-demo/.local/toolchains/winlibs-16.2.0posix-14.0.0-ucrt-r2/mingw64/bin/gcc.exe`。
- CXX：同一bin目录的 `g++.exe`；bin_dir也是该目录。
- 实际命令：加载JSON的cc/cxx/bin_dir → 设置这四个进程环境项 → `go -C . test -race -count=1 ./...`。
- 最终工具结果：**exit=0**，`ok go-demo/lessons/m13_context_budgets/internal/lesson 10.787s`，没有数据竞争诊断。

README提供临时设置和finally恢复的复跑脚本。旧32位GCC失败不是当前阻塞；race通过仅说明本次测试运行未检测到数据竞争，不证明所有可能的调度或跨实例协议。

## 资源清理与实证边界

每组scope最多8秒、main总预算40秒、join独立上限2秒。Context cancel由创建它的owner调用；危险gate都由owner正常/失败路径放行；自建client/worker/callback均有done并在owner收尾join。handler有exited，在所有gate放行与handler结束后关闭本课Transport/listener。每次真实HTTP使用动态loopback端口，不占共享固定端口、不访问外网或学习服务器、没有遗留进程/故障代理/数据库对象。

本课实证为Go1.27.0 Windows下Context语义和真实HTTP JSON RPC，效果是本次进程中的合成扣款计数。`ExecSQL`仅编译/阅读核验，**没有实库driver、SQL取消、statement/lock timeout、事务提交未知结果、gRPC或net/rpc实际运行**。README解释这些契约与限界，未把HTTP实验冒充SQL/MQ验收。网络传剩余预算header也未精确扣除传输耗时，不宣称多机器严格同一deadline或持久exactly-once。
