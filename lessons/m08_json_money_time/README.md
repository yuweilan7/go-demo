# M08：让订单 JSON、金额、时间拥有明确含义

本课解决一个后端常见问题：同一个请求，网关、Go 服务、数据库与前端是否理解成同一个订单。能解 JSON 只说明某些字节能转换为 Go 值；它不能证明数量有效、金额精确，也不能证明 `10:00` 表达了正确的截止时刻。

本课是独立 module，module 路径为 `go-demo/lessons/m08_json_money_time`，`go.mod` 使用 `go 1.27`，没有第三方依赖、没有共享 go.work、没有数据库和外部汇率调用。实验副作用只在本进程的订单值中。跨时区使用真实 `time` 库与内嵌 IANA 时区库；金额使用标准库 `math/big` 做中间计算，最终存储值仍是 `int64`。

学习对象是熟悉 Java 后端的 Go 初学者。核心实验用同一份合成订单，花 10～20 分钟按 E01→E02→E03 阅读并运行；E04 是正式 JSON v2 迁移对照。无需完成额外练习，错误原因与修复已经直接给出。

## 先运行，再顺着符号读

在仓库根目录运行（当前实施 worktree 为 `C:/Users/Lenovo/.codex/worktrees/go-course-m08/go-demo`）：

```powershell
go -C lessons/m08_json_money_time run ./cmd/demo
go -C lessons/m08_json_money_time run ./cmd/jsonv2
go -C lessons/m08_json_money_time test -count=1 ./...
go -C lessons/m08_json_money_time vet ./...
```

也可以在 `lessons/m08_json_money_time` 目录内用 `go run ./cmd/demo`。所有程序同步、有限次执行，无端口、后台 goroutine、timer 或外部资源；不需要先开数据库。`go test` 默认运行下面的独立断言，`-count=1` 避免把缓存成功当成本次执行。

阅读顺序与职责：

| 文件/符号 | 负责什么 | 接着去哪里 |
| --- | --- | --- |
| `cmd/demo/main.go`：`main → run` | 核心实验 owner，顺序运行三段，意外失败退出码 1 | `jsonDemo` / `moneyDemo` / `timeDemo` |
| `cmd/demo/json_demo.go`：`jsonDemo` | 原始 v1 反例、创建订单、三种 PATCH、内存安装边界 | `DecodeCreate/BuildOrder/DecodePatch/ApplyPatch` |
| `internal/contract/json.go`：`Field[T]`、`DecodeStrict` | 字段三态、16 KiB/16 层边界、精确名字、重复名字、单文档 | v1 `Decoder` 与字段 `UnmarshalJSON` |
| `internal/contract/order.go`：DTO、`Order` | 创建与更新的业务约束，候选值不改旧值 | `Money` 与 `UTCInstant` |
| `internal/contract/money.go` | 币种、整数、输入解析、溢出、舍入、报价契约 | `roundRatio` 的商/余数 |
| `internal/contract/time.go` | RFC3339/UTC、毫秒范围、到期边界 | 标准库 `time` |
| `cmd/jsonv2/main.go`：`run` | 同一个 DTO 对照 v1/v2/兼容选项 | 本入口不进入订单核心流程 |
| `internal/contract/contract_test.go` | 独立预期金额、UTC 文本、失败不修改原订单、跨时区到期 | `go test` |
| `cmd/jsonv2/main_test.go` | 无序 JSON 对象字段形状、v2 输入边界 | `go test` |

现在必须理解：导出字段与 tag；JSON 解码、业务校验与安装状态是三个阶段；缺失/null/零值的区别；整数金额仍要检查币种、溢出和舍入；时间点与本地时间显示不同；`Duration` 是纳秒数；v2 的默认协议不同。遇到相关源码时再查：反射故障类型构造、完整 v2 自定义流式接口、所有 tag/选项、时区夏令时规则。这里不为这些延后内容再建一条复杂路径。

## E01：创建订单与 PATCH 为什么不能复用一个普通 DTO

合成订单是 `SKU-M08`，数量 1，CNY 总额 299 分，订单 ID 为 `9007199254740993`，上海截止时间为 2026-10-12 10:00，已有备注 `gift` 和标签 `new`。实验注入 `now=2026-10-12T01:59:00Z`，而不是依赖今天几点，保证重跑仍然有相同业务判断。

