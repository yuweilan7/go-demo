# M18 实际交付、自验与冻结记录

日期：2026-10-12，Asia/Shanghai。最终实现验证完成于 `02:45 +08:00`；本报告随后冻结。

当前状态：**本模块内容与本机自验完成，冻结等待主agent验收。** 十项真实demo、14项顶层独立测试与输入/取消子测试、vet、race均通过。本记录不自标主验收通过，不替代主agent整层复验与提交。

## 写入范围、前置与实际版本

- 唯一课程写入目录：`C:/Users/Lenovo/.codex/worktrees/go-course-m18/go-demo/lessons/m18_gin_middleware/`。全部新源码、README、STATUS、锁文件与无秘密evidence都在其中。
- worktree起点：`90f7381b495c08f4995dd13400fbc34fca69e8c0`，包含主agent第0层已验收成果；未切分支、未commit/push、未创建下级agent。
- 完整读取本模块prompt、`learning_prompts/ORCHESTRATION.md`、`TEACHING_STYLE.md`、`modules.json`、INFRASTRUCTURE与根README/go.mod；本路径及主仓库没有发现适用AGENTS.md。按已安装go-learning-by-running的中文教学方法交付。
- M17硬前置实际目录：`lessons/m17_net_http`；已读STATUS、README、实际fixture/client/owned源码与直接订单断言。使用其订单JSON/Envelope、教学身份、同key返回相同ID/8并发只写1行、非法输入无副作用和取消非完成契约。本模块独立重验对应Gin组合，未重跑M17全部TLS/连接/长流实验。
- 前课实现处于`internal/lab`，跨module不能合法导入；本课使用直接协议契约+最小本课订单适配，没有复制整套M17、replace、循环导入、go.work或根module修改。
- 新材料`D:/IdeaProject/go-demo/files/开发框架`只读，未复制或提交原材料。它提供Gin方向与中间件打印建议，课程扩展为协议、副作用和生命周期实证，未据其性能比较方向声称做过benchmark。
- 实际工具链：`go version go1.27.0 windows/amd64`；GOROOT=`D:/Go`，GOPATH=`C:/Users/Lenovo/go`，GOOS=windows，GOARCH=amd64，GOWORK为空。
- 本课module：`go-demo/lessons/m18_gin_middleware`，`go 1.27`，无toolchain指令；Gin锁定`github.com/gin-gonic/gin v1.12.0`，下载go.mod要求`go 1.25.0`。全部间接依赖锁在go.mod/go.sum，未使用浮动latest。
- Gin实际源码根：`C:/Users/Lenovo/go/pkg/mod/github.com/gin-gonic/gin@v1.12.0`。Gin Sum=`h1:b3YAbrZtnf8N//yjKeU2+MQsh2mY5htkZidOM7O0wG8=`，GoModSum=`h1:VxccKfsSllpKshkBWgVgRniFFAzFb9csfngsqANjnLc=`；对应实际环境记录见[environment.txt](evidence/environment.txt)。
- 默认CC=gcc，本次只读确认为`D:/MinGW/bin/gcc.exe`、MinGW.org GCC8.2.0。M17已有默认32位compiler不能构建amd64 race的失败证据；本课没有再用默认compiler制造重复失败。
- race只读`D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json`，使用已登记WinLibs `16.2.0posix-14.0.0-ucrt-r2`、GCC16.2.0、mingw-w64 14.0.0、amd64 compiler。仅当前验证进程设置CC/CXX/CGO_ENABLED/PATH，finally恢复；未全局安装、重复下载、修改go env或系统PATH。归档SHA由主agent的统一工具链记录负责，本课不声称再次校验归档。
- 官方资料于本次实际访问：Gin现行绑定/body/goroutine/可信代理/context文档、Gin源码tag、Go1.27发布说明、net/http与context；本机`go doc`确认Request.Context与CancelFunc契约。旧examples文档URL返回404后跟随官方目录定位现行路径，README使用已确认的链接。

## 实际命令、工作目录与结果

