# M35 实际交付与验证记录

记录日期：2026-10-12，Asia/Shanghai。执行目录只限 managed worktree 的 `lessons/m35_performance_runtime/`。

课程源码、中文注释、README 与所有实验已交付；普通 run/test/vet、默认及禁内联逃逸诊断、CPU/heap/alloc/mutex/block profile 已实际执行。**race 初次执行因本机 C 编译器架构失败；使用主 agent 提供的 amd64 工具链后，补验已实际 PASS。** 本模块规定的实施和验证范围已完成。本文件记录执行事实，不代表整层已经 ACCEPTED 或已提交。

## 环境与隔离

- Worktree：`C:/Users/Lenovo/.codex/worktrees/go-course-m35/go-demo`。
- 模块：`go-demo/lessons/m35_performance_runtime`，`go.mod: go 1.27`，无 toolchain 指令、无第三方依赖、无硬前置。
- 实际 Go：`go version go1.27.0 windows/amd64`，GOROOT=`D:/Go`，GOAMD64=`v1`，CGO_ENABLED=`1`，GOTOOLCHAIN=`auto`。
- GOEXPERIMENT 环境变量为空；当前 `internal/buildcfg/exp.go` 默认 `GreenTeaGC: true`，没有调整开关。
- CPU：AMD Ryzen 7 7840H，8 核/16 逻辑 CPU；物理内存 33,489,801,216B。
- 主入口实际 NumCPU=16、GOMAXPROCS=16、Windows affinity mask=65535，GOMAXPROCS 环境变量为空。Linux cgroup quota 不适用；Windows Job CPU 限制未测。
- 第 0 层安排 19 个模块并行，采样时没有证明机器安静、CPU 频率恒定或没有邻居负载。ns/op 不当作正式稳定收益；主 agent 在层完成后安排串行复验。
- 子进程只调整自己的运行时参数，顺序执行；固定最大 live 数据约 13MiB/保留 body 32MiB，累计 churn 96MiB。入口 60s、每个内存子进程 25s、OS 查询 10s、CPU profile 0.2..5s；没有远端服务/端口/共享资源变更。

## 入口、正确性和静态验证

工作目录为 worktree 根时实际执行：

```powershell
go -C lessons/m35_performance_runtime run ./cmd/demo
go -C lessons/m35_performance_runtime test ./...
go -C lessons/m35_performance_runtime vet ./...
```

结果：全部退出 0。默认 run 的 E1..E7 均完成；test 的 cmd/demo 无测试文件，lab 包通过；vet 无诊断。增加并发 Pool 断言后，在模块目录再次 `go test -count=1 -v ./...` 和 `go vet ./...`：六项实际测试全部 PASS，lab 包耗时 0.802s。`gofmt -l` 对全部七个手写 Go 文件无输出。

六项独立断言：

| 测试 | 实际证明的事实 |
| --- | --- |
| TestKeyContract | fmt/append/pool 全部满足独立十进制订单键协议，包含 int64 极值及 500 组数据 |
| TestRetentionOwnership | alias/cap 看到原数组修改，clone 保持独立内容 |
| TestBufferOwnership | 归还前清零所有写入字节，长度归零，已返回字符串不受后续复用影响 |
| TestPoolConcurrentRequests | 4 个 worker 各 100 次共享 Pool 请求，键内容属于本请求；owner 等全部退出 |
| TestEscapeSemantics | 局部及跨调用保留的指针/接口/闭包在 Go 语义上给出正确值 |
| TestWorkersComplete | 4 个递归 worker checksum=16900；两个 batch 总数=200000/锁次数准确；已取消和非法输入返回错误 |

本次最后一次正常入口的真实摘录（完整运行输出留在忽略的 `.local/demo-final.txt`）：

```text
E1 key fmt="shop:42/order:9001" append="shop:42/order:9001" invariant=PASS
E2 source=B alias=B cap_trap=B clone=A cap=16 invariant=PASS
E3 borrowed_after_reset=0 owned="secret-order" pool_key="shop:42/order:9001" invariant=PASS
E4 stack before=131072 deep=1179648 after=131072 checksum=16900 invariant=PASS
E5 batch=1 total=200000 lock_calls=200000 observation=2.049ms invariant=PASS
E5 batch=100 total=200000 lock_calls=2000 observation=514.2µs invariant=PASS
E6 alias logical=128 heap=33956816 runtime=40823032 working_set=44015616 os_error="" checksum=576
E6 cap logical=128 heap=33951944 runtime=41076984 working_set=44134400 os_error="" checksum=576
E6 clone logical=128 heap=402640 runtime=29182200 working_set=31805440 os_error="" checksum=576
E6 retention_content_and_heap_gap=PASS (cap 限制没有释放大数组)
E7 GOGC=50 limit=9223372036854775807 live=12582912 churn=100663296 work_gc=13 allocated=100663408 work_ms=6.86 pause_ms=0.54 heap=14031720 runtime=29673720 working_set=30871552 os_error="" checksum=199680
E7 GOGC=200 limit=9223372036854775807 live=12582912 churn=100663296 work_gc=3 allocated=100668392 work_ms=11.07 pause_ms=0.00 heap=14031880 runtime=52283640 working_set=51277824 os_error="" checksum=199680
E7 GOGC=100 limit=8388608 live=12582912 churn=100663296 work_gc=319 allocated=100691176 work_ms=73.13 pause_ms=16.21 heap=14058432 runtime=21391608 working_set=23212032 os_error="" checksum=199680
E7 workload_content_and_soft_limit=PASS (GC 次数/耗时关系仅作本次观察)
```

