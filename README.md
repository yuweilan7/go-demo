# go-demo

一个通过“阅读注释 + 运行代码 + 对比输出”学习 Go 的示例项目。

面向熟练 Java 工程师的 Go 后端课程位于 [learning_prompts/README.md](learning_prompts/README.md)：M00–M35、M37–M39 共 39 份完整 prompt，包含真实中间件实验、必要硬依赖与并行批次。每课按 [中文教学与源码注释规范](learning_prompts/TEACHING_STYLE.md) 交付文档、详细注释代码和可运行实验。课程正在按 [分层实施与监测约定](learning_prompts/ORCHESTRATION.md) 执行，整层验收后汇总并统一提交。第 0～3 层共 36 课已验收，课程入口见 [lessons/README.md](lessons/README.md)，实际验证范围见 [lessons/ACCEPTANCE.md](lessons/ACCEPTANCE.md)。

## 环境要求

- Go 1.27 或更高版本
- 可选：VS Code、官方 Go 扩展、Code Runner

## 运行示例

在项目根目录执行：

```powershell
go run ./examples/basic-syntax/variables
go run ./examples/advanced-syntax/goroutines
go run ./examples/internals/packageinit
```

检查全部包能否正常编译和测试：

```powershell
go test ./...
```

## 目录结构

目录名使用英文，避免中文进入 Go 包的导入路径；下面保留中文定位。

```text
examples/
├── basic-syntax/       # 基础语法
├── advanced-syntax/    # 语法进阶
└── internals/          # 原理剖析
```

### 基础语法 `examples/basic-syntax`

| 主题 | 目录 |
| --- | --- |
| 依赖管理 | `go-modules` |
| 异常捕获 | `panic-recover` |
| error | `errors` |
| defer | `defer` |
| 接口 | `interfaces` |
| 方法 | `methods-embedding` |
| 指针 | `pointers` |
| 函数 | `functions` |
| 循环 | `loops` |
| 条件句 | `conditionals` |
| 数组、切片与 map | `arrays-slices-maps` |
| 结构体 | `structs` |
| 运算符 | `operators` |
| 常量 | `constants` |
| 变量 | `variables` |
| 基础补充：程序结构、格式化输出、闭包状态、接口断言、指针内存、结构体指针选择器 | `program-structure`、`fmt-verbs`、`closurestate`、`interfaceassertions`、`pointermemory`、`struct-pointer-selector` |

### 语法进阶 `examples/advanced-syntax`

| 主题 | 目录 |
| --- | --- |
| Goroutine | `goroutines` |
| Channel | `channels` |
| Sync | `sync-primitives` |
| Select | `select` |
| Context | `context` |
| 定时器 | `timers` |
| 协程池 | `goroutine-pool` |
| 反射 | `reflection` |

### 原理剖析 `examples/internals`

| 主题 | 目录 |
| --- | --- |
| Go 包初始化顺序 | `packageinit` |

## 当前示例

### Go 程序与包的组织结构

目录：`examples/basic-syntax/program-structure`

这个示例通过详细中文注释和控制台输出演示：

- `package main`、`func main` 与 `init` 的执行关系；
- 变量、常量、表达式与零值；
- `fmt.Print`、`Println` 和 `Printf` 的区别；
- 同一包由多个独立 `.go` 文件共同组成；
- 大写标识符导出与小写标识符包内可见；
- 模块路径、目录路径与包名之间的关系；
- 目录名和包名不同虽然合法，但通常不推荐。

### Go 变量、零值与短变量声明

目录：`examples/basic-syntax/variables`

这个示例通过可运行对比演示：

- `var` 的显式类型、类型推断、多个变量，以及分组声明只是源码组织形式；
- `int`、固定宽度整数、浮点数、复数的类型与范围；
- `local`、`&local`、`*counter` 的区别，以及返回局部变量地址为何安全；
- 数值、布尔、字符串以及指针、slice、map、channel、func、interface 的零值；
- `:=` 可以混合“给旧变量赋值 + 声明新变量”的准确规则；
- 普通赋值、多重赋值、交换以及空白标识符 `_`；
- 内层短变量声明造成的变量遮蔽；
- 局部变量未使用时的编译错误；
- 变量的词法作用域、运行时生命周期与逃逸分析之间的区别。

### defer、return 与 panic/recover

目录：`examples/basic-syntax/defer`

这个示例通过可运行对比演示：

- defer 的注册时机、普通实参求值和闭包读取变量的区别；
- 多个 defer 后注册先执行（LIFO）；
- 成功取得资源后立即安排清理，并妥善处理 `Close` 错误；
- return 确定结果值、执行 defer、返回调用者的先后关系；
- 具名结果参数和匿名结果对 defer 修改的不同反应；
- panic 展开调用栈时如何执行 defer，以及 recover 的作用范围。

### Go error、错误链与自定义错误

目录：`examples/basic-syntax/errors`

这个示例通过可运行对比演示：

- `error` 接口和 `(结果, error)` 返回约定；
- `errors.New` 创建的错误为何可以比较，但相同文案并不代表同一个错误；
- 哨兵错误以及 `errors.Is` 的语义匹配；
- `fmt.Errorf` 中 `%w` 和 `%v` 对错误链的不同影响；
- 带错误码的自定义错误、`Unwrap` 与 `errors.AsType`；
- 不可比较的动态错误值和 typed nil 两个接口陷阱。

### Go panic、defer 与 recover

目录：`examples/basic-syntax/panic-recover`

这个示例通过调用顺序和输出演示：

- `panic` 如何终止当前函数的正常控制流并沿调用栈向外传播；
- 栈展开时，各层已经注册的 `defer` 如何执行；
- `recover` 成功后，被中断的函数为何直接返回，而不是从 `panic` 下一行继续；
- 调用者为何可以从被恢复函数的调用点之后继续执行；
- `recover` 在普通代码或普通辅助函数中调用时为何返回 `nil`；
- 每个需要隔离的 goroutine 为何必须在自身入口设置恢复逻辑。

## VS Code

项目提交了 `.vscode` 工作区配置。使用 Code Runner 运行 Go 文件时，它会进入当前文件目录并执行：

```powershell
go run .
```

这样可以正确编译同一包中的所有 Go 文件，而不是只运行当前打开的单个文件。

普通库包没有 `func main()`，不能单独运行。例如 `mymath` 和 `oddname` 应由上层示例程序导入并调用。
