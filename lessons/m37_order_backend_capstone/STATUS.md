# M37 交付与真实验证状态

状态：**READY_FOR_REVIEW**。执行日期：2026-10-12。模块已完成本课五项范围并停止继续实施；主 agent 尚需独立审阅和验收。本课未执行 Git add/commit/push，未推进 M38。

唯一写入目录为 `C:/Users/Lenovo/.codex/worktrees/go-course-m37/go-demo/lessons/m37_order_backend_capstone/`，工作树基线 `d8bedf89df00458e6934c0cf84aceace1710a59b`。主工作区 `D:/IdeaProject/go-demo`、根module、共享go.work、其他lesson、调度状态和基础设施只读。`git status --short`只有本模块未跟踪目录；适用祖先AGENTS为空，已读根README/go.mod、完整M37 prompt、TEACHING_STYLE、ORCHESTRATION、INFRASTRUCTURE及六个硬前置的STATUS/直接契约/集成验收证据。

## 交付文件与课程范围

公开文件共19个：README、STATUS、go.mod/go.sum、.gitignore、cmd/demo/main.go、internal/lab的course.go/course_test.go/lifecycle.go/recovery.go、orderbackend的domain.go/store.go/resources.go/cache.go/messaging.go/app.go/maintenance.go/contracts_test.go、scripts/verify-race.ps1。运行日志、对象登记、临时校验module和二进制全部留在本模块忽略的`.local`目录；未复制凭据。

README从ownership图到真实调用链、值/指针/context/错误语义、E01～E06正常/坏例/修复和一条实际订单的逐符号时间线，直接提供完整解析。唯一公开业务库为`orderbackend`，包含Gin用例、同MySQL Tx库存/订单/outbox、relay、持久inbox/receipt、用户查询与Redis代际缓存。M31通过本地replace只导入公开生成protobuf包；RPC仅实现真实只读Inspect，Reserve/Watch明确Unimplemented。没有复制其他课完整实验、导入其internal或新增另一套业务。

硬前置M08/M19/M23/M26/M30/M31未改，复用有效验收与输入/接线/GORM Tx/缓存/消息/生成协议契约，未重跑全部前置。推荐前置的迁移/观测/测试/owner概念只用于当前设计和说明，不强行引入全部机制。

## 工具、版本和独占范围

实际`go version`为`go1.27.0 windows/amd64`。模块go指令1.27.0，GOWORK空，GOMOD为本模块go.mod。默认环境CGO_ENABLED=1、CC=gcc、CXX=g++；race专用脚本使用安装者登记的WinLibs16.2.0 amd64 UCRT，不使用旧32位编译器，不写全局go env。

| 对象 | 锁定/实际核验 |
| --- | --- |
| Gin | v1.12.0，源码ServeHTTP/Next/CustomRecoveryWithWriter |
| GORM / MySQL dialector / driver | v1.31.2 / v1.6.0 / v1.10.1；实际SQL VERSION()=8.4.11 |
| go-redis / Redis | v9.23.0；实际INFO server=8.2.8 |
| franz-go / kadm / Kafka | v1.22.1 / v1.19.0；安装者READY索引4.3.1，实际metadata和记录协议核验 |
| gRPC-Go / protobuf | v1.84.0 / v1.36.12；实际loopback TCP HTTP2 Inspect |
| M31生成代码 | 本地replace v0.0.0；只消费inventory.pb.go、inventory_grpc.pb.go |

基础设施索引为只读`D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json`：MySQL127.0.0.1:13306、课程库golearn_m20；共享Redis127.0.0.1:16389 DB0；Kafka127.0.0.1:19192。所有credential_file只在内存读取，不输出DSN/口令。Kafka版本来自安装者索引，未重装/重启broker；单KRaft/ISR1 ACK不证明多节点耐久性。

每次run五张精确表为`m37_<run>_{stock,orders,outbox,inbox,receipts}`；topic为`golearn.m37.<run>`，group后缀`.consumer`，client带本run前缀；Redis仅用`golearn:m37:<run>:owner=...:order=...:schema=1:{generation,data}`。每个对象预先证明不存在，发CREATE/写键前登记attempt。数据TTL30s、代际300s、Kafka retention10min/1MiB；所有任务/连接/消息有上限。整个业务只有一个SQL池MaxOpen4/Idle1，两个App重建仍借同一池。HTTP/gRPC端口由OS动态分配loopback地址。

