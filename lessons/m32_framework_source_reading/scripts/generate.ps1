param([switch]$Install, [switch]$Check)
$ErrorActionPreference = 'Stop'
$moduleRoot = Split-Path -Parent $PSScriptRoot
$privateRoot = Join-Path $moduleRoot '.local'
$toolBin = Join-Path $privateRoot 'bin'
$protocRoot = Join-Path $privateRoot 'protoc-36.2'

# 每个外部工具都有本函数作为进程 owner：有限等待，超时终止整个子树并 Wait，最后 Dispose。
# ArgumentList 按参数传递，不拼接 shell 文本；读取两条管道避免输出填满造成 Wait 死锁。
function Invoke-Bounded([string]$Program, [string[]]$Arguments, [int]$Seconds = 180) {
    $info = [System.Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $Program
    $info.WorkingDirectory = $moduleRoot
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    foreach ($argument in $Arguments) { $info.ArgumentList.Add($argument) }
    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $info
    $started = $false
    $primaryFailure = $null
    $cleanupFailure = $null
    try {
        if (-not $process.Start()) { throw "启动失败: $Program" }
        $started = $true
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit($Seconds * 1000)) {
            $process.Kill($true)
            if (-not $process.WaitForExit(10000)) { throw "超时且子进程未退出: $Program" }
            throw "工具运行超过 ${Seconds}s，已终止并 join: $Program"
        }
        $out = $stdout.GetAwaiter().GetResult()
        $err = $stderr.GetAwaiter().GetResult()
        if ($out) { Write-Output $out.TrimEnd() }
        if ($err) { Write-Output $err.TrimEnd() }
        if ($process.ExitCode -ne 0) { throw "工具退出 $($process.ExitCode): $Program" }
    } catch {
        $primaryFailure = $_
    } finally {
        try {
            if ($started -and -not $process.HasExited) {
                $process.Kill($true)
                if (-not $process.WaitForExit(10000)) { throw "清理预算10s耗尽，工具未退出: $Program" }
            }
        } catch { $cleanupFailure = $_ }
        finally { $process.Dispose() }
    }
    # finally清理错误不能覆盖原来的生成/编译错误，两条原因都交给脚本caller。
    if ($primaryFailure -and $cleanupFailure) { throw [System.AggregateException]::new('工具失败且收尾失败', [System.Exception[]]@($primaryFailure.Exception, $cleanupFailure.Exception)) }
    if ($primaryFailure) { throw $primaryFailure }
    if ($cleanupFailure) { throw $cleanupFailure }
}

$goProgram = (Get-Command go).Source
# 环境变量的“未定义”和“存在但为空”是不同状态。PowerShell 7.6 赋回 $null
# 可能留下一个空串变量；所以 caller 的 owner 同时保存存在性与精确原值。
$savedGobin = [pscustomobject]@{
    Exists = Test-Path -LiteralPath 'Env:GOBIN'
    Value = $env:GOBIN
}
try {
    if ($Install) {
        New-Item -ItemType Directory -Path $toolBin -Force | Out-Null
        $archive = Join-Path $privateRoot 'protoc-36.2-win64.zip'
        if (-not (Test-Path -LiteralPath "$protocRoot/bin/protoc.exe")) {
            Invoke-WebRequest -Uri 'https://github.com/protocolbuffers/protobuf/releases/download/v36.2/protoc-36.2-win64.zip' -OutFile $archive -TimeoutSec 120
            $actualHash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash
            if ($actualHash -ne 'F0C128DC0D8492ECEECE83BB459A4C0E316764B929FFBF1AA416357FD644EDD3') { throw 'protoc archive SHA256 与本课实际固定归档不符' }
            Expand-Archive -LiteralPath $archive -DestinationPath $protocRoot -Force
        }
        $env:GOBIN = $toolBin
        Invoke-Bounded $goProgram @('install', 'google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12')
        Invoke-Bounded $goProgram @('install', 'google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2')
        Invoke-Bounded $goProgram @('install', 'github.com/go-kratos/kratos/cmd/protoc-gen-go-http/v3@v3.0.0-20260626125723-668db92c2c00')
    }
    Invoke-Bounded $goProgram @('mod', 'download', 'github.com/go-kratos/kratos/v3@v3.0.0')
    $kratosPath = (& $goProgram -C $moduleRoot list -m -f '{{.Dir}}' github.com/go-kratos/kratos/v3)
    if ($LASTEXITCODE -ne 0 -or -not $kratosPath) { throw '锁定 Kratos 源码目录未解析' }
    $protoc = Join-Path $protocRoot 'bin/protoc.exe'
    foreach ($name in @('go', 'go-grpc', 'go-http')) {
        $path = Join-Path $toolBin "protoc-gen-$name.exe"
        if (-not (Test-Path -LiteralPath $path)) { throw "缺少 $path；先运行 -Install" }
        if ($name -ne 'go-http') { Invoke-Bounded $path @('--version') }
    }
    Invoke-Bounded $protoc @('--version')
    $outputRoot = $moduleRoot
    if ($Check) { $outputRoot = Join-Path $privateRoot 'regenerated'; New-Item -ItemType Directory -Path $outputRoot -Force | Out-Null }
    $arguments = @("--proto_path=$moduleRoot/proto", "--proto_path=$kratosPath/third_party", "--proto_path=$protocRoot/include")
    foreach ($name in @('go', 'go-grpc', 'go-http')) {
        $arguments += "--plugin=protoc-gen-$name=$toolBin/protoc-gen-$name.exe"
        $arguments += "--${name}_out=$outputRoot"
        $arguments += "--${name}_opt=module=go-demo/lessons/m32_framework_source_reading"
    }
    $arguments += 'inventory/v1/inventory.proto'
    Invoke-Bounded $protoc $arguments
    if ($Check) {
        foreach ($file in Get-ChildItem -LiteralPath "$moduleRoot/gen/inventoryv1" -Filter '*.go') {
            $generated = Join-Path $outputRoot "gen/inventoryv1/$($file.Name)"
            if ((Get-FileHash -LiteralPath $file.FullName).Hash -ne (Get-FileHash -LiteralPath $generated).Hash) { throw "生成差异: $($file.Name)" }
        }
        Write-Output 'GENERATION_IDENTICAL: pb/grpc/http 三个生成文件逐字节相同；所有工具已 Wait/Dispose'
    }
} finally {
    if ($savedGobin.Exists) {
        Set-Item -LiteralPath 'Env:GOBIN' -Value $savedGobin.Value
    } else {
        Remove-Item -LiteralPath 'Env:GOBIN' -ErrorAction SilentlyContinue
    }
}
