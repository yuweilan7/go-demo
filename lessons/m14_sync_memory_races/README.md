# M14：共享对象、同步范围与业务正确性

这课围绕 `sku-1` 的最后一件库存展开：两个请求都先看见库存为 1，随后都接受订单、都把库存写成 0。最终库存没有负数，race detector 也可以完全安静，但两张订单只对应一件商品。学完后应能从实际 Go 代码回答三件事：共享的是哪块对象；同步协议到底覆盖哪段访问；业务不变量是否真的成立。

现在必须理解 E01/E02 的临界区、同一把锁和 CAS、E03/E04 的对象别名与发布边界，以及故障报告怎样对应源码。`sync.Map` 的访问模式、Once 失败语义、Pool 借用协议要能识别日常误用。HashTrieMap 的节点拆分、Mutex 调度策略和 Pool 的每 P 缓存属于以后阅读运行时源码时再查的细节；这课不给它们编造跨版本保证。

本课是独立 module：`go-demo/lessons/m14_sync_memory_races`，`go.mod` 声明 Go 1.27。没有外部依赖、硬前置、数据库、共享端口或中间件。测试服务器只使用本机临时 loopback 端口并在返回前关闭。根目录 `go test ./...` 不会递归进入这个嵌套 module。

## 先跑核心实验，再读源码

从仓库根运行：

```powershell
go -C lessons/m14_sync_memory_races run ./cmd/demo
go -C lessons/m14_sync_memory_races test -v ./...
go -C lessons/m14_sync_memory_races vet ./...
go -C lessons/m14_sync_memory_races test -race -count=1 -timeout=30s ./...
go -C lessons/m14_sync_memory_races run -race ./cmd/demo
go -C lessons/m14_sync_memory_races run ./cmd/probe
```

或者把工作目录设为本课目录，然后移除 `-C lessons/m14_sync_memory_races`。Windows 的 `-race` 需要支持 amd64 的 C 编译器；当前机器 PATH 中旧 `D:\MinGW\bin\gcc.exe` 的 target 是 `mingw32`，不能满足此条件。实际可用工具链及进程级 CC 复跑方式见 [STATUS.md](STATUS.md)，不应通过 `CGO_ENABLED=0` 声称 race 检查已经完成。

本次已用主任务统一提供的 WinLibs amd64 编译器完成 race 正常/故障全部补验：9 项测试无 race；正常 demo 保持业务断言通过；probe 收到 5 个 fatal、7 个 race 与 copylocks 拒绝并整体退出 0。原 32 位编译器失败已解决，详见 STATUS 的环境设置与真实诊断。在线包文档当前展示 go1.27.2，本课实现链路和契约同时核对本机 go1.27.0 源码，不据网页版本自动升级本地工具链。

10～20 分钟核心阅读路径：先运行 demo，花 5 分钟只看 E01/E02；沿 [inventory.go](inventory.go) 的 `SplitAtomicSale → StockResult.Conserved` 和 `Inventory.Buy / CASInventory.Buy` 读注释；再读下面两张时间线；最后运行测试及 probe，把“无 data race 的超卖”与“真实共享内存冲突”并排看。其余 E03～E09 分步扩展。这里直接交付原理、反例、修复和解析，没有待完成的 exercise。

| 文件 | 从哪个符号开始 | 职责与副作用 |
| --- | --- | --- |
| [cmd/demo/main.go](cmd/demo/main.go) | `main → run → require` | 顺序调用 E01～E09；ctx 属于入口；业务断言不成立就返回 error 并非零退出 |
| [inventory.go](inventory.go) | `SplitAtomicSale`、`Inventory.Buy`、`CASInventory.Buy` | 同一合成库存的错误和修复；订单成功数独立统计 |
| [snapshots.go](snapshots.go) | `ObserveRW`、`Catalog.Get/Set`、`Snapshots.Read/Update`、`Visibility` | 同一共享对象的锁协议、slice 别名、不可变发布 |
| [map_once_pool.go](map_once_pool.go) | `SplitMapSale`、`ObserveMap`、`ObserveOnce`、`ObservePool` | 单 key 原子边界、Range、失败初始化与临时对象复用 |
| [io_order.go](io_order.go) | `ObserveIO`、`Transfer` | 真实 HTTP 响应屏障、锁外 I/O 和统一锁顺序 |
| [lesson_test.go](lesson_test.go) | `TestBusinessInvariant` 等 9 项测试 | 业务守恒、复制隔离、版本完整性、初始化契约、真实 HTTP、类型错误 |
| [cmd/probe/main.go](cmd/probe/main.go) | `main → probe → run` | 构建两个临时二进制，给故障子进程上限，核对真实诊断，等待退出并清理 |
| [internal/faults/faults.go](internal/faults/faults.go) | `Run → 场景函数` | `faultcases` 标签下的真实错误；默认 test/vet 不编译它们 |
| [cmd/fault/main.go](cmd/fault/main.go) | `main → faults.Run` | 子进程显式故障入口，不建议直接执行无监督死锁模式 |

