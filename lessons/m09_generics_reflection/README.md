# M09：读框架所需的泛型、函数选项、迭代器与反射

这节课围绕订单查询这一个场景：订单结果保留类型，客户端构造固定配置，批量序列可以提前停止，字符串请求参数绑定到查询 DTO。读完应能沿函数签名找到实际对象、预测调用与结束顺序，并对配置别名、资源清理和运行时类型错误做有证据的小改动。

本课没有硬前置，补齐正在使用的函数值、闭包、指针、切片和接口概念。建议按 M02/M03/M04 的顺序学习，但其交付不影响这里的独立运行。核心实验运行与解析约 10～20 分钟；真实库源码与编译器矩阵可分段继续阅读。所有原理、错误和修复都直接给出，不留 exercise。

## 运行与阅读入口

从仓库根目录运行嵌套 module。`go -C` 先切换到课程目录，`cmd/demo` 的文件实验因此只使用本课 `.local/`。根目录的 `go test ./...` 不会自动测试这个嵌套 module。

```powershell
go -C lessons/m09_generics_reflection run ./cmd/demo
go -C lessons/m09_generics_reflection run ./cmd/demo -part all
go -C lessons/m09_generics_reflection run ./cmd/compilecheck
go -C lessons/m09_generics_reflection test ./...
go -C lessons/m09_generics_reflection vet ./...
```

Windows 的 race 还需要能编译 amd64 的 C 工具链。当前机器默认旧 GCC 曾报 `64-bit mode not compiled in`，随后使用主执行者已核验的 portable GCC 16.2.0、仅设置进程环境补验成功。仓库根目录可按本机记录复跑：

```powershell
$m09Toolchain = Get-Content -LiteralPath '.local/course-orchestrator/toolchain.json' -Raw | ConvertFrom-Json
$env:CC = $m09Toolchain.cc
$env:CXX = $m09Toolchain.cxx
$env:CGO_ENABLED = '1'
$env:PATH = "$($m09Toolchain.bin_dir);$env:PATH"
go -C lessons/m09_generics_reflection test -race -count=1 -timeout 40s ./...
```

这个忽略文件属于当前机器的编排记录；其他机器使用自己可用的 amd64 C 编译器运行同一条 Go 测试即可。详细成功路径与旧诊断保留在 STATUS，课程不修改全局 CC/PATH。

默认 `-part core` 顺序运行 E1～E4。`-part generics|options|iter|reflect|library` 分段选择，`-part all` 再加 E5。`compilecheck` 的坏片段是显式字符串输入：每次编译最多 8 秒，总入口最多 60 秒，正常包本身可以编译。

| 文件 | 读法与职责 |
| --- | --- |
| [cmd/demo/main.go](cmd/demo/main.go) | `flag.String` 得到 `*string`；解引用后交给 `lesson.Run`，错误使入口退出 |
| [internal/lesson/demo.go](internal/lesson/demo.go) | `Run` 分段调度；四个 `RunXXX` 组织输入、观察与修复，`CreateOrdersFile` 写真实合成文件 |
| [internal/lesson/generics.go](internal/lesson/generics.go) | `Order/Result/MapResult/MapSlice/Sum/Unique` 的类型与业务逻辑 |
| [internal/lesson/client.go](internal/lesson/client.go) | `Option/NewClient` 的默认值、顺序、验证，以及三个配置快照边界 |
| [internal/lesson/sequence.go](internal/lesson/sequence.go) | `FileLines` 打开、读取、yield、关闭；`ContinueAfterStopBad` 违反停止协议 |
| [internal/lesson/binding.go](internal/lesson/binding.go) | `NilStatus` 检查反射边界；`BindFlat` 转换全部成功才写 DTO |
| [internal/lesson/library.go](internal/lesson/library.go) | 调用真实 backoff/v5 的 `Retry`，观察成功、用尽与已取消 ctx |
| [internal/compileprobe/probe.go](internal/compileprobe/probe.go) | 11 项真实编译器接受/拒绝实验，临时源文件和对象文件随后删除 |
| `internal/lesson/*_test.go`、`internal/compileprobe/probe_test.go` | 按业务不变量、清理事实和语言边界进行独立断言 |
| [STATUS.md](STATUS.md) | 本次实际版本、命令、结果、精确未验边界与清理事实 |

先读 `main → Run → RunOptions → NewClient/WithHeaders → RunSequence → FileLines`，再把 E1/E4 的类型和反射细节补上。各个 `RunXXX` 的输出标签与下面的实验编号一一对应。

