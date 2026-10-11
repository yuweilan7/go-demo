# M03 实际交付与验证记录

日期：2026-10-12。实现状态：**READY_FOR_REVIEW，交付已冻结**。E01～E07 的同步课程实验、错误/修复对照、八个顶层独立测试及补充 race 检查均已真实通过；主 agent 的独立验收与整层提交尚未由本模块执行者进行。首次 mingw32 编译器失败保留为已解决历史，当前 portable amd64 工具链的 race 结果为 PASS。

## 执行目录和版本事实

- worktree 根：`C:/Users/Lenovo/.codex/worktrees/go-course-m03/go-demo`。
- 唯一写入目录 W：`C:/Users/Lenovo/.codex/worktrees/go-course-m03/go-demo/lessons/m03_collections_aliasing`。
- 旧语义 fixture 目录 L：`C:/Users/Lenovo/.codex/worktrees/go-course-m03/go-demo/lessons/m03_collections_aliasing/versioned/go121`。
- `go version`（worktree 根执行）真实输出：`go version go1.27.0 windows/amd64`。
- `go env GOVERSION GOTOOLCHAIN GOROOT GOPATH GOOS GOARCH CGO_ENABLED CC`（worktree 根执行）：`go1.27.0 / auto / D:\Go / C:\Users\Lenovo\go / windows / amd64 / 1 / gcc`。
- `go -C W env GOMOD GOVERSION GOTOOLCHAIN GOWORK GOEXPERIMENT` 实际 GOMOD 为 W 下 go.mod；GOVERSION=go1.27.0，GOTOOLCHAIN=auto，GOWORK/GOEXPERIMENT 均为空。
- 主课程 `go 1.27`，旧 fixture `go 1.21`；两者均无 toolchain/第三方依赖。没有 go.sum，未修改根 module 或共享 go.work。
- `Get-Command gcc -All` 找到 `D:\MinGW\bin\gcc.exe`，`gcc -dumpmachine` 真实输出 `mingw32`。常见的 C:/D:/msys64 amd64 gcc 路径与 C:/Program Files/LLVM clang 路径本轮均未找到可执行文件。
- 后续按主 agent 提供的共享记录 `D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json`，使用 portable WinLibs `16.2.0posix-14.0.0-ucrt-r2`（记录中的 GCC=16.2.0，mingw-w64=14.0.0）。CC/CXX/bin_dir 指向主目录已忽略的 `.local/toolchains/winlibs-16.2.0posix-14.0.0-ucrt-r2/mingw64/bin/`；本模块只读记录和使用现有编译器，没有安装或全局修改。

下表中的 W/L 仅缩短显示路径；实际命令均将上面的绝对路径直接传给 `go -C`，没有在主工作目录运行或改写其他模块。

## 命令、真实结果和证明点

| 工作目录 | 实际命令形式 | 退出码/结果 | 观察点 |
| --- | --- | --- | --- |
| W | `Get-ChildItem -LiteralPath W -Recurse -Filter '*.go' \| ForEach-Object { gofmt -w $_.FullName; if ($LASTEXITCODE -ne 0) { throw ... } }` | 0，无错误输出 | 本课全部手写 Go 文件含旧 fixture 均格式化 |
| W | `go -C W run ./cmd/demo` | 0，最终 `M03 PASS case=all` | 核心 E02/E03/E04/E01，集合 E05，循环 E06，文本 E07 全部正常结束 |
| W | `go -C W test -count=1 -v ./...` | 0，八个顶层测试 PASS、九个子测试 PASS；`ok .../internal/lesson 1.600s`（本次观察） | 空列表、中文、错误归属、读写隔离、尾部清理和 panic/defer 的独立断言 |
| W | `go -C W test -count=1 ./...` | 0，`ok .../internal/lesson 1.776s`（本次观察） | 补充“工厂返回→缓存保留→主动 GC→独立复制”后复验全部断言 |
| W | `go -C W vet ./...` | 0，无诊断 | 主课程静态检查完成 |
| L | `go -C L run .` | 0，`go1.21 fixture PASS` | 同一 Go1.27.0 编译器按旧 module 语义真实执行，:= 闭包为 [3 3 3]，&v 相同，快照为 [1 2 3] |
| L | `go -C L vet ./...` | 0，无诊断 | 旧 fixture 静态检查完成；不代表 vet 自动检查了所有延迟同步回调错误 |
| W | `go -C W run ./cmd/demo -case nil-map` | go run 包装命令退出码 1，输出 `exit status 2` | 真实 nil map 写入 panic，defer 清理先发生；没有“不可达”输出 |
| W | `go -C W test -race -count=1 ./...` | 1，首次构建失败，未执行测试；已解决历史 | 默认 gcc 只支持 mingw32；精确错误如下 |
| W | 进程环境设置后 `go -C W test -race -count=1 -timeout 40s ./...` | 0，`ok .../internal/lesson 6.742s`（本次观察） | portable amd64 编译器构建成功，全部课程测试实际执行，没有 race 报告 |
| worktree 根 | `git -C <worktree根> status --short` | 0，仅 `?? lessons/` | 新文件在课程目录内；没有 git add/commit/push |

