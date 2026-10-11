param([string]$ToolchainFile = 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json')
$ErrorActionPreference = 'Stop'
$moduleRoot = Split-Path -Parent $PSScriptRoot
$goProgram = (Get-Command go -CommandType Application).Source
$ownedNames = @('GOBIN', 'CC', 'CXX', 'CGO_ENABLED', 'PATH', 'GOTOOLCHAIN')

# 本检查脚本是三态实验的 owner：保存 caller 原状态，在同一进程内调用被测脚本，
# 最后恢复全部实验变量。Value 比较是序数精确比较，不能把 $null/空串统一处理。
function Get-EnvironmentSnapshot([string[]]$Names) {
    $snapshot = @{}
    foreach ($name in $Names) {
        $exists = Test-Path -LiteralPath "Env:$name"
        $snapshot[$name] = [pscustomobject]@{
            Exists = $exists
            Value = $(if ($exists) { (Get-Item -LiteralPath "Env:$name").Value } else { $null })
        }
    }
    return $snapshot
}

# 只恢复本进程实验变量；未定义状态通过删除恢复，空串作为存在的值保留。
function Restore-EnvironmentSnapshot([hashtable]$Snapshot) {
    foreach ($name in $Snapshot.Keys) {
        if ($Snapshot[$name].Exists) {
            Set-Item -LiteralPath "Env:$name" -Value $Snapshot[$name].Value
        } else {
            Remove-Item -LiteralPath "Env:$name" -ErrorAction SilentlyContinue
        }
    }
}

# 检查调用完成后的存在性与精确原值；日志只给状态/长度，避免公开 caller PATH 内容。
function Assert-EnvironmentSnapshot([hashtable]$Expected, [string]$Label) {
    $actual = Get-EnvironmentSnapshot @($Expected.Keys)
    foreach ($name in @($Expected.Keys | Sort-Object)) {
        $samePresence = $Expected[$name].Exists -eq $actual[$name].Exists
        $sameValue = [string]::Equals($Expected[$name].Value, $actual[$name].Value, [System.StringComparison]::Ordinal)
        if (-not $samePresence -or -not $sameValue) { throw "$Label 环境未精确恢复: $name" }
        $state = if (-not $Expected[$name].Exists) { 'ABSENT' } elseif ($Expected[$name].Value -eq '') { 'EMPTY' } else { 'NONEMPTY' }
        $length = if ($Expected[$name].Exists) { $Expected[$name].Value.Length } else { 0 }
        Write-Output "ENV_RESTORED label=$Label name=$name state=$state length=$length presence_equal=$samePresence value_equal=$sameValue"
    }
}

# 三个输入状态由检查脚本显式构造；非空值也是模块私有合成值，没有真实凭据。
function Set-SyntheticEnvironment([string[]]$Names, [string]$State) {
    foreach ($name in $Names) {
        if ($State -eq 'ABSENT') {
            Remove-Item -LiteralPath "Env:$name" -ErrorAction SilentlyContinue
        } elseif ($State -eq 'EMPTY') {
            Set-Item -LiteralPath "Env:$name" -Value ''
        } else {
            $value = if ($name -eq 'GOBIN') { Join-Path $moduleRoot '.local/original-gobin-sentinel' } else { "m32-original-$name" }
            Set-Item -LiteralPath "Env:$name" -Value $value
        }
    }
}

# PATH 的三态不能阻止检查本身定位 Go。此函数作用域临时 alias 使用入口已解析的
# 真实 Go exe，被测 race 脚本仍运行真实工具链/测试；函数退出 alias 自动消失。
function Invoke-RealRace {
    Set-Alias -Name go -Value $goProgram -Scope Local
    & "$PSScriptRoot/test-race.ps1" -ToolchainFile $ToolchainFile
}

# 失败是实际 Go 拒绝非法 GOTOOLCHAIN；检查脚本只注入配置，未替换编译器/测试结果。
# caller 要同时看到预期主错误与 finally 后精确恢复，不能把任意失败当作通过。
function Assert-ExpectedGoFailure([scriptblock]$Action, [string]$Label, [string]$ExpectedMessage) {
    $observed = $null
    try { & $Action } catch { $observed = $_ }
    if (-not $observed) { throw "$Label 应当失败却成功" }
    if ($observed.Exception.Message -notmatch $ExpectedMessage) { throw $observed }
    Write-Output "EXPECTED_FAILURE label=$Label error=$($observed.Exception.Message)"
}

# 16 个运行/IDL/依赖文件逐字节保持；再生成只写 .local/regenerated。
function Get-RuntimeHashes {
    $hashes = @{}
    $files = @(Get-ChildItem -Path "$moduleRoot/cmd", "$moduleRoot/internal", "$moduleRoot/gen", "$moduleRoot/proto" -Recurse -File | Where-Object { $_.Extension -in '.go', '.proto' })
    $files += Get-Item -LiteralPath "$moduleRoot/go.mod", "$moduleRoot/go.sum"
    foreach ($file in $files) { $hashes[$file.FullName] = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash }
    if ($hashes.Count -ne 16) { throw "运行文件基线应为16，实际$($hashes.Count)" }
    return $hashes
}

$entryEnvironment = Get-EnvironmentSnapshot $ownedNames
$beforeHashes = Get-RuntimeHashes
Write-Output "ENV_CHECK_BEGIN pid=$PID powershell=$($PSVersionTable.PSVersion) go=$goProgram"
try {
    $env:GOTOOLCHAIN = 'auto'
    foreach ($state in @('ABSENT', 'EMPTY', 'NONEMPTY')) {
        Set-SyntheticEnvironment @('GOBIN') $state
        $expected = Get-EnvironmentSnapshot $ownedNames
        & "$PSScriptRoot/generate.ps1" -Check
        Assert-EnvironmentSnapshot $expected "generate-success-$state"
    }
    Restore-EnvironmentSnapshot $entryEnvironment
    $env:GOTOOLCHAIN = 'auto'
    foreach ($state in @('ABSENT', 'EMPTY', 'NONEMPTY')) {
        Set-SyntheticEnvironment @('CC', 'CXX', 'CGO_ENABLED', 'PATH') $state
        $expected = Get-EnvironmentSnapshot $ownedNames
        Invoke-RealRace
        Assert-EnvironmentSnapshot $expected "race-success-$state"
    }
    Restore-EnvironmentSnapshot $entryEnvironment
    foreach ($state in @('ABSENT', 'EMPTY', 'NONEMPTY')) {
        Set-SyntheticEnvironment @('GOBIN', 'CC', 'CXX', 'CGO_ENABLED', 'PATH') $state
        # 生成脚本靠 Get-Command 定位 Go，因此只在 race 调用期间测试 PATH 三态。
        Restore-EnvironmentSnapshot @{ PATH = $entryEnvironment['PATH'] }
        $env:GOTOOLCHAIN = 'm32-invalid'
        $expected = Get-EnvironmentSnapshot $ownedNames
        Assert-ExpectedGoFailure { & "$PSScriptRoot/generate.ps1" -Install -Check } "generate-failure-$state" '工具退出 1'
        Assert-EnvironmentSnapshot $expected "generate-failure-$state"
        Set-SyntheticEnvironment @('PATH') $state
        $expected = Get-EnvironmentSnapshot $ownedNames
        Assert-ExpectedGoFailure { Invoke-RealRace } "race-failure-$state" 'race退出 1'
        Assert-EnvironmentSnapshot $expected "race-failure-$state"
    }
    $afterHashes = Get-RuntimeHashes
    foreach ($file in @($beforeHashes.Keys | Sort-Object)) {
        if ($beforeHashes[$file] -ne $afterHashes[$file]) { throw "运行文件变化: $file" }
        Write-Output "RUNTIME_IDENTICAL file=$($file.Substring($moduleRoot.Length + 1)) sha256=$($afterHashes[$file])"
    }
    Write-Output 'ENV_CHECK_PASS generate_success=3 race_success=3 generate_expected_failure=3 race_expected_failure=3 runtime_files_identical=16'
} finally {
    Restore-EnvironmentSnapshot $entryEnvironment
    Assert-EnvironmentSnapshot $entryEnvironment 'check-script-caller-finally'
}
