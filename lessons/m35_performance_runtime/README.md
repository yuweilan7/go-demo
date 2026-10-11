# M35：性能与运行时——从订单键到真实内存预算

这一课解决一个实际后端问题：订单请求量变大以后，CPU 花在哪里？大量临时字符串给 GC 增加了什么工作？只把响应体的 16 字节 header 放入缓存，为什么进程还能保留几十 MiB？你将用同一组订单数据走完“保证行为正确 → 正确计时 → 看逃逸和 profile → 只改一个热点 → 重复测量”的过程。

本模块独立使用 `go-demo/lessons/m35_performance_runtime`，`go.mod` 为 `go 1.27`，无第三方依赖和硬前置。根模块的 `go test ./...` 不会遍历这个嵌套 module。本课没有连接数据库、网络服务或共享 DeepHelp，也没有修改它们的内存/CPU 配额。

## 先花 10～20 分钟读完并跑通核心

从仓库根目录运行，PowerShell 命令如下。第一个命令通常只需数秒，剩下时间用于对照代码和日志。

```powershell
go -C lessons/m35_performance_runtime run ./cmd/demo
go -C lessons/m35_performance_runtime test ./...
go -C lessons/m35_performance_runtime vet ./...
```

阅读顺序：

1. `cmd/demo/main.go` 的 `main → run → core → isolated`：入口如何限制时间、如何拥有子进程。
2. `internal/lab/keys.go` 的 `Order → KeyFmt/KeyAppend → HeaderAlias/HeaderCapTrap/HeaderClone`：业务结果和底层数据所有权。
3. `internal/lab/benchmark_test.go`：先看两个 Key benchmark，再看两个测错边界的反例。
4. `internal/lab/escape.go` 和 `lab_test.go`：分配位置与业务语义是两种验收。
5. `internal/lab/memory.go` 的 `RetentionWork → retainOne`、`GCWork`：独立进程如何测可达内存。
6. `internal/lab/runtime.go` 的 `StackDemo → stackFrames`、`CounterDemo`：goroutine 的等待、停止、退出和锁。

现在必须理解：slice 描述符与底层数组不同、逃逸决定属于编译上下文、benchmark 有具体工作边界、GC 只能回收不可达对象、GOMEMLIMIT 是运行时软预算、owner 必须等工作者真正退出。等遇到相关源码时再查：span class 的具体常数、工作窃取队列布局、写屏障汇编、Green Tea 的扫描批处理细节。背旧常数不能代替定位当下请求的热点。

核心输出中 E1 是订单键协议，E2 是别名/独立副本，E3 是缓冲归还后的数据，E4 是有屏障的栈增长，E5 是共享计数守恒，E6 是大数组保留，E7 是 GC 成本及软限制。实际样本见 [STATUS.md](STATUS.md)，每次运行的精确字节、耗时、worker 调度顺序会变化。

## 当前版本和旧材料的证据边界

2026-10-12 实测 `go version go1.27.0 windows/amd64`；`GOROOT=D:/Go`，`GOAMD64=v1`、`CGO_ENABLED=1`、`GOTOOLCHAIN=auto`，`GOEXPERIMENT` 环境变量为空。编译默认实验仍由工具链决定：当前 `D:/Go/src/internal/buildcfg/exp.go` 中 `GreenTeaGC: true`，环境变量为空不等于关闭所有默认实验。本课没有修改实验开关。

本机 AMD Ryzen 7 7840H，8 个物理核、16 个逻辑 CPU，约 31.2GiB 物理内存。入口当前观测 `NumCPU=16`、`GOMAXPROCS=16`、Windows CPU affinity 位图 `65535`，没有设置 GOMAXPROCS 环境变量。Windows 不读取 Linux cgroup CPU quota；本课没有测 Windows Job 对象的 CPU 限制。16 个逻辑 CPU 表示可调度范围，不表示当前进程独占 16 个核。本轮课程在同层 19 个模块并行执行的环境测量，其他 Go 构建、系统负载、频率和温度均可能影响 ns/op。

已读主目录 `files/原理剖析/` 的启发材料及当前源码：

