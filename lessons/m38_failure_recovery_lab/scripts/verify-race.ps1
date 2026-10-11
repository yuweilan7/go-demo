param([switch]$Integration)
$ErrorActionPreference = 'Stop'
$m38Module = Split-Path -Parent $PSScriptRoot
$m38Tools = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' -Raw | ConvertFrom-Json
$m38Names = @('CC','CXX','CGO_ENABLED','PATH','GOLEARN_M38_INTEGRATION','GOLEARN_M38_CHILD_RACE')
$m38Before = @{}
foreach ($m38Name in $m38Names) {
    $m38Before[$m38Name] = @{ Exists = Test-Path -LiteralPath ('Env:' + $m38Name); Value = [Environment]::GetEnvironmentVariable($m38Name,'Process') }
}
try {
    $env:CC = $m38Tools.cc
    $env:CXX = $m38Tools.cxx
    $env:CGO_ENABLED = '1'
    $env:PATH = $m38Tools.bin_dir + ';' + $m38Before['PATH'].Value
    if ($Integration) {
        $env:GOLEARN_M38_INTEGRATION = '1'
        $env:GOLEARN_M38_CHILD_RACE = '1'
    }
    go -C $m38Module test -race -count=1 -v -timeout 300s ./...
    $m38Exit = $LASTEXITCODE
    if ($m38Exit -ne 0) { throw "M38 race exit $m38Exit" }
} finally {
    foreach ($m38Name in $m38Names) {
        $m38Saved = $m38Before[$m38Name]
        if ($m38Saved.Exists) {
            [Environment]::SetEnvironmentVariable($m38Name,$m38Saved.Value,'Process')
        } else {
            Remove-Item -LiteralPath ('Env:' + $m38Name) -ErrorAction SilentlyContinue
        }
    }
    $m38Restored = $true
    foreach ($m38Name in $m38Names) {
        $m38Restored = $m38Restored -and ((Test-Path -LiteralPath ('Env:' + $m38Name)) -eq $m38Before[$m38Name].Exists) -and ([Environment]::GetEnvironmentVariable($m38Name,'Process') -ceq $m38Before[$m38Name].Value)
    }
    Write-Output "M38_ENV_RESTORED=$m38Restored"
    if (-not $m38Restored) { throw 'M38 process environment restoration failed' }
}