README 的 GC 表格保留第一次 profile 生成时的样本（13/3/344 周期、7.37/11.52/68.14ms），上面是最终普通入口的第二组实际样本。两组都说明低于 live 数据的软限制没有成为硬上限；两组 GOGC=200 的 wall time 都比 GOGC=50 长，本课没有据此宣布高/低 GOGC 总更快。`pause_ms=0.00` 是格式化显示精度，不是无 GC 成本。

故障入口也已实际运行：模块目录 `go run ./cmd/demo -mode gc -gogc 0` 返回非零，输出 `有界 GC 实验要求 GOGC=[1,1000]、limit>=8MiB` 和 `exit status 1`。没有分配实验 live 数据或改变共享设置，后续普通入口仍可用。

## 五样本 benchmark

以下命令工作目录均为模块目录：

```powershell
go test -run '^$' -bench '^Benchmark(KeyFmt|KeyAppend|BadSetup|CorrectedSetup|DiscardedLegacy|Escapes)$' -benchmem -benchtime=250ms -count=5 -cpu=1 ./internal/lab
```

实际退出 0，17.772s。完整输出本机保存 `.local/benchmark.txt`。Key 输入每次在订单号 9001..9064 轮转，构建为正常优化、未启用 race/CPU profiler/高采样内存 profiler。

| Benchmark | 五次 ns/op | B/op、allocs/op（各次相同） |
| --- | --- | --- |
| KeyFmt | 80.29 / 91.09 / 95.12 / 92.88 / 118.2 | 32、2 |
| KeyAppend | 29.36 / 26.08 / 25.86 / 24.34 / 29.35 | 24、1 |
| BadSetup | 2586 / 3849 / 2586 / 3255 / 2540 | 32、2 |
| CorrectedSetup | 92.95 / 92.27 / 94.20 / 87.06 / 83.74 | 32、2 |
| DiscardedLegacy | 0.2142 / 0.2211 / 0.2068 / 0.2195 / 0.3448 | 0、0 |
| Escapes/pointer_local | 0.4648 / 0.6439 / 0.5397 / 0.6829 / 0.7406 | 0、0 |
| Escapes/interface_local | 0.7976 / 0.7622 / 0.7582 / 0.8896 / 0.8715 | 0、0 |
| Escapes/closure_local | 0.6645 / 0.5726 / 0.4358 / 0.4226 / 0.4621 | 0、0 |
| Escapes/pointer_retained | 6.762 / 5.263 / 5.429 / 5.998 / 5.274 | 8、1 |
| Escapes/closure_retained | 8.360 / 8.592 / 8.424 / 11.22 / 13.11 | 16、1 |

最初 interface_retained 用常量 1024，五次为约 0.8430..0.8764ns、0B/0alloc。真实结果指出常量装箱可以静态储存；没有把它伪改成“必然分配”。课程将它明确命名为 `interface_constant_retained`，另增加变化的动态 n。只复跑受影响的 interface benchmark：

```powershell
go test -run '^$' -bench '^BenchmarkEscapes/interface' -benchmem -benchtime=250ms -count=5 -cpu=1 ./internal/lab
```

实际退出 0，5.320s，输出 `.local/interface-benchmark.txt`：

| Benchmark | 五次 ns/op | B/op、allocs/op |
| --- | --- | --- |
| interface_local | 0.4143 / 0.4169 / 0.5372 / 0.5788 / 0.5744 | 0、0 |
| interface_constant_retained | 0.7791 / 0.6900 / 0.6442 / 0.6531 / 0.6368 | 0、0 |
| interface_retained（动态 n++） | 7.267 / 8.604 / 10.11 / 8.140 / 7.800 | 8、1 |

当前结论是稳定复现少一次键分配及正确内容；ns/op 收益限定此平台、输入和并发样本，尚不作串行受控环境的稳定收益声明。local 路径只用于分配反例，不代表真实服务请求成本。

