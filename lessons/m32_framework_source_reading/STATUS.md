# M32 实际交付、自验与冻结记录

记录日期：2026-10-12（Asia/Shanghai）。状态：**READY_FOR_REVIEW**。已完成主审要求的R1脚本环境恢复修复与复核，课程目录再次冻结，等待主 agent 独立验收；本文件不自标 ACCEPTED，不替代整层集成、提交或主验收记录。

## 范围、前置与环境

- 唯一写入根：`C:/Users/Lenovo/.codex/worktrees/go-course-m32/go-demo/lessons/m32_framework_source_reading/`，worktree基线`003854afb2e9b848164922a4ca5eb835a584de38`。每次shell都明确本worktree或模块工作目录；没有依赖默认共享cwd。
- 完整读取M32 prompt、TEACHING_STYLE、ORCHESTRATION、INFRASTRUCTURE、根README/go.mod及主目录`files/开发框架`。worktree与逐级祖先没有非空适用AGENTS要求。使用已安装go-learning-by-running教学方法，范围以派工/完整prompt为准。
- M19实际读README/STATUS/业务接口；M31实际读README相关契约、STATUS、proto、service与生成脚本。保留显式装配/小接口/独立收尾及库存RPC语义；前置`internal`不能合法跨module导入，因此只做本课最小协议适配。未改/重跑整套前置、未复制其全部项目。
- 实际工具链`go version go1.27.0 windows/amd64`，GOTOOLCHAIN=auto，module为`go-demo/lessons/m32_framework_source_reading`，`go 1.27.0`，无额外toolchain/replace/go.work。当前实际env见[environment.txt](evidence/environment.txt)。
- 未切分支、未Git add/commit/push、未创建下级agent、未改根module/共享索引/其他课/examples/files。最终Git只出现本模块新目录。
- 未读取/操作共享服务凭据、未访问SQL/Redis/MQ、未安装/停止/重配任何服务/隧道。全部网络fixture为本进程自有动态loopback，库存为每Root独立进程内对象。

## 版本、源码与生成

| 项目 | 实际版本/证据 |
| --- | --- |
| Kratos | `github.com/go-kratos/kratos/v3 v3.0.0`，Go minimum 1.25.0，sum `h1:dCXqKoeo2Bo9jgC72YfuwJ3g7bPCkHeVeNGyZQ51bLE=` |
| gRPC / protobuf runtime | v1.84.0 / v1.36.12；实际module metadata见[versions.json](evidence/versions.json)，全部间接版本在go.mod/go.sum |
| protoc-gen-go / protoc-gen-go-grpc | v1.36.12 / v1.6.2；编译时Go1.27.0；gRPC插件自身依赖protobuf v1.36.11，不是本课runtime版本 |
| protoc-gen-go-http/v3 | 实际二进制module版本`v3.0.0-20260626125723-668db92c2c00`，生成头显示v3.0.0；build metadata见[generator-build.txt](evidence/generator-build.txt) |
| protoc | 官方release v36.2；`libprotoc 36.2`；生成头为v7.36.2；归档实际SHA256 `F0C128DC0D8492ECEECE83BB459A4C0E316764B929FFBF1AA416357FD644EDD3`，未声称独立官方签名 |
| 只读迁移框架 | Kitex v0.16.4、go-zero v1.10.3；已下载、阅读实际官方签名/lifecycle/治理源码，没有运行或生成其服务 |
| 实际源码核对 | [source-symbols.txt](evidence/source-symbols.txt)记录库sum、文件SHA256、符号/行号；[stdlib-contracts.txt](evidence/stdlib-contracts.txt)记录Shutdown/Cancel/GracefulStop/Stop官方本地文档 |

`scripts/generate.ps1 -Install`真实完成：所有protoc/插件只落本模块忽略的`.local/`；官方HTTP插件实际生成了HTTP注册/client源码。旧脚本临时GOBIN仅赋回保存值，没有证明存在性恢复；该缺口已在R1修复并按下方三态实测。proto沿M31方法/字段编号增加HTTP annotation与提交后panic故障模式。`-Check`把同一IDL用同一工具再写私有目录，比对3个生成文件SHA256，结果GENERATION_IDENTICAL。

