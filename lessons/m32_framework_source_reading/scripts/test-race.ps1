param([string]$ToolchainFile = 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json')
$ErrorActionPreference = 'Stop'
$moduleRoot = Split-Path -Parent $PSScriptRoot
$courseTools = Get-Content -LiteralPath $ToolchainFile -Raw | ConvertFrom-Json
# 每个值连同存在性一起保存，避免把原本不存在的变量恢复成一个空串变量。
# Hashtable 只是本次脚本调用的快照；finally 逐项恢复 caller 进程的三态。
$savedEnvironment = @{}
foreach ($name in @('CC', 'CXX', 'CGO_ENABLED', 'PATH')) {
    $savedEnvironment[$name] = [pscustomobject]@{
        Exists = Test-Path -LiteralPath "Env:$name"
        Value = [System.Environment]::GetEnvironmentVariable($name, 'Process')
    }
}
try {
    foreach ($path in @($courseTools.cc, $courseTools.cxx, $courseTools.bin_dir)) { if (-not (Test-Path -LiteralPath $path)) { throw "已登记race工具不存在: $path" } }
    $env:CC = $courseTools.cc; $env:CXX = $courseTools.cxx; $env:CGO_ENABLED = '1'; $env:PATH = "$($courseTools.bin_dir);$($savedEnvironment['PATH'].Value)"
    & $courseTools.cc --version | Select-Object -First 1
    & $courseTools.cc -dumpmachine
    go -C $moduleRoot test -race -v -count=1 -timeout 90s ./...
    if ($LASTEXITCODE -ne 0) { throw "race退出 $LASTEXITCODE" }
} finally {
    # 只恢复当前脚本进程；原不存在就删除，原存在则精确恢复（包括空串）。
    # 未改系统代理/全局PATH/go env，旧32位默认gcc不参与race。
    foreach ($name in @('CC', 'CXX', 'CGO_ENABLED', 'PATH')) {
        if ($savedEnvironment[$name].Exists) {
            Set-Item -LiteralPath "Env:$name" -Value $savedEnvironment[$name].Value
        } else {
            Remove-Item -LiteralPath "Env:$name" -ErrorAction SilentlyContinue
        }
    }
}
