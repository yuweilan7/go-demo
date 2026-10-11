param(
    # 编译器仅在此脚本进程范围内传给 Go，不写 go env -w，也不改系统 PATH。
    [string] $CCompiler = '',
    [string] $CxxCompiler = '',
    [string] $CompilerBinDir = '',
    [switch] $IncludeRace,
    [switch] $RaceOnly,
    [switch] $IncludeDelve
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$moduleDir = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$evidenceDir = Join-Path $moduleDir 'evidence'
$privateDir = Join-Path $moduleDir '.local'
New-Item -ItemType Directory -Path $evidenceDir, $privateDir -Force | Out-Null

# 每个记录都保存实际输出、退出码和工作目录。预期 FAIL 必须匹配故障原因，任意非零并不等于复现成功。
function Invoke-Recorded {
    param(
        [string] $Name,
        [string] $Program,
        [string[]] $Arguments,
        [bool] $ExpectFailure = $false,
        [string] $RequiredPattern = '',
        [string] $ForbiddenPattern = ''
    )
    $lines = & $Program @Arguments 2>&1 | ForEach-Object { $_.ToString() }
    $code = $LASTEXITCODE
    $output = $lines -join [Environment]::NewLine
    $record = @(
        "workdir=$moduleDir",
        "command=$Program $($Arguments -join ' ')",
        "exit_code=$code",
        "expected_failure=$ExpectFailure",
        $output
    ) -join [Environment]::NewLine
    Set-Content -LiteralPath (Join-Path $evidenceDir "$Name.txt") -Value $record -Encoding utf8
    if (($ExpectFailure -and $code -eq 0) -or (!$ExpectFailure -and $code -ne 0)) {
        throw "$Name unexpected exit=$code; inspect evidence/$Name.txt"
    }
    if ($RequiredPattern -and $output -notmatch $RequiredPattern) {
        throw "$Name did not show required fact: $RequiredPattern"
    }
    if ($ForbiddenPattern -and $output -match $ForbiddenPattern) {
        throw "$Name showed unexpected diagnostic: $ForbiddenPattern"
    }
    Write-Output "$Name verified; exit=$code; expectedFailure=$ExpectFailure"
}

$originalCC = $env:CC
$originalCXX = $env:CXX
$originalCGO = $env:CGO_ENABLED
$originalPath = $env:PATH
Push-Location -LiteralPath $moduleDir
try {
    if ($CCompiler) {
        $resolvedCompiler = (Resolve-Path -LiteralPath $CCompiler).Path
        $env:CC = $resolvedCompiler
        $env:CGO_ENABLED = '1'
    }
    if ($CxxCompiler) {
        $env:CXX = (Resolve-Path -LiteralPath $CxxCompiler).Path
    }
    if ($CompilerBinDir) {
        $resolvedBinDir = (Resolve-Path -LiteralPath $CompilerBinDir).Path
        $env:PATH = $resolvedBinDir + [IO.Path]::PathSeparator + $originalPath
    }
    if (!$RaceOnly) {
    Invoke-Recorded 'environment' 'go' @('-C', $moduleDir, 'env', '-json', 'GOVERSION', 'GOOS', 'GOARCH', 'CGO_ENABLED', 'GOTOOLCHAIN', 'GOMOD', 'GOWORK', 'CC')
    Invoke-Recorded 'demo' 'go' @('-C', $moduleDir, 'run', './cmd/demo') -RequiredPattern 'state=pending credited=0 count=0'
    Invoke-Recorded 'tests' 'go' @('-C', $moduleDir, 'test', '-v', '-count=1', '-shuffle=1729', '-timeout=30s', './...') -RequiredPattern 'virtual elapsed=10s'
    Invoke-Recorded 'vet' 'go' @('-C', $moduleDir, 'vet', './...')
    Invoke-Recorded 'vet-faults' 'go' @('-C', $moduleDir, 'vet', '-tags=faults', './faults')
    Invoke-Recorded 'fault-logic' 'go' @('-C', $moduleDir, 'test', '-tags=faults', '-run=^TestWrongCommit', '-count=1', '-timeout=30s', './faults') -ExpectFailure $true -RequiredPattern 'rejected payment mutated business'
    Invoke-Recorded 'fault-shared' 'go' @('-C', $moduleDir, 'test', '-tags=faults', '-run=^TestSharedFixture', '-count=1', '-parallel=2', '-timeout=30s', './faults') -ExpectFailure $true -RequiredPattern 'shared fixture state=paid'
    Invoke-Recorded 'vet-fatal' 'go' @('-C', $moduleDir, 'vet', '-tags=fatalfault', './faults') -ExpectFailure $true -RequiredPattern 'non-test goroutine'
    Invoke-Recorded 'fault-fatal' 'go' @('-C', $moduleDir, 'test', '-vet=off', '-tags=fatalfault', '-run=^TestFatalInWorker$', '-v', '-count=1', '-timeout=30s', './faults') -ExpectFailure $true -RequiredPattern 'owner reached its final line'
    Invoke-Recorded 'fuzz-amount' 'go' @('-C', $moduleDir, 'test', './internal/order', '-run=^$', '-fuzz=^FuzzParseCents$', '-fuzztime=3s', '-parallel=2', '-timeout=30s') -RequiredPattern 'PASS'
    Invoke-Recorded 'cache-first' 'go' @('-C', $moduleDir, 'test', './internal/order', '-run=^TestParseCents$')
    Invoke-Recorded 'cache-second' 'go' @('-C', $moduleDir, 'test', './internal/order', '-run=^TestParseCents$') -RequiredPattern '\(cached\)'
    }
    if ($IncludeRace -or $RaceOnly) {
        Invoke-Recorded 'race-environment' 'go' @('-C', $moduleDir, 'env', '-json', 'GOVERSION', 'GOOS', 'GOARCH', 'CGO_ENABLED', 'CC', 'CXX')
        if ($CCompiler) {
            Invoke-Recorded 'race-compiler' $resolvedCompiler @('--version')
            Invoke-Recorded 'race-runtime-library' $resolvedCompiler @('--print-file-name=libsynchronization.a') -RequiredPattern '[/\\][^\r\n]*libsynchronization\.a'
        }
        Invoke-Recorded 'race-safe' 'go' @('-C', $moduleDir, 'test', '-race', '-count=1', '-timeout=60s', '-parallel=2', './...')
        Invoke-Recorded 'race-fault' 'go' @('-C', $moduleDir, 'test', '-race', '-tags=faults', '-run=^TestRaceSharedCounter$', '-v', '-count=1', '-timeout=30s', './faults') -ExpectFailure $true -RequiredPattern 'WARNING: DATA RACE'
        Invoke-Recorded 'race-logic' 'go' @('-C', $moduleDir, 'test', '-race', '-tags=faults', '-run=^TestWrongCommit', '-count=1', '-timeout=30s', './faults') -ExpectFailure $true -RequiredPattern 'rejected payment mutated business' -ForbiddenPattern 'WARNING: DATA RACE'
        Invoke-Recorded 'race-shared' 'go' @('-C', $moduleDir, 'test', '-race', '-tags=faults', '-run=^TestSharedFixture', '-parallel=2', '-count=1', '-timeout=30s', './faults') -ExpectFailure $true -RequiredPattern 'shared fixture state=paid' -ForbiddenPattern 'WARNING: DATA RACE'
    }
    if ($IncludeDelve) {
        $debugBinary = Join-Path $privateDir 'm10-demo.exe'
        Invoke-Recorded 'debug-build' 'go' @('-C', $moduleDir, 'build', '-gcflags=all=-N -l', '-o', $debugBinary, './cmd/demo')
        Invoke-Recorded 'delve-version' 'dlv' @('version')
        Invoke-Recorded 'delve' 'dlv' @('exec', $debugBinary, '--allow-non-terminal-interactive=true', "--init=$(Join-Path $PSScriptRoot 'delve.commands')", '--', '-experiment=logic') -RequiredPattern 'Cents: 1300'
    }
}
finally {
    Pop-Location
    # 恢复调用者原有环境；空/null 会按 PowerShell 语义撤销本次进程变量。
    $env:CC = $originalCC
    $env:CXX = $originalCXX
    $env:CGO_ENABLED = $originalCGO
    $env:PATH = $originalPath
}
