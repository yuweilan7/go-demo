# M31 实际交付与验证记录

记录日期：2026-10-12（Asia/Shanghai）。本模块实现、生成、教学文档、真实协议实验及 gofmt/run/test/vet/race 全部实际通过，模块交付完成。主 agent 独立验收与整层提交在课程编排状态中另记，本文件不代替主 agent 验收。

## 所有权和实际执行环境

- 唯一写入目录：`C:/Users/Lenovo/.codex/worktrees/go-course-m31/go-demo/lessons/m31_grpc_protobuf/`。
- 下述命令除另注，工作目录均是上述模块目录。编排/教学/prompt/基础设施从 `D:/IdeaProject/go-demo/learning_prompts/` 只读；已有 context 示例读本 worktree；`files/开发框架` 只读主目录。
- `go version`：`go version go1.27.0 windows/amd64`。
- `go env GOVERSION GOOS GOARCH GOTOOLCHAIN CGO_ENABLED CC`：`go1.27.0 / windows / amd64 / auto / 1 / gcc`。
- 独立 module：`go-demo/lessons/m31_grpc_protobuf`，`go 1.27.0`，没有额外 toolchain directive，无前置模块 replace/import。
- 未创建子 agent，未执行 Git add/commit/push；根 go.mod、共享文件、其他课程目录及新资料未修改。

## 锁定版本与生成证据

| 项目 | 实际版本/事实 |
| --- | --- |
| gRPC-Go | v1.84.0；实时 `go list -m -json ...@latest` 后固定，Go minimum 1.25.0 |
| Protobuf-Go 与 protoc-gen-go | v1.36.12；Go minimum 1.23 |
| protoc-gen-go-grpc | v1.6.2；Go minimum 1.25.0；安装时工具本身解析的 protobuf 依赖 v1.36.11，不影响本课 runtime 固定为 v1.36.12 |
| protoc 官方包 | release v36.2 `protoc-36.2-win64.zip`；`libprotoc 36.2`；生成头 `protoc v7.36.2` |
| protoc 压缩包 SHA256 | `F0C128DC0D8492ECEECE83BB459A4C0E316764B929FFBF1AA416357FD644EDD3`（实际下载文件哈希，未声称这是独立官方签名） |
| 间接依赖 | x/net v0.57.0，x/sys v0.47.0，x/text v0.40.0，genproto/googleapis/rpc v0.0.0-20260706201446-f0a921348800 |

真实命令与结果：

| 命令 | 观察/真实结果 |
| --- | --- |
| `./scripts/generate.ps1 -Install` | exit 0；protoc 与两个 Go 插件只落在 `.local/`；打印 `libprotoc 36.2`、`protoc-gen-go.exe v1.36.12`、`protoc-gen-go-grpc 1.6.2`；生成四个 .go 文件 |
| `go mod tidy` | exit 0；模块内生成 go.sum 和实际间接依赖 |
| `gofmt -w`（本目录 cmd/internal/gen 所有 .go） | exit 0；没有修改第三方模块源码 |
| `gofmt -l`（最终所有手写和生成 .go） | exit 0，输出为空 |
| `./scripts/generate.ps1` 再生成 | exit 0；比较生成前后所有四个 .go 的 SHA256，输出 `GENERATION_IDENTICAL` |
| `./scripts/check-reserved.ps1` | 预期 protoc exit 1，`Field "reused" uses reserved number 3.`；脚本确认此故障后自身 exit 0，输出 `PASS reserved 编号复用被拒绝` |
| `git check-ignore .local/protoc-36.2-win64.zip .local/bin/protoc-gen-go.exe` | exit 0；两条路径都确认已忽略 |

下载地址是 `https://github.com/protocolbuffers/protobuf/releases/download/v36.2/protoc-36.2-win64.zip`。插件命令为 `go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12` 和 `go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2`，脚本临时指定本模块 `.local/bin` 为 GOBIN，finally 恢复 GOBIN/PATH；没有 `go env -w` 或全局工具覆盖。

## 真实协议与库存事实

`go run ./cmd/demo` 和最终 `go -C . run ./cmd/demo` 均已实际 exit 0；最终完整日志保存在忽略目录 `.local/demo-final.log`（2026-10-12 01:19:03 +08:00）。最终这次 listener 是 `127.0.0.1:4218`，由 OS 动态分配，仅本 run 使用，不作为固定配置。真实 `ClientConn` 构造后 IDLE，Connect 后观察 Ready，随后用生成 client 发起所有请求。

