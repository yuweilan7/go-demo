# Go 后端课程

面向熟练 Java 工程师、Go 初学者。每课有详细中文 README、透彻注释的源码、直接交付的反例/修复、真实可运行实验与必要独立断言，不布置额外 exercise。共同规范见 [TEACHING_STYLE](../learning_prompts/TEACHING_STYLE.md)。

第 0 层 19 课已完成主 agent 独立验收并汇总；剩余 20 课仍按 [DAG](../learning_prompts/DAG.md) 的严格整层屏障实施。下面“待交付”的链接指向模块 prompt，尚不表示代码或真实实验已完成。验收范围见 [ACCEPTANCE](ACCEPTANCE.md)。

各课是独立 Go module；根 `go test ./...` 不覆盖它们。先读该课 README 的参数/资源/实际范围，再执行例如：

```powershell
go -C lessons/m17_net_http run ./cmd/demo
go -C lessons/m17_net_http test -count=1 ./...
go -C lessons/m17_net_http vet ./...
```

真实中间件课程使用已获授权的独立学习服务，凭据与原始 profile/运行报告放被忽略的 `.local/` 或 `.secrets/`。Windows race 的兼容 C 工具链按每课 README 在命令进程中选用。性能数量、耗时、动态端口和任务顺序按证据范围理解。

## 第 0 层

| 模块 | 内容 | 硬前置 | 交付状态 |
| --- | --- | --- | --- |
| [M00](m00_environment_infra/README.md) | 工具链、项目地图与共享基础设施入口 | 无 | 已验收 |
| [M01](m01_syntax_packages/README.md) | 快速补齐业务代码语法、包与依赖管理 | 无 | 已验收 |
| [M02](m02_values_methods/README.md) | 值、指针、结构体与方法：谁被复制、谁会改变 | 无 | 已验收 |
| [M03](m03_collections_aliasing/README.md) | slice、map、string 与闭包：共享内存和循环陷阱 | 无 | 已验收 |
| [M04](m04_interfaces_nil/README.md) | interface、typed nil、embedding 与可替换依赖 | 无 | 已验收 |
| [M05](m05_errors_contracts/README.md) | error 链、分类和重试边界 | 无 | 已验收 |
| [M06](m06_defer_resources/README.md) | defer、return 与资源释放的精确时间线 | 无 | 已验收 |
| [M07](m07_panic_recovery/README.md) | panic/recover：恢复边界、goroutine 与进程后果 | 无 | 已验收 |
| [M08](m08_json_money_time/README.md) | JSON、金额、时间与业务数据边界 | 无 | 已验收 |
| [M09](m09_generics_reflection/README.md) | 读框架必需的泛型、函数选项、迭代器与反射 | 无 | 已验收 |
| [M10](m10_testing_debugging/README.md) | Go 测试、调试与错误路径的独立证据 | 无 | 已验收 |
| [M11](m11_goroutine_ownership/README.md) | goroutine 的启动、阻塞、完成与回收 | 无 | 已验收 |
| [M12](m12_channels_select/README.md) | channel/select：所有权、背压和结束协议 | 无 | 已验收 |
| [M13](m13_context_budgets/README.md) | context、取消、超时与整条调用链预算 | 无 | 已验收 |
| [M14](m14_sync_memory_races/README.md) | Mutex、atomic、sync.Map 与内存可见性 | 无 | 已验收 |
| [M17](m17_net_http/README.md) | net/http：请求、响应体、连接复用与取消 | 无 | 已验收 |
| [M31](m31_grpc_protobuf/README.md) | gRPC/Protobuf：生成代码、deadline、拦截器与流 | 无 | 已验收 |
| [M34](m34_observability_diagnosis/README.md) | 日志、指标、trace、pprof 与慢请求定位 | 无 | 已验收 |
| [M35](m35_performance_runtime/README.md) | 性能与运行时：benchmark、逃逸、GC 和内存预算 | 无 | 已验收 |

## 第 1 层

