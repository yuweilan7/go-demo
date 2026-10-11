# M03：库存列表中的 slice、map、string 与闭包

读业务代码时，先追问两个问题：**这个变量复制了什么？它里面的可变对象现在由谁拥有？** 本课用同一批库存 A=5、B=0、C=7 复现过滤、追加、缓存与回调的错误，并把修复直接放在同一次运行里。没有额外练习题。

默认入口可以完整跑完。错误过滤、缓存污染和吞掉失败的闭包属于可观察的逻辑错误；写 nil map 的真实 panic 另有显式入口。数据只存在本进程的内存中，`SnapshotCache` 是课程用的同步内存缓存，不代表已经验证 Redis 或多 goroutine 缓存。

## 先建立最小模型

Go 的参数和赋值复制的是**值**。需要再看这个值里有没有指向别处的引用。

| 当前变量 | 赋值或传参复制什么 | 修改副本会影响哪里 |
| --- | --- | --- |
| `[3]int` 数组 | 三个 int 元素 | int 元素独立；数组元素如果本身含 slice/map，仍须继续追引用 |
| `[]Product` slice | 描述一段底层数组的值：起点、len、cap | 改已有元素可能影响所有共享该数组的窗口；改局部 len 不回写另一个描述值 |
| `Product` struct | SKU、Stock、Tags、Meta 各字段的值 | Stock 独立；Tags 的底层数组、Meta 的 map 仍可能共享 |
| `map[string]Product` | map 的值 | 两个变量仍访问同一 map；读出的 Product 又是元素值副本 |
| `string` | 不可变字节序列的值 | 重新赋值改变变量；不能用 `s[i]=...` 改字节 |
| `*Product` | 地址值 | 两个地址若相同，写入就到同一个 Product；指针不是隔离机制 |
| 闭包 | 可继续访问所捕获变量的函数值 | 读取发生在调用时；捕获外部变量不自动创建当时值的快照 |

slice 可想成“窗口凭证”。窗口凭证是副本，仓库货架未必是副本。Java 的 `ArrayList` 方法调用通常拿到同一个列表对象引用，而 Go 的 slice 参数拿到自己的描述值，所以函数内 `s = append(s, ...)` 改变的是函数的 `s`，不能直接把调用者变量的 size/len 改掉。已有元素的共享又与 Java 容器里放可变对象相似；Go 的 struct 元素还会引入“struct 副本独立、嵌套对象共享”的额外一层。

现在必须理解描述值与底层对象、len/cap、复制边界、range 变量和 UTF-8。哈希探测算法、扩容公式、逃逸分析的具体决策和零拷贝优化可以等读到相应源码时再查；本课业务判断不依赖它们。

## 10～20 分钟核心路径

从仓库根目录运行：

```powershell
go -C lessons/m03_collections_aliasing run ./cmd/demo -case core
```

阅读顺序是 [入口](cmd/demo/main.go) 的 `main` → [分派](internal/lesson/run.go) 的 `Run` → [核心实验](internal/lesson/cache.go) 的 `coreExperiment` → [库存模型](internal/lesson/product.go) 的 `Product`、`FilterInPlaceBad`、`FilterSnapshot`、`CloneProduct`、`FilterOwned` → `cacheExperiment` → [slice 实验](internal/lesson/slices.go) 的 `slicesExperiment`。

可以用五分钟讲 A/B/C 过滤、五分钟讲浅拷贝和缓存边界、五分钟讲两次 append、最后核对输出与所有权。程序本身只需要很短时间；10～20 分钟指阅读讲解。

完整入口和分步入口：

```powershell
go -C lessons/m03_collections_aliasing run ./cmd/demo
go -C lessons/m03_collections_aliasing run ./cmd/demo -case slices
go -C lessons/m03_collections_aliasing run ./cmd/demo -case cache
go -C lessons/m03_collections_aliasing run ./cmd/demo -case collections
go -C lessons/m03_collections_aliasing run ./cmd/demo -case loops
go -C lessons/m03_collections_aliasing run ./cmd/demo -case text
go -C lessons/m03_collections_aliasing test -count=1 -v ./...
go -C lessons/m03_collections_aliasing vet ./...
```

`all` 的真实调用链为：

