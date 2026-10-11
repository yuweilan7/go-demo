# PR式交付：按认证用户隔离同key的订单状态查询

## 问题与触发

同一个 request key 只在账号内部唯一。Alice/shared/qty1、Bob/shared/qty2都已由真实订单事务合法创建；原候选状态查询仅`WHERE request_key=?`，再把返回订单owner交给Cache.Query，Bob真实HTTP200取得Alice/qty1/299分的完整订单。缓存层已经验证传入owner与数据一致，仍无法知道认证Bob被上层替换成了Alice。

先冻结的不变量是“同 request key，authenticated Bob 永远拿不到 Alice 的订单”；验收还要求Bob确实得到自己的qty2/598分，而非把泄漏变成错误。专用失败命令实际exit1，日志保留 `.local/logs/candidate-bad.txt`，原文核心：

```text
R01_BAD http=200 authenticated=bob returned=alice quantity=1
M39 FAILED: isolation: 断言失败: 订单完整协议 owner=bob qty=2 got_owner=alice got_qty=1
exit status 1
```

失败run真实库存7、两订单/两outbox；所有handler/Serve最后Done后精确5表/2键/topic/group清理、独立读回不存在、SQL0。没有通过删除Alice数据、修改期望或换Bob key来让验收通过。

## 改后行为与最小业务diff

修复位置只有新切片 `gateway.go/lookupStatus`。教学交付保留候选bad分支用于可复现对照，正常`/status/:key`固定使用修复分支；`/candidate/status/:key`只在有限loopback实验存活。

从坏候选到正常业务分支的有效差异：

```diff
 q := r.Store.DB.WithContext(ctx).Table(r.Store.Table("orders")).Where("request_key=?", key)
+q = q.Where("owner=?", owner)
 q = q.Order("owner ASC").Take(&o)
 if q.Error != nil { return b.View{}, false, q.Error }
-return r.Cache.Query(ctx, r.Store, o.Owner, o.ID, b.Hooks{})
+return r.Cache.Query(ctx, r.Store, owner, o.ID, b.Hooks{})
```

真实SQL先受认证owner约束，缓存读写继续使用同一认证owner。未改订单交易、库存策略、payload、outbox/inbox协议、金额规则、缓存Lua、group/offset、前置源文件或DDL。统一404/401/409安全错误分类属于新网关的协议装配，未引入驱动错误正文泄漏。

Alice/Bob各读两次，第二次真实Redis命中且owner/ID/qty/金额/状态/v1全部正确。Bob不同payload重用shared返回409，未认证401、无原key404；SQL库存7/订单2/outbox2/receipt效果0。这些是本PR影响范围内的必要回归，没有为此重跑M01～M38。

## 审核AI候选diff：不能只问“代码能编译吗”

下面候选是本课对同一问题的审查材料，描述和有效diff落盘，不调用外部AI服务或创建GitHub PR。第1个bad原候选实际运行，最终接受版本实际运行；其余被拒候选是对真实签名/调用链的静态评审，不声称实跑过它们。

| 候选 | 审查事实 / 跨越边界 | 结论 |
| --- | --- | --- |
| 原候选只按key查，缓存使用o.Owner | handler已提供认证owner，但SQL忽略它；缓存参数从非授权结果产生。真实Bob→Alice完整200 | 拒绝，有实际失败证据 |
| 只改Cache.Query的owner为认证owner | SQL仍可能选Alice；缓存miss后的Store.Read(Bob,AliceID)失败，Bob自己的合法订单仍不可用。不能只检查“未泄漏” | 拒绝，授权/可用性条件不完整；静态推演 |
| 增大缓存前缀或把owner拼到用户key | 现有SQL唯一(owner,key)及原key恢复契约没变；新拼接会读不到旧权威记录或继续信任o.Owner | 拒绝，改变key协议却未修SQLscope；静态推演 |
| 用Background替代request ctx避免失败 | 状态查询请求已结束仍可继续占SQL/Redis；没有新增owner/最终join。与此授权bug无关 | 拒绝，跨取消/ownership边界；R03提供对应真实风险 |
| 顺手把consumer改成每worker独自ProcessRecord | 同partition高offset先完成会确认低缺口。与状态修复无关，实际R04坏例证明数据丢效果 | 拒绝，跨协议/offset边界 |
| 给GET外包Transaction并顺手调整Create提交 | 状态GET不是订单Tx owner；不能借“小修复”改库存/outbox提交范围；增加连接/锁且未定义业务收益 | 拒绝，跨Tx边界；静态评审 |
| SQL加认证owner，缓存继续该owner | 参数均来自现有认证caller；用值传递，保留ctx和返回协议，无新资源/no goroutine | 接受，两行有效业务diff与真实回归通过 |

`gofmt` / vet / race各有范围：格式与静态规则不证明用户授权；race检测本次运行中的共享内存竞争，不证明DB事务/MQ进度正确；业务内容、权威SQL和broker读回才对应各业务不变量。错误树中的ErrCleanup/UNKNOWN优先于普通冲突或1062，不能被“已有原单所以返回成功”覆盖。

## 验证与复跑

工作目录为独立模块 `lessons/m39_takeover_code_review`，根目录可按README使用`go -C`。先运行显式bad（预期非零），再运行fixed isolation；完整默认demo执行所有实际短案例。普通test只运行3辅助，3Live明确SKIP；完整`test-race.ps1 -Integration`设置唯一实库开关，运行全部Live并检查实际race。测试不import前置internal，不复制整套订单项目。

依赖校验保留原`go mod verify`对于两个local replace的missing ziphash/exit1，再检查13个实际消费M37/M31文件与第5层已验Git对象一致，实际108个远端依赖图独立verify成功。这是替代范围说明，不能写成原整命令成功。详细每次真实命令、耗时、namespace与冻结哈希入口见STATUS。

## UNKNOWN安全恢复与交付限制

R07实际窗口是“SQL真实Commit之后，可信hook取消并观察ctx，再返回UNKNOWN+Canceled”。同步旧执行者已返回，原owner/key查询确认完整订单和payload后，只续原outbox/同event_id，真实ACK/sent、消费SQL效果、Redis失效、offset最终next1，原order为RECORDED。没有重下单、换key、删中间状态或reset进度；如果原writer尚未Done，found=false不能证明不会晚提交。网络响应截断、真实Kill和更多窗口的证据属于M38，不宣称本PR另跑过。

当前单SKU、创建v1、单Kafka partition/group、单ISR/PLAINTEXT、合成身份、有限内存async接纳。没有生产鉴权、TLS、长期缓存失效保证、跨分区并发、旧generation SQL fencing、同聚合多版本、lease/fencing、支付/发货或生产SLA证明。202进程内任务需明确owner但不耐进程崩溃，生产应另审持久接纳协议。变更只交付可复核文档/源码/实验；没有上传公司资料、创建外部PR、提交或推送，由主agent整层验收后统一处理Git。