| 模块 | 内容 | 硬前置 | 交付状态 |
| --- | --- | --- | --- |
| [M15](../learning_prompts/M15_structured_concurrency.md) | 组合并发：errgroup、worker pool、限流和部分失败 | M11、M12、M13、M14 | 待交付 |
| [M18](../learning_prompts/M18_gin_middleware.md) | Gin 路由、middleware、绑定与请求作用域 | M17 | 待交付 |
| [M20](../learning_prompts/M20_sql_pools.md) | database/sql、真实 MySQL 与连接池生命周期 | M00 | 待交付 |
| [M25](../learning_prompts/M25_redis_client.md) | 真实 Redis、go-redis、TTL 与命令原子边界 | M00 | 待交付 |
| [M28](../learning_prompts/M28_rabbitmq_delivery.md) | RabbitMQ：ack、confirm、重连与毒消息 | M00 | 待交付 |
| [M29](../learning_prompts/M29_kafka_offsets.md) | Kafka：分区、offset、rebalance 与至少一次处理 | M00 | 待交付 |
| [M33](../learning_prompts/M33_resilience_retries.md) | 超时、重试、熔断、限流和资源隔离 | M13、M17 | 待交付 |

## 第 2 层

| 模块 | 内容 | 硬前置 | 交付状态 |
| --- | --- | --- | --- |
| [M16](../learning_prompts/M16_timers_shutdown.md) | Timer/Ticker、后台任务与优雅停机 | M15 | 待交付 |
| [M19](../learning_prompts/M19_composition_architecture.md) | 构造、配置与复杂仓库的真实业务调用链 | M04、M18 | 待交付 |
| [M21](../learning_prompts/M21_transactions_defer_panic.md) | 事务、defer、panic 和 Commit 错误的实库交叉实验 | M20、M05、M06、M07 | 待交付 |
| [M24](../learning_prompts/M24_schema_queries_migrations.md) | 表结构、索引、分页与安全演进 | M20 | 待交付 |
| [M27](../learning_prompts/M27_redis_leases_limits.md) | Redis 锁、租约、fencing 与限流 | M25、M13、M20 | 待交付 |

## 第 3 层

| 模块 | 内容 | 硬前置 | 交付状态 |
| --- | --- | --- | --- |
| [M22](../learning_prompts/M22_mysql_locks_isolation.md) | MySQL 隔离、行锁、死锁与并发库存 | M21 | 待交付 |
| [M23](../learning_prompts/M23_gorm_semantics.md) | GORM：SQL、零值更新、作用域、hooks 与事务 | M21 | 待交付 |
| [M26](../learning_prompts/M26_cache_consistency.md) | 缓存一致性、singleflight、热点与降级 | M25、M15、M21 | 待交付 |
| [M30](../learning_prompts/M30_outbox_idempotency.md) | MySQL + Kafka：outbox、幂等与结果未知 | M21、M29 | 待交付 |
| [M32](../learning_prompts/M32_framework_source_reading.md) | 真实微服务框架：Kratos 主线，Kitex/go-zero 迁移阅读 | M19、M31 | 待交付 |

## 第 4 层

| 模块 | 内容 | 硬前置 | 交付状态 |
| --- | --- | --- | --- |
| [M37](../learning_prompts/M37_order_backend_capstone.md) | 综合业务：订单、库存、缓存、RPC 与 Kafka | M08、M19、M23、M26、M30、M31 | 待交付 |

## 第 5 层

| 模块 | 内容 | 硬前置 | 交付状态 |
| --- | --- | --- | --- |
| [M38](../learning_prompts/M38_failure_recovery_lab.md) | 故障演练：取消、宕机、重复、乱序与恢复对账 | M37、M33、M34 | 待交付 |

## 第 6 层

| 模块 | 内容 | 硬前置 | 交付状态 |
| --- | --- | --- | --- |
| [M39](../learning_prompts/M39_takeover_code_review.md) | 陌生 Go 仓库接手、AI 代码审查与小改动交付 | M32、M38 | 待交付 |