正常 demo 中 E01/E05 故意保留的是**业务逻辑错误**，所有内存访问仍遵循同步契约。真正可能触发 fatal 或 data race 的源码只在 `faultcases` 标签下出现，`probe` 是推荐入口。

## E01/E02：检查再扣减为何必须属于一个状态转移

不变量是 `initial == remaining + sold`，并且两个数都不能负。`remaining>=0` 只能排除负库存，不能排除重复接单。`Conserved` 按业务事实检查，不知道内部用了 Mutex、atomic 还是 map，因此不会和实现共享同一错误推导。

`atomic.Int64` 的 `Load`/`Store` 保证各自操作不可分割，并遵守顺序一致的原子操作契约。它不会把一条 Load 与稍后的 Store 绑成一个事务。Go 原子规则和 Java `volatile` 的目标相近：给共享值的发布/观察建立可见性和排序；和 `volatile int` 的 `x++` 一样，多个正确单操作之间仍能夹进别人的业务操作。不能据此断言 atomic 无条件更快或随 CPU 数线性扩展；争用同一缓存行可能成为瓶颈。

`SplitAtomicSale` 的两个 goroutine 都属于该函数的 owner。`checked` 是容量 2 的确认队列，每条消息说明一个购买者已经读取库存；`release` 是由 owner 唯一关闭的广播门；`wg` 表示还有多少购买者未退出。start/ready 等 channel 只是实验协调，真正的库存访问依旧只有原子操作。

| 步骤 | 参与者 / 函数 / 等待点 | 状态变化 | 下一步与证明 |
| --- | --- | --- | --- |
| 1 | owner / `SplitAtomicSale` | 创建 `remaining=1`、`sold=0` 和两个购买者 | 每人共享同一个 `remaining` 地址 |
| 2 | 两位购买者 / `remaining.Load` | 每人得到自己的 `seen=1` 值副本 | 各发一条 `checked`；随后等待 `release` 或 ctx |
| 3 | owner / 两次 `<-checked` | 确认两人均已读完、还未扣减 | 关闭 `release`，此时仅通知可继续 |
| 4 | 两位购买者 / `Store(seen-1)` | 两次都写 0，各自 `sold.Add(1)` | 单条 Store 都合法，但接受了两张订单 |
| 5 | owner / `wg.Wait` | 确认两者实际退出 | 得到 `1,0,2`，守恒为 false |

本次关键输出：`E01 ... initial=1 remaining=0 sold=2 conserved=false`。这个结果由屏障固定，不是用 `sleep` 或大量 goroutine 碰运气。`go test -race` 仍应通过这段代码；race detector 检查访问协议，不知道“一件商品不能卖两次”。

修复一是 `Inventory.Buy`：所有购买者共享一个 `*Inventory`、同一 `mu`；从检查售罄到扣减和增加销量，都在 `Lock→Unlock` 之间。`Result` 也取这把锁一次读两个关联字段。`defer i.mu.Unlock()` 在成功 Lock 后注册，售罄早返回也会先释放锁再返回调用者。Mutex 没有递归计数；嵌套调用应把“已持锁的内部方法”与“取得锁的外部方法”职责分开，不要再次 Lock。

修复二是 `CASInventory.Buy`：`old := Load()` 后，`CompareAndSwap(old, old-1)` 只在当前值仍等于 old 时更新。成功瞬间是售出这一件的线性化点；失败者重读，发现 0 就售罄。CAS 成功后再 Store 会覆盖别人后续的更新，是额外错误。此类型只有一项业务状态；`SellWithCAS` 等全部任务退出后，数各自返回的成功标记，独立核对库存与销量。不应把它扩展成“CAS 库存 + 普通订单表”就宣称跨对象事务已经完成。