## 实际命令、工作目录和结果

下表默认工作目录为`C:/Users/Lenovo/.codex/worktrees/go-course-m37/go-demo`。命令日志重定向到模块`.local`，正常退出码0表示Run的最终清理也返回成功。Go测试会把package运行目录设为`.../internal/lab`，其对象账本位于该package的忽略`.local`；demo账本位于模块根`.local`。

| 命令 | 当前最终实际结果 / 证据 |
| --- | --- |
| `gofmt -w lessons/m37_order_backend_capstone/orderbackend lessons/m37_order_backend_capstone/internal/lab ...`及`gofmt -l` | 格式列表空，GOFMT_NONEMPTY=0 |
| `go -C lessons/m37_order_backend_capstone run ./cmd/demo` | exit0；`.local/demo-final.log`；namespace `m37_fd0ef16880d703c8`；完整E01～E06、固定SQL/响应/缓存/offset断言及最终清理通过 |
| `go -C lessons/m37_order_backend_capstone run ./cmd/demo -verify-recovery` | exit0；`.local/recovery-final.log`；namespace `m37_3f873d89cc248084`；旧App/client最终关闭后fresh重建，原key/API/cache/consumer继续，未知键拒绝且保留，借用者账本0、原owner精确清理通过 |
| `go -C lessons/m37_order_backend_capstone test -count=1 -v ./...` | exit0；`.local/unit-final.log`；六个辅助顶层测试及子断言通过，三个实库入口明确SKIP，不能把这次当实库验证 |
| `& lessons/m37_order_backend_capstone/scripts/verify-race.ps1 -Integration` | exit0；`.local/race-final.log`；显式设置GOLEARN_M37_INTEGRATION=1，TestLive真实全链25.81s通过、TestCanceledOwnerRealCleanup保留context.Canceled且无cleanup失败、TestBorrowedResourcesRealRecovery10.05s通过；另六个辅助测试通过，无DATA RACE；主链namespace `m37_1372b7607e6cbd81`，借用证明`m37_04c5e97d1efdf2c0`；M37_ENV_RESTORED=True |
| `go -C lessons/m37_order_backend_capstone vet ./...` | exit0，`.local/vet-final.log`无诊断 |
| `go -C lessons/m37_order_backend_capstone mod verify` | **exit1，整个命令未成功**；`.local/mod-verify-local-replace.log`保留下面的原诊断 |
| `go -C lessons/m37_order_backend_capstone list -m ... all`→忽略`.local/remote-verify/go.mod`→`go -C .../.local/remote-verify mod download`及`mod verify` | 实际module图108个远端module独立校验，exit0、`all modules verified`；`.local/mod-download-remote.log`及`.local/mod-verify-remote.log`，明确排除M31本地replace |
| `go -C lessons/m37_order_backend_capstone list -f ... go-demo/lessons/m31_grpc_protobuf/gen/inventoryv1`、Get-FileHash、生成符号rg | 仅消费两份公开生成文件，SHA256与主工作区已集成M31相同，M31 git diff空；`.local/local-m31-dependency.log` |
| `go -C lessons/m37_order_backend_capstone build -o .local/m37demo.exe ./cmd/demo` | exit0；只用于后续精确只读复验，不运行第二套业务 |
| 模块目录执行`.local/m37demo.exe -verify-cleanup <每个实际账本绝对路径>`，有限枚举本模块根.local和internal/lab/.local的15个已登记run | 全部exit0，OWNED_MANIFESTS_VERIFIED=15，逐项VERIFY_GONE、group offsets_empty=true；`.local/cleanup-independent-all.log` |
| 对模块文本文件/日志/登记在内存比较索引读取的口令；Get-CimInstance只查看本课命令进程 | SECRET_MATCH_COUNT=0；OWNED_LIVE_PROCESS_COUNT=0；未输出口令或操作其他进程 |