| 材料 | 能确认的版本 | 本课如何修正 |
| --- | --- | --- |
| `协程调度/GMP调度原理` | 开头明确基于 **Go1.18.1**，并比较 Go1.14 | “每个 G 保证 10ms 让出”不是语言或调度 SLA；N 也不能简单等于物理核数 |
| `逃逸分析` | 未标精确 Go 源码版本 | 不接受“所有 interface 转换都逃逸”“动态 length 必然上堆”“返回指针必然在调用路径堆分配”的绝对结论 |
| `Go内存管理` | 未标精确版本，含旧 `runtime.gosizeclasses.go` 表 | mcache/mcentral/mheap 用作阅读线索，表格条目和旧对象阈值不作业务保证 |
| `Go语言垃圾回收` | 未标精确版本，叙述 Go1.3/1.5/1.7/1.8 历史 | 当前 `mgc.go` 可核验并发标记清扫、写屏障、非分代且非整理；历史不能替代当前实现/成本测量 |

旧材料称“其他函数无法访问本函数栈”“只需编译期知道作用域就能放栈上”也不精确。Go 允许把局部变量地址安全地交给调用方；编译器追踪引用流向、生命周期和可分析性，内联后还会重新考虑。大局部数组可能上堆，也可能留在栈或被优化掉；这不是一次“函数栈空间不足后临时搬对象”的源码语义规则。本机 `BenchmarkBadSetup` 的 4096 条订单数组没有产生额外堆分配，正好说明不能套用教程旧阈值。

## 运行时最小规则：调度、栈、分配与 GC

G 是 goroutine 的运行状态及栈，M 是执行它的 OS 线程，P 是运行 Go 代码所需的调度和分配资源。`go f()` 创建独立执行者，不等待它；G 在 `<-release` 阻塞时是等待状态，M 可以执行别的可运行 G。阻塞系统调用、网络轮询和 channel 的实现路径不同，不能统称为“线程必然阻塞”。业务先后关系由 channel、锁和 owner 的等待定义，不能由本地队列长度或抢占阈值定义。

