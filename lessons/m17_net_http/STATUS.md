# M17 实际交付与验证状态

执行日期：2026-10-12，Asia/Shanghai。首版正常验证于01:21完成；主验收要求的owned request失败路径返工验证于 `02:08 +08:00` 完成。

当前状态：**本次返工已完成并冻结；cancel/unblock/独立join、E14确定性清理实验、最新14项demo/14项测试/vet/race全部通过。** 首版正常路径PASS不能代替失败路径验证；原始默认32位工具链失败与首版日志保留，不代替主agent的最终验收。

## 范围与版本

- 唯一写入目录：`C:/Users/Lenovo/.codex/worktrees/go-course-m17/go-demo/lessons/m17_net_http/`。
- 独立module：`go-demo/lessons/m17_net_http`；`go 1.27`；无第三方依赖、go.sum或共享go.work修改。
- `go version`：`go version go1.27.0 windows/amd64`。
- `go env`：GOOS=windows，GOARCH=amd64，GOROOT=`D:/Go`，GOPATH=`C:/Users/Lenovo/go`，CGO_ENABLED=1，CC=gcc。
- 默认gcc：`D:/MinGW/bin/gcc.exe`，`MinGW.org GCC-8.2.0-3`，只支持32位；默认race失败不是源码测试失败。
- 实际race compiler：课程私有 `WinLibs 16.2.0posix-14.0.0-ucrt-r2`，GCC16.2.0、mingw-w64 14.0.0，`x86_64-ucrt-posix-seh`。只读主agent的 `.local/course-orchestrator/toolchain.json`，cc/cxx/bin_dir只用于本次进程CC/CXX/PATH，CGO_ENABLED=1；未安装或修改任何全局设置。
- compiler索引的归档SHA256：`d5dbafc4a170e762ca6143151ec918fb9e2c72736fb14cd704abebc6bdd5276a`，下载/散列由主agent统一核验；本模块没有重复下载或声称自己重新校验归档。
- 服务/数据：本轮动态loopback listener，HTTP/1.1；TLS实验自己的httptest证书与raw TCP peer；订单账本为独占当前进程内存，SKU=`sku-m17`，无外部/生产服务与数据。
- 只读资料：主目录的ORCHESTRATION、TEACHING_STYLE、M17完整prompt、INFRASTRUCTURE、`files/开发框架`；worktree的根README/go.mod与context/defer示例。未发现适用AGENTS.md。
- 核验官方资料：2026-10-12打开net/http与httptest官方文档，并用当前Go1.27.0的go doc与本地标准库源码确认API和有限自动drain实现。

## 实际命令与结果

所有命令显式使用本模块workdir。下面的绝对 `go -C` 路径可以替换成仓库根的 `go -C lessons/m17_net_http`。

| 命令 | 真实结果 | 证据 |
| --- | --- | --- |
| `gofmt -w ./cmd/demo/main.go ./internal/lab/*.go`（实际逐个文件参数执行） | 完成；最终 `gofmt -l ./cmd ./internal` 无输出 | 已格式化源码 |
| `go -C C:/Users/Lenovo/.codex/worktrees/go-course-m17/go-demo/lessons/m17_net_http run ./cmd/demo` | 本次exit=0，14个真实网络实验全部PASS，包括result先到/finished未到的实际取消清理屏障 | [demo.txt](evidence/demo.txt) |
| 同目录 `go test ./... -count=1 -v -timeout 60s`（实际绝对go -C） | 最新exit=0，14项顶层测试+取消子测试全部通过，lab3.221s | [test.txt](evidence/test.txt) |
| 同目录 `go vet ./...`（实际也用绝对go -C复验） | exit=0，无诊断 | [vet.txt](evidence/vet.txt) |
| 同目录 `go test -race ./... -count=1`，默认CC=gcc | exit=1，runtime/cgo编译失败：`cc1.exe: sorry, unimplemented: 64-bit mode not compiled in` | [race-default.txt](evidence/race-default.txt) |
| 同目录 `go test -race -count=1 -timeout 60s ./...`（实际绝对go -C），WinLibs amd64，进程级CC/CXX/CGO/PATH | 本次返工后最新exit=0，lab4.371s，无race诊断；首版7.124s日志另存 | [race.txt](evidence/race.txt) |

## 正常/故障/修复的实证