本地replace的精确工具诊断保持原样：

```text
go-demo/lessons/m31_grpc_protobuf v0.0.0: missing ziphash: open hash: The system cannot find the file specified.
```

替代核验没有把失败改名为通过。实际消费路径是本worktree的`lessons/m31_grpc_protobuf/gen/inventoryv1`。两份生成文件SHA256：inventory.pb.go=`49B550BD8CCE4A60475796FC2FE41683C879E0CE90718BF80771795554E94F27`；inventory_grpc.pb.go=`82FF836D9002D10AAAB9BD0B8AAFB5EC9BBFC0C507EF28D266C4719D8A32004F`。核验了Inspect的FullMethodName/client Invoke/server handler以及Reserve/Watch/Unimplemented入口。远端依赖全部通过独立校验，但本课原始`go mod verify`仍为exit1的已知工具限制。

race脚本从`D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json`读取cc/cxx/bin_dir，只修改当前PowerShell进程CC/CXX/CGO_ENABLED/PATH/integration开关。每个变量保存原存在性和精确值；原不存在Remove-Item，原已有值包括空串精确恢复。实际失败和成功race都记录M37_ENV_RESTORED=True。

## 内容与副作用的独立断言

| 阶段 | 当前真实证明 |
| --- | --- |
| E01下单/查询/RPC | HTTP完整order字段、598分/CNY/v1/ACCEPTED/创建requestID；SQL1单qty2/stock8/outbox1，consumer门上inbox0/receipt0；真实Redis miss→hit/TTL；Inspect库存8，Reserve Unimplemented且库存不变 |
| E02持久请求幂等/用户隔离 | 六个真实并发同keycaller返回同orderID/创建requestID、各自requestID唯一，SQL不多扣；同key换quantity409，零422，body冒充owner/重复字段400，无身份401；Bob读Alice ID与key均404；Bob自己的同key是另一单/金额299/另一缓存作用域 |
| E03缓存窗口、错误复现与修复 | 真实SELECT旧PENDING停在AfterRead；另一真实Query填旧键；消费SQL已RECORDED但cache仍PENDING、group next=-1；Commit后失效→next1；旧reader fill代际1≠2拒绝；明确坏例普通SET真的错读PENDING，再失效重读恢复RECORDED/miss→hit |
| E04消息/持久去重 | broker前三条相同Key/Value、offset0/1/2；inbox durable first_offset0、receipt效果1；同event_id换qty真实Apply ErrConflict，SQL不变；App真实Close/Done后同group重建再次重复仍效果1、next3，随后Bob订单next4 |
| E05普通error/panic/取消/响应未知 | 真实三个SQL写后普通error/panic各自回滚；Gin恢复panic为非成功；cancel-before到达提交前门，客户端DeadlineExceeded且handler归零后SQL2单qty3stock7；lost-response先实际Commit/到达提交后门再DeadlineExceeded，SQL3单qty4stock6，原key直接权威GET确认COMMITTED，不重新写 |
| E06提前失败/最终owner | 实际Tx写后观察者errEarly，client cancel→App.Stop→真实hook观察Canceled→before_release=true→Release→Close/handler与四任务最终Done→caller最终Done；固定SQL仍3单qty4stock6；未Start App关闭tasks0；Run先Stop全部App再放自有门/join，最终精确清理和池关闭 |
| 独立窄借用证明 | 原App/client关闭、SQL0后fresh OpenResources；原账本两键借用可命中既有PENDING，未知已有fixture ErrOwnership且未删，跨namespace/畸形grant拒绝；原owner再显式授予四键，fresh API原key同完整Order但requestID不同、Bob404、consumer next1、API RECORDED；SQL1单qty2stock8效果1；fresh清理账本0不删源记录，原owner CleanupOwnedVia借fresh连接最后清5表4键/topic/group |
| 最终 | SQL orders3/qty4/stock6且stock+qty=10、outbox3全部sent、inbox3、receipts3、effect_count总3；逐单金额/币种/数量/状态/版本与合法owner/key组合，receipt必须对应源单和inbox；broker records5、group next5 |