| 步骤 | Mutex 修复 | CAS 修复 |
| --- | --- | --- |
| 第一个请求 | 取得唯一锁，检查 1，扣到 0，sold=1，解锁 | 读 1；CAS(1,0) 成功；返回 true |
| 其他请求 | 等 Lock 返回，再见 0，返回 false | 可能先读 0；也可能先读 1 但 CAS 失败，重读 0 |
| owner 等待 | `wg.Wait` 后一次锁下读结果 | `wg.Wait` 后数成功标记并 Load 剩余 |
| 独立结果 | `initial=1 remaining=0 sold=1 conserved=true` | 同样守恒；没有任何具体获胜者顺序保证 |

`SellWithCAS` 的结果切片不必加锁，是因为每个 goroutine 独占一个元素，owner 在 Wait 后才读，没人改切片头。共享同一 slice 不等于访问同一元素一定冲突；共享相同底层元素才是本课的危险位置。

取消路径也要看具体等待点：E01 的购买者在 `select` 观察 ctx，但关闭 release 与 ctx 同时就绪时，select 可能选 release，不能承诺取消后没有售出。owner 无论如何都 Wait 收尾。CAS 在每轮开始检查 `ctx.Err()`，这是协作边界，不能撤销已经成功的 CAS。`TestCancellation` 用“调用前已取消”这一明确条件验证不扣减，不把不保证的窗口写成承诺。

## 同步为什么产生可见性

