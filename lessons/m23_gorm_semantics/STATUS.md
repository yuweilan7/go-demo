# M23 实施与真实验证记录

2026-11-07，下线 RabbitMQ 后，M23 通过现有课程 MySQL 8.4.11 的独占临时表验证 GORM。原交付的 AMQP 实验和验收可从 Git 历史查阅。

| 验证入口 | 实际结果 |
|---|---|
| `go -C lessons/m23_gorm_semantics test ./...` | 离线测试通过，真实 MySQL 需显式开启 |
| `GOLEARN_INTEGRATION=1` 后执行 `go -C lessons/m23_gorm_semantics test -count=1 -v ./gormcourse` | 真实 MySQL 全课和五个辅助测试通过 |
| `go -C lessons/m23_gorm_semantics run ./cmd/demo` | 六组真实实验通过，独立读回回滚库存 5、订单 0、audit 0、outbox 0；提交库存 4、订单 1、audit 1、outbox 1，事件 `sent=false` |
| `go -C lessons/m23_gorm_semantics vet ./...` | 退出 0 |

行锁等待取消后，服务器上的原任务已结束。两个实库运行各自删除临时表并关闭连接池，独立检查课程库剩余表数为 0。验证所建 SSH 转发已关闭。Kafka 发布及消费幂等由 [M30](../m30_outbox_idempotency/README.md) 实验覆盖。
