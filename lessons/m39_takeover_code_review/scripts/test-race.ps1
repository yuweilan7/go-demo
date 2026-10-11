param([switch]$Integration)
$ErrorActionPreference = 'Stop'
$m39Module = Split-Path -Parent $PSScriptRoot
$m39Tools = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' -Raw | ConvertFrom-Json
foreach ($m39Path in @($m39Tools.cc,$m39Tools.cxx,$m39Tools.bin_dir)) {
    if (-not (Test-Path -LiteralPath $m39Path)) { throw '登记的64位race工具链不存在' }
}
$m39Names = @('CC','CXX','CGO_ENABLED','PATH','GOLEARN_M39_INTEGRATION')
$m39Before = @{}
foreach ($m39Name in $m39Names) {
    $m39Before[$m39Name] = @{ Exists = Test-Path -LiteralPath ('Env:' + $m39Name); Value = [Environment]::GetEnvironmentVariable($m39Name,'Process') }
}
try {
    $env:CC = $m39Tools.cc
    $env:CXX = $m39Tools.cxx
    $env:CGO_ENABLED = '1'
    $env:PATH = $m39Tools.bin_dir + ';' + $m39Before['PATH'].Value
    if ($Integration) { $env:GOLEARN_M39_INTEGRATION = '1' }
    & $m39Tools.cc -dumpmachine
    go -C $m39Module test -race -v -count=1 -timeout 180s ./...
    $m39Exit = $LASTEXITCODE
    if ($m39Exit -ne 0) { throw "M39 race exit $m39Exit" }
} finally {
    foreach ($m39Name in $m39Names) {
        $m39Saved = $m39Before[$m39Name]
        if ($m39Saved.Exists) { Set-Item -LiteralPath ('Env:' + $m39Name) -Value $m39Saved.Value }
        else { Remove-Item -LiteralPath ('Env:' + $m39Name) -ErrorAction SilentlyContinue }
    }
    $m39Restored = $true
    foreach ($m39Name in $m39Names) {
        $m39Restored = $m39Restored -and ((Test-Path -LiteralPath ('Env:' + $m39Name)) -eq $m39Before[$m39Name].Exists) -and ([Environment]::GetEnvironmentVariable($m39Name,'Process') -ceq $m39Before[$m39Name].Value)
    }
    Write-Output "M39_ENV_RESTORED=$m39Restored"
    if (-not $m39Restored) { throw 'M39当前进程环境存在性/精确原值恢复失败' }
}
