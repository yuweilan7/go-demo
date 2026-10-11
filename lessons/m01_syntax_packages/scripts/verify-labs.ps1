# 入口从任意目录调用；所有 go 子进程显式 -C，并把 cwd 固定为本模块。
# 子进程最多 30 秒，退出后统一读取输出；超时 Kill 整棵子进程，避免遗留故障进程。
$ErrorActionPreference = 'Stop'
$moduleRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$originalProxy = $env:GOPROXY
$originalSumDB = $env:GOSUMDB
$originalWork = $env:GOWORK

function Invoke-GoLab {
    param([string]$Label, [string[]]$Arguments, [int]$ExpectedCode = 0, [string]$ExpectedText = '')
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = 'go'
    $info.WorkingDirectory = $moduleRoot
    $info.UseShellExecute = $false
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    foreach ($argument in $Arguments) { [void]$info.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $info
    [void]$process.Start()
    # 两个流并行读取，不能先 WaitForExit 后读导致输出管道填满而死锁。
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    try {
        if (-not $process.WaitForExit(30000)) {
            $process.Kill($true)
            $process.WaitForExit()
            throw "$Label 超过 30 秒，已停止子进程"
        }
        $output = $stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult()
        # 诊断走 information/host 流，返回值只保留子进程输出字符串；调用方可据其检查先后次序。
        Write-Host "=== $Label | go $($Arguments -join ' ') | exit=$($process.ExitCode) ==="
        Write-Host $output.TrimEnd()
        if ($process.ExitCode -ne $ExpectedCode) { throw "$Label 退出码不符，预期 $ExpectedCode" }
        if ($ExpectedText -and -not $output.Contains($ExpectedText)) { throw "$Label 缺少证据：$ExpectedText" }
        return $output
    } finally {
        $process.Dispose()
    }
}

# 断言明确事件确实出现且按序排列，不能让缺失事件的 IndexOf=-1 误通过比较。
function Assert-EventOrder {
    param([string]$Output, [string[]]$Events)
    $previous = -1
    foreach ($event in $Events) {
        $position = $Output.IndexOf($event, [StringComparison]::Ordinal)
        if ($position -lt 0 -or $position -le $previous) { throw "L05 事件缺失或顺序错误：$event" }
        $previous = $position
    }
}

try {
    # 用进程范围环境变量使“消费者不继承 replace”失败立即、确定地出现，不探测网络。
    # 不写 go env -w，不修改全局配置；finally 恢复本脚本进程此前的三个值。
    $env:GOWORK = 'off'
    $env:GOPROXY = 'off'
    $env:GOSUMDB = 'off'
    $multi = Join-Path $moduleRoot 'labs/multifile'
    [void](Invoke-GoLab 'L04 单文件漏选' @('-C', $multi, 'run', 'main.go') 1 'undefined: formatReceipt')
    [void](Invoke-GoLab 'L04 整包正常' @('-C', $multi, 'run', '.') 0 'total_cents=2500')
    $init = Join-Path $moduleRoot 'labs/initorder'
    $forward = Invoke-GoLab 'L05 文件 a 在前' @('-C', $init, 'run', 'main.go', 'a.go', 'z.go') 0 'base=10 derived=11'
    $reverse = Invoke-GoLab 'L05 文件 z 在前' @('-C', $init, 'run', 'main.go', 'z.go', 'a.go') 0 'base=10 derived=11'
    Assert-EventOrder $forward @('[L05 variable] base=10', '[L05 variable] derived=11', 'a.go.init', 'z.go.init', '[L05 main]')
    Assert-EventOrder $reverse @('[L05 variable] base=10', '[L05 variable] derived=11', 'z.go.init', 'a.go.init', '[L05 main]')
    foreach ($name in @('singlefile', 'twopackages')) {
        [void](Invoke-GoLab "L06 $name" @('-C', $moduleRoot, 'run', "./labs/$name") 0 'total_cents=2500')
    }
    $app = Join-Path $moduleRoot 'labs/deps/app'
    # -json 把最终选中版本与 Replace 分开，避免把本地源码字符串误认成真实发布版本证据。
    $selected = Invoke-GoLab 'L07 选中 rules' @('-C', $app, 'list', '-m', '-json', 'example.com/m01/rules') 0 'v1.2.0'
    $moduleInfo = $selected | ConvertFrom-Json
    if ($moduleInfo.Path -ne 'example.com/m01/rules' -or $moduleInfo.Version -ne 'v1.2.0' -or -not $moduleInfo.Indirect -or $moduleInfo.Replace.Path -ne '../rules') {
        throw 'L07 最终版本/间接依赖/替换位置与模块图不符'
    }
    [void](Invoke-GoLab 'L07 模块图' @('-C', $app, 'mod', 'graph') 0 'example.com/m01/quote@v1.0.0 example.com/m01/rules@v1.1.0')
    [void](Invoke-GoLab 'L07 app 实际调用' @('-C', $app, 'run', '.') 0 'receipt via local-rules-source')
    $consumer = Join-Path $moduleRoot 'labs/deps/consumer'
    [void](Invoke-GoLab 'L07 消费者遗漏 replace' @('-C', $consumer, 'run', '-mod=mod', '.') 1 'module lookup disabled by GOPROXY=off')
    [void](Invoke-GoLab 'L07 消费者补齐 replace' @('-C', $consumer, 'run', '-mod=mod', '-modfile=fixed.mod', '.') 0 'quote via local-rules-source')
    $failures = @{
        'no-new' = 'no new variables on left side of :='
        'if-scope' = 'undefined: err'
        'export' = 'cannot refer to unexported method save'
        'overflow' = 'overflows'
    }
    foreach ($name in @('no-new', 'if-scope', 'export', 'overflow')) {
        [void](Invoke-GoLab "L08 $name" @('-C', $moduleRoot, 'run', "./testdata/compilefail/$name/main.go") 1 $failures[$name])
    }
    Write-Output 'PASS: L04-L08 正常路径和预期失败证据均已核对，子进程已全部退出。'
} finally {
    $env:GOPROXY = $originalProxy
    $env:GOSUMDB = $originalSumDB
    $env:GOWORK = $originalWork
}
