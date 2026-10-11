# M01 实际交付与验证状态

实施完成，等待主 agent 独立验收。记录时间：2026-10-12（Asia/Shanghai）。本文件记录执行者实际完成的课程内容与运行事实，不代表整层已提交或主 agent 已接受。

交付目录：`C:/Users/Lenovo/.codex/worktrees/go-course-m01/go-demo/lessons/m01_syntax_packages/`。所有写入仅在此目录内；原 examples、主目录 files、根 go.mod/go.sum、共享 go.work、其他模块均未修改。执行者没有创建子 agent，没有 git add/commit/push。

## 版本与范围

| 项目 | 本次实测 |
| --- | --- |
| Go | `go version go1.27.0 windows/amd64`，可执行文件 `D:/Go/bin/go.exe` |
| 模块 | `go-demo/lessons/m01_syntax_packages`，`go 1.27.0`，无 toolchain 指令 |
| Go 环境 | GOVERSION=go1.27.0，GOOS=windows，GOARCH=amd64，GOTOOLCHAIN=auto，GOWORK 为空 |
| 模块范围 | GOMOD 指向本课 worktree 下 go.mod，所有命令显式使用本课/实验 workdir |
| 代理 | 当前 `https://goproxy.cn,direct`，没有执行 go env -w |
| PowerShell | `7.6.5` |
| 第三方直接依赖 | `github.com/tidwall/gjson v1.18.0`，模块自身 go 1.12，在当前工具链成功下载/运行 |
| 第三方间接依赖 | `github.com/tidwall/match v1.1.1`、`github.com/tidwall/pretty v1.2.0`，由实际 tidy/list/graph 确认 |
| 硬前置/外部服务 | 无；没有使用 M00 或数据库/MQ/学习服务器 |

已读取编排 ORCHESTRATION、TEACHING_STYLE、完整 M01 prompt、适用 AGENTS（用户 .codex 的 AGENTS 为空）、根 README/go.mod、指定 basic-syntax 八组原示例及对应 files 材料。原材料全程只读。本次查证官方 Go 规范、模块参考、go 命令/strconv 文档与 GJSON v1.18.0 的真实 go.mod/本机源码符号，不凭旧材料判断版本语义。

## 内容完成情况

| prompt 要求 | 已交付和实际观察 |
| --- | --- |
| :=/= /var、零值、常量/iota、if/switch/for/range | `cmd/demo`、`order`、`input` 实际订单调用链；README 就地讲解作用域和控制流 |
| 多返回值、可变参数、函数值、具名结果 | Parse/Ship/splitCents 签名、Policy/Validate/checks...；独立策略断言观察 AB 后停止 |
| 内层 := 遮蔽 err 吞错 | ShipShadowed 实际返回 nil 并写入零数量订单；ShipAssigned 与 Ship 同输入错误返回且 0 记录 |
| 多文件/包导出/路径与包名/import 副作用 | L00 包启动序列；L04 漏文件失败与整包成功；L06 同一订单的单文件与两包结果一致 |
| init 顺序纠正 | L05 显式 a→z/z→a 文件顺序真实改变 init 顺序；base→derived 变量依赖不变 |
| 数值边界 | int 实测 64；常量255成功/256编译失败；运行时256窄化为0；ParseUint 8位拒绝256；JSON与int64边界独立测试 |
| 直接/间接依赖与 MVS | GJSON 实际 graph/list；合成 quote要求rules1.1与receipt要求1.2，list最终选择1.2且实际运行本地源码 |
| replace 不传递 | consumer 在 GOPROXY=off 下准确失败；fixed.mod 自己补齐 replace 后真实运行成功 |
| 完整解析/修复/行为证据 | 中文 README、详细中文注释源码、5个顶层测试/18个表驱动子测试、4组显式编译故障、evidence 输出 |

## 本次实际命令与结果

下表中的 `MODULE` 是上面的完整交付目录；命令的工作目录均为 MODULE，标 `worktree根` 的只读环境核验是在 `C:/Users/Lenovo/.codex/worktrees/go-course-m01/go-demo`。README 提供汇总回主仓库后的相对路径复跑命令。