### 最小 Go 规则

`CreateDTO.Quantity` 的 Go 字段名大写所以可导出；`json:"quantity"` 指定 JSON 名字。tag 是附在字段上的元数据，不会自动校验业务。小写 `internal` 不导出，即使存在值也不会被 JSON 编码；`json:"-"` 明确排除导出的 `ServerSecret`。这与 Java bean getter/注解扫描模型不同，不能凭“类里有字段”推断 JSON 能看到它。

`var dto CreateDTO` 创建的是值，各字段取得自己的零值。`&dto` 是这个值的指针，解码器通过它修改原 DTO；普通 struct 赋值会复制字段值。复制指针只复制地址，复制 slice 只复制包含底层数组引用的描述符，因此 `ApplyPatch` 会复制备注值和标签元素，避免候选订单与旧订单共享可写存储。

`any` 是 `interface{}` 的别名，表示可持有不同动态类型。E02 的 `map[string]any` 里，JSON number 的动态类型默认是 `float64`；E01 扫描器的 `token.(string)` 则是把已经读到的对象名字从接口中取出，并用 `ok` 判断是否真是字符串。

### E01-A 实际输出：信息是在哪一步丢失的

| 输入（每次使用全新普通 DTO） | quantity | note | tags | 意义 |
| --- | ---: | --- | --- | --- |
| `{}` | 0 | nil | nil | 都没有提供，解码器没有写这些字段 |
| `{"quantity":null,"tags":null}` | 0 | nil | nil | v1 的标量 null 不修改整数；slice null 设为 nil |
| `{"quantity":0,"tags":[]}` | 0 | nil | 非 nil、长度 0 | 明确数量 0，明确已知空集合 |

前三行的 `quantity=0` 相同，不表示输入含义相同。额外运行的“先有 quantity=7 再解 null”得到 7：v1 对整数的 null 是不修改，不是统一赋 0；新值里的 0 原本就是零值。缺失字段同样不会重置已经存在的字段，所以不可跨请求复用 DTO。

改成 `*int64` 后，`{}` 和 `{"quantity":null}` 都得到 nil，`{"quantity":0}` 得到非 nil 且 `*quantity=0`。指针可区分“有没有整数值”，但在全新 DTO 上仍不能区分缺失与显式 null。若接口把 null 解释成删除，指针不足以表达完整 PATCH。

修复是 `Field[T]`：`T` 是本字段的值类型，`Field[int64]` 用于数量，`Field[string]` 用于备注。`UnmarshalJSON` 在字段出现时才被调用，包括 null；没出现时整个 Field 保持零值。`Present` 回答“客户端提没提这个字段”，`Null` 回答“提的是不是 null”，`Value` 回答“非 null 的实际值是什么”。业务必须先看前两个标记，再读取 Value。此类型只用于输入 DTO，不能直接 Marshal 成响应；输出使用专门的 `Order`，否则暴露的是 Field 的内部标记对象。

### E01-B 实际输出：omitempty 不是请求校验

v1 编码结果为 `{"note":""}`：数量 0 被 `omitempty` 省略，非 nil 但长度 0 的 tags 被省略，指向空字符串的 note 仍保留。去掉 `omitempty` 后，nil tags 输出 `{"tags":null}`，非 nil 空 tags 输出 `{"tags":[]}`。

`omitempty` 只影响编码输出，对解码没有效果，不能把缺失输入变成业务默认值，也不能拒绝 null。本课输出订单的 note/tags 不使用 `omitempty`：`note:null` 表示已知没有备注，`note:""` 表示明确空字符串，`tags:[]` 表示已知空集合。消费者若在返回对象中看不到一个字段，其含义必须另订，而不是靠“看起来空”猜测。

`omitzero` 与 `omitempty` 也不同：前者看 Go 零值或类型的 `IsZero()`。Go 1.27 两个 JSON 包都支持它；E04 的 `time.Time` 零值用 `omitzero` 明确省略。v1 的 struct/time.Time `omitempty` 不等同于调用 `IsZero()`。

### E01-C：语法成功、协议明确、业务有效分别证明什么

调用链是 `jsonDemo → DecodeCreate → DecodeStrict → checkObject/walkObject/walkValue → v1 Decoder.Decode → Field/ID/Minor/UTCInstant.UnmarshalJSON → BuildOrder → Order`。

