# M01：从订单校验读懂 Go 语法、包与依赖

这课面对的是一页真实风格的订单业务代码：请求先变成订单值，校验通过才记录出库。你已经会写 Java 后端，本课把精力放在 Go 的声明、作用域、函数边界、包与模块。核心实验约 10～20 分钟：先运行 L00～L03，看到合法订单写入一条记录，再看到 `:=` 遮蔽导致零数量订单错误出库，最后对照两个修复。L04～L08 可按需要分别阅读，原理、反例和答案都已提供，没有待补代码或考核。

本课是独立 module：`go-demo/lessons/m01_syntax_packages`，`go 1.27.0`，没有 `toolchain` 指令。2026-10-12 实际执行使用 **go1.27.0 windows/amd64、PowerShell 7**。旧公司仓库应以其 `go.mod` 和实际工具链为准；本课不要求升级旧项目。

现在必须掌握：辨认 `var`/`:=`/`=`，沿每个 `err` 的声明位置找到它的块，读懂多个返回值，知道 `import` 定位哪个包、谁导出什么、何时出现内存副作用。指针只需看懂 `*Store` 是地址、`&store` 取地址，`error`/`io.Writer` 只需看懂接口签名；别名、typed nil、方法集合、并发内存规则分别交给 M02/M04 及后续课程。defer/取消/事务不在当前调用链中，不需要为了学语法先展开整套资源框架。

## 运行与阅读入口

默认工作目录：`D:/IdeaProject/go-demo`。从仓库根目录执行：

```powershell
go -C lessons/m01_syntax_packages run ./cmd/demo
go -C lessons/m01_syntax_packages test -count=1 -v ./...
go -C lessons/m01_syntax_packages vet ./...
& ./lessons/m01_syntax_packages/scripts/verify-labs.ps1
```

前三条针对本课 module；根目录的 `go test ./...` 不跨越嵌套 `go.mod`。最后的脚本自动定位本课，对每个子进程设置 30 秒上限、检查退出码与诊断文本、等待退出并释放进程句柄。它临时把自身进程的 `GOWORK/GOPROXY/GOSUMDB` 设为 `off`，只让本地模块故障立即出现，结束时恢复原值。初次运行核心入口需要按当前 GOPROXY 下载已锁定依赖；脚本的本地实验不需要网络。

| 文件/目录 | 先读的符号 | 在调用链中的责任 |
| --- | --- | --- |
| `cmd/demo/main.go` | `main → run → compareScope → numericAndAssignment` | caller、输入数据、正常与危险路径对照、行为断言 |
| `input/parse.go` | `Parse` | 真正调用 GJSON 和 strconv，拒绝非法 JSON/数值宽度 |
| `service/ship.go` | `New → Ship`、`ShipShadowed`、`ShipAssigned` | 显式依赖、校验错误传播、控制仓库写入边界 |
| `order/order.go` | `Order`、`Status`、`Policy`、`String` | 值字段、iota、跨包导出、函数类型/接收者签名 |
| `order/validate.go` | `Validate → check(o)`、`TotalCents` | if/switch/for/range、可变参数、总价边界 |
| `service/store.go` | `save`、`Snapshot` | 同包小写方法、真实内存记录和观察副本 |
| `cmd/demo/startup.go`、`startup/` | `describeStartup`、`registry.initialize/init`、`sideeffect.init` | import 副作用和 main 前的包初始化 |
| `labs/` | L04～L07 各自 `main` | 多文件、文件提交顺序、拆包、实际模块图 |
| `testdata/compilefail/` | L08 独立故障 `main` | 明确选择才编译，正常 `./...` 不扫描 testdata |
| `evidence/`、`STATUS.md` | 已保存输出、真实结果 | 2026-10-12 的运行证据及其边界 |

## L00：初始化发生在入口之前

`cmd/demo/startup.go` 空白导入 `_ ".../startup/sideeffect"`：只舍弃在该文件里使用包名的能力，仍执行导入包的初始化。`sideeffect` 依赖 `registry`；`registry` 的包变量 `names` 先调用 `initialize`，再执行自己的 `init`，然后才允许 `sideeffect.init` 调用 `Register`。所有依赖包完成后，才轮到 main 包变量与 main 包的 `init`，最后进入 `main`。

实际日志：

