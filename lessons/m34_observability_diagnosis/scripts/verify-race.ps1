param([string]$ToolchainConfig = 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json')

$ErrorActionPreference = 'Stop'
$moduleRoot = Split-Path -Parent $PSScriptRoot
if (-not (Test-Path -LiteralPath $ToolchainConfig)) {
    throw '本机统一 64 位 C 工具链配置不存在；请传入实际 toolchain.json，或在已有兼容 CC 的 shell 直接执行 go test -race。'
}
$toolchain = Get-Content -LiteralPath $ToolchainConfig -Raw | ConvertFrom-Json
foreach ($path in @($toolchain.cc, $toolchain.cxx, $toolchain.bin_dir)) {
    if ([string]::IsNullOrWhiteSpace($path) -or -not (Test-Path -LiteralPath $path)) { throw '统一工具链配置中的编译器或 bin_dir 不存在。' }
}
# 只改脚本运行进程的环境，不用 go env -w、注册表或系统 PATH；退出时恢复原值。
$originalCC = $env:CC
$originalCXX = $env:CXX
$originalCGO = $env:CGO_ENABLED
$originalPath = $env:PATH
try {
    $env:CC = $toolchain.cc
    $env:CXX = $toolchain.cxx
    $env:CGO_ENABLED = '1'
    $env:PATH = $toolchain.bin_dir + ';' + $originalPath
    & $env:CC -dumpmachine
    if ($LASTEXITCODE -ne 0) { throw 'CC target query failed' }
    & go -C $moduleRoot test -race -count=1 -timeout 60s ./...
    if ($LASTEXITCODE -ne 0) { throw 'M34 race validation failed' }
}
finally {
    $env:CC = $originalCC
    $env:CXX = $originalCXX
    $env:CGO_ENABLED = $originalCGO
    $env:PATH = $originalPath
}