| 实验 | 已运行的事实 |
| --- | --- |
| E01复用 | conn IDs=1,1，第二次GotConn.Reused=true，实际connect/首响应字节可见 |
| E02泄漏 | open_bodies=1；CloseIdle不修复；第二请求deadline且未到handler；修复有界读取1048576字节到EOF、Close，第三请求复用同连接 |
| E03错误层 | 404/坏JSON/200业务拒绝/200未提交/1025字节超1024/200后UnexpectedEOF/晚改500仍200/修复先写409，所有body owner回0 |
| E04重复/输入/丢响应 | 危险重复2行；正常同key同orderID=3,3，修复只新增1行；大body413、尾随JSON400、无auth401均无新增；复用连接POST提交后断开，client EOF、handler增量1、账本3->4，禁止GetBody回放 |
| E05取消/副作用 | client=Canceled，server观察ctx，再release；危险rows0->1；协作rows1->1，全部handler Done被等待 |
| E06响应头预算 | handler entered但未写headers，ResponseHeaderTimeout=150ms；真实timeout awaiting response headers；server观察取消后退出 |
| E07整体预算含body | Do先返回200/err=nil；body读取0字节后Client.Timeout=180ms打断，errors.Is DeadlineExceeded成立 |
| E08ctx含body | Do先返回200，owner cancel，Body.Read=context canceled，handler退出 |
| E09TCP/TLS | 真实TCP/TLS成功并校验本次证书；关闭端口真实dial拒绝；raw peer收到ClientHello首字节后不答，TLSHandshakeTimeout=150ms、无HTTP首字节 |
| E10server读预算 | 不完整header在250ms预算后EOF且handler0；完整header+不完整body在700ms读预算后400，账本0 |
| E11server写预算 | WriteTimeout100ms已过期后release，Flush真实i/o timeout；client EOF；handler仍执行到Done |
| E12长流 | client真实读part1后release，再读part2/EOF；每段刷新1s写deadline，有界读取 |
| E13空闲回收 | server IdleTimeout500ms小于client IdleConnTimeout2s；等真实ConnState live0后下次conn2、Reused=false |
| E14取消中收尾 | 真实HTTP进入handler后取消work ctx，result=Canceled已消费；cleanup gate未开时finished=false、owner_returned=false、owned=1；release后events严格cleanup-finished→owner-return、owned=0；重复join不二次消费result |

独立测试还直接验证8个并发POST只写1行、未知字段/非法数量无副作用，以及伪造X-Forwarded-For未替代真实loopback RemoteAddr；本次新增cleanup阻塞、观察ctx取消非join、Entered早退出join、主/清理错误同时保留。

## 主验收缺口与本次修复

首版`waitRequest(ctx,done)`用同一工作ctx等待结果；ctx取消可能先返回。取消实验Entered失败只cancel，header/write及测试的早路径没有等owned request结束。首版正常运行与race通过是真实事实，但不能证明这些失败路径正确，故本次返工没有沿用其完成结论。

所有doAsync/startRequest caller已逐个核对：`cancellationCase`、header/write、TLS request、测试取消/超时/观察者、E14和清理错误测试，在创建后立即注册完整清理；结果channel只读一次，finished关闭广播独立join。顺序为child cancel→解除必要gate→独立3秒budget等finished→errors.Join保留主/清理错误→fixture cleanup。t.Cleanup顺序保证t.Fatal也先join请求，再关fixture。TLS raw peer创建后同样注册cancel/Open/listener.Close/独立peerFinished join。工作ctx.Done仅能造成等待错误，不会标join成功。

首版命令原日志完整保留：[v1-demo.txt](evidence/v1-demo.txt)、[v1-test.txt](evidence/v1-test.txt)、[v1-race.txt](evidence/v1-race.txt)、[v1-vet.txt](evidence/v1-vet.txt)。默认gcc的最早失败仍在race-default.txt。当前文件名demo/test/vet/race用于返工后的最新实际验证。

## 资源清理与证明边界

当前demo日志每个普通fixture最终为 `bodies=0 owned_requests=0 live=0 Serve=joined listener=closed`。TLS实验的httptest.Close同步完成、raw peerFinished及request finished分别独立join，自有raw socket/listener关闭。E14工作ctx已取消而请求清理未完成时owner确实继续等待，释放gate后才返回；清理失败会被报告而不是标已退出。请求ctx/timer由创建者cancel/Stop，所有等待有界。实验在当前go进程内运行，命令正常exit=0；没有启动外部长期服务，没有修改/终止其他模块或项目进程。

尚未验证的范围：公网DNS/真实OS TCP拨号超时、HTTP/2并发stream、生产负载、外部CA/mTLS运维、数据库持久事务、进程重启、跨实例幂等、完整认证/可信代理策略。关闭端口的dial拒绝和TLS握手超时是两个不同结果，不合并称为connect timeout。本课提供重试规则与幂等说明，未自动重试下单。

未执行git add/commit/push，未写其他模块/根module/原始files；整层验收与提交由主agent负责。