```text
[L00 init] registry 变量初始化
[L00 init] registry.init
[L00 init] sideeffect 注册 "demo-validator"
[L00 init] main 变量初始化：names=[demo-validator]
[L00 init] main.init
[L00 main] names=[demo-validator]
```

这条调用关系由导入依赖确定；不是因为文件名看起来“先后合适”。`Register` 只写本进程的教学名称 slice，没有创建服务器、连接或后台任务。业务 Service 在 `run` 中通过 `New(&store, os.Stdout)` 显式构造，你可以从 caller 看见依赖由谁提供。Go 的 `init` 没有 Spring 容器的依赖解析/生命周期协议，不能假定它自动完成业务依赖注入，也不能给 `init` 传参或自己调用它。

同一个包可以有多个 `init`，包括同一个文件里多个同名 init；它们是特殊初始化声明。一个包在该程序初始化中只初始化一次，多个 caller 重复导入同一个包不会重复初始化。本例 main 与 sideeffect 都依赖 registry，实际仍只看到一条 registry 变量初始化和一条 registry.init；入口也断言注册快照中恰好只有一个 demo-validator。

## L01：声明和业务控制流同时读

正常输入固定为 `ORD-100 / paid / quantity="2" / unit_cents="1250"`。金额以整数分表示；字符串边界让 `strconv` 负责明确判断数值宽度，不经过浮点金额转换。

完整调用链：`main → run → input.Parse → gjson.Valid/Get → strconv.ParseUint/ParseInt → 返回 Order,error → service.New → Service.Ship → order.Validate → allowSmallOrder → order.TotalCents → Store.save → 返回 Receipt,error → Store.Snapshot → run 断言 → main 返回`。每次调用都是同步执行，没有 goroutine、channel、网络等待或连接清理；`os.Stdout` 属于进程，课程函数借用它输出日志，不关闭它。

| 步骤 | 当前参与者/函数 | 值或状态变化 | 下一步与证明点 |
| --- | --- | --- | --- |
| 1 | `run` | `var count/text/enabled/initialErr` 已分别是 0/空串/false/nil | Go 的函数局部变量也有零值，不是 Java 的未确定赋值 |
| 2 | `Parse(raw)` | 检查完整 JSON，再检查四项都是字符串 | malformed/缺字段/JSON 数值会在解析层返回错误 |
| 3 | `Parse` | `qty, err :=` 声明；`cents, err :=` 同块新建 cents、复用 err | 只有全部解析成功才返回订单值；uint8 的转换已有 8 位检查 |
| 4 | `run` | `parsed` 是订单值；`store` 是入口持有的零值仓库 | `New(&store, os.Stdout)` 保存地址；New 失败立即返回 |
| 5 | `Ship` | `err = Validate(...)` 更新具名结果 err | 任意校验失败就返回零 Receipt；尚未调用 save |
| 6 | `Validate` | ID/数量/单价/状态，再逐项执行传入策略 | 状态 Paid 继续，其他状态返回；策略失败后停止 |
| 7 | `Ship → save` | 计算 1250×2=2500，追加一条 Receipt | Snapshot 中 ID/件数/金额是业务事实 |
| 8 | `run` | 三份非法输入分别被拒绝，记录数仍为 1 | 拒绝不能提前追加记录；打印 error 不是这一事实的替代 |

实际输出与解析：

```text
[L01 zero] count=0 text="" enabled=false err=<nil> status=unknown
[L01 accepted] id=ORD-100 quantity=2 total_cents=2500 records=1
[L01 rejected] id=ORD-100 err=出库拒绝：订单 ORD-100 的数量必须在 [1,255] records=1
[L01 rejected] id=ORD-101 err=出库拒绝：订单 ORD-101 尚未支付：status=created records=1
[L01 rejected] id=ORD-102 err=出库拒绝：订单 ORD-102 的状态码不受支持：status=Status(99) records=1
```

前三份非法输入复用已完成合法出库的仓库，所以这里 `records=1` 的意思是“拒绝后没有增加”。L02 使用三个全新仓库，那里修复结果才是 0。测试又从包外分别核对合法金额 2500、件数 2、非法订单无记录及观察结果不能改写内部记录。