| 顺序 | 当前函数与状态 | 下一步/失败影响 |
| --- | --- | --- |
| 1 | `DecodeCreate` 创建本次全新 DTO | 不携带上一请求的 Present/旧数值 |
| 2 | `DecodeStrict` 检查 16 KiB、UTF-8 字节有效性 | 超限/非法字节直接返回 error |
| 3 | token 扫描原始对象；`seen` 属于当前对象 | 在覆盖字段前拒绝重复；解转义后重复也拒绝；顶层名字精确匹配 |
| 4 | v1 解码与 `DisallowUnknownFields` | 把值转换为明确的 Go 类型；类型/范围错误保留 error 原因 |
| 5 | 再次 Decode 要求 `io.EOF` | 流式 Decode 一次成功不足以证明文档已结束；只允许空白尾随 |
| 6 | `BuildOrder(dto, now)` 检查业务 | 版本、ID、SKU、数量、币种、金额、未来截止时间、非 null 标签数组 |
| 7 | 成功返回 Order | 到这里才有可安装的订单值；本课没有 SQL 持久化 |

扫描器也检查嵌套对象的重复名字，不先解到 map 再检查，因为 map 已经丢掉第一次出现的名字。`UseNumber()` 在这里避免只做结构扫描时提前把数字转换成 float64。深度 16 和字节数 16 KiB 是本课有界输入政策，不是 Go 库本身的通用上限。原始字节 UTF-8 校验不代表已经实现所有 Unicode 安全规则：v1 对 JSON 转义中的孤立 UTF-16 代理项仍可能替换成 U+FFFD；生产文本协议如需拒绝它应再订规则并测试。

实际危险结果与修复如下：

| 原文 | 仅调用一次 v1 Decode | `DecodePatch` 的结果与原因 |
| --- | --- | --- |
| `{"quantity":1,"quantity":9}` | quantity=9，error=nil | 拒绝重复名字；一般 v1 是后值替换或按目标类型合并，不能一概称最后一个对象覆盖整对象 |
| `{"quantity":1,"quanTity":9}` | quantity=9，error=nil | 拒绝非精确名字；v1 字段匹配默认忽略大小写 |
| `{"quantitty":3}` | quantity=0，error=nil | 拒绝拼错的未知字段；默认 v1 忽略它 |
| `{"quantity":1} {"quantity":9}` | quantity=1，error=nil | 拒绝第二个 JSON 值；Decode 面向流，仅第一次读取成功 |
| `{"quantity":1} trailing` | quantity=1，error=nil | 拒绝尾随垃圾；第一次 Decode 仍没读取到垃圾 |

`DisallowUnknownFields()` 本身不拒绝重复名字、不强制名字大小写，也不替你读第二份文档，所以这里有完整边界检查。错误返回时 DTO 可能有部分字段被写过：不要忽略 error，也不要使用半解码 DTO。`DecodeCreate/DecodePatch` 将错误结果清成零值并返回；输入校验、业务校验均成功才安装状态。

成功创建的输出为：

```json
{"contract_version":"order.v1","request_id":"req-m08-001","order_id":"9007199254740993","user_id":"42","sku":"SKU-M08","quantity":1,"currency":"CNY","amount_minor":"299","expires_at":"2026-10-12T02:00:00Z","note":"gift","tags":["new"]}
```

将同一输入的 quantity 改为 0 后，`DecodeCreate` error=nil，`BuildOrder` 返回“quantity 必须显式为 1..1000 的整数，不能缺失/null/0”。它能证明反序列化成功与业务有效是两个判断；相同的分离适用于字段缺失、币种错误、截止时间已过期。

### E01-D：直接给出三种 PATCH 的结果

调用链是 `DecodePatch → Field[T].UnmarshalJSON → ApplyPatch(existing, patch, trustedUnitPrice) → 候选 Order/error → owner 成功时才安装`。`existing` 是 struct 值副本；它内部原有的 slice/指针由 ApplyPatch 显式复制。本课以服务端单价 299 CNY 分重算数量后的总额，客户端不能通过 PATCH 修改金额。