## 编译器与反汇编

```powershell
go build -gcflags='go-demo/lessons/m35_performance_runtime/internal/lab=-m=2' -o .local/demo.exe ./cmd/demo 2> .local/escape.txt
go build -gcflags='go-demo/lessons/m35_performance_runtime/internal/lab=-m=2 -l' -o .local/demo-noinline.exe ./cmd/demo 2> .local/escape-noinline.txt
go test -run '^$' -gcflags='go-demo/lessons/m35_performance_runtime/internal/lab=-m=2' -o .local/diagnostic.test.exe ./internal/lab 2> .local/test-escape.txt
go tool objdump -s 'lab.BenchmarkDiscardedLegacy' .local/lab.test.exe
```

以上实际执行成功。默认输出包含 PointerLocal/BoxLocal/ClosureLocal 可以内联；`new(Totals) does not escape`、`n does not escape`；PointerRetained 的 `&Totals{...}`、BoxRetained 返回的 n、ClosureRetained 的函数值流向外部；KeyFmt 的数字参数逃逸摘要；KeyAppend append 不逃逸但返回 string 流向外部。禁内联输出作为当前决策对照，不用于性能数据。

针对测试包的诊断还实际输出两处 `make([]Order, 4096) does not escape`。DiscardedLegacy 实际汇编只有循环计数的 `INCQ/CMPQ/JG`，没有 PointerLocal 的真实调用/对象工作；0.2ns 不是优化收益。`runtime/iface.go: convT64/staticuint64s` 在本机源码实际定位，解释静态小整数存储与 escaping 摘要不等于每次 malloc。

## 实际 profile

profile 生成、pprof 解析均为真实执行。忽略目录 `.local/profiles/` 中保留本机文件；README 给出可重新生成的完整命令。

| profile/命令 | 观察点与真实结果 |
| --- | --- |
| `go run ./cmd/demo -profile-dir .local/profiles` | 第一次 E6/E7 完整通过；生成 heap-alias/cap/clone |
| `go tool pprof -top -sample_index=inuse_space ... heap-alias.pprof` | `retainOne` 存活 32MiB，占采样总量约 99.83% |
| 同上 `heap-cap.pprof` | 仍为 `retainOne` 32MiB，限制 cap 没有解决保留 |
| 同上 `heap-clone.pprof` | 没有存活的 retainOne 大数组；采样总存活约 55.11KiB，不能与 MemStats 逐字节等同 |
| `.local/demo.exe -mode cpu -variant fmt -profile-dir .local/profiles -duration 2s` | 操作 15,331,328 次，checksum=1,131,109,952，CPU profile 完成 |
| 同上 `variant append` | 操作 58,900,480 次，checksum=4,377,011,768，CPU profile 完成 |
| `go tool pprof -top ... cpu-fmt.pprof` | CPU 样本 2.24s；Sprintf cum=1.66s/74.11%，doPrintf/整数处理/分配可定位 |
| 同上 `cpu-append.pprof` | 样本 2.19s；KeyAppend cum=1.59s/72.60%，热点转移为 AppendInt、复制、字符串结果 |
| KeyFmt benchmark 的 `-memprofile`，`pprof -sample_index=alloc_space` | 233.01MB 累计采样；Sprintf flat≈180.50MB，KeyFmt flat≈51.50MB；显示临时键字符串/装箱 |
| 动态 interface/closure benchmark 的 `-memprofilerate=1 -memprofile` | BoxRetained≈5183.34KiB，ClosureRetained≈8703.91KiB；这组 432.2/564.2ns 被高采样显著扰动，只定位分配 |
| `.local/demo.exe -mode mutex -batch 1 -profile-dir ...` | total=200000，LockCalls=200000；mutex 68.18μs 归属 Unlock 持锁路径 |
| 同上 batch100 | total=200000，LockCalls=2000；本次 mutex delay profile 为零，不能解释成永远无竞争 |
| `pprof -top ... block-batch1.pprof` | WaitGroup.Wait≈2264.92μs；Mutex.Lock≈119.85μs，区分 owner join 与争锁 |

CPU、mutex、block 中的 flat/cum 按对应 profiler 的口径解释，CPU sample 可能来自多个线程，累计可超过 wall time。profile 不是正式的 ns/op 比较，短 mutex 运行只有这一份定位样本，没有声称稳定绝对延迟。

第一次未将 PowerShell 的 `-memprofile=.local/...` 整体加引号，工具把路径识别成了额外包并失败；改成 `'-memprofile=.local/...'` 后真实生成成功，README 使用已验证写法。没有通过 go get 增加无关依赖。

## race 与未验证范围

首次实际命令 `go test -race ./...`，模块工作目录，失败：

