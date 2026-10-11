# M02 实际交付与验证状态

状态：课程源码、中文解析、反例修复和独立断言已完整交付；正常运行/test/vet 与实际 race 复验均通过；显式坏例和诊断已实际复现。初次 32 位 GCC 限制已由主 agent 提供的便携 64 位工具链解决，历史失败证据保留。模块已冻结交付，主 agent 的整层验收与提交不由本模块自行宣称。

验证日期：2026-10-12（Asia/Shanghai）。所有写入仅位于本模块目录，没有 git add/commit/push，没有更改根 go.mod/go.sum、其他模块、原 examples/files 或共享调度状态。

## 环境与证据范围

实际工作目录与目标目录均为：

```text
C:/Users/Lenovo/.codex/worktrees/go-course-m02/go-demo/lessons/m02_values_methods
```

| 项目 | 当前实际值 |
| --- | --- |
| `go version` | `go version go1.27.0 windows/amd64` |
| `go env GOVERSION / GOOS / GOARCH` | `go1.27.0 / windows / amd64` |
| `go env GOTOOLCHAIN / GOROOT` | `auto / D:\Go` |
| 模块 `GOMOD` | 本模块目录的 `go.mod`，已单独核对 |
| module / go 指令 | `go-demo/lessons/m02_values_methods` / `go 1.27` |
| toolchain 指令 | 未设置 |
| 第三方依赖 | 无，仅标准库；没有生成 go.sum |
| 默认 shell 的 `CGO_ENABLED / CC` | `1 / gcc` |
| 默认 gcc / target | `D:\MinGW\bin\gcc.exe` / `mingw32` |
| 最终 race 的实际 CC | `D:\IdeaProject\go-demo\.local\toolchains\winlibs-16.2.0posix-14.0.0-ucrt-r2\mingw64\bin\gcc.exe` |
| 最终 race 的实际 CXX | 同目录 `g++.exe`；CGO_ENABLED=1 |
| 最终 race 的编译器/target | WinLibs GCC 16.2.0 / `x86_64-w64-mingw32` |

已完整读取 ORCHESTRATION、TEACHING_STYLE、M02 prompt、根 README/go.mod、指定 examples 与 files 文本；适用祖先 `C:/Users/Lenovo/.codex/AGENTS.md` 实际为 0 字节，worktree/模块内没有额外 AGENTS.md。使用 go-learning-by-running 的教学方式，不创建子 agent。

当前规范和库核验：浏览了 Go 官方 spec 的方法/方法值/表达式/allocation 章节、Go 1.26/1.27 发布说明、sync 文档；另执行 `go doc builtin.new`、`builtin.make`、`sync.Mutex/WaitGroup/Once/Pool/Map/Cond/RWMutex` 与 `sync/atomic.Int64`，核对本机 `D:/Go/VERSION` 和上述 sync 源码契约。没有凭旧资料混用 new API。

## 已执行命令与实际结果

下面的 `<module>` 均表示上方完整绝对路径。普通入口、test、vet 在本模块工作目录运行；也保留可直接从根目录复跑的命令在 README。