现在必须理解：方括号类型参数与圆括号实参、约束允许的操作、闭包何时读取可变对象、seq 是调用协议、owner 在哪里结束资源、反射操作为何可能 panic。递归约束、编译器类型统一算法、unsafe、代码生成和底层对象布局在遇到真实源码时按需查；它们不影响这组实验的正确性。

## 版本事实与原材料核验

本次实际 `go version` 是 `go1.27.0 windows/amd64`，`GOROOT=D:\Go`，模块声明 `go 1.27.0`，没有 `toolchain` 指令，`GOTOOLCHAIN=auto`，没有 `go.work`。标准库说明以本机 `go doc` 和 `D:/Go/src` 的 Go 1.27.0 源码为准，网页通过带版本的包链接核对。

本课专门纠正旧资料中的“Go 不支持方法自己的类型参数”。[Go 1.27 发布说明](https://go.dev/doc/go1.27)已宣布支持泛型方法，同时明确接口方法仍不能声明类型参数；[方法声明规范](https://go.dev/ref/spec#Method_declarations)给出语法。下面用真实编译器同时验证两个边界，不只转述新结论。

唯一第三方依赖锁定 `github.com/cenkalti/backoff/v5 v5.0.3`，`go.sum` 记录校验值；依赖自己的 go.mod 最低版本是 `1.23`。这里解释锁定源码，不因为本课工具链更高就改库 API。[版本文档](https://pkg.go.dev/github.com/cenkalti/backoff/v5@v5.0.3)、[retry.go](https://github.com/cenkalti/backoff/blob/v5.0.3/retry.go)是外部查证入口。

已只读定位原仓库 `examples/advanced-syntax/reflection/main.go` 与 `examples/basic-syntax/functions/main.go`。前者已有 Type/Value/Kind、导出字段、方法绑定的示例，后者解释值传递和闭包。本课扩展业务边界与独立断言，不修改它们。主目录 `files/语法进阶/反射/反射` 实为零字节文件，不能据此说“材料已经讲过”。适用 `.codex/AGENTS.md` 为空；编排与教学要求按当前 ORCHESTRATION/TEACHING_STYLE 执行。

## E1：订单 Result 与小集合变换

命令：`go -C lessons/m09_generics_reflection run ./cmd/demo -part generics`。输入是 101/102/103 三条订单，金额分别 1200/2300/700 分，客户分别 alice/bob/alice。

调用链是：`main → Run → RunGenerics → SampleOrders → Success[Order] → Result[Order].Map[string] → MapResult[Order,string] → mapper(Order) → Success[string] → RunGenerics`。全程同步，mapper 不会启动任务。失败链把 `Failure[Order](original)` 交给 Map，MapResult 先看 Err，不进入 mapper，返回 `Result[string]{Value:"", Err:original}`。

### 把签名逐段读成业务事实

```go
type Result[T any] struct { Value T; Err error }
func Success[T any](value T) Result[T]
func MapResult[T, U any](r Result[T], mapper func(T) U) Result[U]
func (r Result[T]) Map[U any](mapper func(T) U) Result[U]
func MapSlice[S ~[]E, E, U any](values S, mapper func(E) U) []U
func Sum[T IntegerAmount](values []T) T
func Unique[T comparable](values []T) []T
```

`[T any]` 声明类型参数；`(value T)` 接收运行时值。`Success(orders[0])` 由实际值类型推导 T 为 Order；`Failure[Order](err)` 必须明确 Order，因为 error 参数没有提供 T 的信息。编译器会实例化函数/类型，并按相同约束检查函数体；不是运行时先猜一个 T。

`MapResult` 的 r 是值副本。Result 若保存的是 Order，就复制其三个值字段；如果 T 是指针、slice 或 map，复制 Result 不会自动克隆其所指对象。`mapper func(T) U` 是一个参数和一个结果的函数值；回调的签名参与 U 推导。这里的 mapper 没有 error 返回位，若 mapper 自己 panic，正常 Map 会向调用者传播，并不会被包装为 Result.Err。

`MapSlice` 的 S 代表整个切片类型，E 是其元素，U 是投影结果。`~[]E` 允许底层类型为 `[]E` 的命名切片。函数分配新的结果底层数组，输出类型是普通 `[]U`；这与 `slices.Clone` 保留 S 的承诺不同。

`IntegerAmount interface{~int | ~int64}` 中的 `~int64` 包含底层类型为 int64 的所有类型，例如 Cents。写 `int64` 只允许这个精确类型，不包含 `type Cents int64`。`|` 是类型集合并集。因为集合中每一种类型都允许 `+`，Sum 可以求和；约束不会自动解决货币溢出，当前合成金额的确定总和是 4200 分。

`Summarizer interface{ Summary() string }` 是基本接口，可以存运行时对象，也可以当约束。`IntegerAmount` 和 `comparable` 是非基本接口，只用于类型约束，不能声明同类型的变量来“装任意数字”。`any` 是空接口的别名；允许任意 T 不意味着 T 支持任意操作。

`comparable` 使 T 可比较并可做 map key。不要推出“Unique[any] 永远安全”：Go 1.20 起 any 满足 comparable，若动态值是 `[]int`，哈希 key 仍 panic。本课危险入口真实打印 `hash of unhashable type: []int`，修复保留具体 OrderID，断言首次出现顺序为 `[102 101 103]`。Go 的可比较性不等于 Java 中对象普遍拥有 hashCode/equals。

### Go 1.27 方法变化与旧项目选择

`func (r Result[T]) OK() bool` 只使用接收者的 T，Go 1.18 起就合法。`func (r Result[T]) Map[U any](...)` 在方法名后声明新的 U，这才是 Go 1.27 新能力。读 `r.Map(f)` 要先从 receiver 找 T，再从 f 找 U。

当前真实标准库 `(*math/rand/v2.Rand).N[Int intType](n Int) Int` 也是泛型方法。`intType` 是库内部约束名；调用者只需传满足约束的整数类型。demo 传 OrderID(10)，返回类型仍是 OrderID，断言范围 `[0,10)`，不依赖具体随机数字。它要求 n>0，违例会 panic。它是伪随机生成器，不用作安全令牌。

旧项目的 `go` 语言版本低于 1.27 时，可保留包级 `MapResult[T,U]`；不要仅因为机器上的编译器新，就把源码改成新语法。本课 `compilecheck` 用 **Go 1.27.0 编译器的 `-lang=go1.26`** 验证旧语言模式，不声称安装并运行了 Go 1.26 工具链。

接口方法不能写 `Map[T any](T) T`，Go 1.27 仍拒绝；拥有 `Map[T any](T) T` 的具体类型，也不能自动实现要求 `Map(int) int` 的普通接口。需要具体接口时，设计具体签名或把类型参数放到接口类型本身，然后用普通方法实现；不在此扩展抽象层。

Go 的实例化类型有明确类型身份。实验显示 Result[Order] 与 Result[string] 的 reflect.Type 不同，不能照搬 Java 的“运行时统一擦成一个 Result”模型。语言规则也没有承诺每个 T 都生成一份独立机器码；字典/shape 等编译器实现不在这节课的必需知识中。

### 标准库真实签名与观察

| 当前 Go 1.27.0 签名 | 如何读与实验边界 |
| --- | --- |
| `slices.Clone[S ~[]E, E any](s S) S` | 同一 S 输入输出，命名 OrderList 保留类型；元素按赋值复制。Order 值字段独立，`[]*Order` 的对象仍共享 |
| `maps.Keys[Map ~map[K]V, K comparable, V any](m Map) iter.Seq[K]` | 参数是任意底层为 map 的类型；K 必须可作为 key；返回产生 K 的函数值，没有排序保证 |
| `slices.Sorted[E cmp.Ordered](seq iter.Seq[E]) []E` | 消费完整序列，再排序；不是保持懒执行的序列。对 maps.Keys 的结果应用它，得到 `[101 102]` |
| `slices.All[Slice ~[]E, E any](s Slice) iter.Seq2[int,E]` | 返回索引和值，索引顺序为普通切片顺序；E 值的可变别名规则照常适用 |
| `slices.Collect[E any](seq iter.Seq[E]) []E` | 消费序列，收集新切片；空序列返回 nil，没有承诺 clone 元素对象 |
| `maps.All[Map ~map[K]V,K comparable,V any](m Map) iter.Seq2[K,V]` | 成对交付键值，map 遍历顺序仍无保证；本课不靠顺序断言 map 内容 |

这些签名已通过 `go doc` 与本机 `slices/iter.go`、`maps/iter.go`、`slices/slices.go` 核对。[slices Go 1.27.0 文档](https://pkg.go.dev/slices@go1.27.0)、[maps Go 1.27.0 文档](https://pkg.go.dev/maps@go1.27.0)可直接定位同版本 API。

## E2：NewClient(opts ...Option) 的三个快照边界

命令：`go -C lessons/m09_generics_reflection run ./cmd/demo -part options`。输入是 endpoint、timeout 和 `X-Region` header；这里的 Client 只表示配置对象，没有实际 HTTP 请求，`.example.test` 不会被连接。

```go
type Option func(*Config) error
func WithHeaders(headers map[string]string) Option
func NewClient(opts ...Option) (*Client, error)
func NewClientConfig(cfg Config) (*Client, error)
func (c *Client) Snapshot() Config
```

`Option` 是一个命名函数类型。`WithHeaders(...)` 第一次调用返回函数值；此时还没有 Client。返回闭包接收的是构造器后来交给它的 `*Config`。`NewClient` 中 `opts` 是 `[]Option`，`opt(&cfg)` 同步执行闭包，修改同一个本次构造的 cfg。

默认 endpoint 是 `https://orders.example.test`，timeout 是 2 秒。循环按传入顺序应用，后者可以覆盖前者；循环完才通过 NewClientConfig 校验。因而 `[WithTimeout(0), WithTimeout(1s)]` 最终有效，单独 WithTimeout(0) 拒绝，nil Option 在调用前被拒绝。`Option` 自己返回 error 时，构造器用 `%w` 加上索引并保留原因，测试通过 errors.Is 核对。此 API 的应用顺序/最终校验是本课设计，并非所有 options 库的统一规则。

`WithTimeout(timeout time.Duration)` 捕获本次参数变量，参数是 duration 的值副本。外部变量后来重新赋值不会改这份值。`WithHeadersBad(headers map[string]string)` 的参数也是值副本，但 map 值仍指向同一个可变内容，改元素会被闭包观察。若外部仅把 headers 变量重新绑定为另一张 map，则不会重绑该参数；读“捕获变量”时必须认清到底是哪一个作用域的变量。

| 时间 | 入口与对象 | 错误版本观察 | 修复版本观察 |
| --- | --- | --- | --- |
| 1 | RunOptions 创建 headers，值为 sg | WithHeadersBad 捕获原 map 值 | WithHeaders 立即 `maps.Clone` 得到 sg 快照 |
| 2 | 入口把原 map 元素改成 changed-before-apply | 尚未构造，危险闭包将读新内容 | 安全快照仍为 sg |
| 3 | NewClient 建默认 cfg，调用 opt(&cfg) | cfg.Headers 取得外部 map | cfg.Headers 取得选项快照的独立副本 |
| 4 | NewClientConfig 校验后构造 | 最终 Clone 固定“应用时”的值，只能修复构造后窗口 | 最终 Clone 固定 sg |
| 5 | 入口继续改原 map 为 changed-after-build | NewClientBadAlias 的 Client 看到新值 | 正常 NewClient 的 Client 保持旧构造值 |
| 6 | Snapshot 返回 map 后入口篡改它 | 返回原内部 map 会暴露别名 | Snapshot 再 Clone，内部 sg 未变 |

实际输出为：`option-captured-map=changed-before-apply`、`client-alias=changed-after-build`、`captured-snapshot=sg`、`getter-copy internal-header=sg`。这证明三处复制各自修复不同窗口，不能只在最后 Clone 然后宣称 Option 已是创建时快照。

map[string]string 的 string 元素不可原地修改，所以浅 Clone 足够；若配置改成 map[string]*Policy，需要另行定义对象复制策略。这里不支持调用者在构造期间并发改配置，不把 Clone 当成同步手段；任意自定义 Option 也应遵守不私存构造指针的契约。

functional options 适合默认行为清楚、可选项较多、希望提供语义命名的构造入口。配置已经由配置文件完整读入、需要打印/校验/比较时，显式 Config struct 更直观。两条路径共用校验和最终快照，不为了语法风格把所有构造器改成 options。它与 Java Builder 有“先描述配置再构造”的相似处，但 Go 闭包、map 别名和错误返回仍按上面的实际规则执行。

## E3：iter.Seq/Seq2、range-over-func 与真实文件拥有者

命令：`go -C lessons/m09_generics_reflection run ./cmd/demo -part iter`。CreateOrdersFile 把三条订单写到本模块 `.local/m09-orders-*.txt`，writer 先关闭；FileLines 随后每次打开独立 reader。正常、Pull2 和危险包装各使用新的 SequenceTrace。

```go
type Seq[V any] func(yield func(V) bool)
type Seq2[K,V any] func(yield func(K,V) bool)
func FileLines(path string, trace *SequenceTrace) iter.Seq2[string,error]
func Pull2[K,V any](seq iter.Seq2[K,V]) (next func() (K,V,bool), stop func())
```

Seq 是函数类型，Seq2 每次交付两个值；K/V 名字只是通用名字，不要求一定是 map 键值。本课的第二项是 error，所以消费入口写 `for line, err := range ...`。range-over-func 从 Go 1.23 起可用。[iter 文档](https://pkg.go.dev/iter@go1.27.0)和[range 规范](https://go.dev/ref/spec#For_range)说明 yield 与停止协议。

`FileLines(path, trace)` 仅创建闭包，文件尚未打开。真正执行这个函数值时才 os.Open；trace=nil 时分配本次私有观察对象，避免取得文件后才遇到 nil 观察指针。成功打开后立即安排 defer Close，reader 的 owner 是迭代器调用。

range 把循环体包装成 yield。迭代器每调用 yield，一条订单交给循环体；正常循环继续返回 true；break/return 使 generated yield 返回 false，迭代器必须立即返回，不可再调用它。读取真实文件与循环体发生在同一条同步调用链；没有生产者 goroutine、channel 缓冲或 channel close。Seq 也不自动提供跨 goroutine 安全性。

| 步骤 | 当前参与者/函数 | 真实状态变化 | 下一步与证明 |
| --- | --- | --- | --- |
| 1 | RunSequence → CreateOrdersFile | 三行写入，writer Close，入口拥有路径删除责任 | 返回路径交给 FileLines |
| 2 | range 调用 FileLines 返回的闭包 | reader OpenCount=1，注册 defer Close | scanner.Scan 同步读第一行 |
| 3 | iterator → yield → consumer | 第一行被打印，循环继续，yield 返回 true | trace 记录 yield:true，读第二行 |
| 4 | consumer 在第二行 break | yield 返回 false | iterator 记录 yield:false 并 return |
| 5 | FileLines defer | reader CloseCount=1，保存 CloseErr | iterator 返回，消费入口恢复执行 |
| 6 | RunSequence 打印 trace | `read=2 open=1 closed=1` | 函数返回前删除合成文件，删除错误合并进返回 error |

实际事件链严格为 `[open read:1 yield:true read:2 yield:false close]`。测试断言 read=2，意味着第三行没有被扫描，不是“扫描了三行但只打印两行”。CloseCount 只在真正 `file.Close()` 成功后递增；若关闭错误，入口读取 CloseErr 并返回。当前实验没有注入文件系统 Close 失败，不能外推“所有 Close 永远成功”。

### Pull2 的停止责任

`iter.Pull` 对 Seq 返回 `next func()(V,bool)` 与 `stop func()`；Pull2 对 Seq2 返回三项的 next。它们让 owner 主动取下一项，不使序列变成 channel。调用 next 取一条后，序列仍可停在 yield，文件尚未关闭，本次输出 `closed-before-stop=0`。

入口在取得 next/stop 后立即 `defer stop()`，演示又显式调用 stop。stop 结束底层迭代调用，让它从 yield 返回并执行 defer，所以随后 `closed=1`，next 只返回零值和 false。stop 可重复，序列自然结束后调用也合法；owner 不再需要下一项而 next 还没返回 false 时，必须调用 stop。多个 goroutine 同时调用 next/stop 是错误用法，本课全部顺序调用。

“创建序列”“取到一个值”“通知停止”“迭代器返回”“reader 关闭”“入口删除文件”是不同事件。range 的同步协议让第五步完成后才继续入口；Pull2 则让 owner 明确负责 stop。不要按 Java Stream 对象“拿到 iterator 就会自动关闭资源”的直觉补出保证，也不能把 Go seq 解释成 BlockingQueue。

### 有界错误与修复

ContinueAfterStopBad 对外调用 `yield(line,err)` 却不看返回 bool。消费者第一项 break 后，坏包装继续读第二项并调用 generated yield，实际 panic 文案是 `range function continued iteration after function for loop body returned false`。FileLines 的 defer 仍在栈展开时关闭 reader，CatchPanic 在本调用链外层恢复；输出 read=2/closed=1。修复代码就是 FileLines 的 `if !yield(...) { return }`，停止后既不再次 yield，也不多读一项。

独立测试还验证完整消费三行、空文件、open 失败、Scanner 默认 token 上限错误、消费者本身 panic 及 Pull2 的重复 stop。open 失败未拥有 reader，交付一个包装 error 后返回；Scanner 超限在 Err 分支交付包装 error，reader 仍关闭。测试用真实文件，只代表这些文件协议边界，不冒充数据库 Rows 或实库游标验收。

## E4：反射只沿必要边界读和写

命令：`go -C lessons/m09_generics_reflection run ./cmd/demo -part reflect`。场景是 `customer/limit` 字符串输入绑定到 Query；不实现嵌套对象、依赖注入、无限类型转换或通用框架。

Type 是精确类型身份，Value 是本次运行时值的反射表示，Kind 是操作大类。`Cents(1200)` 的 Type 为 lesson.Cents，Kind 为 int64，Value.Int() 读出 1200；相同 Kind 不表示 Type 相同。TypeOf/ValueOf 从 any 的动态值出发，不凭变量的静态接口名保留一层接口槽。

| 输入/Value 来源 | 状态与可做操作 | 故障与修复 |
| --- | --- | --- |
| `var x any; ValueOf(x)` | x 无动态类型/值；TypeOf=nil、Value.IsValid=false、Kind=Invalid | 直接 IsNil panic；先 IsValid |
| `var p *Query; var x any=p` | x!=nil，动态类型 *Query、动态值 nil、Kind=Pointer | IsNil 适用，返回 true；不能仅用 x==nil 检查 |
| `ValueOf(42)` | Kind=Int | IsNil 不适用，会 panic；NilStatus 用显式 error 拒绝 |
| `ValueOf(&empty).Elem()` | 取得接口变量槽，Kind=Interface | IsNil 适用；它与 ValueOf(empty) 的无效动态值不同 |
| `ValueOf(query).FieldByName(...)` | 按值传入结构体，字段 CanSet=false | SetString panic；使用原对象指针再 Elem |
| `ValueOf(&query).Elem().FieldByName("secret")` | CanAddr=true，但未导出：CanSet/CanInterface=false | 可寻址不是允许访问/设置；通过导出字段的正式 API 修改 |

IsNil 的适用 Kind 是 Chan、Func、Interface、Map、Pointer、Slice；无效 Value 和其他 Kind 不适用。测试分别验证各类 typed nil，普通非 nil 指针，以及直接危险调用的 panic。它不是任意值的“判空函数”。[reflect Go 1.27.0 文档](https://pkg.go.dev/reflect@go1.27.0#Value.IsNil)描述这条 API 边界。

### 平面绑定的调用链与修复

`RunReflection → BindFlat(&fixed,input) → ValueOf(dst) → 校验有效/Pointer/非nil → Elem → Struct → Type.Field/Tag.Lookup → Value.Field/CanSet → 暂存转换 → 全部成功才 Set → 返回 → RunReflection`。

`dst any` 接收动态目标，`input map[string]string` 是调用者的字符串参数；函数只读输入 map。`query` tag 在 Type.Field 中读取，实际字段内容通过 Value.Field 得到。只接受请求中出现的导出 string/int 字段；未提供的字段保持旧值，未知 key、重复 tag、未导出字段、unsupported Kind 或数字溢出均返回 error。类型转换之后的业务校验，例如 limit 必须为正数，应由业务层明确完成，这个 binder 不凭字段名字猜规则。

每个待写值使用 `reflect.New(fieldType.Type).Elem()` 建立精确字段类型的暂存 Value。int 用对应 bit width 的 ParseInt 转换并检查溢出，失败用 `%w` 保留原因。待写字段的 Value 指向原 DTO，但准备阶段不 Set；输入全部验证完才逐个写入。因此这是**本函数串行失败路径的全部验证后发布**，不承诺多线程读者看到原子的多字段更新，也没有为任意结构体提供事务机制。

危险版本 BindQueryBad 先写 Customer，再解析 limit。`{"customer":"bob","limit":"oops"}` 返回 error 时留下 `bob/limit=2`；修复 BindFlat 对同一输入返回 error，并保持 `alice/limit=2`，把 limit 改为 3 后才得到 `bob/limit=3`。独立断言检查对象变化，不仅检查 error 非 nil。

### tag/方法读取并不自动意味着绑定发生

`query:"customer"` 与 `json:"customer"` 是两套消费者解释的元数据。Tag.Get/Lookup 只读取字符串；对错别字 tag，反射本身不会自动理解或报业务错误。Query 的值方法集只有 Summary，*Query 方法集包含 SetLimit 和 Summary；非接口类型的 reflect 仅枚举导出方法。

`reflect.Type.MethodByName` 得到未绑定 receiver 的方法，SetLimit 的 Func.Call 需要 `[&fixed, 4]` 两项。`reflect.ValueOf(&fixed).MethodByName` 得到已绑定该对象的方法值，只传 `[6]` 一项。缺失方法返回无效 Value，调用前先 IsValid。错误传入 string "6" 会在 Call 时 panic，Limit 仍是 4；正确 int(6) 才修改原 Query。这把一部分编译期检查移到了运行时，知道类型时优先直接调用或用接口。

真实 `json.Unmarshal(data []byte, v any) error` 实验读取 json tag，得到 alice/limit=2；nil *Query 返回 *json.InvalidUnmarshalError。当前 Go 1.27.0 源码阅读链为 `Unmarshal → decodeState.unmarshal → value → object → indirect/cachedTypeFields → typeFields 的 json tag 读取 → 字段值解码`；`unmarshal` 用 ValueOf 检查非 nil Pointer，object 为 struct 取得字段缓存。这里按实际符号定位，不把方法名猜成反射容器。

JSON 解码不是原子绑定。有效 JSON 中 `limit:"oops"` 类型错误会返回 *json.UnmarshalTypeError，之前 Customer 已变为 bob。本课修复是先解码 candidate，再只在 err==nil 时 `fixed=candidate`；失败候选为 bob/2，正在使用的 fixed 保持 alice/2。Query 只有值字段，此复制足够；含 map/slice/指针的 DTO 还要明确隔离可变对象，不能照抄结构体赋值然后宣称完成深复制。

## E5：一份真实库同时读泛型、Option、普通接口和取消

命令：`go -C lessons/m09_generics_reflection run ./cmd/demo -part library`。使用真实 `backoff/v5 v5.0.3`，Operation 只读合成订单；失败都是可重复的合成读，没有远端写入。没有中间件或服务器安装需求。

```go
type Operation[T any] func() (T, error)
type RetryOption func(*retryOptions)
func Retry[T any](ctx context.Context, operation Operation[T], opts ...RetryOption) (T, error)
func WithMaxTries(n uint) RetryOption
func WithBackOff(b BackOff) RetryOption
type BackOff interface { NextBackOff() time.Duration; Reset() }
```

这里 T 由 `operation func()(Order,error)` 推导成 Order，Retry 的返回值精确为 Order。Operation 没有 ctx 参数；Retry 接收 ctx 不意味着 operation 自动接收取消通知，真实 I/O operation 应在闭包里捕获同一个 ctx 并传给相应 I/O API。

RetryOption 也是闭包，但参数 retryOptions 在库内不导出，应用层通过 WithXXX 创建选项。锁定源码先构造默认值、顺序应用 Option，再注册 Timer.Stop，调用 BackOff.Reset，最后开始循环。WithMaxTries 捕获 uint 值；WithBackOff 捕获 BackOff 接口值，接口里若是可变策略指针，策略对象仍共享，库没承诺复制策略。每次 Retry 本课创建独立策略，不复用有状态对象。

| 路径 | 锁定源码的调用与等待 | 真实观察 |
| --- | --- | --- |
| 首次暂未可见，第二次成功 | operation 返回 error；查次数/永久错误/ctx；NextBackOff=0；Timer.Start；select 等 Timer.C 或 ctx.Done；再次 operation 成功返回 | success calls=2，value.ID=101 |
| 每次失败，MaxTries=2 | 第二次 error 后先命中次数限制，返回该次 error | exhausted calls=2，errors.Is(original)=true |
| ctx 在 Retry 前已 cancel | 库先执行 operation 一次，再在 error 路径检查 context.Cause | already-canceled calls=1，返回 context.Canceled |

第三条不能推出所有取消后的操作都返回 canceled：这个版本先看 operation 成功与 MaxTries，再检查 ctx；若第一次成功会先返回成功，若 MaxTries=1 且第一次失败会先返回该操作错误。已取消 ctx 也不能单独证明“业务副作用未发生”。这正是按实际库调用顺序阅读的价值。[锁定 retry.go 源码](https://github.com/cenkalti/backoff/blob/v5.0.3/retry.go)支持这些顺序，本课断言只覆盖上表实际运行的三条路径。

context deadline 和库的 Timer 分属不同 owner：RunLibrary 用 defer cancel 释放自己的 deadline timer；Retry 在返回时执行内部 defer Timer.Stop。当前源码的 defaultTimer.Stop 检查内部 timer 是否创建。本课不访问库的私有 timer，未把源码上的清理路径冒称直接计数的运行时清理；文件 reader 的计数则是直接观察。

ConstantBackOff(0) 让真实 timer/Retry 路径有界、无需等待较长退避。最大尝试次数控制库循环，但不是强行终止一个卡住的 operation；operation 自己若不返回或不接受 ctx，库无法在这条同步调用中替它抢占退出。这里所有 operation 都立即返回，正常上限三次。

## 编译矩阵、断言证明范围与结束

`compilecheck` 每个片段没有 import，只检查一个语言规则。接受/拒绝必须与预期相同，进程超时、编译器未启动或清理失败都会使整入口失败，不能把工具缺失误当“编译器拒绝”。

| 片段 | 语言模式 | 预期与真实原因 |
| --- | --- | --- |
| method-go127 | go1.27 | 接受方法自己的 U |
| method-go126 | go1.26 | 拒绝：`generic method requires go1.27 or later` |
| function-go126 | go1.26 | 接受包级泛型函数修复 |
| generic-interface-method | go1.27 | 拒绝：`interface method must have no type parameters` |
| generic-method-implements-interface | go1.27 | 拒绝：泛型 Map(T)T 与普通接口 Map(int)int 签名不匹配 |
| type-set-as-value | go1.27 | 拒绝：包含类型约束的接口不能当值类型 |
| slice-not-comparable | go1.27 | 拒绝：[]int 不满足 comparable |
| exact-type-excludes-defined-type | go1.27 | 拒绝：Cents 不满足精确 int64，编译器提示可能缺少 ~ |
| tilde-allows-defined-type | go1.27 | 接受 ~int64 对 Cents 的修复 |
| return-only-inference | go1.27 | 拒绝：仅返回 T 的无实参调用不能推导 T |
| explicit-type-fixes-inference | go1.27 | 接受 Empty[int]() 的修复 |

运行打印真实诊断，不对整个诊断文案做版本不变的承诺。最后 “all 11 boundaries PASS” 意味着包含拒绝的矩阵符合预期，并非坏代码都能编译。

| 独立测试 | 实际证明 |
| --- | --- |
| TestResultBusinessFacts / TestUniqueConcreteKeys / TestCloneShallowBoundary | 4200 分、失败 mapper 调用为零、错误身份保留、实例化类型不同、具体 key 顺序、浅复制边界 |
| TestOptionsSnapshotWindows / TestOptionsOrderAndValidation | 创建/构造/getter 三窗口、Option 复用、覆盖顺序、默认值、无效配置和包装错误 |
| TestBreakClosesRealFile / TestPullOwnerMustStop / TestIteratorPanicStillCloses | break 后两行/已关闭、Pull 尚未结束与 stop 完成、错误协议和消费者 panic 的清理 |
| TestSequenceErrors / TestEmptySequenceAndOptionalTrace | 完整三行、空文件、可选观察者、open 失败、Scanner 超限及 reader 结局 |
| TestNilKinds / TestBindingAllOrNothing / TestBindingRejectsUnsupportedShapes | nil 类别、动态 int 拒绝、失败 DTO 不变、非法输入与受限转换 |
| TestMethodAndJSONBoundaries / TestJSONCandidatePublication | receiver 绑定、Call 参数失败、真实 JSON tag/错误类型/部分写入与候选发布修复 |
| TestRealLibraryContract / TestCompilerBoundaries | 真实版本 Retry 尝试次数/取消边界，真实编译器的语言规则矩阵 |

所有正常入口会结束。RunSequence 返回前关闭 reader 并删除合成文件；compilecheck 等待每个编译子进程结束后删除自己创建的临时目录；没有本课显式启动并遗留的 goroutine、端口、数据库、凭据或外部订单副作用。当前只验证本地真实文件、标准库、锁定第三方库和语言规则，生产网络超时、外部服务副作用、文件系统 Close 故障及任意并发配置修改都不在证据范围。最终命令与实际状态见 STATUS。