| PATCH 输入 | Present / Null / Value | 业务结果 | 原订单 |
| --- | --- | --- | --- |
| `{}` | false / false / 0 | 不改变数量，候选仍 1 件/299 分 | 1 件/299 分 |
| `{"quantity":null}` | true / true / 0 | 拒绝，因为数量不可清除 | 1 件/299 分 |
| `{"quantity":0}` | true / false / 0 | 拒绝，因为订单数量不允许 0 | 1 件/299 分 |
| `{"quantity":2}` | true / false / 2 | 候选 2 件/598 分 | 仍 1 件/299 分 |

备注的三种输入也直接展开：`{}` 保留 `gift`；`{"note":null}` 清除，输出 note:null；`{"note":""}` 设置空字符串，输出 note:""。空字符串不是“没传”。标签的 `{"tags":null}` 被拒绝，`{"tags":[]}` 返回明确空数组；这个约定不会自动因为字段是 slice 就成立，是我们的业务契约。

最后两条日志是真实内存副作用：owner 先接受 `{"quantity":2}` 并执行 `state=candidate`，最终 2 件/598 分；再收到 `{"quantity":0}`，ApplyPatch 返回错误，owner 没有赋值，仍是 2 件/598 分。独立测试另外把返回订单的备注/标签改掉，断言原订单保持 gift/new；也用“先设置 note 再遇到非法 tags”的组合，证明失败没有改写旧订单。它们证明本地候选值隔离，不能证明数据库事务或并发更新正确性。

本课 PATCH 是明确字段策略，不宣称完整实现 JSON Merge Patch 标准。请求的版本由 `/v1/orders/{order_id}` 路由/契约选择提供；body 只开放 quantity/note/tags，传 contract_version 或任意其他名字会被拒绝。

## E02：金额精确不是“把 float64 换成 int64”这一句话

### E02-A 大 ID 的实际损失与修复

`json.Unmarshal → map[string]any → value.(float64)` 实际打印：

```text
原文=9007199254740993 map动态类型=float64 存储=9007199254740992 int64=9007199254740992
UseNumber=9007199254740993；具体int64=9007199254740993；order.v1线上ID用字符串
```

IEEE 754 float64 不能连续精确表达超过 2^53 的所有整数；将已舍入的 float64 再转 int64 不会还原丢掉的一位。`Decoder.UseNumber()` 必须在解码前设置，接口值才能保存 `json.Number` 原文；之后 `Int64()` 会检查整数格式/范围。它不允许超出 int64 的 ID，超过范围要用合适整数类型或检查后的字符串。

直接解到具体 `int64` 同样精确，本课不是说所有 encoding/json 数字都一定损失。但是响应若把大整数作为 JSON number 发给使用双精度数字的消费者，服务端自身精确也救不了消费者，因此 `order_id/user_id` 冻结为字符串。本课自定义 `ID` 还拒绝 0、前导零、符号和 uint64 溢出，避免“同一个 ID 的多种拼法”。

### E02-B 浮点误差、截断、输入精度与溢出

a、b、major 是运行时 float64 变量。不能只打印 Go 的 `0.1+0.2` 常量表达式：Go 常量计算的精度规则会隐藏我们要看的运行时风险。实际结果是：

```text
运行时0.1+0.2=0.30000000000000004 ==0.3=false
运行时0.29*100=28.99999999999999645 转int64=28（29分丢成28分）
整数修复：10+20=30 最小单位；币种=CNY
```

修复链是 `ParseMajor(currency, decimalText, policy) → 普通十进制输入校验 → big.Int 精确分子/分母 → roundRatio → int64 Money → Add/Multiply`。没有经过 float64，也没有先截断再补一个 epsilon。

| 输入 | RejectFraction | HalfUp | 直接原因 |
| --- | --- | --- | --- |
| CNY `0.29` | 29 | 29 | 两位精度刚好表达 29 分 |
| CNY `1.005` | 拒绝额外小数位 | 101 | 百分位之后恰好半分，非负金额向上舍入 |
| JPY `1.50` | 拒绝小数位 | 2 | JPY 本课精度 0；不能默认乘 100 |
| KWD `1.234` | 1234 | 1234 | 本课精度 3，最小单位数为 1234 |
| CNY `92233720368547758.08` | 溢出错误 | 溢出错误 | 对应最小单位超过 MaxInt64 |