实际生成进程由Invoke-Bounded拥有：读取stdout/stderr异步管道，WaitForExit有限180秒，超时Kill自有子树并Wait，finally Dispose；HTTP下载120秒。此次所有工具正常退出，无超时终止。保留私有工具/归档/再生成文件以支持复跑，已确认Git忽略；没有全局工具安装、PATH或go env修改。

## 实际业务与停止实验

最终`go run ./cmd/demo`退出0。完整日志[demo.txt](evidence/demo.txt)，最后一次运行时间2026-10-12约04:19:53～04:19:54 +08:00。耗时不是语义契约。每个listener实际创建`net.Listen("tcp","127.0.0.1:0")`，HTTP/gRPC server获得独占句柄和动态地址；不会使用已有服务端口。

| 实验 | 实际断言/结果 |
| --- | --- |
| E01生成协议/完整调用链 | 真实generated gRPC qty2：20→18；generated HTTP qty1：18→17；完整reply、request_id、tenant、单请求client/server/usecase/commit/exit顺序、fresh HTTP Inspect及仓储快照全部一致 |
| E02业务error | gRPC FailedPrecondition且typed Conflict requested99/available17；HTTP412/STOCK_INSUFFICIENT及metadata17；quantity0→InvalidArgument，未知SKU→NotFound，缺身份→Unauthenticated；实际执行后逐ID writes=0 |
| E02 panic | root明确安装官方Recovery，提交前gRPC Internal/HTTP500+UNKNOWN且writes0；提交后panic返回Internal但writes1，库存17→16；下一次真实Inspect继续可用 |
| E03丢ctx坏例 | 100msclient返回DeadlineExceeded、clientTask最终Done；business.Done仍false；owner随后释放Gate，真实晚提交qty2，writes1/库存14；日志确认tenant丢失 |
| E04修复取消/预算 | Started后主动cancel，gRPC Canceled、实际business.Done且writes0；真实服务器200ms预算分别得到gRPC DeadlineExceeded与HTTP504、writes0；fresh Inspect仍14 |
| E05响应丢失/重试 | owner先等真实Committed，qty2已写库存12；100msclientdeadline后原请求business.Done；第二次同ID NORMAL是显式应用重试，又写qty2，writes2/库存10；没有把request_id当自动幂等或把配置当自动retry实证 |
| E06原M31 bidi契约 | 真实Watch发送1条request、CloseSend，接收sequence1/2且available10，再EOF；2秒流预算、defer cancel、最终root账本join；未重跑M31兼容/背压整套 |
| 核心服务最终收尾 | client所有task先join；ClientConn.Close后状态SHUTDOWN；每HTTP响应Body由实际Invoke defer关闭；独占HTTP Transport idle池由clients关闭；App.Run/transport.Start Done已join、active0、Memory.closed |
| E07 Stop caller | raw HTTP完整200/body=tail已读/Body关闭，handler尾部defer仍等Gate；App.Stop返回nil写入Result，但Stop调用goroutine的最终Done仍false；独立caller门解除后才Join |
| E07真实HTTP强制关闭 | 80msShutdown预算后官方warn/Close；App.Run和两transport.Start完成，实际对两个自有TCP地址再建连均失败；handler.Done仍false、active1、repo仍未关闭 |
| E07修复 | root释放handler门，记录defer.released，Tail.Done+账本idle；最后仓储关闭。ASSERT caller.Done/handler.Done/active0/repository.closed/all-owned-tasks-joined全部通过 |

最终结构化摘要：stock10、lost_ctx_writes1、propagated_writes0、committed_and_retry_writes2、stream_messages2、handlers_joined=true、connections_closed=true。

README有中文最小规则、10～20分钟入口、每份文件的符号、真实HTTP/gRPC完整链路、ctx来源表、业务副作用、所有goroutine/Close owner、正常/error/panic/cancel/stop的时间线和实际日志逐段解析。手写Go中文注释解释责任/caller/值副本与共享指针/接口动态对象/等待/分支/错误/defer/最终Done；生成代码只定位相关符号和契约，没有批量手改。没有额外exercise。

## 真实失败、诊断与修复