| 命令 | 退出/状态 | 观察点与实际结果 |
| --- | --- | --- |
| `gofmt -w <本模块全部 .go 文件>` | 0 | 源码、测试与 testdata 反例均已格式化 |
| `go -C <module> run ./cmd/demo` | 0 | E01～E06 全部返回；对象共享/克隆/方法绑定/nil 对照输出如下 |
| `go -C <module> test -v -timeout 20s ./...` | 0 | 9 个 Test 全部 PASS；本次 lesson 包 `ok ... 0.696s` |
| `go -C <module> test -v -count=1 -timeout 20s ./...` | 0 | 补强值接收者及 Labels 独立断言后重验；9 个 Test 全部 PASS，`ok ... 4.840s` |
| `go -C <module> vet ./...` | 0 | 正常包无诊断，故意坏例 testdata 默认不纳入 |
| `go -C <module> run ./testdata/copylocks` | 0 | 值接收者 caller=0、expected=2；共享 count 与不同锁均 true |
| `go -C <module> vet -copylocks ./testdata/copylocks` | 1，预期故障 | 两条诊断精确定位 Add 值接收者与首次使用后赋值复制 |
| `go -C <module> run ./cmd/nilpanic` | go run 返回 1；子程序显示 `exit status 2` | 真实 nil 字段访问 panic，栈定位 `(*Order).SetStatus` 与 `main.main` |
| 初次 `go -C <module> test -race -count=1 -timeout 20s ./...` | 1，已解决的环境限制 | runtime/cgo 构建失败，32 位 gcc 不支持 64 位模式；原始失败仍保留 |
| 便携 CC 下 `go -C <module> test -race -v -count=1 -timeout 40s ./...` | 0 | 9 个 Test 全部 PASS，无 DATA RACE 报告，lesson 包 `ok ... 2.326s`；本 agent 自己实际执行 |

独立测试逐项结果：`TestCounterConcurrent`、`TestValuePointerAndRebind`、`TestCloneIsolation`、`TestClonePreservesEmptyAndNil`、`TestMethodsBinding`、`TestMapWritebackBoundary`、`TestLocalAddressAndNewExpression`、`TestNilReceiver`、`TestRunSelection` 均 PASS。测试直接断言对象状态和隔离边界，没有把日志匹配作为业务正确性依据。

### 正常入口的关键输出

```text
top-level-distinct=true line-shared=true buyer-shared=true
after ModifyValue caller id=ORDER-1001 status=created qty=2 review=ok buyer=Bob
after ModifyValue returned-copy id=ORDER-1001 status=paid qty=2 review=ok buyer=Bob
after ModifyPointer caller id=ORDER-1001 status=shipped qty=3 review=sent buyer=Carol
after RebindPointer caller-still-original=true returned-new-target=true
caller assigned return: target=ORDER-REPLACEMENT old-order=ORDER-1001
array-copy original-qty=1 copied-qty=7 element-distinct=true
map value before writeback=paid temporary=shipped
map value after writeback=shipped pointer-map=shipped slice=slice-element
method value status=interface-write expression-now=latest shared-buyer=UpdatedBuyer
pointer method value first=bound-first method expression second=expression-second
new(T) type=*lesson.Order status="" buyer-nil=true map-nil=true slice-nil=true
new(expr) order=initialized answer-type=*int answer=42
new(existing-order) top-level-distinct=true line-shared=true original-qty=8
nil slice append=[5] nil map read="" write-panicked=true repaired-map=fixed
make channel type=chan int received=7 drained-closed=true
deep line-distinct=true buyer-distinct=true
deep original isolated id=ORDER-1001 status=created qty=1 review=pending buyer=Alice
deep changed-copy id=ORDER-1001 status=paid qty=2 review=ok buyer=Bob
append at full cap original-len=1 copy-len=2 original-qty=1 copy-qty=99
clone preserves nil slice/map/buyer=true/true/true
fixed caller=2 expected=2 alias-target-same=true
guarded nil status=<missing-order>
unguarded-pointer-method-panicked=true nil-to-value-method-panicked=true caller-continued=true
```

### 隔离坏例的真实输出

复制锁运行（省略每次不同的十六进制地址）：

```text
[BAD value-receiver] local=1 local-lock=<本次副本锁地址>
[BAD value-receiver] local=1 local-lock=<另一次副本锁地址>
[BAD state] caller=0 expected=2 caller-lock=<原锁地址>
[BAD identity] shared-count=true distinct-lock=true copy-acquired-while-original-held=true count=9
```

`go vet -copylocks ./testdata/copylocks` 的两条原诊断：

```text
testdata\copylocks\main.go:20:15: Add passes lock by value: go-demo/lessons/m02_values_methods/testdata/copylocks.Counter contains sync.Mutex
testdata\copylocks\main.go:48:20: assignment copies lock value to copiedAfterUse: go-demo/lessons/m02_values_methods/testdata/copylocks.SharedCounter contains sync.Mutex
```