`RejectFraction` 对人工主单位输入也拒绝多余尾随零精度，例如 CNY 1.230；这是明确的输入格式政策。`HalfUp` 对非负金额的 1.005 得到 1.01，不是银行家舍入。以后涉及退款负数、均摊、多次折扣要另外约定方向、舍入阶段、尾差归属；本课不允许负订单金额，不能把这些策略当作任意金融业务的通用政策。

`Money{Currency, AmountMinor}` 同时保存币种与 `int64` 值。`Add` 拒绝 CNY/USD 直接相加，先用 `MaxInt64-b` 检查加法；`Multiply` 先用 `MaxInt64/quantity` 检查乘法。实际运行 MaxInt64+1 和 MaxInt64*2 都返回明确溢出错误。Go 的有符号整数运算溢出不自动 panic；Java 的普通 long 运算也不自动检查，`Math.addExact` 的语义不能凭类型名搬到 Go。

没有引入 decimal 库：`math/big` 是标准库，中间值精确，`IsInt64()` 检查最终范围；它不是任意精度金额落库。本课数据库映射建议是独立 currency 字符列配 BIGINT amount_minor，并在数据字典冻结币种精度；JSON 是整数字符串。这里没有真实数据库，所以 SQL 列类型、驱动 Scan、持久约束的实证留给后续数据库模块，不声称已经验证。

### E02-C 汇率还要有方向、时间和最小单位转换

`FXQuote` 的分数表达主单位之间的比率：1 Source 主单位等于 Numerator/Denominator Target 主单位。报价必须分子/分母为正、源币种匹配、目标币种受支持、`AsOf < ValidUntil`，使用时刻落在 `[AsOf, ValidUntil)`。

```text
目标最小单位 = 源最小单位 × 汇率分子 × 10^目标精度
             / (汇率分母 × 10^源精度)
```

实际合成报价 `1 CNY = 7/50 USD`：105 分 CNY 换算得到 14.7 分 USD。RejectFraction 返回“结果包含不足一个最小单位的部分”；HalfUp 得到 USD/15；`now==ValidUntil` 报价已过期，拒绝使用。`1 SGD = 113 JPY` 中，100 SGD 最小单位是 1 SGD，正确结果是 113 JPY 最小单位，不能忽略币种精度把 100 直接乘 113 得到 11300。

`big.Int` 在 Convert 中避免分子中间乘积溢出，并在舍入后检查最终 int64。测试覆盖零分母、负分子、币种错向、不支持目标币种、尚未生效、刚好到期、窗口无效、换算最终溢出。报价是合成数据，不提供真实币种市场价格；没有实时交易或资金副作用。

## E03：到期判断必须比较时间点

`time.Time` 表示时间点和用于显示的 Location，可能还带当前进程单调钟；`time.Duration` 是 int64 纳秒数量，不是某天几点。可把前者类比 Java Instant 加显示位置，后者类比 Duration，但 Go 的一个 Time 值同时承担一些墙钟/单调钟行为，不能逐类等同。

### E03-A 同一个截止时刻在三个位置的显示

`ParseInstant("2026-10-12T10:00:00+08:00") → time.Parse(RFC3339Nano) → UTC()` 返回 `2026-10-12T02:00:00Z`。在上海显示为 10:00+08:00，在纽约显示为前一日 22:00-04:00，这是同一个瞬间。

| now 相对截止时刻 | UTC/上海/纽约三种判定 | 原因 |
| --- | --- | --- |
| -1 秒 | false / false / false | `now.Before(deadline)` 为 true |
| 正好 0 秒 | true / true / true | `Expired = !Before`，本契约包含截止边界 |
| +1 秒 | true / true / true | 截止已经过去 |

使用 `Before/Equal` 比较时间点，而不是 `Format()` 的文本，更不要截出日期小时再比较。`In(location)` 只改变已经正确解析的时刻的显示位置；若解析时就把上海 10:00 当 UTC 10:00，它不能替你修正错误的瞬间。

### E03-B 无时区字符串为何让订单多活了八小时

对旧文本 `2026-10-12 10:00:00`：`time.Parse("2006-01-02 15:04:05", text)` 默认解为 UTC 10:00；`ParseInLocation(..., Asia/Shanghai)` 解为上海 10:00，对应 UTC 02:00。运行打印相差 8h，真实截止时刻的 `now` 下，错误解析判定未过期，正确解析判定已经过期。