Audit通过database/sql直接查GORM写出的表；不仅汇总数量，也拒绝意外key、非法金额/状态以及不匹配receipt。HTTP客户端严格解码Envelope并检查完整内容、Body关闭错误与尾随数据。Broker审计用无group短暂reader读真实原始记录，另查实际持久group offset，不改消费进度。

辅助测试直接检验实际Create失败出口RecoverDuplicate：1062与UNKNOWN/cleanup同时出现必须零次原单查询并保留原cause；安全1062才可匹配原payload返回；查询失败/冲突Join原1062。它们不冒称真实Commit/rollback网络故障。Preview已返回而tail尚未Done、caller实际panic与最终Done、Join错误身份、输入和值复制均有辅助断言。实际panic的SQL回滚由E05证明。

## 实际失败、诊断、修复与每次精确收尾

| run / 账本位置 | 实际运行结论 | 独立资源复验 |
| --- | --- | --- |
| `m37_f87d1e75a815b827`，模块`.local` | 最初控制台调用未保留exec sessionID，看到E01/E02后进程自然退出；最终退出码未捕获，**不计完整通过** | 5表6键1topic1group均当前不存在、offset空；本课相关进程已退出 |
| `m37_42b3d3c295a88ece`，模块`.local` | `.local/demo-second.log`，exit1：500ms客户端在目标门前结束，观察者只等Entered导致120s总预算失败，原错`等待actual SQL boundary cancel-before: context deadline exceeded` | 原运行日志记录App最终Done/清理；独立5表6键1topic1group不存在、offset空 |
| `m37_d93800eab17a4e3e`，模块`.local` | `.local/demo-third.log`，exit0；修复观察者同时等Entered/callerDone/root期限，客户端2500ms；此时缓存坏例尚未加强 | 5表8键1topic1group不存在、offset空 |
| `m37_f1dab910cb1cbc7f`，模块`.local` | `.local/demo-pre-cancel-witness-pass.log`，exit0；真实Commit→cache窗口、坏例SET/修复加入后通过；尚未加最终取消确认 | 5表8键1topic1group不存在、offset空 |
| `m37_dd87395db134f485`，`internal/lab/.local` | `.local/race-first-early-window.log`，race实库exit1；E06只cancel客户端便放门，服务端取消未到达而ORDER_COMMIT；固定Audit报告4单qty5stock5/outbox4/inbox3/receipt3/bad1。没有DATA RACE也不能接受业务顺序错误 | 5表8键1topic1group精确清理并独立不存在、offset空；环境恢复True |
| `m37_f1b86efed5bbdacc`，`internal/lab/.local` | `.local/race-stop-only-pass.log`，exit0；先同步Stop根ctx再放门，固定3单通过；最终版本进一步加真实hook取消确认和Commit前检查 | 5表8键1topic1group不存在、offset空；环境恢复True |
| `m37_b7ddc04eac77e685`，`internal/lab/.local` | `.local/race-cancel-witness-pass.log`，exit0；完整显式integration race；真实EARLY_CANCEL_OBSERVED before_release=true和固定SQL通过，此时尚未补窄借用入口 | 5表8键1topic1group不存在、offset空；环境恢复True；SQL0 |
| `m37_9df4289d3197573e`，模块`.local` | `.local/demo-cancel-witness-pass.log`，exit0；实际取消屏障、固定SQL与消息审计通过，此时尚未补窄借用入口 | 5表8键1topic1group不存在、offset空；SQL0 |
| `m37_3f873d89cc248084`，模块`.local` | `.local/recovery-final.log`，exit0；窄借用入口独立真实命令通过，旧client全关后fresh构造；初步证明后又加入能力数量限制，最终race也验证该新版本 | 5表4键1topic1group不存在、offset空；原/fresh SQL分别0 |
| `m37_916a70574199059d`，`internal/lab/.local` | `.local/race-borrow-before-ack-classifier-pass.log`，exit0；窄借用入口后的完整主链race，随后校正ACK后错误分类 | 5表8键1topic1group不存在、offset空；环境恢复True；SQL0 |
| `m37_6129476e5332f68e`，`internal/lab/.local` | 同一前版race日志，exit0；窄借用能力实库race，随后校正ACK后错误分类 | 5表4键1topic1group不存在、offset空；环境恢复True；原/fresh SQL分别0 |
| `m37_257a68b2fab9cb18`，模块`.local` | `.local/demo-borrow-before-ack-classifier-pass.log`，exit0；窄借用入口后的默认主链，随后校正ACK后错误分类 | 5表8键1topic1group不存在、offset空；SQL0 |
| `m37_1372b7607e6cbd81`，`internal/lab/.local` | `.local/race-final.log`，exit0；最终冻结版本完整主链race | 5表8键1topic1group不存在、offset空；环境恢复True；SQL0 |
| `m37_04c5e97d1efdf2c0`，`internal/lab/.local` | 同一最终race日志，exit0；最终冻结版本窄借用能力实库race | 5表4键1topic1group不存在、offset空；环境恢复True；原/fresh SQL分别0 |
| `m37_fd0ef16880d703c8`，模块`.local` | `.local/demo-final.log`，exit0；最终冻结版本默认主链 | 5表8键1topic1group不存在、offset空；SQL0 |

