# M21：事务、defer、panic 和 Commit 错误的实库交叉实验

我是熟练的 Java 后端工程师，刚开始系统学习 Go，准备在 Shopee 阅读和维护复杂后端业务。我理解 Java 的线程池、异常、Spring、事务及常用中间件，但不能假定我懂 Go 的对应机制；已有示例出现过某语法也不等于我掌握了它。请以 Go 自身语义为主，用 Java 类比加速理解，并说清类比失效的地方。目标是让我能读真实代码、预测运行行为、定位错误和做有证据的小改动。

只完成本模块。先读适用的 AGENTS.md、根 README/go.mod、本模块已有交付及下方指定材料；files/ 是启发材料，必须纠正错误、版本差异和未经证实的绝对化结论。教学默认 Go 1.27；重新核对 go version、go env、模块 go/toolchain 指令和官方文档，区分语言保证、库契约及运行时实现。遇到公司旧版本代码，按其实际版本解释，不擅自升级。第三方依赖锁定实际可运行版本，不能凭记忆混用 API。

本模块必须交付中文 README.md、能运行的 Go 实验、必要的独立断言和 exercises.md；复杂机制按“最小规则 → 预测输出 → 错误复现 → 修复 → 业务迁移”递进。讲清调用者、输入输出、状态、资源拥有者、阻塞点及错误/取消/清理路径；重要源码写实质中文注释，简单赋值不凑注释，生成代码和第三方源码不逐行注释。每课保留一个可在 10～20 分钟运行并讲清的核心实验，扩展分步展开。练习给预测、修复、迁移三种任务及验收标准，答案放单独文件或折叠区，便于先自己做。

在下方目标目录创建独立 Go module，module 路径为 go-demo/lessons/目录名，默认入口 cmd/demo，按必要职责拆包。仅修改本模块目录；不改根 go.mod/go.sum、全局索引、其他模块和原 examples/files。依赖前置的源码时采用本模块内明确的本地 replace 或文档约定，禁止建立循环导入、复制整套前置项目或修改共享 go.work。并行运行时各自使用独立端口、数据命名空间、配置和报告；根 go test ./... 不包含这些嵌套 module。使用独立 worktree 或明确的目录隔离，不切换其他任务的分支、不提交或清理他人文件。AI 生成和测试通过不等于学员掌握，未观察到我实际练习就标为待做。

硬前置指本模块组合实验确实需要的已交付成果；核对其 STATUS.md 和直接接口，前置的 prompt 存在不代表已完成。缺失硬前置时可先完成独立讲解/设计，明确尚未完成的组合验收，不代做其他模块。推荐知识前置只影响我的阅读顺序，不阻止独立课程生成。前置未变化时复用其有效证据，不重跑全部课程。

用 gofmt，实际执行 go -C 目标目录 run ./cmd/demo 和相关 go test/go vet；并发课在平台支持时执行 -race，遇到工具链限制报告精确原因。坏例子放显式故障入口或子进程，正常入口保持可用；所有阻塞实验有上限，并用屏障/可观察状态控制顺序，不能用 sleep 猜调度。中间件课必须使用真实服务和真实驱动；本地 HTTP/RPC 进程是实际协议实验，纯函数替身只可辅助单测，不能冒充实库、真实 MQ 或故障验收。按实际结果写 STATUS.md，记录版本、命令、结果、未验范围和资源清理，不以编译通过、HTTP 200 或健康检查代替业务正确性。

中间件和部署模块再读 learning_prompts/INFRASTRUCTURE.md。我已授权在现有学习服务器范围安装所需独立 MySQL/MQ 学习实例并部署本课程代码；需要时直接核对容量、端口与归属后安装，不因常规安装重复询问。不恢复退役 MySQL、不改 DeepHelp 活动数据和服务；共享基础设施变更只能由一个安装者持锁执行，其他模块复用其结果。每课只动自己的合成数据，凭据放已忽略的本地配置且不打印或提交；资源不足时明确真实缺口，继续可独立部分，不用模拟替代已承诺的实证。

## 本模块范围与依赖

目标目录：`lessons/m21_transactions_defer_panic/`。

硬前置：M20、M05、M06、M07

推荐知识前置：建议 M08 金额契约与 M13 context。

学完要能：能逐行审查事务代码，证明失败不留下部分业务效果，并处理提交结果未知。

首先定位：M20 连接/隔离库说明、M05～M07 时间线与 STATUS；本机 database/sql 的 Tx 契约与 MySQL 驱动源码。

## 必须讲解、实现和观察的内容

1. 用真实 MySQL 的库存扣减+订单插入构建最小事务。与 Spring @Transactional/JDBC 类比显式 Tx 传递；Go 没有自动线程绑定事务，把 *sql.DB 传入下层会脱离 tx。每个 SQL、affected rows、读取/写入归属都在同一 Tx 中核对。

2. 先构造危险代码：defer 根据 named err == nil Commit，而 panic 时 err 仍 nil；:= 遮蔽错误；defer 捕获旧 err；错误提前返回但忘 Rollback；操作用了外层 db。逐行画 return/panic/defer/commit 时间线，用表内容独立断言分别复现。

3. 正确基础模式采用成功 Begin 后安排 Rollback、每步显式查错、最后显式 Commit 并检查其错误；如抽 withTx，只抽必要契约，panic 必须按约定 rollback 后传播或明确返回失败，不能 recover 成 nil 再提交。Commit 成功后 Rollback 的已结束结果按契约处理，不制造假失败。

4. 研究 BeginTx ctx 取消、Rollback/Commit 行为、网络断开和服务器已提交但客户端未收到响应的不同可能；不能把 Commit error 一概当未提交，更不能自动重复下单。通过唯一 request_id、查账与业务状态给未知结果恢复方案。

5. 验收成功、第二步错误、业务冲突、panic、取消、错误的 db/tx 混用与 Commit 返回失败；至少一个真实通信故障场景，不用 fake Commit 代替实库证明。练习：审一段无明显语法错的事务；改错而不丢原错误；对提交未知写出安全查询路径。

## 资料与结束条件

优先核对：[执行事务](https://go.dev/doc/database/execute-transactions)、[sql.Tx](https://pkg.go.dev/database/sql#Tx)、[MySQL Go driver](https://github.com/go-sql-driver/mysql)。这些链接是查证入口，当前库版本、源码符号和运行证据必须在执行时重新确认。

最终用一段未预先讲解的代码检验我是否能解释关键语义、指出失败路径并给出修复证据。README 应指出本课必要掌握和可延后内容；STATUS.md 只写实际完成情况。每个命令给工作目录、观察点和真实结果；有未通过的关键验收就保持未完成，不自动继续下一模块。

默认从仓库根目录复跑本课（具体必要参数由交付时补齐）：

```powershell
go -C lessons/m21_transactions_defer_panic run ./cmd/demo
go -C lessons/m21_transactions_defer_panic test ./...
go -C lessons/m21_transactions_defer_panic vet ./...
```