| 实验 | 真实状态/业务证明 | 结果 |
| --- | --- | --- |
| E00 | NewClient.state=IDLE，观察真实 Ready；constructor 成功不等于连通 | 通过 |
| E01 | 初始20扣2，真实 reply.remaining=18；header tenant/owner 与 trailer 校验；断言完整 client/server unary 链顺序及 handler tenant ctx | 通过 |
| E02 | 请求99，status FailedPrecondition，typed Conflict requested=99/available=18，writes=0 | 通过 |
| E02 失败分支 | metadata 缺失→Unauthenticated；quantity=0→InvalidArgument；提交前 panic→Internal；下一次真实 Inspect 库存仍18 | 通过 |
| E03-lost | 400ms deadline；client 已返回 DeadlineExceeded，owner 之后 release gate，真正发生 stock.commit；writes=1，fresh Inspect=16 | 通过 |
| E04-cancel | handler.started 后主动 cancel；client Canceled，handler cancel.before-commit；writes=0，fresh Inspect=16 | 通过 |
| E04-deadline | 正确传请求 ctx，400ms deadline，client DeadlineExceeded；writes=0，fresh Inspect=16 | 通过 |
| E05-committed | owner 等 committed 后等400ms deadline；client DeadlineExceeded，原请求 writes=1，fresh Inspect=14 | 通过 |
| E05 坏重试 | 实验显式发送第二次同 ID NORMAL RPC；writes=2，库存14→12；没有把 request_id 当自动幂等 | 通过 |
| E06-half-close | client CloseSend 后仍收到 seq1/2、库存12；最终 Recv EOF，server.done，被测流 metadata正确 | 通过 |
| E06-early | client 收到第一条后 cancel；读到终止 Canceled，观察 server.done，后续继续复用 Conn | 通过 |
| E06-backpressure | client 未 Recv，64KiB 静态窗口、256KiB消息；本次 Send(2) 150ms未返回；cancel 后 Send error、server.done、客户端终止status | 通过 |
| E07 | V2→V1→V2 unknown保真（12bytes）；scalar 缺失/0同wire，optional/oneofpresence不同；旧字段reserved后保留unknown；V1→V2解码成功但业务拒绝缺currency | 通过 |
| E99 | ClientConn.Close后Shutdown，GracefulStop完成，Server.Serve实际返回、本run listener结束，CLI打印 connection_released=true | 通过 |

实际最终摘要：

```text
PASS stock=12 lost_ctx_writes=1 propagated_writes=0 committed_and_retry_writes=2 stream_messages=2 blocked_send=2
[E99] client.Shutdown; server.Serve.returned; listener.closed
[E99] ASSERT connection_released=true final_stock=12
```

## 独立检查与 race 环境诊断

| 命令 | 真实结果 |
| --- | --- |
| `go test ./...`（最后一次） | exit 0；internal/lesson `ok ... 2.345s`；其他包 no test files；执行真实 TCP/RPC 主实验、已取消 owner 的真实资源收尾、另一个 oneof/金额消息的未知字段保真与 DiscardUnknown 丢失反例 |
| `go vet ./...`（最后一次） | exit 0，无诊断 |
| `go test -race ./...`（默认 compiler，第一次） | exit 1，runtime/cgo `cc1.exe: sorry, unimplemented: 64-bit mode not compiled in`；默认 `D:/MinGW/bin/gcc.exe` 是 `MinGW.org GCC-8.2.0-3`，`gcc -dumpmachine` 为 mingw32 |
| `./scripts/test-race.ps1 -CompilerPath <下述gcc> -CxxPath <下述g++> -BinDirectory <下述bin>` | exit 0；实际执行 `go -C <模块绝对路径> test -race -count=1 -timeout 60s ./...`，internal/lesson `ok ... 5.883s`；无 race 报告，全部三个测试实际执行 |

race 的初次结果是编译器架构不兼容，没有产生可执行的 race 测试，保留为已解决的环境诊断。随后读取主 agent 已验证的 `D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json`，使用现有统一 WinLibs 工具链：

```text
bin_dir=D:/IdeaProject/go-demo/.local/toolchains/winlibs-16.2.0posix-14.0.0-ucrt-r2/mingw64/bin
CC=<bin_dir>/gcc.exe
CXX=<bin_dir>/g++.exe
gcc.exe (MinGW-W64 x86_64-ucrt-posix-seh, built by Brecht Sanders, r2) 16.2.0
gcc -dumpmachine: x86_64-w64-mingw32
```

实际复验命令：

```powershell
$courseTools = Get-Content -LiteralPath 'D:\IdeaProject\go-demo\.local\course-orchestrator\toolchain.json' -Raw | ConvertFrom-Json
./scripts/test-race.ps1 -CompilerPath $courseTools.cc -CxxPath $courseTools.cxx -BinDirectory $courseTools.bin_dir
```

完整复验日志是本模块 `.local/race.log`。脚本仅按参数临时设置当前 PowerShell 的 CC/CXX/CGO_ENABLED、PATH（前置 bin_dir），finally 恢复。没有 `go env -w`，M31 未自行安装另一套 compiler。最终已实际覆盖三个测试，不把初次编译失败说成业务代码失败或数据竞争结果。

## 有界和清理

课程 ctx 总上限12秒；请求 deadline400ms；丢ctx反例独立保险2秒；正常/早退流2秒；背压流3秒、最多64条、单条padding最多256KiB；优雅停止等待2秒后必要时Stop。屏障由明确 owner 关闭，不用 Sleep 猜启动；Send 背压计时从实际 attempts 信号开始。取消通知、业务收尾信号、client调用完成信号、框架停服完成分别观察，README 明确它们的范围。

实验结束没有常驻课程进程、固定监听端口、远端服务安装或共享数据修改。只保留课程文件，以及忽略目录中的生成工具/压缩包/诊断日志与错误proto。`.local/` 不进入课程提交。

## 当前范围与未纳入实证

交付中文README、全部手写Go中文注释、独立go.mod/go.sum、proto与真实生成代码、默认cmd/demo、独立断言、生成/保留编号/race脚本。未验证TLS/生产鉴权、远程服务发现/负载均衡、跨进程崩溃、数据库持久化/事务/持久幂等；这些边界在README直接解释，不用本课内存结果外推生产保证。本模块必须实验、生成和最终 race 均已实际通过，没有遗留关键未通过项。
