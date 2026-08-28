# go-demo

一个通过“阅读注释 + 运行代码 + 对比输出”学习 Go 的示例项目。

## 环境要求

- Go 1.27 或更高版本
- 可选：VS Code、官方 Go 扩展、Code Runner

## 运行示例

在项目根目录执行：

```powershell
go run ./examples/program-structure
```

检查全部包能否正常编译和测试：

```powershell
go test ./...
```

## 当前示例

### Go 程序与包的组织结构

目录：`examples/program-structure`

这个示例通过详细中文注释和控制台输出演示：

- `package main`、`func main` 与 `init` 的执行关系；
- 变量、常量、表达式与零值；
- `fmt.Print`、`Println` 和 `Printf` 的区别；
- 同一包由多个独立 `.go` 文件共同组成；
- 大写标识符导出与小写标识符包内可见；
- 模块路径、目录路径与包名之间的关系；
- 目录名和包名不同虽然合法，但通常不推荐。

## VS Code

项目提交了 `.vscode` 工作区配置。使用 Code Runner 运行 Go 文件时，它会进入当前文件目录并执行：

```powershell
go run .
```

这样可以正确编译同一包中的所有 Go 文件，而不是只运行当前打开的单个文件。

普通库包没有 `func main()`，不能单独运行。例如 `mymath` 和 `oddname` 应由上层示例程序导入并调用。
