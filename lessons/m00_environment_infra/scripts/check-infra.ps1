param(
    [string]$DeepHelpRoot = 'D:/IdeaProject/deephelp',
    [switch]$SkipSSH,
    [switch]$TemporaryTunnel,
    [string]$Report = ''
)

# 入口只读加载既有配置，不接管共享隧道，不安装依赖，不调用 DeepHelp full。
# 只有 -TemporaryTunnel 时创建/关闭本课自己保存的 SSH 进程。
# Python 子进程的工作目录继承课程调用者，源码和报告都属于本模块。
$ErrorActionPreference = 'Stop'
$moduleRoot = Split-Path -Parent $PSScriptRoot
if (-not $Report) { $Report = Join-Path $moduleRoot '.local/infra-current.json' }
$reportAbsolute = [IO.Path]::GetFullPath($Report)
$privateRoot = [IO.Path]::GetFullPath((Join-Path $moduleRoot '.local')) + [IO.Path]::DirectorySeparatorChar
if (-not $reportAbsolute.StartsWith($privateRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Report 必须位于本模块 .local/，不能写到 DeepHelp 或其他模块。'
}
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $reportAbsolute) | Out-Null
$python = Join-Path $DeepHelpRoot '.venv/Scripts/python.exe'
if (-not (Test-Path -LiteralPath $python)) { throw '既有 DeepHelp Python 不存在；先定位已有环境，不自动安装。' }
$arguments = @('-B', (Join-Path $PSScriptRoot 'check_infra.py'), '--deephelp-root', $DeepHelpRoot, '--report', $reportAbsolute)
if ($SkipSSH) { $arguments += '--skip-ssh' }
if ($TemporaryTunnel) { $arguments += '--temporary-tunnel' }
# -B 禁止导入既有环境时生成 pycache；参数没有密码/私钥正文，Python 也不输出原始异常。
& $python @arguments
exit $LASTEXITCODE
