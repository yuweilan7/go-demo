# M08 实际交付与主 agent 验收记录

状态：**ACCEPTED**。2026-10-12 完成本模块主验收；第 0 层尚未全部验收和提交，本文件不代表整层交付已完成。

原执行者完整实现了 README、11 个手写 Go 文件、独立断言与真实证据，随后连续连接中断，尚未保存 STATUS。主 agent 确认原执行者无活跃写入后接管最终记录，保留既有代码与日志；只澄清一处 Java 普通整数加法也不自动报溢出的注释，并在 README 链接已完成的 race 证据，没有改变行为。

## 工具链与作用范围

实际环境为 `go1.27.0 windows/amd64`，GOROOT `D:/Go`、GOTOOLCHAIN `auto`、GOEXPERIMENT 空。独立 module `go-demo/lessons/m08_json_money_time` 使用 `go 1.27`，只有标准库，无 go.sum、其他课程导入或共享 go.work。实施目录为 `C:/Users/Lenovo/.codex/worktrees/go-course-m08/go-demo/lessons/m08_json_money_time`。

默认 Go 1.27 下 `encoding/json/v2` 与 `encoding/json/jsontext` 实际可构建；主线继续使用 `encoding/json` 的 v1 API/兼容语义。独立 v2 入口验证字段形状及输入策略，不能用内部实现变化推断所有旧 DTO 自动采用 v2 协议。环境证据在 [toolchain.txt](evidence/toolchain.txt)。

## 实际命令与结果

执行者日志与主 agent 复验分开记录。以下主 agent 命令全部显式 `go -C <上述模块绝对目录>`，不依赖根 module 的测试范围。

| 命令 | 主 agent 实际结果 | 检查内容 |
| --- | --- | --- |
| `run ./cmd/demo` | exit 0 | E01 JSON/PATCH、E02 金额、E03 时间全部执行 |
| `run ./cmd/jsonv2` | exit 0 | v1、v2 默认、v1 兼容与后置严格选项；失败值不安装 |
| `test -count=1 -v -timeout 40s ./...` | exit 0；10 个顶层测试通过 | 8 项 contract 测试、2 项 jsonv2 测试及表格子案例 |
| `vet ./...` | exit 0，无诊断 | 独立 module 静态检查 |
| `test -race -count=1 -timeout 40s ./...` | exit 0；jsonv2 1.209s、contract 2.079s | 当前测试路径没有 race 报告 |

执行者原始证据：[demo.txt](evidence/demo.txt)、[jsonv2.txt](evidence/jsonv2.txt)、[test.txt](evidence/test.txt)、[vet.txt](evidence/vet.txt)、[race.txt](evidence/race.txt)。主 agent 命令原文保存在主工作区已忽略的 `.local/course-orchestrator/acceptance/M08-*.txt`，正式验收记录为 `.local/course-orchestrator/acceptance/M08.json`。

race 使用共享 `toolchain.json` 中 WinLibs GCC 16.2.0 amd64 的 CC/CXX/bin_dir，只在命令进程中设置环境与 PATH，不修改系统设置。执行者另实际使用进程级 `GOEXPERIMENT=nojsonv2`：v2 的 `go list` exit 1，准确得到 build constraints 排除源码；核心 demo 仍 exit 0，见 [nojsonv2.txt](evidence/nojsonv2.txt) 和 [nojsonv2_core.txt](evidence/nojsonv2_core.txt)。这只是 opt-out 的已知构建条件，不是默认课程入口失败。

## 已验收的行为与失败路径

| 实验 | 独立观察与主验收结论 |
| --- | --- |
| E01 字段与三态 | 普通 DTO 的缺失/null/0会合并；Field[T] 区分 Present/Null/Value。quantity 缺失不变，null/0拒绝，2件得598分；note null清除而空字符串仍为值；tags []清空、null拒绝。失败或修改候选 slice/指针不改原订单 |
| E01 输入边界 | 重复名字、转义后同名、大小写变体、未知字段、第二份文档/尾随垃圾、错误类型、超出整数范围、无效 UTF-8、超长/过深输入真实拒绝。解码通过的 quantity=0 仍被业务层拒绝 |
| E02 大整数与金额 | 通用 map 把9007199254740993读成9007199254740992；UseNumber与具体int64保留原值，线上ID使用规范字符串。运行时0.29*100转整数丢成28；精确十进制修复为29。1.005 HalfUp为101，JPY/KWD倍率不同，加法/乘法/舍入最终溢出拒绝 |
| E02 汇率 | 方向、两币种精度、正分子分母、有效窗口与舍入均验证；105分CNY按7/50主单位比例得到USD15分，1SGD得到113JPY。无效/到期报价和最终溢出拒绝 |
| E03 时间 | 同一截止在UTC/上海/纽约判断一致，截止边界已过期；无时区上海文本误按UTC解析多活8小时；秒/毫秒错位产生1970/58748年，固定毫秒契约拒绝不在[2000,2100)的值。JSON恢复不含单调钟，Equal保留同一时间点 |
| E04 JSON v2 | 实际输出字段形状对照、重复默认拒绝、未知默认忽略/显式拒绝、大小写选项、null清零、非法UTF-8失败、Duration默认无表示、冲突tag失败与兼容选项均运行并解析；没有静默切换订单核心协议 |

主 agent 已阅读全部手写源码、测试和 README 的四组完整解析，核对 caller、输入、值复制/共享、候选安装、error边界以及准确版本和 Java 类比。原理、反例与修复均直接提供，不布置额外 exercise。

## 资源与未验证边界

本课代码同步、有限次数执行，不启动监听器、课程 goroutine、timer或故障代理，不写数据库或文件业务数据；全部 run/test 进程已结束。金额/汇率、订单和时间输入都是合成数据。没有改变系统时间、真实资金、线上协议或共享服务。

验收证明本地字段契约、整数金额、候选值隔离、time API与当前JSON实现行为；不证明数据库约束/事务、并发库存、真实商品定价、账户授权、request_id持久幂等、外部汇率、系统改钟/NTP或任意Unicode规则。README明确说明孤立UTF-16代理项、负金额/退款、DST完整业务政策等额外边界。

未执行 git add、commit、push。整层提交由主 agent 在第0层所有模块ACCEPTED后统一完成。
