# 现有材料评估与课程设计依据

规划与修订日期 2026-10-12。以下记录实际读取/盘点的材料和形成的教学取舍；读取范围包括目录盘点、关键语义抽查和指定源码注释段落。

## 仓库与教学基础

本地 `go-demo` 有根 module（`module go-demo; go 1.27`），53 个 Go 源文件约 205 KB，分布于 basic-syntax、advanced-syntax、internals；新复制的 files/ 有 47 个文件约 647 KB。对全部文件做目录/标题/符号盘点，并重点读取接口/error/defer/panic、context、timer、sync、pool 的关键说明与实现。

这些示例已经提供中文注释和可观察输出，不能假定要从零再抄一遍。新课程以原示例作定位入口，用业务错误与机制组合补足理解，原文件不作为并行模块的共同写入对象。

Python 项目本地 `java_to_python/learning_prompts/README.md` 提供递进路线与自包含 prompt 的设计，读取了当前目录实际存在的 29/30 两份完整 prompt。借用的是“背景/前置/实验/边界/输出/验收都放进单课文件”的方法，Go 课程改为真实后端中间件，不能机械套用 Python 的 FakeLLM/离线默认。

本轮修订另读取 Python 第 21 章 `example.py` 中资源状态、共享事件、取消传播及 finally 清理的注释，第 24 章 `example.py` 中事件类型、迭代协议、调用/拉取与生成器清理的注释。它们将语法含义、对象角色、调用关系、等待点、状态与失败路径写在实际代码旁，这个深度成为 Go 全部模块的注释标准。具体要求与示例见 [TEACHING_STYLE.md](TEACHING_STYLE.md)，每份 prompt 同时内置核心要求。

读取了 go-learning-by-running 技能的方法；采用 Go 语义优先、Java 类比有边界、中文机制讲解和可运行观察。最终保留 39 份课程 prompt；本次交付范围是规划文件，课程代码与运行实验由后续各模块执行时交付。

课程产物统一为文档、详细注释代码、可运行实验与真实结果记录。原有额外学习任务中的有效机制已合并到带完整解析的实验和修复对照。M39 改为直接讲解仓库接手、审查案例与最小修复的完整过程。应用任务停止、资源清理和故障恢复仍属于开发主线；基础设施安装和服务器运行按实验需要准备。

## 旧教程中的具体风险与缺口

| 材料/事实 | 怎样理解与处理 | 对应模块 |
| --- | --- | --- |
| 环境搭建使用 Go 1.22.10 示例；GMP 源码明确基于 1.18.1 | 安装命令与 runtime 源码不能作为当前版本事实，按当前工具链和目标公司版本重验 | M00/M35 |
| 循环材料已补 1.22 变化，但新规则未消除全部共享状态问题 | 继续区分 :=/=、循环值地址/元素地址、共享对象与外层闭包变量，不能简单说循环坑已全修好 | M03/M11 |
| map.md 主线是旧 hmap/bucket；Sync.Map 主线是旧 readOnly/dirty | 本机 GOROOT 的 internal/runtime/maps/map.go 描述 Swiss Table，sync/map.go 使用 HashTrieMap；两种旧实现只按版本阅读 | M03/M14 |
| 逃逸分析将所有接口转换与逃逸联系为必然，并凭语法猜栈/堆 | 需要实际编译器分析、inlining/escape 输出与 profile；语言语义不等于分配实现 | M02/M35 |
| defer/异常材料把 panic/recover 类比为抛出/捕获异常 | 必须区分普通业务 error、panic 展开、同 goroutine 恢复、进程终止，以及返回位置 | M05–M07/M21 |
| 框架文件只有约 62 行 Gin/GORM/Redis/go-zero/Kratos 的补充提纲 | 尚不能教真实请求链、资源 owner、middleware 或 ORM 副作用；补真实库/源码和错误验收 | M18/M19/M23/M32 |
| 反射材料为空，但已有 reflection Go 示例 | 不能以空文档推断示例不存在；泛型/迭代器/选项/反射按实用阅读范围补齐 | M09 |
| 已有 timer Go 示例含较新 channel 语义 | 保留正确说明，并在新课区分 Timer/Ticker/AfterFunc 和 Go 1.27 对旧兼容开关的变化 | M16 |
| Pool 示例有容量和背压测试，但 Submit 持 RLock 阻塞发送，CloseAndWait 需要写锁 | 当前正常测试通过不代表可取消所有任务；遇永久阻塞/递归提交等条件时必须再研究关闭协议 | M15/M16 |
| 现有材料未交付完整实库事务、MQ offset/ack、outbox 与真实故障链 | 独立 SQL/Redis/MQ 课先验边界，再进行组合和故障验证 | M20–M30/M37/M38 |
| 公司框架、Go 版本、流量/部署条件未知 | 使用公开默认栈作可迁移训练，未来按真实目标替换，不猜 Shopee 内部实现 | M00/M32/M39 |