```text
# runtime/cgo
cc1.exe: sorry, unimplemented: 64-bit mode not compiled in
FAIL go-demo/lessons/m35_performance_runtime/cmd/demo [build failed]
FAIL go-demo/lessons/m35_performance_runtime/internal/lab [build failed]
```

`where gcc` 为 `D:/MinGW/bin/gcc.exe`，`gcc -dumpmachine` 返回 `mingw32`，无法为 amd64 race 构建 runtime/cgo。没有将 race 数字混入性能结果，也没有把初次失败记作通过。

随后收到主 agent 已核验的 WinLibs amd64 portable 入口，只在本次 PowerShell 命令进程设置 CC/CXX、CGO_ENABLED=1 与 PATH 前置。没有改全局 PATH、注册表或 Go 配置。模块目录执行：

```powershell
$m35Toolchain = Get-Content -Raw -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' | ConvertFrom-Json
$env:CC = $m35Toolchain.cc
$env:CXX = $m35Toolchain.cxx
$env:CGO_ENABLED = '1'
$env:PATH = $m35Toolchain.bin_dir + ';' + $env:PATH
go test -race -count=1 -timeout 60s ./...
```

真实结果：cmd/demo 无测试文件，`internal/lab` **PASS，5.062s**，退出 0，没有 race 诊断。当前已交付的 6 项测试都实际跑过该 race 构建，包括共享 Pool 并发请求、barrier 栈实验及共享计数/取消路径。

实际 `CC -dumpmachine=x86_64-w64-mingw32`；`CC --version=gcc.exe (MinGW-W64 x86_64-ucrt-posix-seh, built by Brecht Sanders, r2) 16.2.0`。CC 路径为 `D:/IdeaProject/go-demo/.local/toolchains/winlibs-16.2.0posix-14.0.0-ucrt-r2/mingw64/bin/gcc.exe`，CXX 为同目录 `g++.exe`。主 agent 提供的工具链登记 SHA256 为 `d5dbafc4a170e762ca6143151ec918fb9e2c72736fb14cd704abebc6bdd5276a`；本模块复核了实际架构/版本并成功运行 race，没有自行下载/安装。完整成功输出保存在忽略的 `.local/race.txt`。

未验证：Linux cgroup/容器 CPU 容量、Windows Job CPU 预算、非 Go 分配与线上数据库/网络资源、应用真实 p99/生产负载、稳定的串行 benchmark 收益比。上述均不由普通编译、OS 内存查询或本机样本替代。

## 资源和文件结局

所有 goroutine 由 owner 的 WaitGroup 等完成；所有 channel 只有唯一 closer；CPU profiler Stop 后关闭文件；heap/mutex/block 同步写完关闭；OS 查询和实验子进程由 Output 等待回收。没有后台学习服务、端口、锁、故障进程或共享 DeepHelp 调整。`.local/` 仅有忽略的本机日志、profile 和编译文件，不进入整层提交。

只写本模块的 go.mod、.gitignore、README、STATUS、cmd/demo 和 internal/lab 文件。未修改根 go.mod、索引、原 examples/files 或其他模块，未创建子 agent，未 git add/commit/push。主 agent 负责最终独立验收、汇总与整层提交。

## 主 agent 独立复验补证


主 agent 在 2026-10-12 第 0 层审查时补做了一组串行复验：19 个课程子 agent 均已结束，复验期间没有同时派发其他构建，使用相同 Go 1.27.0、相同输入和 `-cpu=1`，每个样本计时 1 秒，共五个样本。

```powershell
go -C lessons/m35_performance_runtime test -run '^$' -bench '^Benchmark(KeyFmt|KeyAppend)$' -benchmem -benchtime=1s -count=5 -cpu=1 ./internal/lab
```

| 实现 | 本组五个 ns/op 样本 | B/op | allocs/op |
| --- | --- | --- | --- |
| KeyFmt | 75.93、75.26、74.94、75.56、74.85 | 32 | 2 |
| KeyAppend | 20.92、20.79、21.27、21.22、21.32 | 24 | 1 |

命令退出 0，package 总耗时 11.926s。重复样本仍确认少一次分配、少 8B/op，本次串行样本的耗时也下降。串行只排除了本课程同时构建的干扰，没有证明恒定 CPU 频率、系统无其他负载或真实服务的输入分布；仍不承诺固定收益比例、生产 p99 或任何平台永远更快。原先并行环境样本保留为历史，不能与本组拼成同一个受控实验。

独立完整入口另生成三份 heap profile：alias/cap 各在 retainOne 保留32MiB，clone没有该存活数组；读取现有CPU profile与objdump确认fmt热点和旧基准空工作。独立run、六项race测试（1.292s）及vet均退出0，GC工作量checksum199680，8MiB软限制实际超出。此记录不表示整层已经提交。
