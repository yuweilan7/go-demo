param([switch]$Integration)
$ErrorActionPreference = 'Stop'
$m37Module = Split-Path -Parent $PSScriptRoot
$m37Toolchain = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' -Raw | ConvertFrom-Json
$m37Names = @('CC','CXX','CGO_ENABLED','PATH','GOLEARN_M37_INTEGRATION')
$m37Before = @{}
foreach ($m37Name in $m37Names) {
    $m37Before[$m37Name] = @{ Exists = Test-Path -LiteralPath ('Env:' + $m37Name); Value = [Environment]::GetEnvironmentVariable($m37Name,'Process') }
}
try {
    $env:CC = $m37Toolchain.cc
    $env:CXX = $m37Toolchain.cxx
    $env:CGO_ENABLED = '1'
    $env:PATH = $m37Toolchain.bin_dir + ';' + $m37Before['PATH'].Value
    if ($Integration) { $env:GOLEARN_M37_INTEGRATION = '1' }
    go -C $m37Module test -race -count=1 -v -timeout 180s ./...
    $m37Code = $LASTEXITCODE
    if ($m37Code -ne 0) { throw "M37 race test exit $m37Code" }
} finally {
    foreach ($m37Name in $m37Names) {
        $m37Saved = $m37Before[$m37Name]
        if ($m37Saved.Exists) {
            [Environment]::SetEnvironmentVariable($m37Name,$m37Saved.Value,'Process')
        } else {
            Remove-Item -LiteralPath ('Env:' + $m37Name) -ErrorAction SilentlyContinue
        }
    }
    $m37Restored = $true
    foreach ($m37Name in $m37Names) {
        $m37Restored = $m37Restored -and ((Test-Path -LiteralPath ('Env:' + $m37Name)) -eq $m37Before[$m37Name].Exists) -and ([Environment]::GetEnvironmentVariable($m37Name,'Process') -ceq $m37Before[$m37Name].Value)
    }
    Write-Output "M37_ENV_RESTORED=$m37Restored"
    if (-not $m37Restored) { throw 'M37 process environment restoration failed' }
}