初次运行退出1：真实监听日志存在，但15秒Ready观察deadline；见[demo-initial-failure.txt](evidence/demo-initial-failure.txt)。增加明确direct地址与此连接WithNoProxy后仍同样退出1，见[demo-health-auth-failure.txt](evidence/demo-health-auth-failure.txt)。因此未把第一次失败武断归因代理。

沿实际client默认health配置→server health注册→stream interceptor发现课程tenant认证误套框架health.Watch，导致connection永不Ready。修复为只对生成的Inventory_Watch_FullMethodName施加业务流认证与账本；health.Watch由框架health.Stop负责。修复后全部真实协议、库存与生命周期实验通过。保留两次失败，不把建连成功/HTTP200/静态配置当业务验收。

## 最终实际命令与证据

除首轮仓库只读勘察，下表命令workdir均为上述模块绝对目录。Go run/test/vet也可以从仓库根用README的`go -C lessons/m32_framework_source_reading ...`复跑。

| 命令 | 实际结果 | 证据 |
| --- | --- | --- |
| `gofmt -w`本模块全部手写/生成Go | 退出0；最终gofmt-l无输出 | 当前Go文件格式 |
| `go mod tidy` | 退出0；只写本模块go.mod/go.sum | 模块锁定文件 |
| `go run ./cmd/demo`（修复后最终） | 退出0；E01～E07及完整业务摘要通过 | [demo.txt](evidence/demo.txt) |
| `go test -v -count=1 -timeout 90s ./...`（最终） | 退出0；5项顶层测试全部实际PASS，internal/lab约1.832s | [test.txt](evidence/test.txt) |
| `./scripts/test-race.ps1` | 退出0；执行真实`go -C <module> test -race -v -count=1 -timeout 90s ./...`，5项PASS，无race诊断 | [race.txt](evidence/race.txt) |
| `go vet ./...`（最终） | 退出0，无诊断；空证据文件为真实无输出 | [vet.txt](evidence/vet.txt) |
| `go mod verify` | 退出0，all modules verified | [mod-verify.txt](evidence/mod-verify.txt) |
| `./scripts/generate.ps1 -Install` | 退出0，实际插件/归档安装到私有目录并生成3文件 | generator-build、生成头与脚本 |
| `./scripts/generate.ps1 -Check`（最终） | 退出0，3生成文件逐字节相同、所有工具Wait/Dispose | [generation-check.txt](evidence/generation-check.txt) |
| `./scripts/source-map.ps1` | 实际锁定版源码下载/符号/hash读取完成 | [source-symbols.txt](evidence/source-symbols.txt) |
| `git check-ignore .local/...` | 两个样本退出0，protoc归档与插件已忽略 | .gitignore及实际命令输出 |
| `& ./scripts/check-environment-restore.ps1`（R1） | 退出0；同进程3次generate -Check、3次race及6次预期Go失败，存在性+原值精确恢复，16运行文件hash保持 | [environment-restore-r1.txt](evidence/environment-restore-r1.txt) |

race只读`D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json`，实际WinLibs GCC16.2.0/x86_64-w64-mingw32。脚本设置当前PowerShell进程CC/CXX/CGO_ENABLED/PATH；旧版仅保存值的恢复缺口已在R1改为存在性+精确原值恢复。没有使用旧D:/MinGW默认32位compiler、没有go env -w或另装compiler。原冻结race internal/lab约2.992s；R1三次race约1.968s/2.033s/2.015s，均无数据竞争报告，不外推所有未运行分支都无race。

独立断言不仅调用默认入口：30个真实并发gRPC请求争20库存，恰好20成功/10缺货，余额0且成功remaining不重复；已取消总owner仍在独立预算关闭Root；未Start的两个真实listener精确回收；StartOwnedTask先Result只含主错误、tail解除后真正cleanup error通过errors.Join加入final，再Done/Join检查同时保留两原因。

## R1：主审发现、修复与环境三态实证

主审已经独立通过真实demo、全部5项race、vet与mod verify，并读完课程源码/生成入口/README/STATUS。主审指出Windows PowerShell 7.6.5把保存的`$null`赋回Env可能让原不存在变量变成存在的空串变量；原README/本STATUS的笼统“finally恢复”声明不充分。该反馈属于真实脚本收尾缺口，已保留在此记录，未改写原运行日志。

