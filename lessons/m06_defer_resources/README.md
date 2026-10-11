# M06：defer、return 与资源释放的精确时间线

这课围绕一个订单批量导出场景：处理合成订单 `O-100`，把 `order=O-100 amount=99\n` 写进临时文件，给 caller 返回结果，并确保资源在恰当时机释放。内容是 22 个 ASCII 字节，所有文件均由课程临时创建。你将先预测语言行为，再运行危险版和修复版，最后对照真实文件内容、错误原因、资源计数和子进程退出码。

本模块独立可运行，没有硬前置，不导入其他课程。推荐先读 M01/M02/M05，但没有读过也可以沿本课注释理解 `:=`、指针、具名结果和闭包。本次实际使用 `go1.27.0 windows/amd64`，独立 `go.mod` 的 module 为 `go-demo/lessons/m06_defer_resources`、`go 1.27`，没有第三方依赖或显式 `toolchain` 指令。环境和完成范围见 [STATUS.md](STATUS.md)。

## 先跑核心实验，控制在 10～20 分钟

当前已验证工作目录是 `C:\Users\Lenovo\.codex\worktrees\go-course-m06\go-demo`。在这个目录执行：

```powershell
go -C lessons/m06_defer_resources run ./cmd/demo -only core
go -C lessons/m06_defer_resources test -count=1 -v ./...
go -C lessons/m06_defer_resources vet ./...
```

从任意目录也可以用绝对模块路径：

```powershell
go -C C:/Users/Lenovo/.codex/worktrees/go-course-m06/go-demo/lessons/m06_defer_resources run ./cmd/demo
```

主 agent 汇总到 `D:\IdeaProject\go-demo` 后，在该根目录使用同样的相对命令。根模块的 `go test ./...` 不会替你测试这个嵌套 module。

核心路线：E01 用 3 分钟预测注册与 LIFO；E02 用 5 分钟画返回槽与 error；E03 用 5 分钟观察循环积压和每轮关闭；E04 用 5 分钟理解为什么 `Write` 返回成功还不能记最终成功。阅读扩展时再分别运行 E05、E06。默认没有参数会运行全部实验。

| 入口参数 | 实验及观察点 | 主要源码符号 |
| --- | --- | --- |
| `-only semantics` | E01：求值发生在何时、闭包读哪个变量、旧接收者与 LIFO | `RunSemantics`、`registerValues`、`receiverValues` |
| `-only returns` | E02：return 表达式、结果槽、具名 err、过早捕获与遮蔽 | `namedReturn`、`captureTooEarly`、`shadowResult`、`explicitReturn` |
| `-only files` | E03：真实文件最多同时打开多少个、哪些文件被遗漏 | `fileBudget`、`batchBad`、`processOne`、`receiverFileTrap` |
| `-only buffers` | E04：Flush/Close 顺序、真实内容、完整 error 原因 | `writeWrongOrder`、`writeOrdered`、`RunBuffers` |
| `-only ownership` | E05：请求与应用的关闭所有权 | `auditClient`、`borrowedRequestBad`、`borrowedRequestFixed` |
| `-only process` | E06：普通 panic 清理、Exit/Fatal 跳过 defer、先返回再退出 | `panicBoundary`、`processOwnedFile`、`RunChild` |

例如逐步运行：

```powershell
go -C lessons/m06_defer_resources run ./cmd/demo -only returns
go -C lessons/m06_defer_resources run ./cmd/demo -only files
go -C lessons/m06_defer_resources run ./cmd/demo -only buffers
go -C lessons/m06_defer_resources run ./cmd/demo -only ownership
go -C lessons/m06_defer_resources run ./cmd/demo -only process
```

本课现在必须理解：defer 的作用域是**包围它的函数调用**；函数值、参数与接收者的求值时机；闭包读变量的时机；LIFO；return 先确定结果、清理后才交给 caller；资源拥有权；不能丢掉写出/关闭错误。编译器如何优化 defer、runtime 的栈展开细节、数据库提交失败分类可以遇到相关源码时再查。

## 阅读顺序与完整调用关系

先读 [cmd/demo/main.go](cmd/demo/main.go)。`main → run → RunSemantics/RunReturns/RunFiles/RunBuffers/RunOwnership/RunProcess` 是入口链。每次调用同步完成，没有业务 goroutine；每个实验返回时自己的 Go defer 已经执行。`run` 返回后 `main` 才根据课程是否失败决定进程退出码。

再按下面顺序读 [internal/lesson](internal/lesson)。所有 `.go` 文件属于同一个 `lesson` 包，拆文件只是让责任容易定位；`internal` 限制该包由所属 module 范围内导入。

