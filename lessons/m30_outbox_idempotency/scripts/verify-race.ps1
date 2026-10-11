param([switch]$Integration)
$ErrorActionPreference = 'Stop'
$lessonModule = Split-Path -Parent $PSScriptRoot
$lessonToolchain = Get-Content 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' -Raw | ConvertFrom-Json
$lessonNames = @('CC', 'CXX', 'CGO_ENABLED', 'PATH', 'GOLEARN_M30_INTEGRATION')
$lessonSaved = @{}
foreach ($lessonName in $lessonNames) { $lessonSaved[$lessonName] = [Environment]::GetEnvironmentVariable($lessonName, 'Process') }
$lessonCode = 1
try {
    $env:CC = $lessonToolchain.cc
    $env:CXX = $lessonToolchain.cxx
    $env:CGO_ENABLED = '1'
    $env:PATH = $lessonToolchain.bin_dir + ';' + $lessonSaved['PATH']
    if ($Integration) { $env:GOLEARN_M30_INTEGRATION = '1' } else { $env:GOLEARN_M30_INTEGRATION = '' }
    & go -C $lessonModule test -race -v -count=1 -timeout 6m ./...
    $lessonCode = $LASTEXITCODE
} finally {
    foreach ($lessonName in $lessonNames) {
        # PowerShell把传给.NET string参数的$null转换为空字符串；缺失变量应真正删除。
        if ($null -eq $lessonSaved[$lessonName]) {
            Remove-Item -LiteralPath ('Env:' + $lessonName) -ErrorAction SilentlyContinue
        } else {
            [Environment]::SetEnvironmentVariable($lessonName, $lessonSaved[$lessonName], 'Process')
        }
    }
    $lessonRestored = $true
    foreach ($lessonName in $lessonNames) { if ([Environment]::GetEnvironmentVariable($lessonName, 'Process') -ne $lessonSaved[$lessonName]) { $lessonRestored = $false } }
    Write-Output ('M30_RACE_ENV_RESTORED=' + $lessonRestored)
    if (-not $lessonRestored) { $lessonCode = 1 }
}
exit $lessonCode
