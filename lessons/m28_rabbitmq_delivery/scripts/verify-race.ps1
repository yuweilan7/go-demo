param(
    [switch]$Integration,
    [string]$ToolchainIndex = 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json'
)

# 本脚本只给当前PowerShell/Go子进程设置编译器，安装者拥有的工具链JSON只读。
# -Integration同时运行真实RabbitMQ；没有开关时仅测试账本/owner收尾，不能算MQ验收。
$ErrorActionPreference = 'Stop'
$lessonDirectory = Split-Path -Parent $PSScriptRoot
$courseCompiler = Get-Content -LiteralPath $ToolchainIndex -Raw | ConvertFrom-Json
if (-not (Test-Path -LiteralPath $courseCompiler.cc)) { throw '登记的amd64 C编译器不存在' }
$savedCourseEnvironment = @{}
foreach ($name in @('CC', 'CXX', 'CGO_ENABLED', 'PATH', 'GOLEARN_M28_INTEGRATION')) {
    $savedCourseEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
try {
    $env:CC = $courseCompiler.cc
    $env:CXX = $courseCompiler.cxx
    $env:CGO_ENABLED = '1'
    $env:PATH = $courseCompiler.bin_dir + ';' + $env:PATH
    $env:GOLEARN_M28_INTEGRATION = if ($Integration) { '1' } else { '0' }
    & go -C $lessonDirectory test -race -v -count=1 -timeout 120s ./...
    if ($LASTEXITCODE -ne 0) { throw "M28 race失败，go退出码=$LASTEXITCODE" }
}
finally {
    # 即使Go返回失败/脚本throw也恢复原值；null恢复为不存在的进程环境变量。
    foreach ($name in $savedCourseEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $savedCourseEnvironment[$name], 'Process')
    }
}