| 工作目录 | 实际命令 | 观察点 | 真实结果 |
| --- | --- | --- | --- |
| worktree根 | `go version` / `go env GOVERSION GOOS GOARCH GOTOOLCHAIN GOMOD GOPROXY` | 工具链与根模块，不改变配置 | exit 0，Go 1.27.0 windows/amd64 |
| MODULE | `go mod tidy` | 真实下载固定 GJSON 与传递依赖，生成独立 go.sum | exit 0，go.mod 含两项 indirect |
| MODULE | `gofmt -w` 本模块 rg 列出的所有 .go | 教学源码、测试、显式故障输入格式 | exit 0；最终 `gofmt -l` 无输出 |
| MODULE | `go -C MODULE run ./cmd/demo` | L00-L03 正常/危险/修复及状态断言 | exit 0；ORD-100为2件2500分/1记录，坏版0件/1记录，两个修复0记录，DONE |
| MODULE | `go -C MODULE test -count=1 -v ./...` | 真正调用 GJSON/strconv；从包外核对仓库和错误因果 | exit 0，5个顶层测试、18个表驱动子测试全部通过 |
| MODULE | `go -C MODULE vet ./...` | 静态检查 | exit 0，无诊断；不将它当吞错 bug 的行为证明 |
| MODULE | `go list -m all` / `go mod graph` / `go list -m -json` 三个第三方模块 | 真实直接/间接版本 | exit 0，与 go.mod/go.sum 一致 |
| MODULE | `go mod verify` | 下载缓存内容校验 | exit 0，`all modules verified` |
| MODULE | `go -C MODULE/labs/deps/app list -m all` | 合成图最终选择 | exit 0，quote1.0/receipt1.0/rules1.2及本地Replace |
| MODULE | `& ./scripts/verify-labs.ps1` | 每个子进程30秒有界，核对成功与预计失败的退出码/诊断 | 最终 exit 0，PASS，全部子进程退出；详细每条命令见 evidence/labs.txt |
| MODULE | 对 app/quote/receipt/rules 逐项 `go -C MODULE/labs/deps/名称 test ./...`、`vet ./...` | 嵌套模块不由课程 ./... 覆盖，单独静态验收 | 全部 exit 0；这些模块无测试，行为由脚本实际 run 与 graph 证明 |
| MODULE | `go -C MODULE/labs/deps/consumer test '-modfile=fixed.mod' ./...` 与 `vet '-modfile=fixed.mod' ./...` | 正确消费者模块 | 全部 exit 0，无测试/无 vet 诊断；实际 run 由脚本证明 |
| MODULE | `git diff --check`、`git status --short` | 只读范围核验 | diff --check exit 0；只有本模块新文件（git聚合显示lessons未跟踪） |

## 保留的实际证据与曾修复的问题

- [evidence/demo.txt](evidence/demo.txt)：完整 L00～L03 实际输出，包括非法出库与修复的 records 值。
- [evidence/tests.txt](evidence/tests.txt)：`test -count=1 -v ./...` 完整输出，输入与业务断言结果。
- [evidence/modules.txt](evidence/modules.txt)：课程真实 `go list -m all` 和 `go mod graph`。
- [evidence/labs.txt](evidence/labs.txt)：L04～L08 每条真实子进程命令、退出码和输出；四组编译错误与遗漏 replace 是预期失败。

扩展脚本首次核验时，编译器常量溢出实际诊断是 `as uint8 ... (overflows)`，最初脚本要求连续文字 `overflows uint8`，因此脚本自身断言失败。已根据真实 Go 1.27 文案改为匹配 `overflows`，并修正诊断输出流与返回字符串的分离，随后完整复跑通过。它不是课程业务失败或编译故障未复现。

一次单独静态核验用未引用的 PowerShell 参数 `-modfile=fixed.mod` 被 shell 解析为 `fixed`，Go 返回 `file does not have .mod extension`。已改成引号参数并重新实际 test/vet 成功；脚本自始使用字符串参数数组，修复消费者 run 已成功。README 保留带引号复跑命令。

## 副作用、清理与未验证边界

本课只写自己的合成订单内存 Store，日志和依赖缓存。没有外部业务请求、SQL、Redis键、消息、凭据或远端安装；依赖通过全局 Go 缓存复用，但课程数据只属于本进程。正常入口没有后台 goroutine、端口、连接或文件句柄。扩展脚本对每个子进程有30秒上限，实际全部正常或预期失败退出；finally 释放进程句柄并恢复临时进程环境，没有遗留故障进程或代理。

没有跑 race：本课不包含并发读写或 goroutine，不将当前单线程 Store 宣称为并发安全。没有跑数据库/消息/生产幂等验收，也没有验证其他操作系统/32位目标或旧 Go 工具链。合成 example.com 版本是本地图，不宣称发布过这些标签；真实第三方版本/下载/内容校验另有证据。核心输入与状态协议是教学范围，不承诺对重复 JSON键、超大恶意载荷或生产重投实现完整安全契约。

没有待补关键实验；主 agent 待验重点：从 main 沿源码核对所有 err 绑定、save 的发生资格与内存记录，独立复跑正常入口/测试、L04-L08脚本，检查注释是否足以让 Go 初学者理解包初始化与 MVS/replace。整层 ACCEPTED 和提交由主 agent 记录。