上述风险不是说相关旧材料全部错误。已经正确的原示例仍可用；每课只对其实际使用的结论核验、纠正并注明边界。

## 版本核验

本次实际执行：

```text
go version
go version go1.27.0 windows/amd64

go env GOVERSION GOTOOLCHAIN GOOS GOARCH CGO_ENABLED
go1.27.0
auto
windows
amd64
1
```

根 go.mod 是 1.27。本机标准库确认存在 errors.AsType 和 WaitGroup.Go。官方 Go 1.27 发布说明涉及泛型方法、JSON v2、timer channel 与 goroutineleak profile，相关课程均要求在运行时对照真实版本和文档，不将旧印象作为结论。[Go 1.27](https://go.dev/doc/go1.27)、[Go 1.26](https://go.dev/doc/go1.26)、[Go 1.25](https://go.dev/doc/go1.25)

官方发布记录在规划日列有 Go 1.27.2，故“1.27 最新主版本”与“本机安装最新补丁”是两件事。本次交付 prompt，没有更新本机 Go。[发布记录](https://go.dev/doc/devel/release)

## 共享环境依据

读取 DeepHelp 的 AGENTS、PROJECT_STATE、ROADMAP、LOCAL_SETUP、infra README/Compose、postgres README、接入索引及相关部署说明。文档明确正式关系存储已转 PG，旧 MySQL 退役；MQ 没有在所读现行入口登记。

这些是当前文件证据，未把旧 handoff 的 PASS 或 .local 中历史健康报告当实时在线证明。本次没有服务器登录、服务安装、容量测试、数据库写入或故障注入。执行对应模块时再按 INFRASTRUCTURE 的唯一安装者与命名空间核验。

## 本次验证范围

根 `go test ./...` 实际成功，覆盖根 module 的包；大多数包是编译/无测试文件，goroutine-pool 的现有测试命中缓存。这不代表运行了每个 main 示例，也不包含 go-modules 下的三个嵌套 module。

prompt 修订阶段改动都是 prompt、教学规范、导航、依赖说明和机器索引，未修改原 Go 实现。该阶段结构检查覆盖 39 个编号/文件、依赖节点、无环、拓扑批次、prompt 与 JSON 的直接硬前置和推荐知识前置一致性，以及所有本地 Markdown 链接。随后用户授权按整层屏障实施全部课程，当前执行状态见 [ORCHESTRATION.md](ORCHESTRATION.md) 与各模块实际 STATUS。

本轮结构核验实际通过：39 份 prompt、45 个目录内交付文件、39 个 DAG 节点、42 条边、19 个无硬前置模块、7 层拓扑分层；包含根 README 在内的 90 处本地 Markdown 链接全部可解析。prompt 的标题、目标目录、硬前置、推荐知识前置、独立运行命令与 modules.json 一致；图节点/边、索引和拓扑批次一致。

39 份 prompt 均已包含详细中文注释、调用链/等待点/状态时间线及完整实验解析要求；额外学习任务与删除模块的失效引用检索结果为零。`git diff --check` 通过。本轮只改文档与规划索引，根 Go 测试的上轮结果作为原实现基线保留，未为文档改动重复执行。