| 文件 | 责任与去向 |
| --- | --- |
| [trace.go](internal/lesson/trace.go) | `Trace` 同步输出日志；writer 由入口提供，课程不关闭 stdout；第一次输出失败会交回入口 |
| [semantics.go](internal/lesson/semantics.go) | 纯语言实验，将独立观察值交给 caller 与测试 |
| [returns.go](internal/lesson/returns.go) | 返回槽、过早 error 捕获、内层 `:=` 遮蔽及修复 |
| [files.go](internal/lesson/files.go) | 真实文件获取与关闭；计数包装、循环错误/两种修复、第二次获取失败、资源变量重赋值 |
| [buffers.go](internal/lesson/buffers.go) | `bufio.Writer → *os.File → 文件系统`；退出时写出并合并 error |
| [ownership.go](internal/lesson/ownership.go) | 真实文件客户端在应用与两个请求之间借用，应用最后关闭 |
| [process.go](internal/lesson/process.go) | 当前 goroutine 普通 panic 展开；独立 child 真实 Exit/Fatal；父进程等待退出并删除目录 |
| [lesson_test.go](internal/lesson/lesson_test.go) | 检查结果、错误链、真实文件内容、最终资源状态和临时目录删除；helper test 仅供子进程入口 |

完整真实日志保存在 [evidence/demo.txt](evidence/demo.txt)，独立断言日志在 [evidence/test.txt](evidence/test.txt)。下面的摘录来自这次运行，不是预先编写的假输出。

## E01：执行到 defer 时，究竟保存了什么

在 `registerValues` 中，`value := 10` 声明本次调用的局部 int，`fn` 是保存函数值的变量，`r` 指向 caller 的观察对象。执行 `defer fn(evaluateArgument(t, value))` 时，先求函数值和实参；`evaluateArgument` 当场运行并返回 10，旧 `fn` 与该 int 副本被登记。后续才执行 `value = 20` 与 `fn = 新函数`。