修复必须改变解析所用时区或改线上契约明确 offset，不能只让日志格式显示 +08:00。本课新接口拒绝不带 Z/offset 的文本；旧接口如果业务明确提供“上海本地钟表时间”，则调用 ParseInLocation，并把这个约定写进字段契约。Go layout 使用参考时间 `2006-01-02 15:04:05`，不是 Java `yyyy-MM-dd HH:mm:ss`；月份、分钟数字位置不同也不能随便替换。

`ParseInstant` 先拒绝无 offset、逗号小数、超过九位精度、非法 offset 形状，再由 time.Parse 验证真实日期，最后把时间统一到 UTC 并限制 `[2000,2100)` 年。这是本课订单范围，不是 RFC3339 能表达的完整范围。嵌入 `time/tzdata` 使 Windows 实验独立拥有真实时区数据；固定 +08:00 offset 可表达本次时刻，但不能代替含夏令时变化的 IANA 区域规则。

### E03-C 单位混用的实际影响

运行结果：`time.Duration(30)=30ns`，`30*time.Minute=30m0s`。把接口的 ttl_minutes=30 直接强转 Duration 并加到 Now，会几乎立即过期；正确做法是字段名和单位明示，再检查数值/乘法范围后乘 time.Minute。Duration 大约可容纳 290 年的纳秒时长，本课没有把无限大的外部数字直接乘入 Duration。

同一截止时刻的 Unix 秒是 `1791770400`，Unix 毫秒是 `1791770400000`：

| 错误/修复调用 | 实际结果 | 到期影响 |
| --- | --- | --- |
| `time.Unix(milliseconds, 0)` | year=58748 | 变成极远未来，订单错误地没过期 |
| `time.UnixMilli(seconds)` | 1970-01-21T17:42:50Z | 变成很早过去，订单错误地已过期 |
| `ParseUnixMillis(1791770400000)` | 2026-10-12T02:00:00Z | 与标准截止时刻 Equal |
| `ParseUnixMillis(1791770400)` | 范围错误 | 阻止秒误传毫秒进入订单 |

`Unix/UnixMilli` 与时区显示无关。`ParseUnixMillis` 不是自动猜单位，而是固定“字段必须是毫秒”并检查订单范围。新版本协议使用 RFC3339 UTC 字符串；如果历史接口确需数字则名字必须为 `expires_at_unix_ms`，不能悄悄让同一个 expires_at 同时接受秒、毫秒、字符串。

### E03-D 单调钟不能序列化到数据库

`start=time.Now()` 带墙钟和当前进程单调钟，`end=start.Add(30*time.Minute)` 保留两种读数。两者都有单调部分时，`end.Sub(start)` 等测量用单调钟，即使墙钟有校准也能保持这次进程内测量的语义。这里没有真的等 30 分钟，也没有修改系统时钟。

本机实际打印：Now 含 `m=` 标记 true，Add 后 true，JSON 恢复后 false；`end.Sub(start)=30m0s`；`start.Equal(restored)=true`；`start==restored=false`；`start.Round(0).Equal(restored)=true`。`m=` 的具体数值是本次观察，不是线上协议或跨进程时钟。

`MarshalJSON/Format` 不保存单调部分，解析/构造的时间也没有它；`UTC/In/Local`、`Round/Truncate`、`AddDate` 会去掉它。只要比较两方中任一没有单调部分，就回到墙钟。落库后订单截止时刻是跨进程墙钟事实，不能声称 JSON 恢复后仍能抗系统改钟；计时与业务到期要分清。

`==` 还比较 Location 和单调读数；`Equal` 比较时间点，业务通常应选它。测试不把某个平台的 == 结果当成普遍强制断言。本课验证了 JSON 丢失单调部分及 Add/Sub，不通过修改系统钟模拟 NTP，也不承诺系统休眠期间单调钟一定累计真实时长。

## E04：Go 1.27 正式 JSON v2 的真实对照