所有shell的workdir明确为`C:/Users/Lenovo/.codex/worktrees/go-course-m18/go-demo`；`go -C lessons/m18_gin_middleware`将Go进程工作目录切入本独立module。下表不是计划，而是本轮实际执行事实。

| 命令/过程 | 观察点与真实结果 | 证据 |
| --- | --- | --- |
| `go -C lessons/m18_gin_middleware mod download -json github.com/gin-gonic/gin@v1.12.0`，然后`go ... mod tidy` | exit0；实际版本/path/sum已读取；依赖成功下载，go.mod/go.sum仅写本课 | [environment.txt](evidence/environment.txt)、锁文件 |
| `gofmt -w`本课所有实际`.go`文件路径（PowerShell递归列出后逐参数传入） | 成功；最后`gofmt -l lessons/m18_gin_middleware/cmd lessons/m18_gin_middleware/internal`无输出 | 已格式化源码 |
| `go -C lessons/m18_gin_middleware run ./cmd/demo` | 最终exit0；10项全部PASS；当前HTTP1.1端口动态，最终工具墙钟约6.728s（包含故障binary构建），不是语义承诺 | [demo.txt](evidence/demo.txt) |
| `go -C lessons/m18_gin_middleware test ./... -count=1 -v -timeout 120s` | 最终exit0；14项顶层测试、10个输入子测试及4个取消模式子测试全部通过；lab4.147s | [test.txt](evidence/test.txt) |
| `go -C lessons/m18_gin_middleware vet ./...` | 最终exit0，无诊断；证据文件为空正是实际无输出 | [vet.txt](evidence/vet.txt) |
| `go -C lessons/m18_gin_middleware test -race ./... -count=1 -v -timeout 120s`，进程级WinLibs CC/CXX/CGO/PATH | 最终exit0，lab4.260s，无race诊断；环境finally恢复 | [race.txt](evidence/race.txt) |
| `go doc net/http.Request.Context`、`go doc context.CancelFunc`与实际Gin源码读取/rg符号定位 | 确认入站handler返回取消、出站ctx控制取消、cancel不等待退出；README记录确切版本/文件/符号/行号 | [source-symbols.txt](evidence/source-symbols.txt) |
| 最后`git status --porcelain=v1 --untracked-files=all`、gofmt-l、自有.local检查 | 只有本模块新文件；gofmt无输出；临时fault目录已删除、.local为空 | 当前worktree文件范围 |

开发中第一次编译发现`internal/lab/experiments.go: undefined: contains`，已改为`strings.Contains`。首次demo/14项test/vet/race通过后新增直接Gin-context取消反例与缓存严格契约检查，所以最终确实重新跑了受影响完整模块命令。上表和evidence都是最终实现的实际结果，没有把此前PASS当新增路径验收。主agent若要求修复，将另记录返工版本；当前实现停止写入等待主审。

## 必须内容与真实业务/清理事实

