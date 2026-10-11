# M05 实际交付与验证记录

记录时间：2026-10-12，Asia/Shanghai。本次最终普通验证时间约 01:15:55 +08:00；同日补跑 portable amd64 工具链的竞态检测。

当前状态：课程源码、中文 README、完整正常/错误/修复解析、普通实验与竞态检测全部完成，可交主 agent 审查；run/test/vet/race 均通过。最初 mingw32 无法构建 amd64 runtime/cgo 的环境问题已通过主 agent 统一提供的 portable amd64 编译器解决。未标主 agent ACCEPTED，未提交、未推送；交付已冻结。

## 路径与工具链

唯一源码写入目录及实际工作目录：

```text
C:\Users\Lenovo\.codex\worktrees\go-course-m05\go-demo\lessons\m05_errors_contracts
```

只读核对了主目录的 ORCHESTRATION、TEACHING_STYLE、完整 M05 prompt、`files/基础语法/go语言error`，以及本 worktree 的根 README/go.mod 和 `examples/basic-syntax/errors/main.go`。祖先 `C:\Users\Lenovo\.codex\AGENTS.md` 存在但为空，未找到模块内额外 AGENTS。没有改主目录、原 examples/files、其他 module、共享 go.work、根 go.mod/go.sum。

实际环境：

| 项 | 真实值 |
| --- | --- |
| `go version` | `go version go1.27.0 windows/amd64` |
| `go env GOVERSION` | `go1.27.0` |
| `GOTOOLCHAIN` | `auto` |
| `GOOS / GOARCH` | `windows / amd64` |
| `GOMOD` | 当前目录的独立 `go.mod` |
| `GOWORK` | 空，未启用共享 workspace |
| `CGO_ENABLED` | `1` |
| 初始 `CC` | `gcc`，解析为 `D:\MinGW\bin\gcc.exe` |
| 初始 `gcc -dumpmachine` | `mingw32` |
| race 补验 `gcc -dumpmachine` | `x86_64-w64-mingw32` |
| module | `go-demo/lessons/m05_errors_contracts` |
| go / toolchain 指令 | `go 1.27.0` / 无 toolchain 指令 |
| 第三方依赖 | 无，仅标准库，没有 go.sum |

race 使用的真实编译器路径：

```text
CC=D:\IdeaProject\go-demo\.local\toolchains\winlibs-16.2.0posix-14.0.0-ucrt-r2\mingw64\bin\gcc.exe
CXX=D:\IdeaProject\go-demo\.local\toolchains\winlibs-16.2.0posix-14.0.0-ucrt-r2\mingw64\bin\g++.exe
```

路径只读加载主 agent 的 `D:\IdeaProject\go-demo\.local\course-orchestrator\toolchain.json`，只在验证命令的 PowerShell 进程中设置 CC/CXX、CGO_ENABLED=1 和 PATH 前缀；没有修改全局环境、下载或另行安装。标准库 Go 版本仍为 1.27.0。

核对本地 `go doc errors.AsType/As/Join/Unwrap`、`io.Reader/Writer`、`bufio.Writer.Flush`、`fmt.Errorf`，及 Go 1.27.0 的官方版本文档、Go 1.13 包装说明、Go 1.27 Vet 发布说明。As/AsType 两种 API 均在当前模块实际编译运行；AsType Go 1.26 起可用。Go 1.27 printf analyzer 的相关诊断也核对了本地标准工具源码。

## 命令与结果

以下命令均在上面的绝对模块目录执行，不依赖主目录未落盘的课程。

| 实际命令 | 观察点 | 真实结果 |
| --- | --- | --- |
| `gofmt -w .\cmd\demo\main.go .\internal\course\errors.go .\internal\course\policy.go .\internal\course\io.go .\internal\course\export.go .\internal\course\unknown.go .\internal\course\labs.go .\internal\course\course_test.go` | 所有手写 Go 文件格式化 | 完成，exit 0 |
| `go -C . run ./cmd/demo` | core、io、cleanup、unknown、bad 全部执行，正常结束 | exit 0，最终输出 `M05 PASS` |
| `go -C . test -count=1 -timeout=20s -v ./...` | 独立 caller 包的语义与真实副作用断言 | exit 0，12 个 Test 函数、10 个表格子案例通过；最终 package 结果约 `0.974s` |
| `go -C . vet ./...` | 静态契约检查 | exit 0，无诊断 |
| `gofmt -l` 加上述全部 Go 文件 | 未格式化文件列表 | exit 0，无输出 |
| 初次 `go -C . test -race -count=1 -timeout=30s ./...` | 原始 mingw32 环境 | exit 1，历史工具链限制，已解决，原始错误见下文 |
| 补验 `go -C . test -race -count=1 -timeout=40s ./...` | amd64 编译器下的 race instrumentation，含真实 HTTP 的共享状态 | exit 0，`cmd/demo [no test files]`、`internal/course 2.136s` 通过，无竞态报告 |

所有实际 run 都有最终状态断言；没有把日志 PASS 文案、HTTP 200 或编译成功当成业务内容证明。普通测试 timeout 20 秒；最终 race timeout 40 秒；网络实验 main 总上下文 5 秒、局部 3 秒、单客户端 2 秒；Reader 最多 4 次调用且本课每次 Read 立即返回；没有无上限阻塞、sleep 协调或业务重试循环。

## 关键实测证据