`runtime.GOMAXPROCS(0)` 读取当前 Go 执行并行度上限，并不改变它。当前工具链在未手动固定时，会考虑逻辑 CPU、affinity，Linux 还考虑 cgroup quota，并可自动更新；手动调用正数或设置环境变量会停止这种默认自动更新。它不是 goroutine 总数，也不是 OS 线程总数。部署到 Linux 容器时，读取实际 `cpu.max`/旧 cgroup quota、affinity 和该应用的语言版本，不能用本机 16 的值代替服务环境。[GOMAXPROCS 官方契约](https://pkg.go.dev/runtime#GOMAXPROCS)

goroutine 栈可以随调用深度增长，必要时当前运行时复制栈并修正相关指针。`stackFrames` 在每层保留 2048 字节数组，64 层递归的 4 个 worker 到最深处后发 ready，随后真正等待 release。owner 收齐 ready 才读 `StackInuse`，不使用 sleep 猜调度。当前样本为 `131072 → 1179648 → 131072` 字节；这是一组实验观测，不是每个 G 固定 2KiB 或总按某倍数变化的保证。

| 步骤 | 参与者/当前函数 | 当前状态和真正的等待点 | 证明与后续 |
| --- | --- | --- | --- |
| 1 | owner `StackDemo` | 建立 `ready`、`release`、结果 channel、WaitGroup | `Add` 在启动 worker 前，owner 是唯一 closer |
| 2 | 四个 worker `stackFrames` | 递归至 depth=0，外层数组仍需在返回后读 | 每个 worker 发一条 ready，再等 `<-release` |
| 3 | owner | 依次等四条 ready；或观察 ctx 取消 | 四条收到才测 Deep；取消走错误返回，但 defer 仍运行 |
| 4 | owner 的 defer | `close(release)` 发继续通知 | 通知本身还不证明 worker 已返回 |
| 5 | worker | 最深处接收成功，逐层计算 checksum 返回 | `defer wg.Done()` 表示本 worker 已完成 |
| 6 | owner | `wg.Wait()` 返回，关闭并排空结果 channel | checksum=16900；此后才读 After、返回调用者 |

4 个结果的缓冲容量是 4，即使 owner 走超时分支，也能先 `wg.Wait()`，不会卡在没人接收的结果发送上。对 `StackInuse` 不作精确数值断言；对实际内容 checksum 和收尾路径作断言。Java 的线程栈通常更容易按固定预留大小理解，Go 的可增长栈、G/M/P 和线程池不一一对应。

在当前源码，普通堆分配可进入 per-P 缓存及 span 相关路径；GC 并发标记可达对象，部分阶段仍需 STW，分配中的 goroutine 还可能执行 assist。因此“分配只影响内存，不影响 CPU/尾延迟”不成立。堆中仍可达但业务不再需要的数组也不会被 GC 猜测为垃圾。JVM 经验中的“GC 能识别没再用的缓存内容”“Minor GC 固定快速回收所有短命对象”不能直接搬到这里。

## E1：有证据的订单键热点，只改构造方法

协议是 `shop:<有符号十进制 ShopID>/order:<有符号十进制 OrderID>`。两个函数都接收 `Order` 值副本并返回可独立保存的字符串，没有共享可写请求对象。

调用链：`core → KeyFmt → fmt.Sprintf → string`；修正链为 `core → KeyAppend → strconv.AppendInt → string(buf)`。`KeyAppend` 用 `[64]byte` 局部数组，最长两个 int64 加前缀共 52 个字节，容量具有类型边界依据。返回 string 的独立所有权保留，不尝试 unsafe 零拷贝，不引入全局缓冲。业务契约通过独立的 `strconv.FormatInt` 构造预期值，覆盖零、负数、最小/最大 int64 及 500 组确定性数据。

为什么选这一个改动：基线 CPU profile 中 `fmt.Sprintf` 累计占约 74.11%，可以看到 `doPrintf`、`printArg`、整数格式化；分配 profile 的 `alloc_space` 中 `fmt.Sprintf` 约 180.50MB，KeyFmt 直接归属约 51.50MB。原样复现以后，才把通用格式化替换为已知协议的整数追加。pool 和锁批量是独立教学对照，不混入这次热点优化。

当前五样本数据（各自同为 9001..9064 轮转的订单号）：

| 实现 | ns/op 的五个实测样本 | B/op | allocs/op |
| --- | --- | --- | --- |
| KeyFmt | 80.29、91.09、95.12、92.88、118.2 | 每次 32 | 每次 2 |
| KeyAppend | 29.36、26.08、25.86、24.34、29.35 | 每次 24 | 每次 1 |

这组数字支持当前输入和工具链下“少一次分配，少 8B/op”；ns/op 在本轮样本中下降，但并发环境未受控，不能承诺稳定收益比例或永远更快。fmt 的 `ShopID=42` 使用当前小整数静态储存优化，虽然逃逸输出报告其流向堆，也不意味着每次再分配 8 字节。返回字符串本身仍需要生命周期稳定的存储，这不是零分配优化。

CPU profile 的 append 路径转移到 `strconv.AppendInt`、字节复制和 `slicebytetostring`。本次两个 2 秒 CPU 定位运行分别完成约 1533 万和 5890 万次操作，期间工作量、订单号位数及后台 GC 不完全相同；这两次数量不作为正式性能比较，也不能把 profile 百分比差当作单请求耗时降幅。

## E1 的测量方法与两类失真 benchmark

以下扩展命令均在 **模块目录** `lessons/m35_performance_runtime` 运行。第一条准备 `.local`，该目录被模块 `.gitignore` 忽略；用它保存本机二进制、完整诊断和 profile。

```powershell
New-Item -ItemType Directory -Force .local/profiles | Out-Null
go test -run '^$' -bench '^Benchmark(KeyFmt|KeyAppend|BadSetup|CorrectedSetup|DiscardedLegacy|Escapes)$' -benchmem -benchtime=250ms -count=5 -cpu=1 ./internal/lab
```

`-run '^$'` 不运行普通 Test，`-count=5` 提供五次测量，`-cpu=1` 只限制该测试进程 GOMAXPROCS，不会消除其他模块/系统负载。ns/op 是一轮键构造的均摊耗时；B/op、allocs/op 是本次 Go 堆分配，不能拿来直接计算进程 RSS。不要用 `-race` 数据、采样率为 1 的 profile 数据、IDE build+run 的 wall time 当作正常性能结果。

当前 `testing.B.Loop` 在本地 `go doc` 和 `D:/Go/src/testing/benchmark.go` 已核验：首次调用重置计时，false 时停表，循环前后的 setup/cleanup 不计时；写成 `for b.Loop()` 时工具链保活循环体中的调用输入/结果。本课还轮转订单号，让基准执行真实字符串构造。B.Loop 不会替你决定业务测量边界，也不保证消除 constant folding、虚假的工作设计或环境噪声。[testing benchmark 官方说明](https://pkg.go.dev/testing#B.Loop)

**反例 A：每次重建批次。** `BenchmarkBadSetup` 在计时循环内创建并初始化 4096 条订单，然后只构造一条键。实际约 2540～3849 ns/op；`BenchmarkCorrectedSetup` 把同一批次移到循环前，约 83.74～94.20 ns/op。两者仍是 32B/2alloc，初始化数组在当前编译器没有增加堆分配。错因是 CPU 工作边界，不是“GC 终于没分配批次”。只有需求本来不包含重建时，外提 setup 才是正确测量。改的是 benchmark，业务键实现没变，不能宣布生产速度提升几十倍。

**反例 B：旧 b.N 空工作。** `BenchmarkDiscardedLegacy` 调用可内联的 `PointerLocal` 却丢弃结果，本机测到 0.2068～0.3448 ns/op、0B/0alloc。实际反汇编只剩循环 `INCQ/CMPQ/JG`，没有对象构造或业务计算。程序“运行了循环”不证明所宣称的工作发生过。修正可用 B.Loop、动态输入和明确消费结果；本课 Key benchmark 对动态输入真的生成字符串，普通 Test 再验证结果。不要向正式键基准加逐条 fmt.Println，打印将重写测量对象。

主 agent 在本层完成后负责安静环境的串行复验。正式评估还应固定构建、CPU/容器预算、输入分布、GOGC/GOMEMLIMIT、重复样本，观察 p50/p99 和应用真实负载。没有收益证据时保留更易读的实现，不继续堆砌 pool、unsafe 或对象全局复用。


主 agent 在 2026-10-12 第 0 层审查时补做了一组串行复验：19 个课程子 agent 均已结束，复验期间没有同时派发其他构建，使用相同 Go 1.27.0、相同输入和 `-cpu=1`，每个样本计时 1 秒，共五个样本。

```powershell
go -C lessons/m35_performance_runtime test -run '^$' -bench '^Benchmark(KeyFmt|KeyAppend)$' -benchmem -benchtime=1s -count=5 -cpu=1 ./internal/lab
```

| 实现 | 本组五个 ns/op 样本 | B/op | allocs/op |
| --- | --- | --- | --- |
| KeyFmt | 75.93、75.26、74.94、75.56、74.85 | 32 | 2 |
| KeyAppend | 20.92、20.79、21.27、21.22、21.32 | 24 | 1 |

命令退出 0，package 总耗时 11.926s。重复样本仍确认少一次分配、少 8B/op，本次串行样本的耗时也下降。串行只排除了本课程同时构建的干扰，没有证明恒定 CPU 频率、系统无其他负载或真实服务的输入分布；仍不承诺固定收益比例、生产 p99 或任何平台永远更快。原先并行环境样本保留为历史，不能与本组拼成同一个受控实验。

## E2/E6：16 字节 slice 如何保留 4MiB 响应体

`body[:16]` 只创建一个引用原数组的视图；`body[:16:16]` 只把 cap 设为 16，限制未来 append 在原数组中的空间。它们均不复制 header。`bytes.Clone(body[:16])` 才创建独立数据。E2 修改 `body[0]` 为 B：alias/cap 都看见 B，Clone 仍为 A，这项是与 GC 无关的数据所有权证明。

E6 的调用链是 `all → isolated → child(retention) → RetentionWork → retainOne → Header...`。父进程等待一个子进程完整退出后才运行下一个，因此 GC 参数、残留空闲 span 和采样设置不跨 variant 共用。

| 阶段 | owner/等待点 | 数据变化与证明 |
| --- | --- | --- |
| 1 | 父进程 `child`，`Output()` 等待结果 | 同一个已编译入口作为子进程，25 秒上限；异常返回保留 stderr |
| 2 | 子进程 `RetentionWork` | 基线 GC 后创建 8 个 4MiB body，触碰各页并写入 header 标记 |
| 3 | 子进程 `retainOne` 返回 | 完整 body 描述符消失，但 alias/cap 的 header 仍指向大数组；Clone 已切断关系 |
| 4 | 子进程 owner | GC，读取 HeapAlloc、runtime bytes、OS WorkingSet；核验 128 字节内容及 checksum=576 |
| 5 | `writeProfile` | keep 仍存活时写 heap profile，再 `runtime.KeepAlive(keep)` 明确观测寿命 |
| 6 | 子进程返回/退出，父进程解码 JSON | 无缓存文件或常驻 worker；父进程继续下一 variant |

本次 alias/cap 的 HeapAlloc 均约 33,951,800B；Clone 约 397,592B，三者逻辑保留量都是 128B。父入口检查 alias/cap 比 clone 多于 16MiB，只针对这个固定实验的观测验收，不把 clone 的整个 Go 堆断言为 128B。

heap profile 的 `inuse_space` 中 alias/cap 的 `retainOne` 明确各为 **32MiB**，clone 不再有这项存活大对象。clone 的 OS WorkingSet 仍约 30.9MB，说明不可达对象回收、空闲页可复用、页归还 OS 是不同事实。`cap=16` 即使限制 append，也无法使一个仍可达的 4MiB 数组只回收尾部。

```powershell
go run ./cmd/demo -profile-dir .local/profiles
go build -o .local/demo.exe ./cmd/demo
go tool pprof -top -sample_index=inuse_space .local/demo.exe .local/profiles/heap-alias.pprof
go tool pprof -top -sample_index=inuse_space .local/demo.exe .local/profiles/heap-cap.pprof
go tool pprof -top -sample_index=inuse_space .local/demo.exe .local/profiles/heap-clone.pprof
```

heap profile 是 Go 采样归属，不是全进程内存图；`inuse_space` 看仍活的分配，`alloc_space` 看累计分配，不要混看后称“内存泄漏”。小对象的统计有更新/采样与 profiler 自身开销，`MemProfileRate=1` 也不令它与 MemStats 精确逐字节相等。大数组是否被保留需要可达关系、当前 heap 和 profile 共同证明。

## E3：缓冲复用、清零和 sync.Pool 的所有权

危险代码就在 `core`/`TestBufferOwnership`：`borrowed := scratch.Data` 复制 slice 描述符，却没有复制字节。owner `ResetForReuse` 后 borrowed 的首字节已经变成 0。如果把 borrowed 当成稳定响应返回，归还后清零/下一次覆盖就会破坏先前请求的内容；并发访问还会产生 race。

修正是先 `owned := string(scratch.Data)` 得到可独立保存的结果，再清零并归还。`KeyPool.Key` 的实际顺序为 `Get → 单个调用独占 Scratch → AppendInt → 返回字符串求值 → defer 清零整个 cap 范围 → Put → 调用者取得字符串`。defer 闭包在返回前读取当前 s；返回值在清零之前已计算好。Put 之后这个调用不能再读写 s，哪怕对象没有被回收。

`TestBufferOwnership` 验证所有曾写入字节被清零、长度归零、独立字符串保持内容；再做 100 次 KeyPool 使用，旧结果仍稳定。不测试 Get 必须拿回同一指针，因为 sync.Pool 允许删除任何值。`New` 建立后备对象，池空仍然正确；运行中的 KeyPool 不可复制。[sync.Pool 官方契约](https://pkg.go.dev/sync#Pool)

Pool 适合可丢弃的临时内存；连接、文件句柄需要确定的关闭责任和容量限制，不能交给可能直接丢值的 Pool。缓存项需要失效、命中、容量和生命周期契约，也不能依赖 Pool。清零整个 cap 有 CPU 成本，过大的 buffer 还可能不值得复用。本课只验证这个复用方案的正确性，没有测出它比 KeyAppend 更快，也没有把它应用到正式优化路径。

## E4/E5：如何从 CPU/锁现象找到正确修改边界

`CounterDemo` 的业务契约是 4 个 worker 各处理 50,000 条合成订单，最后返回总数。`batch=1` 每条加锁；`batch=100` 在一个 worker 内合并本批，再共享相加。总数均为 200,000，锁次数从 200,000 变成 2,000。每个 worker 在开始门 `<-start` 等待，owner 关闭它后工作；每批前检查 ctx；锁只保护 total/calls；owner `wg.Wait()` 后读取结果。

取消不是强杀：worker 下一批看到 ctx，停止循环并 Done；owner 仍等四个退出才返回包装的取消错误。锁中没有 I/O/channel 等待，计数函数有固定工作量；参数非法时在创建 worker 前返回错误。测试覆盖正常、已取消和非法 batch，验证独立总数与锁次数，而不以“wg.Wait 没报错”代替业务验收。

本次 mutex delay profile 的 batch1 累计 68.18μs，归属 `sync.(*Mutex).Unlock` 的持锁调用栈；它记录造成他人等待的锁释放路径，不是说 Unlock 自己执行了 68μs。block profile 约 119.85μs 在 `Mutex.Lock`，约 2264.92μs 在 owner 的 `WaitGroup.Wait`。后者是等待 worker 完成，不能见到它就删掉正确的 join。batch100 的 mutex profile 在本次短运行里是零采样，表示没有记录到竞争延迟，不保证任何系统/负载都无竞争。[诊断 profile 的官方口径](https://go.dev/doc/diagnostics)

```powershell
.\.local\demo.exe -mode mutex -batch 1 -profile-dir .local/profiles
.\.local\demo.exe -mode mutex -batch 100 -profile-dir .local/profiles
go tool pprof -top .local/demo.exe .local/profiles/mutex-batch1.pprof
go tool pprof -top .local/demo.exe .local/profiles/block-batch1.pprof
```

batch100 是正确性的业务前提：只需要最终总数。如果调用者依赖每条订单后的即时进度、按锁串行的外部副作用或取消后每一项的可见状态，批量将改变契约，需要重新设计。本课不把原始 wall time 的 `0s` 解读成零成本；它只是短操作与本机计时分辨率的观测。

## 逃逸/inlining：从编译器结论到实际分配

下列命令只给目标教学包开启诊断，没有 `all=-m` 淹没在所有标准库输出中。

```powershell
go build -gcflags='go-demo/lessons/m35_performance_runtime/internal/lab=-m=2' -o .local/demo.exe ./cmd/demo 2> .local/escape.txt
go build -gcflags='go-demo/lessons/m35_performance_runtime/internal/lab=-m=2 -l' -o .local/demo-noinline.exe ./cmd/demo 2> .local/escape-noinline.txt
Select-String -Path .local/escape.txt -Pattern 'can inline|does not escape|escapes to heap|capturing by value'
```

`-m=2` 看内联决策和流向，`-l` 关闭内联仅作为诊断对照。关闭内联改变代码生成，不把这种二进制 benchmark 当作默认构建的生产速度。局部函数的摘要和内联调用点可能有不同结论，看到“函数返回地址逃逸”还要沿真实 caller 看。

| 路径/函数 | 当前编译证据 | 当前无 profile 的分配结果与解释 |
| --- | --- | --- |
| `PointerLocal` | `new(Totals) does not escape`，可内联 | 0B/0alloc；new 的指针语法不要求上堆 |
| `BoxLocal` | `n does not escape`，可内联 | 0B/0alloc；局部接口不一定分配 |
| `ClosureLocal` | 闭包调用内联 | 0B/0alloc；闭包未交给外部保留 |
| `PointerRetained` → pointerSink | 对象流入返回结果；sink 跨调用存活 | 8B/1alloc；确实保留这个对象 |
| `BoxRetained(1024)` → boxSink | 函数摘要 n 逃逸，但调用点输入为常量 | 0B/0alloc；可使用静态常量存储，超过小整数缓存也不足以推出每次分配 |
| `BoxRetained(n++)` → boxSink | 动态值跨调用存活 | 8B/1alloc；动态接口装箱实测 |
| `ClosureRetained` → closureSink | 捕获 n 的值，函数值逃逸 | 16B/1alloc；保存函数值不会创建 goroutine |
| `KeyAppend` | append 不逃逸，返回 `string(buf)` 逃逸 | 固定订单输入为 24B/1alloc；局部 scratch 与返回结果分别判断 |

local 路径的几个 sub-nanosecond 数字只说明非常小的、可内联/常量折叠的控制路径，不能推广成一次真实请求成本。retained 和 local 的对象生命周期不同，不拿它们直接给“指针比值快/慢”排名。要决定是否改为指针，先看结构体大小、拷贝成本、别名权限、缓存局部性和逃逸后的成本，再测同一业务契约。

分配定位命令（PowerShell 将带 `=` 的 profile 参数整体加引号，避免本机原生命令参数解析差异）：

```powershell
go test -run '^$' -bench '^BenchmarkKeyFmt$' -benchmem -benchtime=500ms -count=1 -cpu=1 '-memprofile=.local/profiles/key-fmt-alloc.pprof' -o .local/lab.test.exe ./internal/lab
go tool pprof -top -sample_index=alloc_space .local/lab.test.exe .local/profiles/key-fmt-alloc.pprof
go test -run '^$' -bench '^BenchmarkEscapes/(interface_retained|closure_retained)$' -benchmem -benchtime=250ms -count=1 -cpu=1 -memprofilerate=1 '-memprofile=.local/profiles/escape-alloc.pprof' -o .local/escape.test.exe ./internal/lab
go tool pprof -top -sample_index=alloc_space .local/escape.test.exe .local/profiles/escape-alloc.pprof
```

第二个命令把采样率设为 1，本次动态接口/保留闭包在 profile 中分别归属约 5,183.34KiB 和 8,703.91KiB。这组带 profiler 的 benchmark 耗时约 432/564 ns/op，显著改变了正常的 7～13ns 控制路径，故只用于定位分配，不能放进速度排名。

CPU 定位按已构建二进制运行；文件在 StopCPUProfile 完成写出后关闭：

```powershell
.\.local\demo.exe -mode cpu -variant fmt -profile-dir .local/profiles -duration 2s
.\.local\demo.exe -mode cpu -variant append -profile-dir .local/profiles -duration 2s
go tool pprof -top .local/demo.exe .local/profiles/cpu-fmt.pprof
go tool pprof -top .local/demo.exe .local/profiles/cpu-append.pprof
```

两个 CPU profile 的 samples 合计可能超过 2 秒 wall time，因为后台 GC 和其他线程也被采样。flat 代表函数本身，cum 代表含被调用函数的累计；不同函数累计项可以重叠，不能简单相加到 100%。没有启动 pprof HTTP 服务、没有暴露端口。

## E7：GOGC、GOMEMLIMIT 的成本和预算边界

`GCWork` 只在专属子进程调用 `debug.SetGCPercent` 和 `debug.SetMemoryLimit`，保存旧值并用 defer 恢复。代码固定保留 12MiB 活数据，环形缓冲保留最后 1MiB，累计临时分配 96MiB（1536 次 × 64KiB）。触碰数据、累加 checksum=199680，再读取统计，避免优化掉假工作。子进程没有永久的外部副作用，返回/退出后内存由 OS 回收。

GOGC 控制 GC 的堆增长目标。更高的目标通常换来更多内存余量、更少周期；更低目标通常提高 GC 频率，但最终 CPU/延迟还受对象图、根、分配速率和运行环境影响。当前 pacer 的目标包含上轮存活 heap 和 stack/global 扫描根，不能写成“GOGC=100 保证 HeapAlloc 永远不超过 live 的两倍”；触发点和最终堆大小也不是同一时刻。[GC 指南中的成本模型](https://go.dev/doc/gc-guide)

当前一次有界对照：

| 参数 | 工作期间 GC 次数 | 工作耗时 ms | 累计 STW pause ms | 规范化终态 HeapAlloc | runtime bytes |
| --- | --- | --- | --- | --- | --- |
| GOGC=50，limit=MaxInt64 | 13 | 7.37 | 0.52 | 14,037,216B | 30,468,344B |
| GOGC=200，limit=MaxInt64 | 3 | 11.52 | 0.51 | 14,036,560B | 52,930,808B |
| GOGC=100，limit=8MiB | 344 | 68.14 | 16.50 | 14,052,184B | 21,367,032B |

GOGC=200 周期少却在本次 wall time 更长，已经是“不应把单样本直接说成更快”的反例。PauseTotalNs 只含暂停时间，不包含所有并发 GC CPU 和 assist 时间；不能用它代表完整 GC 成本。最终额外 GC 用于规范化存活观测，`WorkNumGC` 不包含这次强制终态 GC。Go 参数变化发生在子进程进程级，不能拿到线上请求里逐条设。

8MiB 预算小于必须存活的 12MiB 数据，GC 没有权力回收这些仍可达的对象。软限制会增加回收/归还工作，也允许超过以维持进度；太低可能造成大量 GC、降低吞吐。它既不是 OOM 防护墙，也不是进程 RSS 硬上限。官方契约的运行时口径为 **`MemStats.Sys - MemStats.HeapReleased`**，包括 Go 管理但尚未归还的映射内存，排除二进制、非 Go 分配、OS 代表进程使用的部分资源。GOGC=off 时软限制仍可触发 GC。[SetMemoryLimit 的官方边界](https://pkg.go.dev/runtime/debug#SetMemoryLimit)

```powershell
.\.local\demo.exe -mode gc -gogc 50 -limit 9223372036854775807
.\.local\demo.exe -mode gc -gogc 200 -limit 9223372036854775807
.\.local\demo.exe -mode gc -gogc 100 -limit 8388608
```

HeapAlloc、runtime bytes 和 OS WorkingSet 是三个口径。采样函数先读 Go 统计，再只读查询本进程 Windows WorkingSet，因此也不是完全同一瞬间。RSS/WorkingSet 包含驻留视角；空闲 Go 页、线程/栈、二进制/共享页、非 Go 分配及 OS 管理资源各有不同计量关系。网络 socket 的内核缓冲、数据库驱动连接、TLS 缓冲、连接池、cgo/mmap、sidecar/容器里的其他进程都需要独立预算。连接/响应体等仍要显式 Close 或按协议归还，GC 不代替资源 owner。

实际部署的预算步骤是先测真实 RSS/容器峰值、live heap、goroutine 栈、请求并发与其他资源，再给 Go 运行时留出足够 headroom，验证负载及 OOM/延迟行为。本课只有本机有界数据，没有做线上容量结论，也没有为了挤入共享服务器而调整 DeepHelp 配额。

## 按当前源码继续定位，而不是复述旧教程

这些符号在本轮 `D:/Go/src` 实际核验存在；行号随版本变化，用符号重新搜索：

| 本课现象 | 当前阅读入口 | 先回答什么 |
| --- | --- | --- |
| B.Loop 的计时/保活 | `testing/benchmark.go: (*B).Loop` | 哪些 setup 排除、哪种循环语法生效 |
| G 可运行/调度 | `runtime/proc.go: findRunnable, schedule` | runnable 与等待态如何换执行者，不猜 10ms SLA |
| 栈增长/复制 | `runtime/stack.go: newstack, copystack` | 当前 goroutine 为何需扩栈，指针为何仍安全 |
| 分配 | `runtime/malloc.go: mallocgc`，`mcache.go/mheap.go` | 请求的类型/大小如何走当前分配路径 |
| 接口装箱 | `runtime/iface.go: convT64` 与调用点汇编 | 静态值/动态值是否真有每次分配 |
| GC 流程 | `runtime/mgc.go: gcStart`、`mgcmark.go: gcAssistAlloc` | 暂停阶段、后台工作、分配者 assist 分别发生在哪里 |
| GC 目标/软限制 | `runtime/mgcpacer.go: heapGoalInternal, memoryLimitHeapGoal` | 根扫描、非 heap 运行时内存如何参与预算 |
| 默认实现开关 | `internal/buildcfg/exp.go: GreenTeaGC` | 本工具链默认实现为何不能从空环境变量推断 |

官方 [GC 指南](https://go.dev/doc/gc-guide)、[testing](https://pkg.go.dev/testing)、[诊断指南](https://go.dev/doc/diagnostics) 是契约与查证入口；精确的编译决策以实际 Go1.27.0 输出为准。维护公司旧版本代码时重新运行它的工具链，核对 `go`/`toolchain` 指令和实际运行环境，不能先升级版本再解释旧问题。

## 验收、失败与资源结局

普通测试有六项：完整订单键协议、slice 独立所有权、归还清零与旧字符串稳定、并发请求使用 Pool 的隔离、逃逸路径的值语义、worker 计数/取消/非法参数与递归 checksum。源码注释解释实际调用、值/别名、等待、错误返回和 defer 顺序。`run` 失败返回 main 打印非零退出；错误的 profile 参数、文件写入、子进程超时与 JSON 失败不会被 PASS 文案掩盖。

入口整体 60 秒，单个内存子进程 25 秒，Windows OS 查询 10 秒；CPU 定位只允许运行 0.2～5 秒且不超过一亿操作，锁/递归固定上限。worker 用 barrier 控制阶段、不用 sleep；channel owner 唯一、WaitGroup 等收尾；profile 文件都显式关闭；子进程由 Output 等待回收。没有常驻进程、端口或实验故障代理。

race 初次因 PATH 中的 `mingw32` C 编译器失败；主 agent 提供 WinLibs GCC16.2.0 的 amd64 入口后，在命令进程内设置 CC/CXX 和 PATH，实际 `go test -race -count=1 -timeout 60s ./...` 已 PASS（lab 包 5.062s，无 race 诊断）。只编译普通纯 Go 课程不需要这套 C 编译器；Windows amd64 race 构建需要匹配架构的工具链。详细入口与初次失败原文见 [STATUS.md](STATUS.md)。race 只验证被执行路径的并发访问，没有把它的耗时用于性能结果。

profile/二进制只留在忽略的 `.local/`，需要复验时按上面的命令重新生成；本课正文永久保留实际样本与解释。模块规定的实施/验证范围已完成，ns/op 仍限定本轮并发环境。课程交付不意味着主 agent 已验收整层或提交，二者由课程调度记录确认。