| 课程项 | 已运行事实与直接断言 |
| --- | --- |
| E01路由组/Next/M17协议 | A.before→B.before→H→B.after→A.after；两个201/OK/committed=true且ID=1，账本1行；同key不同qty409/CONFLICT无新增；独立8并发同key仍1行 |
| E02Abort漏return | 坏例子401却追加第二JSON且写1行；后续H不执行而当前函数继续；修复401单JSON、0新增 |
| E03Bind/ShouldBind/业务校验 | Bind坏例子400/text/plain、两个JSON且1新增；ShouldBind修复400/INPUT单JSON无新增；qty=-1可通过required，坏例子201写入，业务修复422无新增 |
| E04重复读body | 第一次middleware消费后第二次绑定EOF，400/BODY_ALREADY_READ、rows0；有界缓存修复201/OK/ID1、rows1；独立缓存拒绝未知字段/尾随JSON/257字节/空key |
| E05Context/Copy/owner | Copy.Request同指针、底层writer=nil、Keys指针value浅共享、默认Gin.Done=nil；request派生任务Canceled/effects0，独立Rig owner任务响应后effect1；result=nil时finished=false且owner_returned=false，放开cleanup才join |
| Context实际池观察 | 16次顺序真实请求交替Alice/Bob；最终demo观察6次地址复用，身份每次正确重设；不读取已归还对象、不保证复用次数、不用NumGoroutine猜清理 |
| E06Recovery与提交 | 同请求panic前500/PANIC且rows0；先提交再panic仍500/false但rows1，直接证明Recovery不回滚；worker自身入口recover→ErrWorkerPanic，owner join，active0 |
| E07认证/归属/日志 | Bob伪造X-User=alice仍403/FORBIDDEN；未可信代理时伪造XFF不改变127.0.0.1；日志是服务器ID+路由模板等allowlist；token/Cookie/query/自报X-Run和伪造IP没有进入Audit |
| E08真实依赖取消 | detached客户端Canceled、dependency_canceled=false，rows0→1；直接默认Gin-context依赖未观察取消，提交检查使rows1→1；forward依赖真实观察取消、rows1→1；150ms依赖预算返回504/DEPENDENCY_TIMEOUT/false、rows1→1 |
| E09可信代理 | 单独engine显式信任127.0.0.1模拟代理；XFF链解析203.0.113.9、身份仍alice；文档明确实际代理部署/入口控制未验 |
| E10Recovery边界故障 | goroutine-panic和copy-write两个真实HTTP子进程都exit2且panic来源标记符合；每个8秒预算、实际wait完成，非超时；临时binary目录已移除 |
| 实际取消收尾独立测试 | 工作ctx取消+真实HTTP Result=Canceled可读时附加cleanup仍关闭：calls1/bodies0/Finished未关闭；owner进入独立join仍不返回；release后calls0/owner完成 |
| 观察早失败与错误保留 | 已取消observer不能代表真实请求完成；提前观察失败仍cancel/Open/新预算join；主错误与cleanup错误errors.Join同时保留 |

中文README给入口到真实`Ledger.Commit.append`/依赖HTTP的完整符号调用链、Next图、正常/拒绝/panic/取消/cleanup时间线；手写入口、业务函数、并发、故障、清理和关键断言都配具体中文注释。直接交付反例与修复，没有未完成exercise。

## 资源最终状态与未验证范围

每个普通demo Rig最终实际输出`bodies=0 calls=0 jobs=0 app_live=0 dependency_live=0 app_handlers=0 dependency_handlers=0 Serve=joined`。计数分别表示本课Body/OwnedCall/后台Job所有权和真实ConnState/handler，不等于全进程goroutine统计。清理失败会errors.Join返回使整项失败；不会在budget耗尽时伪装joined。

异步创建点立即defer/t.Cleanup，顺序为cancel→解除必要gate→独立Background派生3秒budget等Finished；Result只消费一次、Finished可重复等待。Rig先cancel root/Open，再Shutdown应用保证不会再注册Job，随后重新快照全部后台任务、Open/Join、关闭依赖空闲池、Shutdown依赖并join两份Serve；listener由各自server关闭。故障子进程同步wait，不产生长期进程；`.local/fault-*`目标归属在递归删除前核对，最终.local为空。只管理本课创建的对象/端口，没有杀其他任务进程、改全局工具链/网络或动外部基础设施。

未验证：生产负载、公网网络故障、HTTP2/3、持久数据库/事务、进程重启和跨实例幂等、外部身份提供方、反向代理实际部署、跨进程可靠后台任务、流式response中途panic、完整依赖error/重试/熔断策略。真实依赖为本地独立HTTP服务，账本为本进程真实内存副作用；没有用这些替代数据库/MQ。race覆盖当前父进程内实际路径；Faults创建的故障binary用普通go build，未声称该子进程带race。ctx检查与提交非原子，实验只严格证明已观察取消的屏障窗口内无额外写入，不承诺任意线上Canceled都等于订单未创建。

本模块没有主验收标志，没有git提交或推送；整层验收与一次提交由主agent负责。