```text
same_text=true same_error=false sentinel_match=true
direct_assert=false As=true AsType=true same_pointer=true
good_dependency=true lost_dependency=false Unwrap_type=*course.DatabaseError
actual=*ValueCodeError search_T=false search_pointer=true code=V05
actual=ValueCodeError search_T=true search_pointer=false
bad_type=*course.RuleError bad_equal_nil=false AsType_ok=true matched_pointer_nil=true fixed_equal_nil=true
scenario=before-write ... kind=dependency ... next=may-retry
scenario=bad-schema ... kind=dependency ... next=do-not-retry
scenario=lost-commit-ack ... kind=outcome-unknown ... next=reconcile
scenario=canceled preserved=true kind=canceled next=do-not-retry
scenario=deadline preserved=true kind=deadline next=do-not-retry
bad="" bad_EOF=true fixed="ORDER-05\n" fixed_error=<nil>
partial="ORDER-" preserved_unexpected_EOF=true
file_content="ORDER-05\n" bytes=9
stage=write cause_preserved=true commit_calls=0 close_calls=1 committed=false
stage=flush cause_preserved=true commit_calls=0 close_calls=1 committed=false
stage=commit cause_preserved=true commit_calls=1 close_calls=1 committed=false
stage=close cause_preserved=true commit_calls=1 close_calls=1 committed=true
commit_preserved=true close_preserved=true tree=true errors.Unwrap(join)==nil=true
bad_commit=false bad_close=true fixed_commit=true fixed_close=true
```

真实 loopback HTTP 观察：

```text
server: POST #1 stored ORDER-05 before response
client: POST failed; outcome unknown; no POST replay
server: GET request_id found ORDER-05
first_error_has_url_cause=true first_error_has_EOF=true before_query_next=reconcile
after_query confirmed=true order=ORDER-05 stored=1 POST=1 GET=1
```

独立测试除了错误类别，还验证了实际返回的原始 `*url.Error` 和 EOF、同一 request_id 的订单内容、服务端一笔订单、恰好一次 POST 和一次 GET。文件测试实际读取 9 字节内容，并对已转移关闭责任的旧句柄 Write，匹配 `os.ErrClosed`。

## 曾遇到的真实失败与修复

初次 `go test` 的默认 vet 与显式 `go vet` 检出了：

```text
%w wants operand of error type ValueCodeError, not pointer type *ValueCodeError (defeats errors.Is)
(*testing.common).Fatal call has possible Printf formatting directive %v
```

第一项是 Go 1.27 新检查：ValueCodeError 用值接收者，值本身已经实现 error，却直接把指针交给 `%w`。最终 T/*T 教学对照通过显式 OperationError.Cause 分别保存两种动态形态，实际生产式类型均采用指针接收者，仍完整复现两种匹配差异。第二项是测试 Fatal 中包含 `%v` 字样，已改为无误导格式占位符的文本。未关闭 vet、未使用 `-vet=off`，最终 test/vet 均通过。

初始 race 原始错误：

```text
# runtime/cgo
cc1.exe: sorry, unimplemented: 64-bit mode not compiled in
FAIL go-demo/lessons/m05_errors_contracts/cmd/demo [build failed]
FAIL go-demo/lessons/m05_errors_contracts/internal/course [build failed]
```

这是初始 PATH 的 mingw32 C 编译器与 windows/amd64 Go 的不匹配，不是代码竞态报告。主 agent 后来提供已忽略目录中的 portable amd64 编译器，本模块在当前进程加载后补跑 race 实际通过。本模块未自行安装工具链，也未改全局环境。race 的真实结果已独立记录，没有用普通测试替代。

## 资源结局与证据范围

- 文件实验只在本模块 `.local/m05-receipt-*` 创建专属目录，退出通过带错误反馈的清理删除。`.local/` 已忽略；最后目录枚举为空，没有残留回执。测试在当前包内创建 `.m05-test-*`，由 t.Cleanup 删除。
- HTTP 使用系统分配的 loopback 端口和本次进程的 `REQ-M05-001` 内存 ledger。每个请求 Body 有明确关闭者，Hijack 的 TCP 连接由 handler Close，业务工作段通知被 owner 接收，server.Close/transport.CloseIdleConnections/cancel 在实验返回前执行。无后台进程、远端服务变更或保留故障代理。
- DatabaseError 是合成类型，FaultSink 是阶段故障替身；没有实库 SQL、数据库 Commit、RPC server 或 broker 验收。实际 HTTP 与真实文件系统行为是本课的扩展实证。
- 未验证数据库持久化、磁盘断电、支付网关、跨进程 exactly-once、消息 ack 和 M33 完整请求重试。上述内容不属于本模块交付范围。
- 修改仅在分配的 `lessons/m05_errors_contracts/`；没有 git add/commit/push。根 worktree `git status --short` 只显示新建 lessons 目录，主 agent 按整层规则验收与汇总。

## 主 agent 验收重点

1. 沿 CreateOrder → persistOrder → Classify 读源码旁中文注释，确认 error 对象、字段、动态形态、caller 与返回位置讲清。
2. 检查 As/AsType 都真实编译，T/*T 对照未把 vet 的版本行为错误讲成语言禁止，typed nil 仍展示 AsType ok=true 与 nil 指针。
3. 检查 I/O 返回有效字节与 error 的独立契约，以及 Write/Flush/Commit/Close 故障全部反馈、Join 不覆盖主失败。
4. 检查三类下一步有独立断言，HTTP 未重放 POST，查询以 request_id 和业务内容核实，取消不被误解为副作用自动回滚。
5. 核对共享 amd64 C 编译器的补跑命令、实际 exit 0 与资源清理范围；主 agent 据源码与独立复验决定 ACCEPTED。