`var` 可以在包级和函数内声明，省略初始值时有零值；`:=` 只能用于函数内部的短声明；`=` 只给已经可见的变量赋值。同一个块内 `shipper, err := ...` 合法：shipper 是新变量，err 早已声明且类型一致，因此只给原 err 赋新值。若两边名称都已有，如重复 `quantity := 3`，没有新非空白变量就编译失败。`_` 不是能读回的“只写变量”，只是丢弃位置；生产调用中丢弃 error 会抹掉你需要处理的业务失败。

`Order` 的字段当前全是值，`Validate(o Order)` 得到一份订单副本。本课不把这个规则扩张成“传参永远不能修改 caller 对象”：`New(store *Store, ...)` 复制的是地址，`save` 通过这个地址修改 caller 的同一个仓库。slice/map/接口的更深语义在后续模块讲解。

`Status uint8` 是命名类型，`Unknown Status = iota` 和后续省略表达式的声明项生成 0、1、2、3、4；注释与空行不计数，同一项中的两次 iota 也读同一个编号。它没有封闭 enum 集合，`Status(99)` 仍可构造，因此 `switch` 必须保留 default。外部 JSON 使用稳定字符串，避免把插入一项后会改变的 iota 编号直接当业务协议。

`if length := len(o.ID); length == 0` 中，length 只属于这个 if 的初始化作用域与分支链；作用域结束就不能使用。`len(string)` 数字节，不数 Java 字符或 Unicode 码点。`switch o.Status` 匹配后默认不贯穿，只有 Paid 分支继续函数后续语句；多值 case 表示多个可匹配状态。`if` 条件必须是 bool，`&&/||` 从左向右短路，Go 不把 0/空串/nil 当条件。

`for i := 0; i < len(invalid); i++` 控制 slice 的顺序访问，i 只在循环中可见，`i++` 是语句，不能写 `x := i++`。`for index, check := range checks` 得到索引和函数值副本。`checks ...Policy` 在函数内部是 slice；caller 可传零项/多项，`checks...` 表示把已有 slice 展开。`check(o)` 当场同步执行，nil 函数值先被拒绝。测试中三个策略记录 A/B/C，B 返回失败后观察到 **AB**，C 不执行，仓库仍为空，`errors.Is` 也能找回经 `%w` 包装的原因。

## L02：同名 err 不等于同一个变量

读 `service/ship.go` 时先给变量命名：`ShipShadowed` 中 `var err error` 是 **E_outer**；内层 `{ err := Validate(...) }` 是 **E_inner**。Go 允许重叠局部作用域同名声明；Java 对方法内局部名称的限制不能直接套用。E_inner 是一份独立的 error 接口变量，赋它不会同步更新 E_outer。

| 实际代码位置 | err 绑定 | 离开该作用域后的后果 |
| --- | --- | --- |
| `input.Parse` 的 `qty, err :=` | Parse 函数体新 err | 后续同块 cents 新建，err 复用 |
| `run` 的 `parsed, err :=` | run 函数体新 err | 后续 shipper/receipt 新建时复用 |
| `run` 循环中的 `if _, err := Ship(...); ...` | 本次 if 的新 err | 当场在 if/else 判断/输出，退出 if 即失效 |
| `Validate` 中 `if err := check(o); ...` | 本次策略 if 的新 err | 失败立即 return 交回 caller，没有跨块依赖 |
| `Ship` 的具名结果 `err error` | Ship 的结果变量 | `err = Validate` 更新它，返回失败给 caller |
| `ShipShadowed` 函数体的 `var err` | E_outer，初始 nil | 从未更新，被末尾检查和 return 读取 |
| `ShipShadowed` 内层 `err :=` | E_inner，接到数量错误 | 只被日志读取，退出内层后不能再读 |
| `ShipAssigned` 内层 `err =` | 更新函数体已有 err | 外层 if 能看到非 nil 并在 save 前返回 |
| `main` 的 `if err := run(); ...` | main 中这个 if 的新 err | 失败被输出并退出；不会泄漏到其他函数 |

危险版时间线是：`compareScope → bad.ShipShadowed → E_outer=nil → 新建 E_inner=数量错误 → 打印 E_inner → 离开内层 → 读取 E_outer=nil → 跳过错误分支 → save(Quantity=0) → caller 收到 nil`。错误已经在日志里出现，控制流却继续写了数据，这正是业务风险。

