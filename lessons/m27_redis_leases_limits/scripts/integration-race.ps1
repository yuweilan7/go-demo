param(
    [string]$ToolchainIndex = 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json'
)

# 脚本只由课程读者/验收者主动调用。$PSScriptRoot 是当前脚本目录，
# 用它定位所属module，避免默认共享cwd把检查跑到根module或其他课。
$moduleDirectory = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$toolchain = Get-Content -LiteralPath $ToolchainIndex -Raw | ConvertFrom-Json
foreach ($path in @($toolchain.cc, $toolchain.cxx, $toolchain.bin_dir)) {
    if (-not (Test-Path -LiteralPath $path)) { throw '安装者登记的64位编译器路径不存在' }
}

# 环境值只在这个PowerShell进程内改变，保存空值才能在finally恢复“原先未设置”。
# 不执行go env -w，不安装、不修改系统/用户PATH，也不控制基础设施。
$savedEnvironment = @{}
foreach ($name in @('CC', 'CXX', 'CGO_ENABLED', 'PATH', 'GOLEARN_M27_INTEGRATION')) {
    $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$raceExit = 1
try {
    $env:CC = $toolchain.cc
    $env:CXX = $toolchain.cxx
    $env:CGO_ENABLED = '1'
    $env:PATH = $toolchain.bin_dir + ';' + $savedEnvironment['PATH']
    $env:GOLEARN_M27_INTEGRATION = '1'
    go -C $moduleDirectory test -race -v -count=1 -timeout 65s ./...
    $raceExit = $LASTEXITCODE
} finally {
    foreach ($name in $savedEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], 'Process')
    }
}
exit $raceExit