本次只改`generate.ps1`的GOBIN快照/恢复、`test-race.ps1`的CC/CXX/CGO_ENABLED/PATH快照/恢复和相关文档；新增可复跑检查脚本及R1证据。每个快照保存`Exists`与`Value`，finally原不存在使用`Remove-Item -LiteralPath Env:...`，原存在使用`Set-Item`精确恢复，包括空串。没有改Go、IDL、生成文件、go.mod/go.sum，没有修改主目录私有repair记录或验收状态。

实际执行`& ./scripts/check-environment-restore.ps1 2>&1 | Tee-Object -FilePath evidence/environment-restore-r1.txt`，同一PowerShell 7.6.5进程PID930320，退出0；日志中三轮真实race服务时间分别为04:37:49～50、04:37:54、04:37:58～59 +08:00。被测脚本由`&`调用；每项检查同时核对`Test-Path Env:`存在性与序数精确值，没有通过另开shell或只比值掩盖恢复缺口。

| 被测对象 | 原不存在ABSENT | 原存在空串EMPTY | 原存在非空NONEMPTY |
| --- | --- | --- | --- |
| generate -Check，GOBIN | 生成一致、存在性/原值恢复 | 生成一致、存在性/原值恢复 | 生成一致、存在性/原值恢复 |
| race，CC/CXX/CGO_ENABLED/PATH | 全部5项PASS，四变量恢复 | 全部5项PASS，四变量恢复 | 全部5项PASS，四变量恢复 |
| generate -Install -Check预期失败 | 临时GOBIN设置后真实Go退出1，finally恢复 | 同左，空串保留 | 同左，原非空值保留 |
| race预期失败 | 四变量设置后真实Go退出1，finally恢复 | 同左，四空串保留 | 同左，四原非空值保留 |

失败注入只设置检查脚本拥有的`GOTOOLCHAIN=m32-invalid`；实际Go打印`invalid GOTOOLCHAIN`，生成owner Wait/Dispose并保留“工具退出1”，race保留“race退出1”。没有替换工具、伪造业务PASS或故意修改课程代码。PATH不存在/空串时，检查函数使用自身局部alias定位入口已解析的真实`D:/Go/bin/go.exe`；被测脚本仍运行实际工具链和测试。检查脚本finally又核对入口六变量：CC/CXX/CGO_ENABLED/GOBIN/GOTOOLCHAIN恢复ABSENT，PATH恢复原NONEMPTY且精确值相等；日志只打印长度，未公开caller完整PATH。

R1前后16个运行/IDL/依赖文件逐SHA256相同，完整列表与哈希在R1日志；原demo/vet/mod verify等成功证据按主审要求复用，未重复无关检查。R1三次生成再现均3文件逐字节相同，三次race均5项PASS、无race诊断；最终摘要`ENV_CHECK_PASS generate_success=3 race_success=3 generate_expected_failure=3 race_expected_failure=3 runtime_files_identical=16`。此次新增动态loopback仅属于race真实fixture，均按原owner完成停止/join；没有常驻工具或服务、没有共享中间件副作用。修复不扩张原尚未证明范围，最终验收仍由主agent执行。

## 资源结局与尚未证明范围

本模块所有手写任务均启动即有Task/Root/scenario owner、停止方式和唯一最终Done。任何提前返回也cancel/Release屏障，再用Background新3/4秒预算join；正常实验不以缓冲Result代表defer完成。HTTP handler账本封账后等待实际idle，仓储在handler退出后关闭。所有工具进程Wait/Dispose，所有run/test进程正常退出，没有常驻实验服务、故障代理、固定端口或共享数据副作用；私有工具/归档作为可复跑材料保留且不提交。

未验证生产TLS/身份认证、外部注册发现、自动重试/缓存、真实SQL/事务/持久幂等、跨进程崩溃、跨实例一致性；Kitex/go-zero只有源码阅读，没有运行/生成实证。gRPC阻塞业务强制Stop分支只读了源码，实际Stop故障使用HTTP handler；不冒充验证所有库内部goroutine的独立完成。Kratos Merge与signal registration的私有生命周期限制已在README说明，不能据本课宣称长期反复多App生产资源保证。所有本课必需实验已实际通过，没有遗留关键未通过项，最终验收由主agent执行。