首次 Race 构建失败的真实原文（已由下方 portable 工具链补验解决）：

```text
# runtime/cgo
cc1.exe: sorry, unimplemented: 64-bit mode not compiled in
FAIL    go-demo/lessons/m03_collections_aliasing/cmd/demo [build failed]
FAIL    go-demo/lessons/m03_collections_aliasing/internal/lesson [build failed]
FAIL
```

补验的完整实际命令，在 W 中执行：

```powershell
$m03Toolchain = Get-Content -LiteralPath 'D:\IdeaProject\go-demo\.local\course-orchestrator\toolchain.json' -Raw | ConvertFrom-Json
$env:CC = $m03Toolchain.cc
$env:CXX = $m03Toolchain.cxx
$env:CGO_ENABLED = '1'
$env:PATH = $m03Toolchain.bin_dir + ';' + $env:PATH
go -C C:/Users/Lenovo/.codex/worktrees/go-course-m03/go-demo/lessons/m03_collections_aliasing test -race -count=1 -timeout 40s ./...
```

真实补验输出：

```text
?   go-demo/lessons/m03_collections_aliasing/cmd/demo [no test files]
ok  go-demo/lessons/m03_collections_aliasing/internal/lesson 6.742s
```

上述 shell 进程结束即丢弃这些临时环境设置；没有全局改 CC、PATH 或 Go 环境，也没有用 `CGO_ENABLED=0` 冒充 race。课程实验全部同步，没有故意制造 map 并发写入。这次 PASS 只证明实际运行的测试未检测到数据竞争，不证明 SnapshotCache 已支持并发访问，实际同步仍交 M14。

## 关键真实输出

正常 all 入口的最终版本实测：

```text
错误过滤: filtered SKU=[A C]; input SKU=[A C C]; cache SKU=[A C C]
错误结果两次 append: withD=[A C E] withE=[A C E] cache=[A C E]
快照修复: original SKU=[A B C] A={SKU:A Stock:5 Tags:[热卖] Meta:map[warehouse:深圳]}; snapshot SKU=[A C] A={SKU:A Stock:99 Tags:[请求标签] Meta:map[warehouse:请求仓]}
两分支快照修复: D=[A C D] E=[A C E] original=[A B C]; D标签=[D 分支标签] E标签=[请求标签]
空列表过滤: nil结果 nil=true; 空输入结果 nil=false len=0; 全缺货结果 nil=false len=0
独占修复: owned=[A C] len/cap=2/2; 丢弃尾槽={SKU: Stock:0 Tags:[] Meta:map[]}
只 Clone 外层: shallow A={SKU:A Stock:5 Tags:[浅拷贝标签] Meta:map[warehouse:浅拷贝仓]}（Stock 独立，Tags/Meta 仍共享）
Put/Get 双向复制: 后续读取 A={SKU:A Stock:5 Tags:[热卖] Meta:map[warehouse:深圳]}
E04 小窗口: 原数组=1048576 bytes window len/cap=4/4 shared=true 值="ZBCD"; copied independent=true 值="ABCD"
E04 生命周期: 工厂已返回，缓存 len/cap=4/1048576，GC后数组末尾哨兵=7
E04 生命周期修复: 缓存 len/cap=4/4 值="ABCD"，旧窗口引用已清除
有容量两次 append: base=[A B Y D] first=[A B Y] second=[A B Y]; 同一第0项=true
无空余容量两次 append: base=[A B C D] first=[A B X] second=[A B Y]; 同一第0项=false
nil slice: nil=true len/cap=0/0 JSON=null; 空 slice: nil=false len/cap=0/0 JSON=[]
make 后写入 0: A=(0,true) missing=(0,false)
循环后调用: := 声明=[A B C]; = 赋值=[C C C]; 循环体快照=[A B C]
&v: 各轮地址不同=true 元素地址相同=false; 改副本 Stock=100 原 Stock=5; 原 Tags=[副本仍共享标签]
独立变量捕获共享 *Product: [42 42 42]
共享外部 err: bad=[A:ok B:ok C:ok]; 每轮保存 currentErr: fixed=[A:ok B 缺货 C:ok]
range 长度与数据: seen=[1 9] iterations=2 final=[1 9 3]
name="库存A" bytes=7 runes=3; 错误 [:4]="库\xe5" bytes=e5 ba 93 e5 validUTF8=false; 修复前2码点="库存"
range string 的字节起点: [0:库 3:存 6:A]
M03 PASS case=all；全部同步步骤完成，无外部资源或后台任务
```