nil 故障入口：

```text
panic: runtime error: invalid memory address or nil pointer dereference
goroutine 1 [running]:
go-demo/lessons/m02_values_methods/internal/lesson.(*Order).SetStatus(...)
main.main()
exit status 2
```

`SharedCounter` 故意违反首次使用后不能复制的契约，其 TryLock 成功属于本次运行观察。不能把这个输出当成语言永久保证或生产环境可用策略。

## race 工具链修复、未验证范围与资源清理

初次 race 命令的精确错误是：

```text
# runtime/cgo
cc1.exe: sorry, unimplemented: 64-bit mode not compiled in
FAIL go-demo/lessons/m02_values_methods/cmd/demo [build failed]
FAIL go-demo/lessons/m02_values_methods/cmd/nilpanic [build failed]
FAIL go-demo/lessons/m02_values_methods/internal/lesson [build failed]
```

初次只读核查 `Get-Command gcc,clang,zig -All` 只找到该 32 位 GCC；当时检查的常见 C:/D:/msys64 的 ucrt64/mingw64、C:/Program Files/LLVM、D:/MinGW64、D:/mingw64 候选均不存在。随后主 agent 统一准备兼容 64 位便携工具链，并提供 `D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json`。本 agent 只读该记录，解析 cc/cxx/bin_dir，并用 Resolve-Path 取得实际绝对路径，没有重复下载或安装。

该记录标明 WinLibs tag `16.2.0posix-14.0.0-ucrt-r2`、GCC 16.2.0、MinGW-w64 14.0.0，包 SHA256 为 `d5dbafc4a170e762ca6143151ec918fb9e2c72736fb14cd704abebc6bdd5276a`。下载和包摘要核验由主 agent 负责；本 agent 未重新下载或重新核算压缩包摘要。实际执行该 CC 的 `-dumpmachine` 与 `--version` 分别得到 `x86_64-w64-mingw32` 与 GCC 16.2.0，当前进程 `go env CC CXX CGO_ENABLED` 确认使用上述实际路径与 1。

本 agent 最终命令为 `go -C <module> test -race -v -count=1 -timeout 40s ./...`，9 个测试全部 PASS，输出 `ok go-demo/lessons/m02_values_methods/internal/lesson 2.326s`，退出码 0，没有 DATA RACE 报告。主 agent 在通知工具链时另报其独立复验 PASS（1.961s）；本状态的完成依据是本 agent 自己随后运行的 2.326s 记录。

CC/CXX、CGO_ENABLED 和 PATH 仅在执行该命令的 PowerShell 进程中临时设置，try/finally 恢复原值；没有 go env -w、setx 或全局配置修改，没有通过关闭 CGO 绕过 race。默认 32 位 GCC 仍是原有环境，所以不读取课程工具链记录直接复跑 race 时，可能再次遇到历史错误；README 已给出当前学习机的临时配置命令。

race 证明范围为本次运行覆盖到的实际访问路径，不能推出所有未来扩展代码都无竞争；违反首次使用后不可复制契约的坏锁程序没有并发写入，也不用于制造 race。当前没有剩余未完成的本模块关键验证项。

其他明确范围：没有验证生产订单、实库或网络协议；本课不声称做过这些。没有承诺固定十六进制地址、worker 调度顺序、锁公平性、逃逸位置、分配次数、违反 sync 契约后的所有运行时结果，也没有实现通用循环对象图的深克隆。

资源结局：所有正常入口与坏例进程均已结束；业务数据仅在进程内。demo 无 worker、无外部服务/端口/文件写入。局部 make channel 已由创建者关闭，nil channel 未被发送/接收。并发测试 4 个 worker 均 Done，Wait 返回后 owner 取得 finished 并停止 timer；Counter 的每次成功 Lock 都有 defer Unlock。坏锁示例只有单 goroutine，TryLock 非阻塞，成功锁均 Unlock；nilpanic 没有后台任务或外部资源需要额外清理。