```text
[L02 bad] inner_err=订单 ORD-100 的数量必须在 [1,255]
[L02 bad] caller_err=<nil> quantity=0 records=1
[L02 fixed =] caller_err=出库拒绝：订单 ORD-100 的数量必须在 [1,255] records=0
[L02 fixed return] caller_err=出库拒绝：订单 ORD-100 的数量必须在 [1,255] records=0
```

最小修复是把内层 `err :=` 改成 `err =`，让外层检查读取真正接收失败的变量。更容易维护的修复是当前 `Ship`：在校验点立即判断和 return，把写入安排在成功分支后面。也可以写 `if err := Validate(...); err != nil { return Receipt{}, err }`，因为这个 err 的使用和错误传播都在同一个 if 中；“所有 `:=` 都危险”同样是错误结论。修复改变的是失败跨越块边界的传递与 save 的执行资格，不是打印更详细的文案。

具名结果签名 `Ship(...) (receipt Receipt, err error)` 表示进入函数就有 Receipt{} 和 nil 两个结果变量。它不是 Java 返回对象的字段。显式 `return Receipt{}, wrappedErr` 将按位置确定两个返回值，caller 可以用完全不同名称接收。代码不使用长函数裸 return；本课也没有 defer 改写具名结果，那个时机在后续 defer 课展开。

## L03：输入边界、多返回值与多重赋值

`strconv.IntSize` 实测 64；Go 的 int/uint 只能承诺 32 或 64 位，不能假定所有平台等于 Java int。固定协议字段应根据需要选择 int8/int16/int32/int64 或对应 unsigned 类型；Java long 最接近固定 int64。byte 是 uint8 别名，rune 是 int32 别名。float32/float64 是浮点类型，不能用“范围大”推断“金额精确”；本课用 int64 分。不同数值类型一般不能隐式混合：总价先写 `int64(o.Quantity)` 再乘单价。

```text
[L03 widths] int=64位 int64=固定64位 uint8=0..255
[L03 conversion] constant=255 runtime uint16(256)->uint8=0
[L03 checked] 256 被 8 位输入边界拒绝：strconv.ParseUint: parsing "256": value out of range
[L03 assignment] left=ORD-101 right=ORD-100
[L03 named] whole_yuan=25 remainder_cents=0
```

`const maximum = 255; var safe uint8 = maximum` 在编译期检查常量可表示。`uint8(incoming)` 中 incoming 已是运行时 uint16 值，转换不会返回 error，256 转换后是 0。若直接转换外部输入，256 件可能被当成 0 件；本课先 `ParseUint(..., 10, 8)`，再 `uint8(qty)`，255 成功而 256/负数/非数字失败。单价先用 ParseInt 限定 int64 可表示范围，再由 Validate 限定 0～1,000,000,000 分；两层都成立后，255 件总价最多 255,000,000,000 分，不发生 int64 乘法溢出。常量越界和运行时窄化是两个不同检查点。

`left, right = right, left` 先求右侧两个旧值，再赋给左侧，因此顺利交换。规则是每个右值可赋给对应左值，不是简单要求整组变量类型都相同。`splitCents(2500) (wholeYuan, remainderCents int64)` 有两个具名结果，内部多重赋值 `cents/100, cents%100` 得到 25/0；caller `net, remainder := ...` 按位置接收。多个返回值不是一份自动包装的 tuple 对象，也没有默认抛异常，错误值要由 caller 明确处理。

## L04：同包多文件、导出与 import 名称

工作目录仍为仓库根。分别执行：

```powershell
go -C lessons/m01_syntax_packages/labs/multifile run main.go
go -C lessons/m01_syntax_packages/labs/multifile run .
```

第一条实测退出 1，诊断 `undefined: formatReceipt`。第二条退出 0，输出 `id=ORD-100 total_cents=2500 files=main.go+format.go`。`main.go` 与 `format.go` 同目录、同为 package main，整包编译时共享包级声明，所以 main 可以调用小写函数。指定文件列表时 Go 只构建给定文件形成的临时包，不能凭 main.go 的名字自动补齐目录。`go run main.go format.go` 同样能选齐；日常优先 `go run .` 或完整包路径。