map 原始键顺序本次看到 `[A B C]`；这只是观察，代码和测试不要求它稳定或一定变化。每段输出对应的函数、状态、错误原因和修复解析已直接写在 README，没有需要学习者自行补出的练习。

故障入口实际输出与栈链：

```text
E05 故障前: nil map len=0；下一句尝试写 A=5
E05 故障清理: defer 已执行；没有外部资源需要关闭
panic: assignment to entry in nil map
... lesson.nilMapFailure → lesson.Run → main.main
exit status 2
```

## 覆盖、资料核对和未验范围

- 已完成 slice 传参、子窗口、len/cap、两次 append 的有/无空余容量分支、copy 数量/Clone 浅层边界、独占过滤尾部清理。
- 已完成过滤污染缓存、写入后继续修改、嵌套 slice/map 浅拷贝、Put/Get 双向复制，以及两业务分支各自独立快照。
- 已完成 1 MiB 数组与 cap=4 小窗口的真实别名观察；另让工厂返回小前缀、由缓存保留，再实际触发一次 GC 验证末尾哨兵仍可读；复制后替换缓存并清除旧窗口引用。不声称旧数组已回收或 RSS 下降。
- 已完成 nil/空 slice、JSON 差别、nil map 的安全操作/真实 panic 修复、两值查找、无序遍历的排序边界、不可取址 map 元素的源码说明与可运行读改写。
- 已完成 go1.27 `:=`/`=`、`&v`/`&slice[i]`、struct 副本的嵌套引用、外层变量、共享指针与共享 err，另以 go1.21 fixture 真实验证旧循环语义。
- 已完成 UTF-8 中文字节/码点、错误截断与修复、安全转换的修改隔离、空文本/0/大上限/坏字节/负数/国旗的边界断言。
- 本轮已读取编排/教学规范、完整 M03 prompt、原始三个 Go 示例及指定 files 材料；本地各级适用 AGENTS 文件检查到 `.codex/AGENTS.md` 为空，worktree/主项目无 AGENTS 文件。
- 已在线查证 Go 规范、Go1.22 发布与迁移说明、Go1.27.0 slices/maps Clone 合同、官方 Swiss Table 变更，并核对本机 `runtime/map.go`、`internal/runtime/maps.Map/table`、`runtime/slice.go`、slices/maps Clone 源码。链接及资料纠正在 README。
- 当前 race 已补验通过；未验范围仍为 M14 并发同步、生产缓存/数据库/Redis、GC/RSS 指标、unsafe 零拷贝、完整 UI 字素截断、完整 Go1.21 运行时兼容。这些不属于本课已通过的同步证明。

## 交付文件和资源结局

交付文件：`go.mod`、`cmd/demo/main.go`、`internal/lesson/{product,slices,cache,collections,loops,text,run,lesson_test}.go`、`versioned/go121/{go.mod,main.go}`、`README.md`、`STATUS.md`。

课程无端口、外部服务、凭据、数据库命名空间、后台进程或 goroutine；显式大数组最大 1 MiB。正常入口全部同步操作后返回，nil-map 故障入口有限步骤退出。局部引用和 clear 尾槽说明位于源码旁。未生成提交或残留二进制，不改共享配置、主目录和其他模块；只将本模块结果交给主 agent 进一步独立验收。
