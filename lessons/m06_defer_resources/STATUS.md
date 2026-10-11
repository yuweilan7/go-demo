# M06 交付与实际验证状态

状态：**完整实施，自验通过，待主 agent 独立验收**。本文件不代表课程整层已 ACCEPTED，不含 Git 提交/推送声明。没有遗留 exercise 或待补关键实验。

记录日期：2026-10-12，时区 Asia/Shanghai。最近环境快照时间为 `2026-10-12 01:15:16 +08:00`。唯一写入范围为本模块；主目录与其他模块只读。所有命令均在当前 worktree 显式定位本模块。

## 运行环境与来源核对

| 项目 | 本次实际值/范围 |
| --- | --- |
| worktree 根目录 | `C:\Users\Lenovo\.codex\worktrees\go-course-m06\go-demo` |
| 独立模块 | `lessons/m06_defer_resources` |
| `go version` | `go version go1.27.0 windows/amd64` |
| `GOVERSION / GOTOOLCHAIN` | `go1.27.0 / auto` |
| `GOMOD` | 当前 worktree 的 `lessons\m06_defer_resources\go.mod` |
| `GOOS / GOARCH / CGO_ENABLED` | `windows / amd64 / 1` |
| `GOROOT` | `D:\Go` |
| module / go 指令 | `go-demo/lessons/m06_defer_resources` / `go 1.27` |
| toolchain / 依赖 | 未写显式 toolchain；仅标准库，无第三方依赖，无需 go.sum |
| 硬前置 | 无；不依赖其他课程源码、状态或服务 |

实际读取 ORCHESTRATION、TEACHING_STYLE、M06 完整 prompt、适用的 `.codex/AGENTS.md`（空文件）、worktree 根 README/go.mod、worktree 原 defer 示例。新材料仅从主目录 files 下只读，未复制或提交材料。

已核对官方语言规范 defer/return、官方 defer/panic/recover 文章，以及本机 Go 1.27 标准库 os.Exit、File.Close、bufio.Writer.Flush、errors.Join、sql.DB.Close、http.Client.CloseIdleConnections、Response.Body 文档。查阅本机 runtime/编译器与 Windows File.Stat 源码时使用 `D:\Go\src`。首次误用 `C:\Program Files\Go\src` 搜索未找到文件，随后以 `go env GOROOT` 的真实路径核对；未改工具链安装。

环境快照：[evidence/environment.txt](evidence/environment.txt)。README 已区分语言保证、标准库契约、本机实现和未执行的协议/实库范围。

## 实际执行命令与结果

以下命令的工作目录均为 `C:\Users\Lenovo\.codex\worktrees\go-course-m06\go-demo`。`$module` 在执行时设为 `C:\Users\Lenovo\.codex\worktrees\go-course-m06\go-demo\lessons\m06_defer_resources`，不是根 module。

| 实际命令 | 观察点 | 真实结果与证据 |
| --- | --- | --- |
| 模块内全部 `.go` 文件逐一 `gofmt -w`，最终逐一 `gofmt -l` | 仅格式化本模块，检查仍需格式化的文件 | 执行成功；最终 `-l` 没有输出 |
| `go -C $module run ./cmd/demo` | 全量 E01–E06 正常、危险、修复和清理路径 | exit code 0，最后 DONE；[demo.txt](evidence/demo.txt) |
| `go -C $module run ./cmd/demo -only core` | 10～20 分钟核心路线 E01–E04 | exit code 0，最终资源目录清理；[core.txt](evidence/core.txt) |
| `go -C $module test -count=1 -v ./...` | 6 组独立实质断言与 1 个 helper 入口，禁用测试结果缓存 | exit code 0；全部 PASS；[test.txt](evidence/test.txt) |
| `go -C $module vet ./...` | 包级静态检查 | exit code 0，无诊断；[vet.txt](evidence/vet.txt) |
| `go -C $module env GOVERSION GOTOOLCHAIN GOMOD GOOS GOARCH CGO_ENABLED GOROOT` | 当前工具链、系统、嵌套模块确实生效 | 实际值见上表与 environment.txt |

测试中的 normal child 是测试二进制 helper，输出可附带测试框架 PASS；demo 的 child 是当前 demo 程序，demo.txt 中 normal 输出为 normal-body。两者调用同一 RunChild，均核验真实退出码、文件写入与清理标记。

## 各项实验已观察到的业务/资源事实