普通目录同一组参与构建的文件组成一个包，不是每文件一个 Java 类；构建约束和 `_GOOS/_GOARCH` 文件会影响哪些文件参与构建，外部测试包 `_test` 是专用例外。`import` 声明的名称在当前文件作用域，因此 format.go 导入 fmt 不会让 main.go 获得 fmt 名称。一个 module 可含多个包，包的 import 字符串一般是 module 路径加相对目录，不是文件路径。

大小写看标识符的首个 Unicode 大写字母；本课用 ASCII。`service.Ship` 是导出方法，`service.save` 小写只在 service 内可见。字段也有同样边界：Receipt 的 ID 可读取，Store.records 不能包外直接读。Go 没有 Java 的 public/private/protected 关键字与逐类私有边界。

## L05：不能按文件名猜 init 的语言保证

```powershell
go -C lessons/m01_syntax_packages/labs/initorder run main.go a.go z.go
go -C lessons/m01_syntax_packages/labs/initorder run main.go z.go a.go
```

两次都先输出 `base=10` 再输出 `derived=11`：a.go 的 derived 明确依赖 z.go 的 base，包变量初始化会先满足这个依赖。第一次 init 输出 a→z，第二次 z→a，main 最后读到 10/11。这是当前实际工具链按显式文件提交顺序运行的观测。