```text
main → flag.Parse → lesson.Run
  → coreExperiment
      → NewInventory → FilterInPlaceBad → 两次 append（错误）
      → FilterSnapshot → CloneProduct → 分支 CloneProducts（修复）
      → FilterOwned → clear 尾部引用
      → cacheExperiment → SnapshotCache.Put/Get → CloneProducts
          → retentionExperiment
      → slicesExperiment → mutateAndAppend
  → collectionsExperiment
  → loopsExperiment → ObserveLoops / errorCallbacks → 同步执行已保存回调
  → textExperiment → PrefixRunes
  → 返回 nil 或 error → main 非零退出处理
```

每一步同步调用、同步返回；没有启动 goroutine、等待 channel、锁或网络，也没有隐含的后台取消。各函数拥有本次局部合成数据；函数退出后若再无引用，内存才可供 GC 回收。日志 Writer 属于调用者，`Run` 不关闭 stdout。写日志失败会保留第一项错误并返回，关键状态断言失败会立即结束本次入口。

## 实际版本与 module 边界

本轮实测日期为 2026-10-12：

```text
go version go1.27.0 windows/amd64
GOVERSION=go1.27.0  GOTOOLCHAIN=auto  GOROOT=D:\Go
GOPATH=C:\Users\Lenovo\go  GOOS=windows  GOARCH=amd64
CGO_ENABLED=1  CC=gcc  GOWORK=(空)  GOEXPERIMENT=(空)
```

本课 [go.mod](go.mod) 是 `module go-demo/lessons/m03_collections_aliasing`、`go 1.27`，没有 `toolchain` 指令、第三方 require 或 go.sum。仅用本机标准库。旧语义小入口的 [go.mod](versioned/go121/go.mod) 明确为 `go 1.21`，也没有 toolchain 指令；它并未下载或运行 Go 1.21 二进制，而是让本机 Go 1.27 编译器按该包的旧语言版本编译。

两个 module 的语言版本分别决定循环语义。根 module 的 `go test ./...` 不会进入本课这个嵌套 module；本课的 `go test ./...` 同样不会进入 `versioned/go121`，旧入口须单独执行。全课没有硬前置，不导入或复制其他课程，也不修改根 go.mod/go.work。

## E01：传参和两次 append 为什么会“时而污染”

定位 [slices.go](internal/lesson/slices.go)：`slicesExperiment → mutateAndAppend`。

`caller` 是 len=2、cap=4 的 slice。`mutateAndAppend(caller)` 复制描述值给 `s`。`s[0]="A*"` 修改共享数组；`s=append(s,"LOCAL")` 在数组下标 2 写入 LOCAL，却只把局部 `s.len` 改成 3。返回的描述值存到 `returned`，原 `caller.len` 仍然为 2。

真实输出：

```text
传参: caller=[A* B] len=2 cap=4; returned=[A* B LOCAL] len=3; 隐藏槽=[A* B LOCAL ]
```

`caller[2]` 仍越界：合法下标只看 len。日志先显式 `caller[:cap(caller)]` 扩展窗口，才可以观察第三槽。cap 并不意味着普通下标可以访问整个容量。

接下来 `base=[A B C D]`，`prefix=base[:2]`。它的 len=2、cap=4。顺序完全确定：

| 步骤 | 语句与参与者 | 底层数组与描述值 | 可观察结果 |
| --- | --- | --- | --- |
| 1 | `first := append(prefix,"X")` | 有容量，写下标 2；first.len=3；prefix.len 仍=2 | base 变 `[A B X D]` |
| 2 | `second := append(prefix,"Y")` | 又从同一个 len=2 前缀追加，仍写下标 2 | X 被 Y 覆盖 |
| 3 | 打印三份窗口 | first、second 与 base 共享这项 | first 的第三项也变成 Y |

```text
有容量两次 append: base=[A B Y D] first=[A B Y] second=[A B Y]; 同一第0项=true
```

换成 `limited=full[:2:2]`，len=cap=2。这是三下标 `[low:high:max]`，长度为 high-low，容量为 max-low。追加一项需要 len=3，超出 cap，两次 append 各自领取新数组：

```text
无空余容量两次 append: base=[A B C D] first=[A B X] second=[A B Y]; 同一第0项=false
容量封顶后直接改元素: full[0]="still shared"，已扩容结果 first[0]="A"
```

最后一行来自扩容完成后直接改 `limited[0]`。`limited` 自己仍指向原 full；容量封顶只控制未来 append，不能使现有元素独立，也不能使嵌套 map/slice 独立。业务若要给两个可变分支各自一份列表，应复制各自需要拥有的内容。

`copy(dst,src)` 的目标必须已经有足够长度；它不会改变 dst.len，返回实际复制数量。本例 `make([]string,len(base))+copy` 与 `slices.Clone(base)` 得到独立的 []string 数组：

