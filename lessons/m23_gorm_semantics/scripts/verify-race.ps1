param(
    [string]$ToolchainIndex = 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json'
)

# 本脚本只读取安装者已有工具链，并临时设置本进程的C/race环境。
# finally逐项还原，即使go失败也不会改全局PATH、go env或安装任何东西。
$ErrorActionPreference = 'Stop'
$moduleRoot = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$toolchain = Get-Content -LiteralPath $ToolchainIndex -Raw | ConvertFrom-Json
foreach ($path in @($toolchain.cc, $toolchain.cxx, $toolchain.bin_dir)) {
    if (-not (Test-Path -LiteralPath $path)) { throw '安装者登记的race工具链路径不存在' }
}
$environmentNames = @('CC', 'CXX', 'CGO_ENABLED', 'PATH', 'GOLEARN_INTEGRATION')
$savedEnvironment = @{}
foreach ($name in $environmentNames) { $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
$code = 1
try {
    $env:CC = $toolchain.cc
    $env:CXX = $toolchain.cxx
    $env:CGO_ENABLED = '1'
    $env:PATH = $toolchain.bin_dir + [IO.Path]::PathSeparator + $savedEnvironment['PATH']
    $env:GOLEARN_INTEGRATION = '1'
    & $toolchain.cc -dumpmachine
    if ($LASTEXITCODE -ne 0) { throw 'race编译器不能运行' }
    $reportDirectory = Join-Path $moduleRoot '.local'
    New-Item -ItemType Directory -Force -Path $reportDirectory | Out-Null
    $reportPath = Join-Path $reportDirectory 'race-final.txt'
    # stdout/SQL只有本课合成值，密码/URI不会进入报告；报告本身仍放忽略目录。
    & go -C $moduleRoot test -race -v -count=1 -timeout 120s ./... *> $reportPath
    $code = $LASTEXITCODE
    Get-Content -LiteralPath $reportPath
} finally {
    foreach ($name in $environmentNames) {
        # Windows本机.NET把SetEnvironmentVariable(null)保留成空字符串；
        # 原来不存在的变量用Env provider移除，才能还原“不存在”的精确状态。
        if ($null -eq $savedEnvironment[$name]) {
            Remove-Item -LiteralPath ('Env:' + $name) -ErrorAction SilentlyContinue
        } else {
            [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], 'Process')
        }
    }
    $restored = $true
    foreach ($name in $environmentNames) {
        if ([Environment]::GetEnvironmentVariable($name, 'Process') -cne $savedEnvironment[$name]) { $restored = $false }
    }
    Write-Output ('process_environment_restored=' + $restored)
    if (-not $restored) { $code = 1 }
}
exit $code