修复观察者select时另有一次编译失败：`invalid operation: cannot receive from non-channel func() <-chan struct{} ctx.Done`，漏掉调用括号，已修复为`ctx.Done()`；未把编译失败计入运行通过。以上保留实际失败而不降低断言、删除意外单后改写为通过或用mock替代。

每次资源删除只使用已登记精确名称，先停App、取消并join已接纳handler/真实启动任务，再清理数据、最后关闭client/池。清理预算来自新的Background，不能沿用已取消请求预算；任何清理失败Join原错误。清理失败或UNKNOWN优先于1062/原单匹配，不能被它们抹掉。正常、提前错误、SQL panic、超时及未启动App均走该owner收尾。全部复验结束SQL open/inuse0，本课进程0；未FLUSH Redis、扫描/删除共享前缀、重置他人group、控制服务或隧道。

## 复用边界与当前未验范围

导出构造/业务/owner接口在README列出。App借Resources、资源创建者持精确Manifest；OpenResources不创建表或topic、不自动取得旧对象owner。M37两个App之间沿用原Resources与Cache登记。fresh Resources通过BorrowCacheKeys显式借用原授权账本的精确键，不添加fresh清理registry；未知已有键仍ErrOwnership。Manifest.Validate只证明同run合法精确名字，caller授权必须来自创建者而非HTTP输入/不可信JSON；借用前必须确认旧App.Done/旧进程退出，保证同namespace一个业务写者，该能力不是分布式锁。原owner保留原Resources/账本，通过CleanupOwnedVia借同namespace新client清理自己的登记。新增键仍归新登记者，不广泛收编。RunRecovery已实证该窄接口，不提前做M38强杀矩阵。

RequestID新增独立Resources instance16hex前缀，计数跨App不归零、fresh Resources也不复用旧追踪号；原订单创建requestID仍由MySQL冻结不改，幂等键不受追踪ID影响。有限同步Hooks默认nil，门属于实验owner，实际Commit后的错误只保留UNKNOWN，不撤销既成效果。

最后源码核对修正了AfterRelayACK错误出口：真实ACK之后hook取消/错误Join UNKNOWN和原cause，不能让App将它当成纯停机context.Canceled吞掉；MarkSent RowsAffected非1也UNKNOWN。该分类修正后的相同默认入口/完整integration race重新通过，前版通过日志保留。没有把此源码分类核对冒称实际制造了ACK/sent间强杀；该真实故障窗口由M38验证。

当前已证明单进程真实网络协议和实库副作用、同group客户端重建、持久幂等、缓存窗口、正常/提前失败最终关闭。没有制造DB Commit响应丢失、relay ACK与sent间强杀、SQL消费Commit与offset间强杀、跨OS恢复、Kafka多节点/副本耐久性、SASL/TLS、Redis复制/驱逐/断电、长压测、多SKU锁顺序/死锁或生产认证。RPC没有锁库存或跨服务Tx，状态没有支付/发货。完整崩溃矩阵留M38，本课结束后不继续实施。