```text
copy/Clone: n=4 source=[A B Y D] copied=[COPY B Y D] cloned=[A CLONE Y D]
copy 到 len=1 的目标: n=1，目标=[A]
```

这里 string 元素不可变，复制这一层已经够用。换成 []Product，则必须继续追 `Tags/Meta`。`slices.Clone` 的合同是浅拷贝并保留 nilness，结果也可能有额外容量；不能把 `cap(clone)==len(clone)` 当作它的合同。[Go 1.27.0 slices.Clone 文档](https://pkg.go.dev/slices@go1.27.0#Clone)

append 超过容量后需要新存储是可依赖的规则；新 cap 究竟是多少是实现细节。本机源码在 `D:/Go/src/runtime/slice.go` 的 `growslice`/`nextslicecap`，包含增长选择与分配尺寸调整。旧材料中的 2 倍/1.25 倍、阈值和公式不能变成业务判断条件，本课不写任何倍率断言。

## E02：库存过滤、缓存污染、两个业务分支和修复

定位 [cache.go](internal/lesson/cache.go) 的 `coreExperiment`，并读 [product.go](internal/lesson/product.go) 的四个过滤/复制函数。

库存源 `[A(5),B(0),C(7)]` 还被缓存持有。`badCache:=input` 没有复制三个元素。错误过滤用 `result:=products[:0]`：len 归零了，数组和可用容量还在。

| 顺序 | 当前函数/语句 | 状态变化 | 下一步为何发生 |
| --- | --- | --- | --- |
| 1 | `coreExperiment` 创建 input，保存 badCache | 两份描述值共享 A/B/C 数组 | 调用过滤函数，输入仍被调用者保留 |
| 2 | `FilterInPlaceBad` 看 A | Stock>0，写入结果下标 0 | A 仍为 A |
| 3 | 看 B | Stock=0，跳过，不增加结果长度 | 下一个入选项将写下标 1 |
| 4 | 看 C | Stock>0，C 覆盖原下标 1 的 B | 输入和缓存都看到 `[A C C]` |
| 5 | 返回结果 | result.len=2，input/cache.len=3 | 结果只是新长度的共享窗口 |
| 6 | 从结果两次追加 D/E | 两次都写第三槽 | 第一分支和缓存最终也看到 E |

```text
错误过滤: filtered SKU=[A C]; input SKU=[A C C]; cache SKU=[A C C]
错误结果两次 append: withD=[A C E] withE=[A C E] cache=[A C E]
```

这不是 goroutine 问题，单线程按这些顺序执行也一定出错。错误由“调用者仍保留输入”与“过滤函数擅自复用数组”的契约冲突产生。

`FilterSnapshot` 分配独立结果数组，入选项再经 `CloneProduct` 复制 Tags 和 Meta。修改 snapshot 的 Stock、Tags、warehouse 后，original 仍保持 A=5、热卖、深圳。接着两个分支分别 `CloneProducts(snapshot)` 再追加，使它们的列表和嵌套内容各自独立：

```text
快照修复: original SKU=[A B C] A={SKU:A Stock:5 Tags:[热卖] Meta:map[warehouse:深圳]}; snapshot SKU=[A C] A={SKU:A Stock:99 Tags:[请求标签] Meta:map[warehouse:请求仓]}
两分支快照修复: D=[A C D] E=[A C E] original=[A B C]; D标签=[D 分支标签] E标签=[请求标签]
空列表过滤: nil结果 nil=true; 空输入结果 nil=false len=0; 全缺货结果 nil=false len=0
```

空列表通过明确分支处理，不去取 `&s[0]`；本课约定保留 nil 与非 nil 空的差别。返回的空列表是否统一为 nil，是 API 契约选择，应当明确，不能依赖打印时两者都像 `[]`。

如果业务真的将唯一可变所有权移交给过滤函数，复用数组可降低分配成本。`FilterOwned` 正是这个修复：沿用原地过滤算法，在遍历结束后 `clear(products[len(result):])` 清理尾槽的 Product 引用，再封顶返回容量。真实调用者移交后不能继续业务使用旧 input 或其旧对象别名。

```text
独占修复: owned=[A C] len/cap=2/2; 丢弃尾槽={SKU: Stock:0 Tags:[] Meta:map[]}
```

fmt 的 `Tags:[] Meta:map[]` 不能证明 nil；独立测试和核心断言实际检查 Tags/Meta 为 nil。清理发生在 range 完成后，避免抹掉尚未读取的输入。这里只证明旧尾槽不再保存引用，未证明 GC/RSS 已下降。复制成 []*Product 会保留同一 Product 身份和可变对象，不能默认解决缓存污染。

## E03：浅拷贝的边界，缓存写入和读取各自的所有权

定位 `cacheExperiment → SnapshotCache.Put/Get → CloneProducts → CloneProduct`。

第一版直接 `bad["stock"]=request`。写入返回后，请求1继续改 request；请求2读到被改过的内容。写入 map 的成功只证明保存了 slice 值，不意味着缓存冻结了请求数据。

第二版 `shallow:=slices.Clone(request)`。新数组里的 Product.Stock 为独立值，Tags 和 Meta 依然共享：

```text
缓存未复制: 下一请求读取 A={SKU:A Stock:88 Tags:[污染标签] Meta:map[warehouse:污染仓]}
只 Clone 外层: shallow A={SKU:A Stock:5 Tags:[浅拷贝标签] Meta:map[warehouse:浅拷贝仓]}（Stock 独立，Tags/Meta 仍共享）
maps.Clone 嵌套 slice: source[A]=[共享标签] clone[A]=[共享标签]
```

第三行单独补充 map 容器的相同边界：`maps.Clone(map[string][]string)` 新建 map，但复制的值仍是 slice 描述值，不能递归复制数组。[Go 1.27.0 maps.Clone 文档](https://pkg.go.dev/maps@go1.27.0#Clone)

修复的时间线是：

| 顺序 | 参与者 → 当前函数 | 哪份对象归谁 | 证明点 |
| --- | --- | --- | --- |
| 1 | 请求1 → `cache.Put` | 请求1持有 request；缓存获得独立数组/Tags/Meta | 写入后 request 的继续修改不污染缓存 |
| 2 | 请求2 → `cache.Get` | 请求2获得另一份独立可变快照 | 请求2修改返回值不污染缓存 |
| 3 | 请求3 → `cache.Get` | 从缓存的原始快照再复制 | Stock=5、热卖、深圳仍成立 |

```text
Put/Get 双向复制: 后续读取 A={SKU:A Stock:5 Tags:[热卖] Meta:map[warehouse:深圳]}
```

当前 Product.Meta 是 `map[string]string`，Tags 是 `[]string`，复制容器后 string 值不可变，因此这套复制深度刚好够用。如果未来加 `map[string][]byte`、`*Price` 或更深结构，要重新设计边界。不能把函数名叫 Clone/DeepCopy 当作永远正确的保证。

生产代码也可以约定独占转移、只读共享或不可变封装。Go 没有 `const []Product` 来强制全部嵌套对象只读，若 Getter 暴露可变对象，要靠 API 和审查兑现约定。本例选择读写各复制一次，目的是让隔离事实可观察；这不意味着所有生产缓存都必须如此分配。

## E04：为什么 cap=4 的小切片还可能保留 1 MiB

定位 [slices.go](internal/lesson/slices.go) 的 `retentionExperiment`。

本函数创建 1 MiB `large`，截取 `window=large[:4:4]`，再 `make+copy` 得到 compact。window 只有四项且 cap=4，但仍是大数组上的窗口；capacity 不是底层分配对象的大小。

```text
E04 小窗口: 原数组=1048576 bytes window len/cap=4/4 shared=true 值="ZBCD"; copied independent=true 值="ABCD"
E04 生命周期: 工厂已返回，缓存 len/cap=4/1048576，GC后数组末尾哨兵=7
E04 生命周期修复: 缓存 len/cap=4/4 值="ABCD"，旧窗口引用已清除
E04 PASS：large/window/stored 的本地引用已清除；未测量 GC/RSS
```

`large[0]='Z'` 后 window 看到 ZBCD，而 compact 保持 ABCD。地址比较只判断本例非空 byte 元素是否同一位置，不输出 unsafe 地址数字，也不推断 GC 时机。持久缓存只留 compact，并使旧数组所有引用消失，才有机会释放大对象；仅让原 large 变量离开作用域、仅 `large=nil`、或仅限制 window.cap 都不足以断言旧数组已无引用。[官方 slice 的保留数组说明](https://go.dev/blog/slices-intro)

第二阶段让 `retainedPrefix` 工厂创建另一份 1 MiB 数组，只返回四字节前缀，由 `prefixCache` 保存。工厂已经返回，创建者的局部变量已离开作用域；本课再调用 `runtime.GC()`，缓存仍能在 cap 范围内读取数组末尾的哨兵 7。它证明缓存窗口仍可使用整块存储，不把 GC 当作自动复制或只保留 len 字节的机制。修复把四字节内容复制到新数组，替换缓存条目，并清除调用者自己的 `stored` 旧窗口；只改缓存而还持有 stored，也不能断言旧数组不再被引用。

函数结尾同时清除 large/window/stored 局部引用，`runtime.KeepAlive(compact)` 确保第一阶段观察期间结果仍活跃。实验确实触发一次 GC，但不采样回收量/堆/RSS，也不声称后续旧数组已经被回收。如果大 string 的小子串需要长期保存，`strings.Clone` 有显式新分配契约，通常应在分析实际内存保留问题后使用，空串是例外。[strings.Clone 文档](https://pkg.go.dev/strings@go1.27.0#Clone)

## E05：nil/空集合、map 元素与并发边界

定位 [collections.go](internal/lesson/collections.go) 的 `collectionsExperiment`。

```text
nil slice: nil=true len/cap=0/0 JSON=null; 空 slice: nil=false len/cap=0/0 JSON=[]
append nil slice: [A] nil=false
nil map 读/删/遍历: value=0 ok=false len=0 iterations=0
make 后写入 0: A=(0,true) missing=(0,false)
```

nil slice 与非 nil 空 slice 都 len=0、可 range、可 append；两者都不能访问下标 0。`encoding/json` 默认把 nil slice 编码成 null，把非 nil 空 slice 编码成 []，会影响 API 响应，需要按合同选择。本例无 omitempty 标签、无自定义 Marshaler。[encoding/json 文档](https://pkg.go.dev/encoding/json@go1.27.0#Marshal)

nil map 可读、len、range、delete、clear；写入要先 `make(map[string]int)` 或用非 nil 字面量。`value,ok:=m[key]` 的 ok 表示键是否存在，单看 value==0 分不清缺失和真实零库存。`delete` 不存在键安全；nil map 的 range 为零轮。

```text
map 别名 A=6; 本次 rawKeys=[A B C]（顺序无保证）; 排序后=[A B C]
map struct 读出后只改局部: p.Stock=8 map.Stock=5
显式回写后: map.Stock=8 map.Tags=[map 副本共享标签]
```

rawKeys 只是本次观察，下次可能相同，也可能不同；不能用“没变化”证明有序，也不能断言两次必定不同。需要稳定日志、导出或签名时，先收集键，再排序。本课独立断言检查排序后覆盖 A/B/C。

map 元素不能取址，`&bySKU["A"]` 不合法；`bySKU["A"].Stock=8` 也不能给这个 struct 元素字段赋值。可运行修复是 `p,ok:=bySKU["A"] → p.Stock=8 → bySKU["A"]=p`。中间 p 是 struct 副本，Stock 修改未回写，p.Tags 的元素修改却已经触达共享对象。这两种副作用发生时机必须分开判断；代码旁保留了不合法写法和预期编译诊断。

`map[string]*Product` 允许先取出指针再修改对象；它引入了共享对象的身份与同步责任，不应该为了躲过 map 元素不可取址就普遍改成指针。

### 显式真实错误与修复

```powershell
go -C lessons/m03_collections_aliasing run ./cmd/demo -case nil-map
go -C lessons/m03_collections_aliasing run ./cmd/demo -case collections
```

第一条只有固定步骤，无无限循环。实测先打印：

```text
E05 故障前: nil map len=0；下一句尝试写 A=5
E05 故障清理: defer 已执行；没有外部资源需要关闭
panic: assignment to entry in nil map
...
exit status 2
```

`nilMapFailure` 注册 defer → 写 nil map 触发 panic → 同一个 goroutine 展开栈执行 defer → Run/main 无 recover → runtime 结束进程。panic 后的“不可达”日志没有执行。`go run` 包装命令的退出码实测为 1，内部程序报告 exit status 2，二者须分别看。测试里的局部 recover 只为了证明清理日志和阻止预期故障终止整套测试。第二条用 make 初始化 map 并完成上述正常输出。

map 同步边界本课只说明：并发只读需要安全发布且全程无修改；有写者时，其他读写、遍历都需要正确同步或严格所有权。运行时可能检测并发违规并 fatal，但没有 fatal 不等于没有 race，普通 recover 也不能作为并发 map 的保护方案。map 外面加锁也不自动保护锁外暴露的 *Product/Tags/Meta。实际同步实现和真实并发实验交 M14，本课不复制旧资料中的无限写读循环。

## E06：新循环变量规则解决了哪一层

定位 [loops.go](internal/lesson/loops.go)：`loopsExperiment → ObserveLoops → callStringFunctions`，以及 `errorCallbacks → validateSKU`。

这组实验明确先保存所有回调、结束循环、再按顺序同步调用。没有 goroutine 和 sleep，结果由变量身份决定。

| 写法 | 本 module go 1.27 | go 1.21 fixture | 本课修复 |
| --- | --- | --- | --- |
| `for _,sku := range skus` | range 子句每轮声明新 sku | 各轮复用同一 sku | 旧版在循环体内 `snapshot:=sku` |
| `var sku string; for _,sku = range skus` | 复用已有 sku | 同样复用已有 sku | 每轮显式创建快照，或在合适处改成声明形式 |
| `for i:=0;...;i++` | init 声明的循环变量适用新规则 | 旧版共享循环变量 | 阅读时同时看模块/文件版本 |
| 捕获外层变量或重复地址 | 外层仍共享，对象仍共享 | 仍共享 | 设计每次回调的状态所有权与复制深度 |

新规则与 module 的 go 指令以及可能的文件版本约束关联，不能只看电脑安装了什么编译器。[Go 1.22 发布说明](https://go.dev/doc/go1.22)，[官方循环变量迁移说明](https://go.dev/blog/loopvar-preview)

本课新版本实测：

```text
循环后调用: := 声明=[A B C]; = 赋值=[C C C]; 循环体快照=[A B C]
&v: 各轮地址不同=true 元素地址相同=false; 改副本 Stock=100 原 Stock=5; 原 Tags=[副本仍共享标签]
&slice[i]: 地址相同=true; 原 Stock=101
= 的 &v: 指针相同=true; 三个 SKU=C/C/C
```

`&v` 即使新版本每轮不同，也指向本轮 Product 副本，不是原元素；修改 Stock 不会回写。副本的 Tags 却仍引用原数组，所以标签会变。需要原元素身份时使用 `&products[i]`；需要独立数据时复制所需字段，两种需求不要混淆。本例 Product 非零大小，地址比较用于观察具体位置，没有把零大小变量的地址特性泛化到这里。

另运行旧 fixture：

```powershell
go -C lessons/m03_collections_aliasing/versioned/go121 run .
go -C lessons/m03_collections_aliasing/versioned/go121 vet ./...
```

```text
runtime=go1.27.0 module-language=go1.21
旧 := 闭包=[3 3 3]; &v 全相同=true; 元素地址相同=false
循环体快照修复=[1 2 3]
go1.21 fixture PASS：编译器版本与模块语言语义分别核对
```

fixture 没有升级 go 指令或设置 GOEXPERIMENT，旧循环是真实执行结果，非用新版本 `=` 冒充旧版本。它只覆盖这里的循环声明/地址/快照差异，不声称运行了一个完整 Go 1.21 标准库环境。

### 独立变量仍可能指向同一业务对象

```text
独立变量捕获共享 *Product: [42 42 42]
捕获外层变量后继续改: after
共享外部 err: bad=[A:ok B:ok C:ok]; 每轮保存 currentErr: fixed=[A:ok B 缺货 C:ok]
```

第一行三个循环变量都保存同一 *Product，调用前 Stock 改成 42；每个闭包都读这个对象。第二行闭包读外层 out，创建后把 out 改成 after，执行就得到 after。

第三行的外部 `var err error` 只有一份。构造 A 回调时 err=nil；构造 B 时 err=“B 缺货”；构造 C 时 err 又被覆写为 nil。所有回调随后读取最终 nil，B 的失败丢失。修复用每轮 `currentErr:=err` 保存错误归属。本例 error 没有后续被改的可变内容；如果自定义 error 自身指向可变对象，复制 error 接口也不能自动冻结它。若改成多 goroutine 同时写读共享 err，还会增加 race；新循环规则对此没有帮助。

Java lambda 对捕获的局部变量通常要求 final/effectively final，但其中对象仍可变；Go 允许闭包直接读写捕获变量。不要套用“编译器不会让我捕获随后修改的局部变量”的 Java 直觉。

### range 固定长度，内容却不是不可变快照

```text
range 长度与数据: seen=[1 9] iterations=2 final=[1 9 3]
```

range 开始时基于长度 2 遍历；第一轮修改底层第二项为 9，再追加 3。第二轮读取同一底层数组的第二项，看到 9；不会因新的 numbers.len=3 多遍历一轮。若 append 换了数组，后续修改落在哪个数组还要重新追别名，不能把本例推广成所有 append 场景都相同。

## E07：中文商品名的 string、[]byte 和 rune

定位 [text.go](internal/lesson/text.go)：`textExperiment → PrefixRunes`。

`string` 是不可变字节序列，通常装 UTF-8 文本，但允许装任意字节；`byte` 是 uint8 别名，`rune` 是 int32 别名，用来表示 Unicode 码点。UTF-8 中文往往一个码点占多个字节。

```text
name="库存A" bytes=7 runes=3; 错误 [:4]="库\xe5" bytes=e5 ba 93 e5 validUTF8=false; 修复前2码点="库存"
range string 的字节起点: [0:库 3:存 6:A]
```

`len(name)` 为 7，`name[0]` 是一个 byte，不是“库”。`name[:4]` 按字节截断，留下库的三个字节和存的第一个字节，得到非法 UTF-8。`range string` 的索引是码点开头的字节位置 0/3/6，值是 rune。不能拿它的索引当“第几个中文字”。

修复函数先拒绝负上限，再 `utf8.ValidString` 验证业务输入，之后 `[]rune(text)` 解码。上限足够就返回原 immutable string；否则在码点边界截取。空输入与上限 0 返回空串，非法输入返回 error，由 textExperiment/Run/main 逐层处理；本函数没有可取消的 I/O 和清理资源。[utf8 文档](https://pkg.go.dev/unicode/utf8@go1.27.0)

```text
改 []byte: 原 string="库存A"，改后 bytes=58 ba 93 e5 ad 98 41
[]byte 转 string 后再改源: string="库存A" 源 bytes="库存B"
改 []rune: 原="库存A" 新="仓存A"
边界: 空文本="" err=<nil>; 上限0="" err=<nil>; 非UTF8 err=商品名不是有效 UTF-8; 负数 err=码点上限不能为负数: -1
```

第一行将首字节改为 X 后并不是合法中文重写，所以日志用十六进制展示；原 string 未变。第二行把 buffer 转为 string 后，再改 buffer 的 ASCII A 为 B，之前的 string 保持库存A。第三行按 rune 替换库为仓，新 string 为仓存A，原值仍为库存A。

安全转换必须保持这些可观察的隔离结果；是否堆分配、是否进行物理 copy 可由编译器在不改变结果的前提下优化。本课不写“每次转换必分配一次”或“某种表达式必零拷贝”。`unsafe.String`/`unsafe.Slice` 等零拷贝写法涉及不可修改、底层生命周期、对象保留和并发所有权；阅读到它们时必须有实际契约和证据，本课禁止靠猜测用 unsafe 改写。

rune 数量也不等于视觉字符数量。测试用国旗 🇨🇳 展示：它由两个码点构成，按 1 rune 截取会得到 🇨。组合重音与 emoji 同样可能包含多个码点；本函数合同只保证“有效 UTF-8 按码点截取”，不保证 UI 的字素完整。

## 旧材料纠正与本机源码入口

已只读参考原示例 `examples/basic-syntax/{arrays-slices-maps,loops,closurestate}` 和主工作区 `files/原理剖析/数据结构/{slice,slice面试与分析,map.md,string是什么}`。没有修改或提交原材料。

| 旧资料容易带出的结论 | 本课处理 |
| --- | --- |
| copy 被叫作“深度拷贝” | 只复制元素这一层；Product 的 Tags/Meta、map 的 slice 值用 E03 证明仍共享 |
| 子切片“扩容后修改就不影响原 slice” | 扩容后的那个结果可以分离数组；旧窗口仍共享，而且元素中的引用仍可能共享 |
| 按固定倍率预测 append 后容量 | 只用容量够/不够判断是否换数组，倍率与尺寸调整不当语言保证 |
| map 一律是 hmap+bmap+溢出桶，查找恒定 O(1) | 旧 bucket 源码只按旧实现阅读；哈希复杂度不是语言承诺，不能保证每次恒定成本 |
| 每次 string 转换一定 copy；string 都是 UTF-8 | 区分不可变/可观察隔离与分配优化；UTF-8 是本课商品名的业务契约，非法 string 可存在 |
| 安装新 Go 后旧循环教程自动失效 | 同时核对 module/file 语言版本，go1.21 fixture 真实复验旧语义 |
| map 并发没报错就安全 | 不依赖 runtime 的偶然检测；实际同步交 M14，不启动无限风险入口 |

本机 Go 1.27.0 的 `D:/Go/src/runtime/map.go` 和 `map_faststr.go` 导入 `internal/runtime/maps`，使用 `maps.Map`；`D:/Go/src/internal/runtime/maps/map.go` 的设计说明、`Map`，以及 `table.go` 的 `table` 都基于 Swiss Table。官方说明 Go 1.24 引入这一实现。旧 `hmap`、`bmap`、`oldbuckets`、overflow 的字段不能直接解释本机路径。[官方 Swiss Table 变更说明](https://go.dev/blog/swisstable)

此处只核对实现版本和符号入口，不把分组大小、探测/扩容细节变成 Go 程序必须遵循的合同。`D:/Go/src/slices/slices.go:Clone`、`D:/Go/src/maps/maps.go:Clone` 的本机实现与本次标准库版本一起读取，用于校准浅拷贝和 nilness。语法规则的权威入口为 [Go 规范](https://go.dev/ref/spec)，实际业务结论同时由本课输出和断言支持。

## 独立验证、证明范围和资源结局

[lesson_test.go](internal/lesson/lesson_test.go) 有八个顶层测试：

| 测试 | 独立证明的业务事实 |
| --- | --- |
| `TestFilterOwnership` | 可售项 A/C 原顺序正确；输出 scalar/嵌套修改不污染输入；反例真覆盖 B |
| `TestEmptyBoundaries` | nil/非 nil 空/全缺货和嵌套 nil/空契约；空集合不越界 |
| `TestCacheOwnsInputAndOutput` | 写入方和读取方都不能污染后续读取；缓存缺失与 nil/空快照可区分 |
| `TestOwnedFilterClearsDiscardedReferences` | 独占过滤废弃尾槽清零，返回 cap 封顶 |
| `TestLoopAndErrorOwnership` | :=、= 与快照结果；B 的失败由其自己的回调保留 |
| `TestUTF8BusinessBoundary` | 中文、空串、0、大上限、负数、非法字节与国旗码点合同 |
| `TestNilMapPanicAndDefer` | 实际 panic，defer 执行，不继续写入后的不可达语句 |
| `TestRunFailureBoundary` | 完整入口能完成；未知编号/日志失败会返回真实错误并保留原因 |

`go test -count=1 -v ./...` 和 `go vet ./...` 实测通过；旧 fixture 单独 run/vet 通过。Race 首次使用默认 `D:/MinGW/bin/gcc.exe` 时，在 runtime/cgo 构建阶段遇到 `cc1.exe: sorry, unimplemented: 64-bit mode not compiled in`。该工具链问题已经解决：读取共享 portable WinLibs 配置，仅给本次 PowerShell 进程设置 CC/CXX、CGO_ENABLED 和 PATH 后，实际执行 `test -race -count=1 -timeout 40s ./...`，退出码为 0，`internal/lesson` 本次输出 `ok ... 6.742s`，没有 race 报告。旧失败保留为已解决历史，详细记录见 [STATUS.md](STATUS.md)。

当前 Windows 环境复跑 race 的命令如下。环境变量只影响这次终端进程及它启动的 Go 子进程，不调用 `go env -w`、不改系统 PATH；退出该进程后不会留下全局配置变化。

```powershell
$m03Toolchain = Get-Content -LiteralPath 'D:\IdeaProject\go-demo\.local\course-orchestrator\toolchain.json' -Raw | ConvertFrom-Json
$env:CC = $m03Toolchain.cc
$env:CXX = $m03Toolchain.cxx
$env:CGO_ENABLED = '1'
$env:PATH = $m03Toolchain.bin_dir + ';' + $env:PATH
go -C lessons/m03_collections_aliasing test -race -count=1 -timeout 40s ./...
```

Race 通过证明实际执行的这些测试未检测到数据竞争；本课同步缓存仍没有并发访问契约，不由这次结果扩展为“map/快照缓存已经并发安全”。

整课数据来自当前进程，最大显式大数组为 1 MiB，没有端口、数据库、缓存服务、后台进程、goroutine 或待关闭连接。正常入口在本次全部同步操作后返回；故障入口有限步骤 panic 后退出；测试局部 recover 后结束。返回的独立快照没有暴露被清理旧尾槽，large/window 引用在本例结束前清除。未验范围包括并发同步、生产缓存性能、GC/RSS、unsafe 零拷贝与完整字素截断，分别不由本课的同步断言替代。