Go 的 happens-before 可以理解为“同一 goroutine 内的先后”和“同步操作连接的先后”的传递闭包。不要用墙上时间、日志先后、`sleep`、机器只有一个 CPU 或“这个 goroutine 早启动”代替同步边界。Go 对无 data race 程序提供可按顺序一致执行理解的保证；有 race 的程序可能被运行时报告终止，也可能出现不一致对象，不能靠本次打印正确证明安全。[官方内存模型](https://go.dev/ref/mem)

| 原语 | 真正连接的两个事件 | 本课定位 / 边界 |
| --- | --- | --- |
| `go` 语句 | 启动语句之前的动作 → 新 goroutine 的开始 | 创建前初始化库存可安全交给 worker；worker 自己退出不自动通知 owner |
| 同一 Mutex | 较早 Unlock → 后来成功返回的 Lock | 不同锁不建立对应保护协议；失败 TryLock 没有同步效果 |
| RWMutex | 写解锁与后续读锁、读解锁与后续写锁按库契约连接 | RLock 不是写许可，不能升级，不能因递归读锁碰巧可用就采用 |
| channel | 对应发送 → 接收完成；关闭 → 因关闭而返回的接收 | close 通知可继续，不等于 receiver 已执行完；Wait/join 另负责退出 |
| atomic | 观察到某原子写的原子读；所有原子操作顺序一致 | 只保护该操作/发布协议，不自动保护后续可变 map |
| Once | 初始化函数完成 → 各次 Do 返回 | 普通配置字段安全可见；panic/失败不会自动重试 |
| sync.Map | 被读方法观察到的写效果，按每种方法的读写定义 | 单 key 操作不会成为跨 key 事务 |
| Pool | Put(x) → 实际返回同一 x 的 Get；New 返回 x → Get 返回 x | 只有拿到同一对象时适用；Put 后不能继续改它 |

`Visibility` 的写者先写普通 string `payload`，再 `ready.Store(true)`。读者只在 `ready.Load()` 观察到 true 后读取 payload，且写者此后不再改 payload。这里 atomic 建立发布，普通 payload 不需要每次都变成 atomic。反例 `unsynchronizedPublication` 的普通 bool 不具备这个协议，观察到 true 也不产生发布保证。教学循环每轮看 ctx 并 `runtime.Gosched()`；生产等待更常采用 channel/Once，而不是长时间自旋。

Mutex 不是 JVM monitor 的完全替身。Go Mutex 没有绑定某个 goroutine 的可重入 owner，合法的协议甚至允许一个 goroutine Lock、另一 goroutine Unlock；通常仍由取得锁的函数负责 defer 释放，因为更容易推理。Java 的 `synchronized` / `ReentrantLock` 能重入，搬到 Go 后递归 Lock 会挂住。Go 的 goroutine 也不是 Java 线程的等价物；这里讨论的是同步关系与对象访问，不靠线程 ID 推理。

## E03/E04：读锁、slice 别名与不可变快照

`ObserveRW` 先让两个 reader 真实取得 RLock，并通过 ready 确认；它们在读锁里等 release。writer 发出 writerStarted，再调用 Lock 等所有 reader 释放。owner 在开门前看到 `writer-before-release=false`，开门并 Wait 后得到 `final=1`。writerStarted 只说明走到尝试锁之前，不能被当成“writer 已排到内部队列”的证明；本课结论由仍持有的读锁保证。

RWMutex 适合某些读取多、临界区有足够工作量的场景，但不是每个“读多写少”场景都比 Mutex 快。它允许多个 reader，或一个 writer。RLock 下仍然写共享字段、map 或 slice 元素同样是错误。writer 等待期间新增 RLock 会受到阻挡，所以递归 RLock 可能自锁；RLock→Lock 升级和 Lock→RLock 降级均不支持。[当前 sync API](https://pkg.go.dev/sync)

`Catalog.Set` 首先复制输入 slice 内容，再在写锁内替换 map 项。调用者必须保证复制输入期间没有别人改输入。`Catalog.Get` 在读锁内读共享 map/数组并复制输出。为什么两端都复制：Set 只复制 slice 头会留下调用者的数组别名；Get 直接返回内部 slice 则让读者在解锁后修改内部数组。demo 修改 input 与 Get 返回值后，再次读取仍是 `[phone case]`。故障 `backingArray` 的 `right := left[:]` 刻意保留数组别名，报告应指向 `left[0]++` 与 `right[0]`。

`Snapshots` 的关系是 `writerMu → 私有 owned 副本 → 完整构建 → Value.Store(*Config)`，读者 `Value.Load → 同一版指针 → 读者专属副本`。atomic.Value 原子替换的是一个动态值；它不递归冻结值，也不会锁住里面的 map/slice。`valueInternalMap` 发布 map 后原地写，race detector 会证明这个边界。

| 时间 | 写者 / 读者 | 对象与状态 | 证明点 |
| --- | --- | --- | --- |
| 1 | `NewSnapshots` | 深到本课字段层次复制输入，发布 v1 | 外部改输入 map/slice 不影响发布对象 |
| 2 | reader / `Read` | 一次 Load 固定 v1 指针，复制成读者所有 | 后续发布不把这一份读结果变成 v2 |
| 3 | writer / `Update` | 取得 writerMu，复制 v1，在未发布的 owned 上生成 v2 | 不会把一半字段暴露给 reader |
| 4 | writer / `Store` | 原子发布完整 v2；退出时解锁 | 后续 Load 可能见 v1 或 v2，但不应混合版本字段 |
| 5 | `TestSnapshotVersions` | 4 个写者共 200 次更新，4 个读者并发核对 version/limit/label | 最终 200/2000，旧快照仍 0；同时 -race 无报告 |

writerMu 仍必要：两个 writer 若都从 v1 生成 v2 再各 Store，会丢更新，即使 Store 完全原子。可选择 atomic.Pointer 的 CAS 不可变指针方案，但还要设计重试、旧指针生命周期与 ABA/副作用边界；当前业务字段多、更新次数有限，互斥写者更容易读。

`cloneConfig` 只处理本课的 `map[string]int` 和 `[]string`。未来字段若是 `map[string]*Mutable`、嵌套 slice，必须继续按对象所有权复制。callback 同步运行且不得在后台修改 owned；第二次封存复制阻止 callback 保留的普通别名指向最终发布对象。指针接收者只是避免复制容器，不会让返回的可变指针自动安全。

`TestAtomicValueTypeContract` 实际复现并 recover 普通 panic：零值 Load 返回 nil；Store(nil interface) 被拒绝；后续 Store 的动态具体类型必须与第一次一致；CAS 的旧值必须可比较，map/slice 不能这样比较。`Store((*Config)(nil))` 有动态类型，所以容器本身接受它，但我们的 Read 不允许 nil 配置。Value、typed atomic、Mutex、RWMutex 等同步对象首次使用后均不能复制；struct 赋值、参数、返回值、slice append 可能隐式复制，审查不能只搜显式 Copy。

## E05：sync.Map 只负责它承诺的单操作

`SplitMapSale` 复用最后一件库存和双检查屏障，得到 `sold=2 / remaining=0 / conserved=false`。把 `Load` 与 `Store` 换成并发安全方法，只是消除了容器访问冲突，没有改变业务窗口。多个 key 的读写、Load 后修改指针内部字段、Range 求和，都不会自动变成事务。

`ObserveMap` 有两条单 key 协议。8 个调用者先各自构造 candidate，再 LoadOrStore("order-1", "created")，只有 `loaded=false` 的一位是真正存入者；日志 `candidates=8 create-winners=1` 提醒你实参构造仍执行了 8 次。如果构造过程就发邮件/扣款，LoadOrStore 无法撤销那些副作用。接着 8 个付款者 CAS 同一 key 的 created→paid，只有一位返回 true。`old` 必须可比较；本课用 string，测试另复现 map 旧值 panic。

这不是数据库幂等事务：“获胜写入内存”与“外部扣款成功”仍是两个动作，进程可在它们之间中断。这里只有可验证的本机状态转移，不能凭一次 CAS 证明跨进程业务效果。

官方列出的适用模式是某 key 写一次、多次读（如只增长缓存），或多个 goroutine 处理互不重叠 key 集合。普通 `map[K]V + Mutex/RWMutex` 通常提供更强类型约束，也更容易把关联不变量放到同一临界区。别用“读多、更新多、删多、写少”这类含混口号或旧实现直接判断当前性能；具体 workload 应基准测量。

Range 对每个 key 至多访问一次，没有顺序保证，也没有整体一致快照保证。`ObserveMap` 先存 left=1/right=1，在第一个 callback 里先把**已经访问**的 key 改 2，再把另一个改 2。真实整体只走“都 1 → 首个 2/另一个 1 → 都 2”；本次却记录首个 1、另一个 2，组合从未整体存在。callback 和后续 callback 在同一调用 goroutine 顺序执行，但遍历每次读取的对象可以变化，故不需要 sleep 或猜 writer 顺序。

当前运行记录 `map[left:2 right:1] mixed=true`；键的先后、是否一定观察到混合都不是 API 保证。测试只核对两 key 各存在、值为允许的 1/2，不把某次 HashTrieMap 输出写成跨版本断言。要拿整体快照，用统一外部锁保护普通 map、或发布整个不可变 map 版本；只有部分写者遵守外部锁仍不够。

## 当前源码与原材料的版本边界

已读 worktree 中 `examples/advanced-syntax/sync-primitives`；只读主目录 `files/语法进阶/Sync` 与 `files/原理剖析/数据结构/Sync.Map`（实际是无扩展名文件，prompt 的组合路径不是实际目录）。这些材料能提供问题场景，但不能作为当前源码契约。

本机 `go version` 为 `go1.27.0 windows/amd64`，GOROOT 是 `D:\Go`，`GOEXPERIMENT` 为空。核验的调用链：

```text
sync.Map.Load / LoadOrStore / CompareAndSwap / Range
  D:/Go/src/sync/map.go：Map.m 是 internal/sync.HashTrieMap[any, any]
  → D:/Go/src/internal/sync/hashtriemap.go
    Load：初始化后按 key hash 遍历 root/children 的 atomic.Pointer
    LoadOrStore：找候选节点 → 节点锁 → 再检查 → 插入/发布
    CompareAndSwap：寻找并比较 → 发布替换后的 entry
    Range：iter(root.Load())，逐节点/entry 调 callback
```

这次源码说明“使用 atomic 的读取路径和节点级写协调”，不能把 sync.Map 宣称成所有操作都无锁。读代码时 internal 包不能作为业务依赖导入；节点数、哈希切片长度、字段名字不是兼容 API。

| 原材料说法 | 本课纠正 |
| --- | --- |
| sync.Map 本质就是 readOnly/dirty/misses、nil/expunged | 材料自己标了 Go 1.17；当前 Map 用 HashTrieMap。历史源码仅用于理解旧公司版本，不能套到 1.27 |
| 最后一份完整源码的 `missLocked` 无条件提升 dirty | 材料前段有 `if misses < len(dirty) return`，末尾整合源码缺这个判断；连历史实现都应对照对应发行版官方源码，不复制手工节录 |
| Mutex 由 OS 调度器实现，atomic 性能按 CPU 数线性提高 | 当前 Mutex 有 Go 运行时协调；原子语义不等于性能保证，也不等于业务多步骤原子 |
| WaitGroup 前用 sleep 等 goroutine / channel 必须任务数大小缓冲 | 退出要真实完成信号；无缓冲 channel 也可逐条确认。当前工具链支持 WaitGroup.Go（Go 1.25 引入） |
| Once 初始化对象“永久驻留内存” | 对象生命周期来自引用与 GC；Once 只约束一个实例执行一次及完成可见性 |
| Pool.New 是构造函数 / 回收是随机时间后的 GC | New 是可选回调字段；对象可在任何时刻无通知移除，不能推导固定时间、数量或取回顺序 |
| runtime fatal 可以按一般 panic/recover 处理 | 普通 panic 与 fatal 不同；concurrent map 与 runtime deadlock 必须在隔离进程观察 |

## 故障探测：真实报告与修复映射

`go run ./cmd/probe` 构建普通和 race 两个 `faultcases` 二进制到独占临时目录，构建最多各 60 秒；每个故障子进程最多 5 秒；vet 最多 30 秒。探测器必须收到预期文字与失败退出，race 子进程必须退出 66；超时不当成成功证明。每段打印完整真实栈，再给 `PASS`。故障源码不参加默认 `go vet ./...`，故意复制锁的标签 vet 预期失败，外层 probe 验证该失败后整体成功。

| 故障 mode / 函数 | 真正的问题与等待点 | 检查与修复 |
| --- | --- | --- |
| `map` / `ordinaryMap` | 共享一个普通 map，两个 worker 有写写/读写冲突，连不同 key 也涉及共享内部结构 | 非 race 实际观察 concurrent map fatal；race 指向赋值/访问；修复 `Catalog` 同对象同锁或合适 sync.Map |
| `different-lock` / `differentLocks` | 两把不同 Mutex 各改同一个 count 地址 | race 指向两次 count++；修复 `Inventory` 的唯一锁协议 |
| `value-receiver` / `copiedCounter.add` | 每次值接收者复制 mu，count 指针仍共享；`*c.count++` 等价 `(*c.count)++` | vet 指出 passes lock by value，race 指向共享整数；修复指针接收者并确保同一对象 |
| `copy-struct` / `copiedUnlockedStruct` | source 首次使用后被复制，当前未持锁也不能复制；共享指针+分离锁 | vet 指出 assignment copies lock value；race 证明并发访问；修复只复制指针或把全新对象从业务数据重建 |
| `copy-locked` / `copiedLockedStruct` | 副本继承已加锁状态，main 在副本 Lock；原锁 defer 等函数退出才能执行，且无法解副本 | 真实 runtime deadlock；修复禁止复制，传原对象指针并重新设计持锁调用边界 |
| `reentry` / `reentry` | 同 goroutine 第二次 Lock 等自己的第一次锁释放 | 真实 runtime deadlock；修复内部 helper 假定 caller 已持锁，或解除嵌套协议 |
| `rw-upgrade` / `upgrade` | main 持 RLock 调 Lock，写锁等自己的 reader；defer RUnlock 永远到不了 | 真实 runtime deadlock；修复一开始 Lock，或 RUnlock 后 Lock 并重新核验条件 |
| `lock-order` / `circularOrder` | 两人屏障确认各持一锁，再尝试对方锁；owner Wait 两人 | 真实 runtime deadlock；修复稳定 id 全局一致取锁顺序，见 Transfer |
| `atomic-value-map` / `valueInternalMap` | Value.Load 安全，返回的 map 内容却被读写并发改 | race 指向 map 内容；修复深到必要层次复制、发布后不变，见 Snapshots |
| `slice` / `backingArray` | 两个 slice 头共享同一数组元素 | race 指向数组读写；修复 Catalog 的输入输出复制或共同锁协议 |
| `visibility` / `unsynchronizedPublication` | 普通 ready/payload 没有同步发布 | race 指向 ready/payload；修复 Visibility 的原子发布、channel 或 Once |

读 race 栈时，第一段指出访问位置与 goroutine，`Previous read/write` 指另一条冲突路径，`Goroutine ... created at` 回到 owner 的创建位置。不要只改报告那一行：检查双方是否都锁同一对象、有没有复制、返回值有没有泄漏别名。测试通过只能说明执行过的路径没有检出 race，不能证明未执行分支；再用库存、版本、总账等独立业务不变量补足范围。[官方 race detector 说明](https://go.dev/doc/articles/race_detector)

值接收者的危险要看字段关系：本课 `count *int` 复制后仍指同一整数，所以产生分离锁保护共享对象的 race。如果字段改成普通 `count int`，方法可能只修改自己的副本，原对象不变，表现为没有 race 但更新丢失；这并不让含同步对象的值接收者成为合适设计。指针接收者修复还必须保证所有调用者指向同一对象：两个不同 wrapper 各自持 Mutex、内部却指向同一 map/数组，仍是 `different-lock` 那类问题。

普通 map 并发访问的 runtime 检测不是每个调度都保证触发 fatal；本次 native 运行确实触发。probe 收不到目标诊断就判失败，而不是拿“本次没有崩溃”证明错误安全。race 证据针对实际访问缺少同步，业务不变量证据针对最终业务事实，三个维度各自有范围。

fatal 不是普通 panic；故障进程的 defer 不能作为可靠清理机制。probe owner 在 CommandContext/CombinedOutput 返回后已经等该子进程退出，并在自己的 defer 删除这次 MkdirTemp 创建的目录。故障进程没有数据库、文件、外部服务副作用；死锁资源都是进程内锁。临时目录绝对路径经检查，仅删除 owner 自己创建的目录。

## E06：Once 缓存一次执行，不负责失败重试

`ObserveOnce` 的正常初始化有 8 个调用者。唯一进入 f 的调用者写 config，其他 Do 会等初始化完成；各自在自己结果槽位中读 `ready`。`NormalCalls=1, AllReady=true` 是执行次数与可见性两项事实。源码 `D:/Go/src/sync/once.go` 先看 done，慢路径使用 mutex，f 返回或 panic 展开时 defer 将 done 标 true。单靠 CAS 把 done 提前置 true 不够：其他调用者可能在 f 完成前返回，违反 Once 完成保证。

panic 实验在**调用 Do 的同 goroutine** 的外层闭包里 recover；第一次初始化 panic，done 仍标完成。第二个 Do 不会重新执行初始化，`PanicInitCalls=1`。若 f 内递归调同一个 Once.Do，也会等待自己的 f 完成导致死锁。原始 Once.Do 后续调用不重复 panic；`OnceFunc/OnceValue/OnceValues` 的包装函数会在每次调用重复同一 panic 值，不能混为一谈，已核验 `D:/Go/src/sync/oncefunc.go`。

错误初始化把 error 保存到外部字段，Do 本身没有 error 返回协议。两次调用只执行一次失败初始化，`ErrorInitCalls=1`；OnceValues 也缓存第一次的 `(值,error)`，不会因为 error 非 nil 自动重试。如果业务要求失败后再试，就不能同时要求“一次执行”。`RetryingInitializer.Get` 在 mutex 下检查成功缓存、执行一个有上限的 loader、只缓存成功；本次失败返回给 caller，下次 caller 显式调用构成新尝试，最终 `RetryCalls=2, RetryValue=ready`。

不要并发把旧 Once 改成零值来“重置”：那会替换仍有人使用的同步对象。需要更复杂的并发初始化状态机时，可设计 idle/loading/ready/failed 与等待通知；本课先用最小串行尝试把责任讲清。真实网络 loader 必须 ctx/超时；持锁 loader 会串行化所有请求，这个成本应明确。

## E07：Pool 借出的是对象，归还交出所有权

`ObservePool` 的 dirty Pool 故意不 Reset 就 Put，下一次若取回同一 buffer，会看到前一个请求的 15 字节文本；若 Pool 丢弃/选择别的对象，dirty-len 可能为 0。不能为了证明危险而断言每次一定复用，尤其 race 模式的实现可以随机丢弃对象。危险来自“允许复用而调用者没有清理”，不是每次都要发生泄漏。

修复 clean Pool 的协议：New 创建空 buffer → Get 获得独占使用权 → 写入本请求数据 → Reset → Put → 从此不再读写这个对象。所有能进入 clean Pool 的对象都为空，所以无论 Get 返回旧对象还是 New，都有 `clean-len=0`。bytes.Buffer.Reset 不擦除底层容量的所有字节；有秘密擦除要求时应另外设计，并限制超大缓冲保留，不能把 Reset 当安全清零。

Pool 自身并发安全，不等于借出的同一对象可以被两人同时修改。Put 两次、Put 后继续使用、把对象返回给 caller 同时 Put，都破坏所有权。任意对象可以无通知移除，GC 实验只记录本次 New 次数；Pool 不保证 FIFO、容量、生命周期，也不能保存订单状态、数据库连接、socket 等必须有明确关闭协议的资源。Pool 没有 Close，临时对象由正常引用和 GC 管理。[sync.Pool 契约](https://pkg.go.dev/sync#Pool)

## E08/E09：等待放大、锁外 I/O 与锁顺序

`ObserveIO` 使用真实 httptest loopback HTTP，不是纯函数假请求。参与者与收尾如下：

| 步骤 | owner / 请求 goroutine / HTTP handler | 当前等待与状态 | 观察与收尾 |
| --- | --- | --- | --- |
| 1 | owner 创建 server 和 responseGate，启动请求 goroutine | owner 等 requestStarted 或 ctx | server 临时端口专属本次实验 |
| 2 | 请求 goroutine Lock、复制 price；修复版立即 Unlock | 调 client.Do 等网络响应；坏范围版一直持锁 | price 为 string 值，本课没有同时变价 |
| 3 | server handler 实际收到请求并 close(requestStarted) | handler 等 responseGate 或 request ctx | owner 已知请求未完成，不用 sleep 猜 |
| 4 | owner TryLock | hold=true 必失败；false 成功后立即 Unlock | 失败 TryLock 本身不建立可见性，我们只检查可获得性，不读受保护字段 |
| 5 | owner release responseGate | handler 写 priced；客户端读 Body、Close；坏范围版此时才 Unlock | done channel 交回本次返回值 |
| 6 | owner 接收 done 后函数返回 | CloseIdleConnections、release、server.Close 的 defer 清理 | 出错/取消路径也先释放门、等请求退出，再关闭服务器 |

本次两行输出 `hold=true observer-acquired=false`、`hold=false observer-acquired=true` 证明锁范围的实际影响，不拿平均耗时或 HTTP 200 当证明。Body 必须真的读到 `priced`。生产网络 I/O 若 2 秒等待，那么其他只需读本地字段的请求也可能被锁拖 2 秒；把数据复制到锁外能消除这个锁等待范围。但如果 I/O 后要基于旧价格更新订单，必须重新加锁并核验版本/条件，锁外 I/O 并不自动保证整个远端业务事务。

`ObserveCanceledIO` 沿同一实现运行，先等 requestStarted，确认真实 handler 正在响应门等待，再由 owner 调 cancelRequest。它在释放 responseGate **之前**等待 done，避免成功响应与取消同时发生。真实 client.Do 返回保留 context.Canceled 的错误链，持锁版先解锁，再发送 done；handler 观察自己的请求 context 后退出，owner 释放门并关闭服务器。demo 输出 `error-is-canceled=true`，测试对锁内/锁外两种版本都核对取消错误与观察者的锁状态。另一个测试核对调用前就已取消的路径，二者的实际等待阶段不同。

错误返回从 HTTP NewRequest/client.Do/ReadAll/Body.Close 传到 owner，用 `%w` 保留底层原因。取消是请求范围的通知；本课 ctx 分支释放门并接收 done，才能证明请求 goroutine 已退出。server.Close 等 handler 收尾。持锁范围只包含本地合成数据，未留下运行中的服务器或等待 gate 的 worker。

`circularOrder` 的屏障让 A 已锁 a、B 已锁 b 后再要求对方锁，形成循环等待，日志与 runtime 栈直指第二次 Lock 和 owner Wait。`Transfer` 修复按稳定唯一 id 小→大取两把锁，无论业务转账方向如何，都采用同一个全局顺序；余额检查与扣/加在同一对锁内。defer 后登记先释放，所以按大→小解锁。两个不同账户单独 Balance getter 不构成并发总账快照；本课先 Wait 全部转账退出，再读取总额。因此 demo `a=100,b=100,total=200`，测试 400 次双向转账仍是 1000/1000/2000，是真正的业务副作用验证。

## 实际验证范围

正常 test/vet 不包含标签下的危险源码；race 正常检查与故障 race 报告是两条证据，必须分别完成。正常测试除了“跑过并发”还断言库存守恒、唯一订单状态转移、输入/输出隔离、完整版本、200 次更新无丢失、余额守恒、HTTP 请求与锁范围、Once/Pool/Value 具体契约。历史材料和当前源码的核对不代替实验。

所有实际命令、编译器问题与补验结果、观察值以及清理事实统一记录在 [STATUS.md](STATUS.md)。本课不声称覆盖所有 CPU 架构、所有调度、生产性能、跨进程订单幂等性或未执行路径；这些不是这次独立学习模块的验收范围。官方查证入口：[内存模型](https://go.dev/ref/mem)、[sync](https://pkg.go.dev/sync)、[sync/atomic](https://pkg.go.dev/sync/atomic)。
