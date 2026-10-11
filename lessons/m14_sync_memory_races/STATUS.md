# M14 实际交付与验收记录

当前状态：**IMPLEMENTED / LIVE VERIFIED：本模块全部教学、正常/故障/修复实验及 race 补验完成，交付冻结**。这是执行者的事实记录，主 agent 尚未进行整层独立验收/提交。

执行日期：2026-10-12（Asia/Shanghai）。唯一写入范围：`C:/Users/Lenovo/.codex/worktrees/go-course-m14/go-demo/lessons/m14_sync_memory_races/`。下面所有命令工作目录均为此 module，除明确标注的只读版本检查。未 git add/commit/push，未修改其他模块、根 go.mod/go.sum/go.work、examples 或主目录 files。

## 环境与查证

| 项 | 实际值 / 证据 |
| --- | --- |
| Go | `go version go1.27.0 windows/amd64` |
| module / go 指令 | `go-demo/lessons/m14_sync_memory_races` / `go 1.27`，无 toolchain 指令与第三方依赖 |
| go env | `GOROOT=D:\Go`，`GOVERSION=go1.27.0`，`GOOS=windows`，`GOARCH=amd64`，`CGO_ENABLED=1`，`CC=gcc`，`GOTOOLCHAIN=auto`，`GOEXPERIMENT` 为空 |
| 原 PATH C 编译器 | `D:\MinGW\bin\gcc.exe`，`gcc -dumpmachine` 输出 `mingw32` |
| race 进程级编译器 | 主 agent 统一提供的 WinLibs `16.2.0posix-14.0.0-ucrt-r2`，GCC 16.2.0 / MinGW-w64 14.0.0；CC=`D:\IdeaProject\go-demo\.local\toolchains\winlibs-16.2.0posix-14.0.0-ucrt-r2\mingw64\bin\gcc.exe`，CXX 同 bin 的 g++.exe，仅设置当前命令环境；配置只读自主目录 `.local/course-orchestrator/toolchain.json` |
| 当前源码核验 | `D:/Go/src/sync/map.go` 的 `Map.m = internal/sync.HashTrieMap[any,any]`；读取 hashtriemap 的 Load/LoadOrStore/CompareAndSwap/Range 路径；读取 once.go/oncefunc.go/pool.go/atomic/doc.go 与 mutex/rwmutex 契约 |
| 官方在线查证 | [内存模型](https://go.dev/ref/mem)、[sync](https://pkg.go.dev/sync)、[atomic](https://pkg.go.dev/sync/atomic)、[race detector](https://go.dev/doc/articles/race_detector)；网页包文档当前展示 go1.27.2，本课实现依据本机 go1.27.0 源码，不升级学习者工具链 |
| 材料只读 | worktree `examples/advanced-syntax/sync-primitives`；主目录 `files/语法进阶/Sync`、`files/原理剖析/数据结构/Sync.Map`（二者实际为无扩展名文件） |
| 旧材料纠正 | readOnly/dirty 属于 Go 1.17；末尾手工 missLocked 缺阈值判断；atomic 非线性性能保证；Once 非永久内存驻留；Pool.New 是回调、复用/移除时机不保证 |

## 已执行的正常验证

| 命令 | 观察点 | 真实结果 |
| --- | --- | --- |
| `gofmt -w inventory.go snapshots.go map_once_pool.go io_order.go lesson_test.go cmd/demo/main.go cmd/fault/main.go cmd/probe/main.go internal/faults/faults.go` | 手写 Go 正常/标签源码格式 | 完成；后续修改的 Go 文件也分别 gofmt |
| `go run ./cmd/demo`；最终另执行 `go -C 'C:/Users/Lenovo/.codex/worktrees/go-course-m14/go-demo/lessons/m14_sync_memory_races' run ./cmd/demo` | E01～E09 全路径与 owner 收尾 | 均退出 0，`DEMO PASS`；原子与 sync.Map Load/Store 均实际超卖 2 张单，Mutex/CAS 卖 1 件且守恒；最终含 handler 已收到请求后的真实取消 |
| `go test -v ./...` | 独立业务/协议/对象断言 | 9 项测试 PASS；本课包及 demo/probe 编译通过，faultcases 默认排除 |
| `go vet ./...` | 正常源码误复制同步对象等检查 | 退出 0，无诊断 |
| `go vet -tags=faultcases ./internal/faults` | 故意错误的值接收者与两种 struct 复制 | 预期退出 1；`add passes lock by value` 一条，`assignment copies lock value` 两条 |
| `git diff --check` | 本 worktree 范围空白错误 | 退出 0；新增文件仍未暂存，最终还需主 agent 整层核对 |

首次正常组合检查发现 `cmd/probe` 局部变量 `raced/traced` 命名不一致，导致 test/vet 编译失败；已统一名称并真实复跑成功。此失败不是正常 demo 的业务失败，未把最初检查写成通过。

9 项测试的独立事实：库存 0/1/7/100 对 200 购买者售罄且守恒；两个指针别名共享一把锁售出 300 件；调用前取消不扣减；RW 两个 reader 同持读锁时 writer 不完成；slice 输入和读结果不能改内部数组；4 写者共 200 版更新不丢失、每次读保持 version/limit/label 同版；sync.Map 单 key 创建与支付各一位赢家、Range 两 key 各访问一次并最终都写成 2；Once 完成/失败缓存与成功后缓存的重试策略；Pool 任何来源的干净对象可见长度 0；真实 HTTP 正常/已收到请求后取消两路径与锁范围；400 次双向转账余额 1000/1000/2000；Value nil、动态类型、map CAS 错误有真实 panic。

本次正常关键日志（Range 键顺序与 Pool 复用属于观察，不是断言）：

```text
E01 atomic Load/Store: initial=1 remaining=0 sold=2 conserved=false
E02 Mutex: {Initial:1 Remaining:0 Sold:1} conserved=true；CAS 同样守恒
E03 RWMutex: readers=2 writer-before-release=false final=1；slice 隔离=[phone case]
E04 snapshots: v1/10/v1 与 v2/20/v2；atomic 发布普通字段="sku-1:完整配置"
E05 sync.Map: sold=2 conserved=false；candidates=8 create-winners=1 pay-winners=1 state=paid
E05 Range: map[left:1 right:2] mixed=true（另一次运行键顺序相反）
E06 Once: NormalCalls=1 AllReady=true PanicInitCalls=1 ErrorInitCalls=1 RetryCalls=2 RetryValue=ready
E07 Pool: dirty-reused=true dirty-len=15 clean-len=0 New-before-GC=1 after-GC=2
E08 real HTTP: hold=true observer-acquired=false body="priced"
E08 real HTTP: hold=false observer-acquired=true body="priced"
E08 real HTTP cancel-at-gate: observer-acquired=false error-is-canceled=true（请求实际退出后返回）
E09 lock-order: a=100 b=100 total=200
```

最终普通/race demo 和 9 项测试都实际覆盖真实 HTTP `cancel-at-gate` 路径。

## 已执行的有界故障验证

先 `go build -tags=faultcases -o .local/fault-native.exe ./cmd/fault`，再由 PowerShell Start-Process（隐藏窗口，stdout/stderr 独立重定向）逐一执行模式，`WaitForExit(5000)`；超时会 Kill 并 Wait，且实验判失败。不是直接无限等待坏程序。

| 模式 | 实际退出 / 诊断 | 业务或同步错误定位 |
| --- | --- | --- |
| map | 2 / `fatal error: concurrent map writes`（诊断含 concurrent map） | 两 worker 并发改同一普通 map |
| copy-locked | 2 / `fatal error: all goroutines are asleep - deadlock!` | 副本 Mutex 持有已锁状态，原对象解锁不能解开副本 |
| reentry | 2 / 同上 | 同 goroutine 再 Lock 同一 Mutex |
| rw-upgrade | 2 / 同上 | 持读锁再取写锁，等待自己的 RUnlock |
| lock-order | 2 / 同上 | 两 worker 各持一锁等对方，owner Wait |

上述 5 个进程均在 5 秒界内真实退出，非超时替代诊断。原始 stdout/stderr 留在本课已忽略的 `.local/native-*.log`，摘要在此保留；临时 native 二进制已用 LiteralPath 删除，不纳入课程提交。

## race 原始失败与已完成补验

已执行 `go test -race -count=1 -timeout=30s ./...`，真实失败：

```text
# runtime/cgo
cc1.exe: sorry, unimplemented: 64-bit mode not compiled in
FAIL ... [build failed]
```

原因是 PATH 的 32 位 MinGW。主 agent 随后统一准备经核验的私有 portable amd64 编译器，避免多个 module 重复下载或修改全局环境；原失败作为已解决历史保留。本课实际只使用进程级 CC/CXX/CGO_ENABLED/PATH 复验，没有执行 `go env -w` 或修改全局 PATH。

实际补验命令前的进程环境设置：

```powershell
$taskToolchain = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' -Raw | ConvertFrom-Json
$env:CC = $taskToolchain.cc
$env:CXX = $taskToolchain.cxx
$env:CGO_ENABLED = '1'
$env:PATH = $taskToolchain.bin_dir + ';' + $env:PATH
```

将这段放在专门用于本课的 PowerShell 进程中执行；关闭进程即结束环境作用范围。跨机器复跑可把 CC/CXX 换为自己的 amd64 C 编译器。

| 已补验命令（工作目录同上） | 真实结果 / 观察 |
| --- | --- |
| `go test -race -count=1 -timeout=30s ./...` | 退出 0，本课包 `ok ... 13.749s`，demo/probe 编译通过；9 项独立断言通过，无 DATA RACE |
| `go run -race ./cmd/demo` | 退出 0，`DEMO PASS`；E01/E05 超卖照样存在但无 data race；Mutex/CAS/snapshot/转账不变量同时通过；真实 HTTP 正常与取消收尾通过 |
| `go run ./cmd/probe` | 退出 0，`PROBE PASS: 5 fatal + 7 race + copylocks`；各子进程在 5 秒上限内实际退出，构建在各 60 秒上限内完成，vet 在 30 秒内返回预期拒绝；完整诊断记录 `.local/probe.log` |

完整 probe 的实际摘要：

```text
PASS fatal map               diagnostic="concurrent map"
PASS fatal copy-locked       diagnostic="all goroutines are asleep - deadlock"
PASS fatal reentry           diagnostic="all goroutines are asleep - deadlock"
PASS fatal rw-upgrade        diagnostic="all goroutines are asleep - deadlock"
PASS fatal lock-order        diagnostic="all goroutines are asleep - deadlock"
PASS race  map               diagnostic="WARNING: DATA RACE"
PASS race  different-lock    diagnostic="WARNING: DATA RACE"
PASS race  value-receiver    diagnostic="WARNING: DATA RACE"
PASS race  copy-struct       diagnostic="WARNING: DATA RACE"
PASS race  atomic-value-map  diagnostic="WARNING: DATA RACE"
PASS race  slice             diagnostic="WARNING: DATA RACE"
PASS race  visibility        diagnostic="WARNING: DATA RACE"
PASS vet   copylocks: expected rejection
PROBE PASS: 5 fatal + 7 race + copylocks
```

probe 对 race 子进程独立核对 `ExitCode()==66`，不是看到打印字样就判成功。栈定位：普通 map 的 66/70 行；不同锁 84/90；值接收者 108；已使用 struct 副本 137/143；Value 内 map 214 与 mapaccess；共享数组 233/238；无同步发布 ready 252/257。完整源码位置以当前 [faults.go](internal/faults/faults.go) 为准，README 给出各修复符号。

race demo 的 Pool 实际观察 `dirty-reused=false dirty-len=0 clean-len=0 New-before-GC=2 after-GC=3`，与普通 demo 的复用不同，两个结果都符合 API。Range 本次 `map[left:2 right:1] mixed=true`；不把具体键顺序或必现混合设为断言。

## 资源与交付边界

正常实验每次返回前等待其拥有的 goroutine；真实 HTTP Body Close、CloseIdleConnections、响应门 release 和 server.Close 均在正常/取消/错误分支完成。故障子进程只接触本进程内合成状态，无外部业务数据；probe 的二进制位于它独占、核对过的绝对临时路径，递归清理由 owner 检查错误并完成。不安装中间件、改全局 Go 环境或占用固定端口。

README、详细中文源码、9 项独立断言、正常和故障入口已落盘，没有 exercise。本模块关键实验没有遗留未通过项；待主 agent 独立验收与整层交付。所有 exec 会话均已退出，probe 临时二进制由 owner 删除且清理错误会使命令失败；本课 `.local` 只保留忽略的诊断日志。未测所有 CPU/Go 历史版本、生产性能或跨进程外部支付事务；当前代码及实验没有承诺这些结果。