规范确定包变量依赖和初始化阶段，并按文件提交给编译器的顺序确定跨文件声明/`init` 的展示顺序；规范鼓励构建系统按文件名字典序提供文件以便复现，但不能把“文件名较小”当成独立于构建方式的业务保证。`go run .` 常见的稳定名字顺序不等于显式换文件顺序仍保持原顺序。业务构造用明确参数和调用链；不要通过改 a/z 文件名修理初始化依赖。[Go 规范：包初始化](https://go.dev/ref/spec#Package_initialization)是语义查证入口，实际顺序保存在 `evidence/labs.txt`。

## L06：单文件拆成两个正确的包

```powershell
go -C lessons/m01_syntax_packages run ./labs/singlefile
go -C lessons/m01_syntax_packages run ./labs/twopackages
```

两者都输出同一份 ORD-100 的 2500 分。拆前 `package main` 中 `main → validate`；拆后两个目录分别是 `package main` 与 `package validation`，调用是 `main → policies.Validate`。旧小写 validate 改成导出的 Validate；main 的 import 字符串定位 `module/labs/twopackages/validation`，`policies` 是本文件的显式别名。省略别名时默认叫 validation，来自 package 声明，不来自文件名 validate.go；目录名与包名可以不同，但一致命名通常减少阅读成本。

两包仍属于同一个 module，所以没有新 require/replace。这两个最小实验只展示组织变化和基本签名，完整订单状态、数量/金额边界与真实记录使用前面的 `input/order/service`。不能把两个 package 声明塞进同一普通目录，也不能把可执行 main 包当库包导入；本课依赖方向从 caller 指向规则，不允许 order 再导入 service 构成循环。

## L07：读一次真实依赖图，再观察 MVS/replace

首先查看课程正在用的真实第三方依赖：

```powershell
go -C lessons/m01_syntax_packages list -m all
go -C lessons/m01_syntax_packages mod graph
go -C lessons/m01_syntax_packages list -m -json github.com/tidwall/gjson github.com/tidwall/match github.com/tidwall/pretty
go -C lessons/m01_syntax_packages mod verify
```

GJSON 固定 **v1.18.0**，直接被 input 包 import；其真实 go.mod 要求 match v1.1.1、pretty v1.2.0。`go mod tidy` 后本课记录二者为 indirect；它们不被本课源码直接 import，但沿 GJSON 的包依赖参与构建。本次 list 的 Indirect 为 true，graph 同时显示主模块和 GJSON 指向二者，verify 输出 `all modules verified`。标准库 fmt/strconv 不作为第三方 require 出现，模块路径和包路径也不是普遍相同概念。

`go.sum` 记录模块内容/模块 go.mod 的校验值，版本选择依据 go.mod 的 requirement 图；不能把 go.sum 看成会独立选择依赖版本的 Maven 锁文件。本课保留 go.mod/go.sum，不运行无界 `go get -u`。当前 go 指令既决定本模块采用的语言语义，也设置最低 Go 版本要求；Go 1.21 后不只是说明文字。`toolchain` 若存在是工具链选择建议，本课没有此指令，GOTOOLCHAIN 实测 auto。详见[模块参考](https://go.dev/ref/mod#go-mod-file-go)。

再读全在本课内的合成模块实验：

```powershell
go -C lessons/m01_syntax_packages/labs/deps/app list -m all
go -C lessons/m01_syntax_packages/labs/deps/app mod graph
go -C lessons/m01_syntax_packages/labs/deps/app list -m -json example.com/m01/rules
go -C lessons/m01_syntax_packages/labs/deps/app run .
```

app 直接 import quote/receipt；quote 要求 rules 至少 v1.1.0，receipt 要求至少 v1.2.0，实际选中 **同路径的 v1.2.0**。MVS 是选择能满足图中最低要求的一组版本，当前选中版本是该路径图中要求的最高版本；不是冲突时随便选较低版，也不是每次联网找全网最新。`app/go.mod` 保留 rules v1.2.0 indirect，实际 graph 仍可看见 quote→v1.1.0 与 receipt→v1.2.0 两条不同要求。

```text
example.com/m01/quote@v1.0.0 example.com/m01/rules@v1.1.0
example.com/m01/receipt@v1.0.0 example.com/m01/rules@v1.2.0
list: rules Version=v1.2.0, Replace.Path=../rules, Indirect=true
[L07 quote] ORD-100 quote via local-rules-source
[L07 receipt] ORD-100 receipt via local-rules-source
```

example.com 路径和版本是合成教学图，全部源码由本地 replace 提供；字符串 `local-rules-source` 证明真实运行了本地代码，不能证明某个公网发布标签的内容。本地 replace 改源码位置并可能改变其依赖图，不更改 import 的逻辑模块路径；replace 本身不把未 require 的模块自动加入图。这些全部本地替换的实验没有下载内容，因此不必创建 go.sum。

第三方消费者不继承 quote 自己的 `replace rules => ../rules`。consumer 的坏版 go.mod 只替换 quote；以 consumer 为 main module 构建时，rules 仍会按模块路径寻找。脚本在进程环境 GOPROXY=off 下实测退出 1：`module lookup disabled by GOPROXY=off`；日志中的 downloading 是尝试解析模块，不表示下载成功。修复文件 `fixed.mod` 在 consumer 自己补齐 rules replace，随后实际运行成功：

```powershell
# 先运行脚本可同时观察有界故障与修复；手动复跑修复无需修改环境。
go -C lessons/m01_syntax_packages/labs/deps/consumer run '-modfile=fixed.mod' .
go -C lessons/m01_syntax_packages/labs/deps/consumer test '-modfile=fixed.mod' ./...
go -C lessons/m01_syntax_packages/labs/deps/consumer vet '-modfile=fixed.mod' ./...
```

PowerShell 中含等号与点的 `-modfile=fixed.mod` 使用引号，避免 shell 参数解析影响文件名。`-modfile` 让修复可独立复跑，不覆盖故障版 go.mod；这里没有修改共享 go.work。[模块参考：replace](https://go.dev/ref/mod#go-mod-file-replace)和[MVS](https://go.dev/ref/mod#minimal-version-selection)支持这两个边界。

## L08：明确隔离的编译故障与完整修复

以下命令在仓库根分别退出 1，直接解析见表。正常 `run ./cmd/demo`、`test ./...`、`vet ./...` 都不选它们；验证脚本检查这些失败是预计诊断而不是任意失败。

```powershell
go -C lessons/m01_syntax_packages run ./testdata/compilefail/no-new/main.go
go -C lessons/m01_syntax_packages run ./testdata/compilefail/if-scope/main.go
go -C lessons/m01_syntax_packages run ./testdata/compilefail/export/main.go
go -C lessons/m01_syntax_packages run ./testdata/compilefail/overflow/main.go
```

| 故障 | 本次真实诊断 | 为什么编译失败 | 完整修复方向 |
| --- | --- | --- | --- |
| `quantity := 2; quantity := 3` | `no new variables on left side of :=` | 同块没有新变量，不是赋值语句 | 第二条写 `quantity = 3`；若 `quantity, status := 3, "paid"`，status 新建且 quantity 复用 |
| if 结束后 `fmt.Println(err)` | `undefined: err` | err 只属于该 if 初始化作用域 | 在 if 内立即处理/返回；确需后续读取才在外层 var 声明并在 if 中用 `=` |
| 包外 `store.save(...)` | `cannot refer to unexported method save` | 方法小写，caller 所在包不同 | 业务由导出的 Service.Ship 调用包内 save；不要为绕过边界随意导出底层写入 |
| `const input=256; var quantity uint8=input` | `... as uint8 value in variable declaration (overflows)` | 常量在编译期不能由 uint8 表示 | 固定合法数量 255，或按协议合理选更宽类型；真实字符串输入用 ParseUint 检查，不窄化截断 |

这些故障都在编译阶段结束，main/仓库写入未发生；L02 则完全能编译却产生非法副作用，两类证据不能互相替代。`go vet` 实测通过，但它没有自动阻止本课的合法语法遮蔽 bug，所以仍需独立的业务状态断言。

## 材料纠正与验证边界

本次只读原仓库的指定 basic-syntax 八组示例及 `files/基础语法` 的变量、常量、运算符、条件、循环、函数、依赖管理和 `files/原理剖析/程序初始化`。已吸收能帮助阅读业务代码的部分，纠正以下结论，没有修改原材料。

| 材料说法 | 本课采用的准确边界 |
| --- | --- |
| `:=` 左侧不能有已声明变量 | 同块允许旧变量与至少一个新非空白变量混合；内块同名会新建并遮蔽 |
| `if` 与其他语言没区别 | 初始化语句有自己的作用域、条件必须 bool，err 的绑定要跟声明走 |
| 所有 Go 代码至少有 main | 可执行程序要 main 包/main 函数；库包不需要 main |
| `init` 总按文件名顺序 | 文件提交顺序/变量依赖是语义边界；字典序是构建系统的推荐行为 |
| `go` 指令只是标识 | 当前语义版本与最低工具链要求，不能随意忽略或升级 |
| indirect 仅源于缺失 go.mod 等旧场景 | 当前 go 1.17+ 图裁剪也需显式传递 requirements，观察本课 tidy/list/graph |
| `exclude` 与 `retract` 混为一谈、写 `restract` | exclude 是 main module 排除选择；retract 是作者声明撤回版本，正确拼写 retract；本课无需修改它们 |
| string 的 Sizeof 总是 16 | 平台/实现相关；内容字节不计入 string 值大小，本课不依赖这个数字 |
| range 闭包总输出最后一个值 | Go 1.22+ 且模块语言版本支持时，`:=` 声明的循环变量逐轮新建；外部已有变量用 `=` 则仍复用 |
| map 遍历“随机”或同 goroutine 增删是未定义行为 | 规范是顺序未指定；未访问前删除不再产生，新加项可能产生也可能跳过；不依赖随机分布 |
| 值传递绝不影响实参对象 | 参数值被复制；地址/slice 等值副本仍可能找到同一个对象，M02 展开 |

教学业务副作用仅是本进程 Store 的内存记录。它验证“失败不写记录”的控制流边界，**不宣称数据库持久化、消息发布、生产幂等或多 goroutine 安全**。Store 没有并发锁，也没有重复合法 ID 防重投逻辑；不要把这个语法实验直接当生产仓库。本课没有取消和长等待，故障子进程 30 秒有界；没有文件句柄、Rows、Tx、连接、端口或后台任务需要清理，退出后合成记录消失。未运行 race 是因为本课没有并发行为，而不是隐藏竞态。

第三方源码不批量加注释：版本锁定 GJSON v1.18.0，实际查看的入口为 `gjson.go` 的 `Type`、`Result`、`Result.String`、`Get`、`Valid`，分别负责结果类别、值转换、路径查找和 JSON 合法性。源码随固定版本可查：[v1.18.0 go.mod](https://raw.githubusercontent.com/tidwall/gjson/v1.18.0/go.mod)、[v1.18.0 gjson.go](https://github.com/tidwall/gjson/blob/v1.18.0/gjson.go)。语言查证优先[Go 规范](https://go.dev/ref/spec)，命令选文件/选包查证[go 命令文档](https://pkg.go.dev/cmd/go)，数值解析查证[strconv](https://pkg.go.dev/strconv)。运行结果、准确版本和曾修复的验证问题见 [STATUS.md](STATUS.md)，保存的输出见 [evidence/demo.txt](evidence/demo.txt)、[tests.txt](evidence/tests.txt)、[modules.txt](evidence/modules.txt)、[labs.txt](evidence/labs.txt)。