| 实验 | 真实观察 | 独立证明 |
| --- | --- | --- |
| E01 参数/函数值/闭包/指针/LIFO | 保存实参 10，晚读闭包/指针 20；替换 fn 未改变已登记调用；顺序 3→2→1 | result 字段、顺序 slice、求值先于注册日志 |
| E01 接收者 | 指针旧对象当前字段 A*，值副本 A，晚读 current 为 B | 三个不同结果字段 |
| E02 return | caller 分别得到具名 15、匿名 10、具名但改 local 的 10 | 直接 int 结果 |
| E02 错误捕获与遮蔽 | 过早实参捕获 nil；闭包修复读到业务 error；内层吞错返回 nil；显式 return err 修复 | errors.Is(ErrBusiness) 与 cleanup 所见 error 分开验证 |
| E03 危险长循环 | 两个真实文件同时持有，第三次预算拒绝；函数返回后倒序全关 | opened=closed=2，peak=2，live=0，两个实际内容正确 |
| E03 两种批量修复 | 小函数 defer 与显式 Close 都完成四条记录，逐轮释放 | opened=closed=4，peak=1，live=0，四条实际内容正确 |
| E03 第二次获取失败 | 目标父目录不存在，源文件仍在返回前关闭 | 原始 PathError 的 NotExist 原因、同一 source 的 Read 得到 ErrClosed |
| E03 旧接收者/晚读闭包 | 方法 defer 关闭旧 A、遗漏 B；闭包关闭 B、遗漏 A | 内层返回后、兜底前的两对象真实 I/O 状态；外层随后关闭残留 |
| E03 稳定实参修复 | 给 A/B 各登记稳定文件实参并保留 Close error；重绑 current 后两者都关闭 | 内层返回后、兜底前 old_closed=true、new_closed=true |
| E04 错误 Flush/Close 顺序 | Close 先执行，Flush 真实失败，caller 却收到 nil，实际文件空 | 内容为空、FlushErr 匹配 ErrClosed、success=false |
| E04 正常修复 | Flush 后 Close、caller 返回 nil、实际 22 字节完整 | 文件与 orderRecord 完全相等，两清理 error 均 nil |
| E04 Close 失败 | 有意重复 Close，实际内容已有 22 字节，caller 仍保留 Close 失败 | errors.Is(ErrClosed)，success=false |
| E04 主/清理错误共存 | ErrBusiness、真实 Flush 与重复 Close error 都保留，实际内容为空 | errors.Is 两类原因、Flush/Close 各标签、独立错误字段 |
| E05 借用所有权 | 错误请求关闭客户端后第二个请求失败；修复两次请求写出两条内容后应用关闭 | 第二 Write 匹配 ErrClosed；修复内容等于两条 orderRecord |
| E06 普通 panic | 先文件 Close，外层 recover，再回到 caller；panic 后行未执行 | 原对象 Read 已关闭、content=before-panic、after=false |
| E06 正常/Exit/Fatal/修复 child | exit code 为 0/23/1/24；cleanup marker 有/无/无/有 | 每个真实进程结果、payload.txt 真实内容、仅 defer 写入的 Close 成功标记 |

## 首轮故障与修复记录

首次全量运行在 E03 失败：`[E03.acquire_failure.caller] target_failed=true source_closed=false`，退出码 1。源文件 Close 已返回 nil，但验证使用 `source.Stat()` 并假定其 error 可匹配 os.ErrClosed。Windows File.Stat 当前实现直接调用句柄查询，关闭后的平台错误不保证这个类别；这是验收方式错误，不能删掉验证来掩盖。

修复为同一 source 的 `Read(make([]byte, 1))` 并检查 os.ErrClosed。普通 panic 场景相同核验也改用 Read。之后重新执行全量 demo，得到 `target_failed=true source_closed=true`，全量 exit code 0；最终全部测试与 vet 通过。README 和源码旁均说明这次平台差异。未把“Stat 不可用”推断成文件没有关闭，也未用只打印 Close 日志取代独立 I/O 验证。

## 资源清理、危险上限与未验证范围

真实合成数据分别处于系统临时目录 `m06-files-*`、`m06-buffers-*`、`m06-ownership-*`、`m06-process-*`。每个实验拥有自己的目录，文件关闭后 RemoveAll；测试用路径 Stat 独立确认目录确已不存在。没有修改主目录材料、其他课程、根 go.mod、go.sum 或 go.work。未执行 git add、commit、push。

危险循环持有真实文件最多 2 个，以本课预算拒绝观察风险，没有制造 OS 描述符耗尽。资源重绑危险版限定在内层函数，观察后由外层马上兜底关闭。Exit/Fatal 为当前程序的独立 child，每个最多等待 5 秒，父进程实际等待退出并清理；没有遗留后台业务进程或故障代理。本课没有业务并发；未执行 -race，不把普通 run/test 说成 race 验收。

未验证真实 MySQL 事务、HTTP body/连接复用、MQ、磁盘故障、fsync/断电耐久性、runtime 致命 throw/OOM、其他 OS 的运行结果或 defer 性能。事务只给风险伪代码并明确留给 M21 实库证明；Close 故障是重复关闭的真实文件 API 失败，Flush 故障是向真实已关闭文件写入，均没有冒充实库/硬件故障。