这里的 `:=` 用于声明变量，`=` 给已有变量赋值。函数值也是值，变量 `fn` 改存别的函数不会回头改已经登记的旧调用。[语言规范的 defer 规则](https://go.dev/ref/spec#Defer_statements)规定先保存调用目标与实参，退出时逆序调用；这个规则不要求某一种 runtime 存储结构。

| 时间 | 当前参与者/语句 | 状态变化 | 下一步 |
| --- | --- | --- | --- |
| 1 | `RunSemantics → registerValues` | `value=10`，`fn=旧函数` | 执行 defer1 |
| 2 | `evaluateArgument(t, value)` | 真正打印求值日志，返回 int 副本 10 | 保存旧函数值与参数 10 |
| 3 | 登记 defer2 闭包 | 没传入 value 参数；闭包保留访问变量的能力 | 稍后执行体内再读 value |
| 4 | 登记 defer3，实参 `&value` | 保存指向该局部变量的指针副本 | 稍后通过指针读取对象 |
| 5 | 普通函数体 | `value=20`，`fn` 改为新函数 | 到函数末尾，开始退出 |
| 6 | defer3 | 从指针指向的位置读到 20 | 接着执行 defer2 |
| 7 | defer2 | 闭包读到同一变量的当前值 20 | 接着执行 defer1 |
| 8 | defer1 | 旧函数收到当初保存的 10 | `registerValues` 返回 caller |

真实日志的第一段：

```text
[E01.eval] 实参表达式现在执行 value=10
[E01.body] defer1 已注册
[E01.body] defer2 已注册
[E01.body] defer3 已注册
[E01.body] value=20，fn 已替换；函数体结束
[E01.defer3] 通过已保存指针读取 *p=20
[E01.defer2] 闭包执行时读取 value=20
[E01.defer1] 旧函数收到注册时保存的实参=10
[E01.caller] arg=10 closure=20 pointer=20 replaced_called=false order=[3 2 1]
```

第一行在“defer1 已注册”之前，证明实参表达式没有一起延迟。最后一行 `replaced_called=false` 证明已注册的 defer 没有重新查 `fn`。`order=[3 2 1]` 是本函数登记顺序的反向；不同函数各自处理自己的 defer，不能把整个程序的 defer 当成一个无作用域的全局栈。没有走到的分支不会登记 defer，进入 `{}` 或 for 下一轮也不会触发包围函数的退出。

指针实参“现在确定”与“内容稍后可能变化”并不矛盾。复制 `&value` 只复制地址，不是深拷贝所有可达对象。slice、map 或其他含引用的参数也必须分清保存的值和该值所引用的对象。

接着读 `receiverValues`。`first` 指 A，`second` 指 B，`current` 起初存 first 指针。三个 defer 依次登记：指针方法调用、值方法调用、闭包晚读 current；然后 A 的字段改成 `A*`，current 改指 B。实际日志：

```text
[E01.receiver.body] 旧对象 name=A*，current 改指 B
[E01.receiver.closure] 闭包晚读 current=B
[E01.receiver.value] 接收者副本的字段=A
[E01.receiver.pointer] 旧对象的当前字段=A*
```

闭包最后登记所以最先执行，看到 current 当前是 B；值接收者保存的是当初 A 的结构体副本，所以仍为 A；指针接收者保存旧对象指针，所以读取旧对象的**当前字段** A*。方法接收者同样参与注册时的求值，不能认为 `defer current.Close()` 在退出时再去选择 current 指向谁。E03 用真实文件验证这个区别带来的遗漏。

对 Java 经验的连接：你可以把闭包访问局部状态的能力当成一个提示，但 Java lambda 的 effectively final 限制不能直接搬到本课。Go 闭包可以读取这里后来被重新赋值的局部变量。`finally` 也不能解释实参快照和 LIFO；必须按上述八步预测 Go 行为。

## E02：return 先做什么，defer 到底能改谁

三个函数均把 `local` 设为 10，return 中调用 `returnExpression`。先读当前函数的返回声明：`(result int)` 声明一个可在函数内读写、初值为 0 的具名结果变量；单独 `int` 没给结果一个可由闭包命名访问的名字。[return 规范](https://go.dev/ref/spec#Return_statements)规定显式表达式先确定并赋给结果，再执行 defer。

| 函数 | return 表达式求值 | 结果赋值后 | defer 实际修改对象 | caller 收到 |
| --- | --- | --- | --- | --- |
| `namedReturn` | 求得 local 的 int 副本 10 | `result=10` | `result += 5` | 15 |
| `anonymousReturn` | 求得 local 的 int 副本 10 | 匿名结果已确定为 10 | `local += 5` | 10 |
| `namedButLocal` | 求得 local 的 int 副本 10 | `result=10` | `local += 5`，未改 result | 10 |

“匿名结果槽”是理解值传递的辅助说法，不承诺编译器真的分配某个特定栈槽。关键是 return 里的 int 值已确定；修改另一个 int 变量不会回写它。若返回的是指针、slice、map，defer 仍可能修改共同引用的对象；本表只论证独立 int 值，不能扩展成“匿名返回永远不受 defer 影响”。

实际日志逐行核对：

```text
[E02.named.return_expr] 表达式求得 10
[E02.named.defer] 退出时 result=10 local=10，result+=5
[E02.anonymous.return_expr] 表达式求得 10
[E02.anonymous.defer] local 改为 15；已确定的 int 结果没有改变
[E02.named_local.return_expr] 表达式求得 10
[E02.named_local.defer] local=15 result=10；只改 local
[E02.caller] named=15 anonymous=10 named_local=10
```

第一对日志显示 result 已经由 0 变成 10，defer 才读它；不是 defer 先改成 5 再被 return 覆盖。第二对和第三对显示 local 变成 15 并不能推导 caller 收到 15。成功/失败状态如果放在具名结果里，退出清理必须读写那个结果变量，而不是一个看起来同名或同义的别的变量。

### 错误一：`defer cleanup(err)` 保存了过早的 nil

`captureTooEarly` 的具名 `err` 初值是 nil，`defer observeCleanup(..., err, ...)` 在注册时保存一个 nil error 接口副本。之后 `err = ErrBusiness`，return 将业务失败交给 caller；清理收到的参数仍是 nil。修复 `captureAtExit` 登记一个无 err 实参的闭包，到退出时执行函数体读取最终具名 err。

| 步骤 | 危险版 `captureTooEarly` | 修复版 `captureAtExit` |
| --- | --- | --- |
| 注册 | 延迟调用参数保存 nil | 闭包保留对变量 err 的读取 |
| 业务 | 具名 err 改为 ErrBusiness | 同样改为 ErrBusiness |
| return 结果赋值 | 最终结果为 ErrBusiness | 最终结果为 ErrBusiness |
| 清理 | 函数参数仍是 nil | 此时读取具名结果 ErrBusiness |
| caller | 收到失败，但退出观察是成功 | caller 与退出观察都收到失败 |

对应真实日志：

```text
[E02.early.body] 注册时 err=nil，现在 err=业务错误
[E02.early.cleanup] 观察到 err_non_nil=false
[E02.late.body] 闭包已注册，现在 err=业务错误
[E02.late.cleanup] 观察到 err_non_nil=true
[E02.caller] early(return_err=true cleanup_err=false) late(return_err=true cleanup_err=true)
```

`observeCleanup` 只观察 error，没有事务能力。放到真实业务里，同样的捕获错误可能让失败请求打印成功或错误判断是否需要清理；不能因为 caller error 是对的就忽略退出行为。若多个 defer 都能改 err，观察闭包还必须排在这些修改之后执行：想让观察最后执行，就要**较早登记**它。

### 错误二：具名 err 被内层 `:=` 遮蔽

读 `shadowResult` 的两个作用域。函数结果变量 err 在函数体范围内；`if _, err := validateOrder(); err != nil` 的初始化部分为 if 创建内层 err。这个 err 是新变量，函数结果 err 仍为 nil。危险版只在内层打印错误，离开 if 后裸 `return` 返回外层 nil。修复 `explicitReturn` 在内层执行 `return err`：先求内层 error 的值，然后赋给函数的具名结果，退出闭包读到失败。

```text
[E02.shadow.body] 内层 err 是业务错误，只打印后离开 if
[E02.shadow.cleanup] 观察到 err_non_nil=false
[E02.fixed.body] 内层失败通过 return err 赋给具名结果
[E02.fixed.cleanup] 观察到 err_non_nil=true
[E02.caller] shadow(return_err=false cleanup_err=false) fixed(return_err=true cleanup_err=true)
```

不要把 `:=` 记成“总是新变量”或“总会复用外层”。在同一个函数体块中，`f, err := os.Create(path)` 可以因为 f 是新变量而复用已声明的具名 err；在新的 if/for 内层块中，那个 err 会是内层新声明。作用域判断比相同拼写更重要。

还有一个编译边界：在 shadowResult 的内层 if 中写裸 `return` 会报 `result parameter err not in scope at return`。危险版离开 if 后的裸 return 可以编译，所以编译器并没有替你拒绝这次吞错。需要的是正确传播错误，而不只是“换一种能编译的 return”。

## E03：立即登记释放还不够，函数范围也要正确

`RunFiles` 创建唯一临时目录，`fileBudget.acquire → os.Create` 获得真实文件；`ownedFile.Close → File.Close` 真正关闭句柄，然后才更新计数。包装器只统计当前课程资源，不模拟数据库资源。它给每次批处理设上限 2；我们观察资源积压，不故意触发操作系统的描述符耗尽。

危险 `batchBad` 处理 4 条同样的数据，循环中每次获取成功后登记 defer。函数直到第三轮预算拒绝才返回；for 一轮结束没有退出 batchBad。先在运行前预测下表：

| 时间 | 当前调用/状态 | defer 登记状态 | 真实资源 |
| --- | --- | --- | --- |
| 1 | 第 1 轮 acquire 成功，写入 22 字节 | 登记文件 1 的 Close | live=1 |
| 2 | 第 1 轮结束、继续同一函数 | Close 1 尚未执行 | live=1 |
| 3 | 第 2 轮成功并结束 | 又登记 Close 2，仍未执行 | live=2 |
| 4 | 第 3 轮检查预算失败 | 没有新文件，不登记它的 Close | live=2 |
| 5 | `return openErr`，先设置最终 error | 准备运行既有 defer | live=2 |
| 6 | 后登记的 Close 2 | 真正调用 File.Close 成功 | live=1 |
| 7 | 先登记的 Close 1 | 真正调用 File.Close 成功 | live=0 |
| 8 | caller 获得 error，读取真实文件 | ErrBudget 保留，两个内容正确 | peak=2、opened=closed=2 |

对应日志：

```text
[E03.bad] 获取 1 live=1
[E03.bad] 第 1 轮结束 live=1；尚未退出 batchBad
[E03.bad] 获取 2 live=2
[E03.bad] 第 2 轮结束 live=2；尚未退出 batchBad
[E03.bad] 获取 3 被预算拒绝 live=2
[E03.bad] 准备错误返回，返回前 live=2
[E03.bad] 关闭 2 live=1
[E03.bad] 关闭 1 live=0
[E03.bad.caller] opened=2 closed=2 peak=2 live=0 budget_err=true
[E03.bad.content] 真实文件记录核验=2
```

危险版最终 live=0，说明不是永久泄漏，但它在批处理中把释放拖得太久，已经无法完成这批任务。只检查“函数最终关闭过全部句柄”会漏掉这个容量错误。

修复 `batchScoped → processOne → acquire → WriteString → return → deferred Close → batchScoped 下一轮`。`processOne` 是每轮资源 owner；它使用具名 err，把 Write 与 Close 的错误通过 `errors.Join` 一起返回。记录第一轮的实际日志：

```text
[E03.scoped] 获取 1 live=1
[E03.scoped] 关闭 1 live=0
[E03.scoped] 第 1 轮完成，小函数已返回 live=0
[E03.scoped.caller] opened=4 closed=4 peak=1 live=0 budget_err=false
[E03.scoped.content] 真实文件记录核验=4
```

最后两行是在第四轮之后；中间同样的第二～第四轮完整日志见证据文件。Close 在“第 1 轮完成”之前，证明小函数返回等待自己的 defer 全部执行。对比修复 `batchExplicit`：每轮 Write 后直接调用 Close，再 Join 两者错误；本次同样 `opened=4 closed=4 peak=1 live=0`，实际四条内容均正确。它也有效，但新增多个 return 分支时更容易漏关闭。每轮小函数通常更容易审查所有路径。

“循环里不能写 defer”是过度结论。循环确实可以使用 defer；正确问题是资源是否可以活到包围函数返回。本课 for 内调用 processOne 没问题，小函数里的 defer 也没问题。现代 Go 的循环变量规则也与老资料不同：Go 1.22 起，for/range 中由循环声明的迭代变量具有每轮语义；不能把“所有循环闭包都访问同一个 i”当现行规则。本课的危险例子使用**明确重赋值的 current 变量**，不依赖旧循环语义。

### 第二次获取失败：已经成功取得的源文件仍需清理

`secondAcquireFailure` 先写 source.txt，再 `os.Open`，成功后立即登记源文件 Close。目标位于不存在的父目录，`os.Create` 返回真实 `PathError`。这条失败路径没有取得 destination，所以没有 destination Close，但不能漏掉 source。

```text
[E03.acquire_failure] 源文件获取成功并注册 Close
[E03.acquire_failure] 目标获取失败，源文件清理由已注册 defer 负责
[E03.acquire_failure] 源文件退出关闭 close_ok=true
[E03.acquire_failure.caller] target_failed=true source_closed=true
```

先保留原始获取错误，source Close 的错误再通过 Join 保留；示例外层确认 `errors.Is(err, os.ErrNotExist)` 与原 source 的 Read 得到 `os.ErrClosed`。这两个事实成立后，场景把预期故障作为完成，不把它伪装成“目标文件已创建”。

本次首轮验证曾误用 `File.Stat` 检查关闭后错误，得到 `source_closed=false`。核对本机 `D:\Go\src\os\stat_windows.go` 的 `File.Stat` 发现它直接查询底层句柄，该路径的错误不保证是 `os.ErrClosed`。现改用对**同一原文件对象**的 Read 检查 I/O 关闭状态。路径 `os.Stat` 用于确认临时目录不存在仍然合适；不要混淆“句柄关闭”和“路径是否存在”。

### 旧接收者与晚读闭包各会漏掉谁

`receiverFileTrap` 外层创建真实 A、B 文件并登记兜底；内层执行危险版，返回后先检查原 A/B 对象，观察到遗漏后外层立即收回残留资源。这个观察点位于兜底**之前**，所以不能被“课程最后没有残留”掩盖。

| 版本 | 初始 current | defer 注册 | 后续赋值 | 内层返回时的关闭对象 | 遗漏 |
| --- | --- | --- | --- | --- | --- |
| 方法接收者 | A 指针 | `defer current.Close()` 保存 A | `current = B` | A | 新取得的 B 没有自己的清理 |
| 晚读闭包 | A 指针 | `defer func(){ current.Close() }()` | `current = B` | B | 原先 A 丢失了匹配的关闭 |
| 稳定实参修复 | A 指针 | 给 A 单独登记带 owned 实参的清理 | `current = B` 后也给 B 单独登记 | B 然后 A | 两个对象都被关闭 |

```text
[E03.receiver.body] 登记后 current 从 A 改指 B
[E03.receiver.caller] old_closed=true new_closed=false；外层随后兜底关闭残留
[E03.closure.body] 登记后 current 从 A 改指 B
[E03.closure.caller] old_closed=false new_closed=true；外层随后兜底关闭残留
[E03.receiver-fixed.body] 登记后 current 从 A 改指 B
[E03.receiver-fixed.caller] old_closed=true new_closed=true；外层随后兜底关闭残留
[E03.owner] 临时目录删除=true
```

修复没有通用的“都换成闭包”口诀。每个成功获取的资源必须有自己的稳定身份与清理范围：同一函数确实需要同时使用 A/B 时，receiver-fixed 给每个对象分别登记带 owned 实参的清理，重赋值 current 不会改已经保存的 owned；闭包只晚读具名结果 err，用 Join 保留关闭失败。批处理则可采用 E03 的 processOne，不改绑 f，每轮退出只关闭自己那一个，并缩短资源生命期。危险版也故意忽略了 Close error；修复中的稳定实参与 processOne/E04 均处理它。外层 `closeUnlessAlreadyClosed` 仅是故障实验兜底，用于避免测试二次关闭已经被危险版关闭的文件；普通业务不以它替代错误处理。

## E04：Write 返回成功、Flush 成功、业务成功是不同事实

`RunBuffers → writeWrongOrder/writeOrdered → bufio.Writer → *os.File`。writer 的 4096 字节缓冲足以容纳 22 字节记录，`WriteString` 成功后数据留在用户态缓冲，`Buffered()=22`。直到 `Flush` 才把残留数据交给下层 writer；[bufio.Writer.Flush 契约](https://pkg.go.dev/bufio#Writer.Flush)明确这一层的职责。它不等于 fsync，不验证磁盘断电耐久性。

危险 `writeWrongOrder` 先注册 Flush，后注册 Close：return 后 LIFO 先 Close，随后 Flush 向已关闭文件写，得到真实 `os.ErrClosed`。它只把错误存到观察字段，没有赋回 err，caller 仍收到 nil；文件实际为空。

```text
[E04.bad.body] 错误地提前记成功 buffered=22；尚未 Flush
[E04.bad.defer] Close 先执行 ok=true
[E04.bad.defer] Flush 后执行 failed=true closed_cause=true（故意未传播）
[E04.bad.caller] err_nil=true bytes=0 success=false business_cause=false closed_cause=false flush_failed=true close_failed=false
```

最后一行 `closed_cause=false` 是检查**caller 的 error 链**，不是否认 Flush 失败：它的 err 是 nil，所以 errors.Is 无法找到被丢掉的原因；`flush_failed=true` 才是独立观察到的清理失败。这个反例同时暴露“过早记成功”和“丢清理错误”两个问题。

修复 `writeOrdered` 在 os.Create 成功后立即安排一个闭包，顺序明确是 `Flush → Close → errors.Join`，而不是依赖两个 defer 的源码排布。w 开始为 nil，后续初始化；退出时闭包读取这个 writer。如果还未有 writer，仍要 Close 已有文件。

| 步骤 | 正常修复路径 | 错误/清理路径 |
| --- | --- | --- |
| 1 | os.Create 成功，取得 f | 获取失败没有新资源，直接返回 |
| 2 | 立刻登记清理，再建立 w | 后续提前 return 仍走已登记清理 |
| 3 | WriteString 返回，buffered=22 | 主 Write/业务错误先成为结果 err |
| 4 | return out,nil/业务错误 | 显式返回先赋具名 out 和 err |
| 5 | defer Flush | 保留 Flush error，继续尝试 Close |
| 6 | defer Close | 保留 Close error，不覆盖主 error |
| 7 | Join 原 err、Flush、Close | `errors.Is` 能分别找到各原因 |
| 8 | 回到 RunBuffers | 读真实文件内容，再决定 success |

四种路径的本次实际结果：

| 路径 | caller err 为 nil | 文件字节 | Flush error | Close error | 最终 success |
| --- | --- | --- | --- | --- | --- |
| bad：Close 先于 Flush且丢错误 | true | 0 | true | false | false |
| fixed：Flush 再 Close | true | 22 | false | false | true |
| close-failure：提前 Flush/Close，制造第二次 Close | false | 22 | false | true | false |
| business-and-cleanup：业务失败且提前关闭缓冲下层 | false | 0 | true | true | false |

正常修复日志：

```text
[E04.fixed.body] Write 只进入缓冲 buffered=22；此时不记最终成功
[E04.fixed.defer] 先 Flush failed=false
[E04.fixed.defer] 后 Close failed=false
[E04.fixed.caller] err_nil=true bytes=22 success=true business_cause=false closed_cause=false flush_failed=false close_failed=false
```

Close 故障日志中，先提前写出并关闭真实文件，退出时的 Flush 无残留所以成功，**第二次 Close 返回错误**：

```text
[E04.close-failure.fault] 故意提前 Flush/Close，退出 Close 将是重复关闭
[E04.close-failure.defer] 先 Flush failed=false
[E04.close-failure.defer] 后 Close failed=true
[E04.close-failure.caller] err_nil=false bytes=22 success=false business_cause=false closed_cause=true flush_failed=false close_failed=true
```

这证明 error 不为 nil 不必意味着“完全没有副作用”：22 字节已写出并可读取。这里重复 Close 是有意制造的明确 API 错误，不能把它称为真实磁盘故障、网络故障或“第一次 Close 内部失败”。[os.File.Close 契约](https://pkg.go.dev/os#File.Close)说明已关闭对象再次 Close 返回错误。

最后一条故障路径把主 error 设成 ErrBusiness，defer 再同时产生 Flush 和 Close 错误：

```text
[E04.business-and-cleanup.fault] 故意提前 Close 并返回业务错误
[E04.business-and-cleanup.defer] 先 Flush failed=true
[E04.business-and-cleanup.defer] 后 Close failed=true
[E04.business-and-cleanup.caller] err_nil=false bytes=0 success=false business_cause=true closed_cause=true flush_failed=true close_failed=true
[E04.owner] 临时目录删除=true
```

`labeledError` 用 `%w` 标注操作名并保留原因，`errors.Join` 忽略 nil、保留多个非 nil 原因，`errors.Is` 能找到 ErrBusiness 与 os.ErrClosed；测试还检查 Flush 和 Close 各自的标签，避免一个 closed error 掩盖另一个。若业务错误是核心问题，不应用一次 `err = closeErr` 把它覆盖。[errors.Join 文档](https://pkg.go.dev/errors#Join)描述的是多原因错误契约。

这里清理仍选择尝试 Flush；真实项目必须按业务设计决定失败时是否应继续写出、删除临时文件、写到新文件再原子替换。defer 只控制调用时机，没有事务回滚能力；返回 error 不会撤销已发生的文件写入。日志中的 success 在写函数全部 defer 执行之后才决定，只表示这次内容、error 与清理事实正确。

## E05：谁创建客户端，谁决定它的最后关闭

`RunOwnership` 充当应用 owner，建立一个长寿命 `auditClient`，里面持有真实 `*os.File`。两个顺序请求调用 Record，只借用它。这个简单客户端是实际文件客户端，未声称复现 HTTP keep-alive 或数据库连接池。

危险链：`应用 → borrowedRequestBad → client.Record → 请求 return → defer client.Close → 应用发第二个请求 → Record 返回 os.ErrClosed`。第一请求写入成功不等于共享客户端可以被请求关闭。

修复链：`应用 → borrowedRequestFixed → Record → 请求返回 → 第二次请求 Record → 两个请求均完成 → 应用 Close → caller 读取两条实际记录`。

```text
[E05.bad.request] 请求返回前错误地关闭借来的客户端
[E05.bad.owner] 第二个请求 closed_cause=true
[E05.fixed.request] 本请求写入完成，不取得应用 Close 所有权
[E05.fixed.request] 本请求写入完成，不取得应用 Close 所有权
[E05.fixed.owner] 全部请求后应用关闭 close_ok=true
[E05.fixed.caller] 第二请求成功=true records=2
[E05.owner] 临时目录删除=true
```

第二条的真实 error 原因和修复版文件内两条完整订单记录是测试事实。把这个所有权思路用于别的库时必须再查具体 API：`sql.DB.Close` 是应用级生命周期操作；`http.Client` 没有通用的 `Close`，不要虚构一个；请求取得的 `Response.Body` 则需要按响应体契约关闭。M06 没有执行 HTTP body 或 SQL 连接实验，课程的文件证据不能冒充这些协议验收。

## E06：普通 panic 展开，与立即结束进程

普通 panic 的调用链是 `RunProcess → panicBoundary → childOwnedPanic → triggerPanic`，均在同一个 goroutine。文件在 childOwnedPanic 里成功创建、立即登记关闭、写入 `before-panic\n`，然后触发普通 panic。栈展开先执行本层 Close，再到 panicBoundary 的延迟函数直接 recover。recover 后 boundary 返回 caller；不会回到 triggerPanic 调用的下一句。

```text
[E06.panic.body] 文件已写入，触发普通 panic
[E06.panic.resource] 栈展开时 Close close_ok=true
[E06.panic.boundary] 直接 recover 得到 "订单处理 panic"
[E06.panic.caller] recovered=true file_closed=true after_panic_line=false content="before-panic\n"
```

`after_panic_line=false` 是一个独立观察变量。源码在 triggerPanic 之后有实际的设置与 `after-panic` 写入，但未执行；文件内容也没有后半段。Read 检查同一文件对象已经关闭。恢复必须在同一 goroutine 的延迟函数中直接调用 recover；[官方 defer/panic/recover 说明](https://go.dev/blog/defer-panic-and-recover)给出这个边界。这里的恢复只为教学隔离故障，不是给任意业务 panic 自动返回成功的建议。

还有一个 error 路径差异：panic 中断了 `err = childOwnedPanic(...)` 的普通返回赋值，下层即使改它自己的具名 err，也不会像正常 return 那样交给这一层。因此资源函数把 Close error 放进恢复边界提供的 `cleanupErr` 槽，边界的 defer 再 Join 到自己的结果 err。本次真实 Close 成功，槽是 nil；结构仍保留清理失败的传播路径。不要把“普通 return 能改具名结果”泛化成“任何栈展开都自动返回了那个 error”。

进程实验由父进程 `RunProcess → exec.CommandContext → 同一 demo 可执行文件` 启动四个 child。每个 child 用私有环境变量选择 RunChild，创建自己的 payload 文件并立即登记 Close 和 cleanup 标记。父进程每次同步等待 child 退出，检查**真实进程退出码**、原 payload 内容与清理标记；每个等待最多 5 秒，结束后 cancel 释放 deadline timer，最后删除整个临时目录。

| child 模式 | 当前资源 owner 中发生什么 | 退出前 Go defer | 真实退出码 |
| --- | --- | --- | --- |
| normal | 资源函数正常 return | Close 后写 cleanup 标记 | 0 |
| exit | owner 内部 `os.Exit(23)` | 跳过 | 23 |
| fatal | owner 内部 `log.Fatal` | 跳过 | 1 |
| repaired | owner 先 return ErrBusiness，外层随后 Exit | owner 已 Close 并写标记 | 24 |

实际 demo 子进程日志：

```text
[E06.normal] exit_code=0 cleanup_ran=true output="normal-body"
[E06.exit] exit_code=23 cleanup_ran=false output="exit-body"
[E06.fatal] exit_code=1 cleanup_ran=false output="fatal-body"
[E06.repaired] exit_code=24 cleanup_ran=true output="owner-returned-before-exit"
[E06.owner] 子进程均已被等待；临时目录删除=true
```

`cleanup_ran` 要求 marker 文本为 `close_ok=true`，由 child 的 defer 真实 Close 成功后写入；它不是父进程猜测。两个危险 child 结束后，OS 会回收进程持有的句柄，但这不能代替 Go 清理中的 Flush、锁文件删除、业务收尾或日志写出。[os.Exit 契约](https://pkg.go.dev/os#Exit)明确不会执行延迟函数；[log.Fatal 契约](https://pkg.go.dev/log#Fatal)是打印后调用 os.Exit(1)。

修复版仍以非零码退出，故障没有被吞掉；只是先让资源拥有者返回完成清理，再由最外层决定进程状态。普通 recoverable panic 与 runtime 的致命 throw/fatal 问题、OOM、系统终止不是一类边界，不能承诺后者会执行普通 defer。本课没有制造 runtime 内部致命故障，也不把普通 panic 的证据泛化到它们。未处理的 panic 只展开当前 goroutine 的有关调用栈，也不能推导进程中其他 goroutine 都已清理。

## 事务只看风险顺序，实库证明留在 M21

本课没有数据库、Begin/Commit/Rollback 调用或虚构事务对象。下面是**阅读风险用的伪代码**，不可直接运行，不是完整事务方案：

```text
1. tx = Begin(ctx)                         // 获取成功后才有 tx 的清理责任。
2. 安排该 tx 的退出处理                    // 必须保存这个 tx，不能被后续变量重绑替换。
3. 在 tx 内写业务                          // 若误用外层 db，就不在同一事务里。
4. 遇到 error/panic，走既定失败路径          // defer 时机不等于已经证明数据库回滚。
5. 调用 Commit，并按驱动/协议分类返回结果    // 报错可能涉及未知结果，不能推断一定未提交。
6. 根据已确认的业务事实报告完成              // 不能在 Commit 之前把业务日志记成成功。
7. 退出清理并处理 Rollback/Close 的实际契约  // 已提交后 Rollback 不会撤销已提交的事实。
```

E02 的 `defer cleanup(err)` 过早保存 nil、内层 err 遮蔽、观察闭包过早执行会让上述失败判断错位；E03 的资源重赋值与循环范围会让 Tx/Rows/连接持有时间错位。但文件失败没有证明数据库回滚，Close 成功也没有证明 Commit。M21 将用真实 MySQL 证明实际 Begin/Commit/Rollback 与 panic；M06 只给语言与文件清理依据。

## 原材料的修正与版本边界

本课只读定位了原 worktree 的 `examples/basic-syntax/defer/main.go`，以及主目录新材料 `files/基础语法/go语言defer`、`files/原理剖析/数据结构/defer面试与分析`、`files/原理剖析/数据结构/defer`，没有改动或复制整批原材料。

| 原材料中的说法或遗漏 | 本课按当前证据修正 |
| --- | --- |
| “每个 defer 语句都创建 _defer 并进入链表” | 不是语言保证；Go 1.27 runtime 同时说明链式记录与 open-coded defer，不能套用旧结构字段 |
| “循环体不要使用 defer” | 判断资源是否允许活到包围函数退出；E03 每轮小函数也使用 defer，完整处理四条 |
| 只写 `defer file.Close()` | 这是常见简写，但会忽略返回的 Close error；E04 展示真实错误与完整保留方式 |
| 零值 `var num int` 的例子说第一步结果设置为 1 | num 的零值是 0，结果应先为 0，defer 改的是局部 num；原解释有笔误 |
| “return 不是原子操作” | 这里是顺序语义说明，与并发原子性/JMM 无关；应按表达式求值、结果赋值、defer、caller 四阶段读 |
| 把 panic/recover 当所有异常均能捕获 | 必须查当前 goroutine、recover 调用位置与故障类别；Exit/Fatal/致命 runtime 问题有不同边界 |

本次核对 `D:\Go\src\runtime\panic.go` 顶部说明、`runtime2.go` 的 `_defer`、`cmd\compile\internal\ssagen\ssa.go` 中 open-coded/deferprocStack 分支，确认旧数据结构解释不能当当前完整实现。它们是 Go 1.27.0 的**实现证据**，不承诺所有函数都采用某一路径。本课未做性能基准或反汇编，不用“某条 defer 必在堆/栈”作教学结论。阅读旧公司工程应先看该工程实际 Go 版本与模块指令，再判断循环变量和库契约；不要为了跑本课擅自升级公司项目。

## 独立断言与证明范围

`go test -count=1 -v ./...` 有六组实质验收测试，另有一项仅供独立 child 的 `TestHelperProcess`。测试直接检查观察字段、文件系统与错误原因，而不是只匹配整段日志：

| 测试 | 它要守住的事实 |
| --- | --- |
| `TestRegistrationAndReceivers` | 实参 10、闭包/指针 20；旧函数仍被调用；LIFO；值副本 A、旧指针字段 A*、闭包晚读 B |
| `TestReturnSlotsAndErrorVisibility` | 返回 15/10/10；过早捕获的 nil 与最终 error 不同；遮蔽吞错；显式 return 传播 |
| `TestRealFileLifetime` | 危险版 peak=2、预算失败、最终全关；两种修复 peak=1、四条真实内容；获取失败清理；旧接收者和晚闭包的遗漏 |
| `TestFlushCloseAndJoinedCauses` | 虚假 nil 下的空文件；修复完整 22 字节；Close 错误保留；主 error 和两项清理 error 均保留 |
| `TestBorrowedClientOwnership` | 错误关闭使下一请求得到 ErrClosed；修复后两条实际记录 |
| `TestProcessBoundary` | panic 关闭、恢复位置、实际内容；四个真实子进程退出码和清理标记 |

每个真实资源场景还先看自身 `Removed`，再独立 `os.Stat(TempDir)` 确认路径确实不存在。全部实验都是串行路径，日志先后有调用链保证，不依赖调度巧合、sleep 或精确耗时；子进程确有 5 秒上限。课程没有模拟实库验收，没有 HTTP/MQ/数据库服务，没有泄露凭据，也没有遗留业务进程。实际运行结果、首轮发现的问题和未验证范围均如实记录在 STATUS。