本机核验 `go version go1.27.0 windows/amd64`，`GOTOOLCHAIN=auto`，`GOROOT=D:\Go`，`GOEXPERIMENT` 为空。`go list encoding/json/v2 encoding/json/jsontext` 正常成功。[Go 1.27 发布说明](https://go.dev/doc/go1.27)确认这两个包已经正式可用，默认 v1 API 由 v2 实现支撑但保留 v1 语义。代码导入不同包路径时才选择对应 API/默认协议，不是运行时自动把所有旧 DTO 当 v2。

另用只在本次子进程生效的 `GOEXPERIMENT=nojsonv2` 验证：`go list encoding/json/v2` 退出 1，原始错误为 `build constraints exclude all Go files`；核心 `go run ./cmd/demo` 仍退出 0。环境随后恢复。可核对 `evidence/nojsonv2.txt` 和 `evidence/nojsonv2_core.txt`。所以无需设置旧教程的实验开关，opt-out 后 v2 入口不可构建是已知条件，并非课程业务失败。

正式 API 是 `Marshal/MarshalWrite/MarshalEncode` 和 `Unmarshal/UnmarshalRead/UnmarshalDecode`，带可变 `Options`。本课实际使用 Marshal/Unmarshal 和正式选项，没有创造一个 v2 `Decoder.UseNumber`。`jsontext` 负责低层语法；`AllowDuplicateNames` 属于它；`RejectUnknownMembers/MatchCaseInsensitiveNames` 属于 v2。主线订单仍使用 v1，E04 独立运行。

同一 shape 值：quantity=0、enabled=false、note 指向空字符串、tags/meta=nil，zero_time 是零值并标记 omitzero。实际输出：

```text
v1={"note":"","tags":null,"meta":null}
v2默认={"quantity":0,"enabled":false,"tags":[],"meta":{}}
v2+DefaultOptionsV1={"note":"","tags":null,"meta":null}
```

| 观察点 | v1 API | v2 默认 | 影响/显式控制 |
| --- | --- | --- | --- |
| quantity=0/enabled=false + omitempty | 省略 | 保留 | v2 看 JSON 空值，0/false 不空；明确 Go 零值策略用 omitzero |
| 非 nil 指针指向空字符串 + omitempty | 保留 note:"" | 省略 | 不能把 import 替换看作字段形状不变 |
| nil slice/map | null/null | []/{} | v2 可用 FormatNilSliceAsNull/FormatNilMapAsNull 选择，或全套 v1 兼容 |
| 零 time.Time + omitzero | 省略 | 省略 | 与本类型 IsZero 的业务定义一致 |
| 重复 quantity | 后值 9，无错误 | 在第二个名字报错；目的值可能已为1 | jsontext.AllowDuplicateNames 显式控制；不要用失败后的部分目的值 |
| `QUANTITY` | 匹配 quantity=4 | 默认未知，忽略，quantity=0 | MatchCaseInsensitiveNames(true) 后为4；默认大小写严格不代表默认拒绝未知 |
| `quantitty` | 忽略 | 忽略 | v2 RejectUnknownMembers(true) 才拒绝未知名字 |
| Unmarshal 输入 `{} {}` | 拒绝 | 拒绝 | 两个整体 API 都要求单值；不要与前面的单次流式 Decode 混淆 |
| 已有quantity=7再解null | 7 | 0 | 改包路径会改变复用目的值上的 null 语义 |
| 非法 UTF-8 字符串字节 | 替换为 U+FFFD | 错误 | jsontext.AllowInvalidUTF8 可显式调整，核心输入主动拒绝非法字节 |
| time.Duration=time.Second | 数字1000000000（纳秒） | 默认无表示、错误 | 兼容选项可恢复；接口应定义明确 duration 字段单位/文本 |
| 两个 Go 字段同 json tag | 本例输出 {}，无错误 | 字段冲突错误 | 故障入口用反射构造，正常业务 DTO 的名字应唯一 |

`jsonv1.DefaultOptionsV1()` 是整套兼容选项，不只控制重复；它用于 `jsonv2.Marshal/Unmarshal`。本课测试独立验证兼容后字段形状相同，也验证 `DefaultOptionsV1(), jsontext.AllowDuplicateNames(false)` 的后置选项重新禁止重复。迁移时可逐条明确改变策略，并冻结/升级 contract_version，再检查真实消费者。

不要沿用旧实验包教程的 tag：Go 1.27 正式接口已移除旧 `format/unknown` tag，旧 `inline` 改为 `embed`；正式 `string` tag 针对本字段顶层数字值，不是对任意 bool/string/嵌套内容一概加引号。本课的金额用自定义 Minor JSON 方法固定字符串协议，未依赖这些旧 tag。正式 tag/选项以本机 `go doc encoding/json/v2` 和[当前 v2 文档](https://pkg.go.dev/encoding/json/v2)为准。

## 后续综合项目可直接参考的 order.v1 字段契约

| 字段 | JSON/Go 表示 | 创建边界 | PATCH 与输出 |
| --- | --- | --- | --- |
| contract_version | string / string | 必须为 order.v1 | PATCH 从 /v1 路由选契约；输出始终 order.v1 |
| request_id | string / string | 1..64 ASCII 字母数字、下划线、横线 | 不可 PATCH；仅追踪键，本课未实现幂等存储 |
| order_id / user_id | string / ID | 规范正整数，范围 1..MaxUint64 | 不可 PATCH；保持字符串，不能变 JSON number |
| sku | string / string | 1..64 ASCII 字母数字、下划线、横线 | 不可 PATCH；本课不验证真实 SKU 目录/用户权限 |
| quantity | JSON integer / int64 | 显式 1..1000，缺失/null/0 均拒绝 | 缺失不变；null/0 拒绝；新数量服务端重算总额 |
| currency | string / string | 冻结白名单 CNY/SGD/USD(2)、JPY(0)、KWD(3) | 不可 PATCH；与金额始终配对 |
| amount_minor | **JSON 整数字符串** / Minor(int64) | 显式正整数；不接受 JSON number/小数/指数/溢出 | 不可 PATCH；输出同样字符串 |
| expires_at | RFC3339 string / UTCInstant | 明确 Z/offset；[2000,2100)；必须晚于 now | 输出统一 UTC、最多9位小数；截止时刻本身过期 |
| note | string 或 null / *string | 缺失/null 同表示暂无备注 | PATCH缺失保留、null清除、空字符串保留为值；输出不省略 |
| tags | 非 null array / []string | 必须显式数组，最多8个合法标签 | PATCH缺失保留、null拒绝、[]清空；输出已知空集合为[] |

`amount_minor` 与 ID 在 JSON 上选字符串是本课明确选择，JSON 消费者需要按此校验，不可以同时“兼容”数字/字符串而不记录版本。创建金额目前验证表示与范围，真实商品目录定价、账户授权、request_id 的幂等持久性和并发写入由后续业务层补齐；不能把本课接口解码当作全部下单安全措施。

## 验证证据与证明范围

原始日志在 `evidence/demo.txt`、`evidence/jsonv2.txt`、`evidence/test.txt`、`evidence/vet.txt`、`evidence/toolchain.txt`，完成情况和真实命令在 [STATUS.md](STATUS.md)。测试中的期望数字 29/101/598/113、UTC 文本和毫秒值都来自独立业务事实，未通过被测函数重新算期望。

本课没有连接数据库、调用外部服务、改系统时间或测线上协议；没有并发路径。已额外使用主 agent 提供的便携 amd64 编译器执行 `go test -race -count=1 -timeout 40s ./...`，结果通过，原始记录在 `evidence/race.txt`；这项检查不能替代字段、金额与时间的业务断言。普通入口的危险样例都捕获错误/只读取局部值，有限次数结束；退出后没有服务、连接、timer 或故障代理残留。

原结构体资料位于主目录 `files/基础语法/go语言结构体`，已只读；worktree 的 `examples/basic-syntax/structs/main.go` 已阅读。这些资料讲 struct 组合字段与复制有帮助，但没有规定线上字段的缺失/null、数值精度或时区语义，本课没有把资料省略的部分当成隐含保证。

查证入口是[encoding/json](https://pkg.go.dev/encoding/json)、[Go 1.27 发布说明](https://go.dev/doc/go1.27)、[encoding/json/v2](https://pkg.go.dev/encoding/json/v2)、[time](https://pkg.go.dev/time)。本机源码入口为 `D:/Go/src/encoding/json/v2_options.go` 的兼容选项、`D:/Go/src/encoding/json/v2/arshal.go` 的 Marshal/Unmarshal、`D:/Go/src/time/time.go` 的单调钟说明。文档、语言规则、当前库契约与本次实际观察在正文中分别说明；旧版本项目应按其工具链重新核对，不擅自升级。
