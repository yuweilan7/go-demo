param()

$ErrorActionPreference = 'Stop'
$moduleRoot = Split-Path -Parent $PSScriptRoot
Push-Location -LiteralPath $moduleRoot
try {
    if (-not (Test-Path -LiteralPath '.local/latest/report.json')) {
        throw '请先在本模块执行 go run ./cmd/demo，生成 .local/latest。'
    }
    # 各报告从真实二进制 profile 生成。原始数据累计到采样点，fixed 快照仍包含 bad
    # 的历史；diff_base 用相同进程中的 bad 样本作基线，不把历史等待算作新等待。
    $commands = @(
        @{ Name='cpu-bad-top.txt'; Args=@('-top','-nodecount=12','.local/latest/cpu_bad.prof') },
        @{ Name='cpu-bad-source.txt'; Args=@('-list=digestOnce','.local/latest/cpu_bad.prof') },
        @{ Name='cpu-fixed-top.txt'; Args=@('-top','-nodecount=12','.local/latest/cpu_fixed.prof') },
        @{ Name='block-bad-top.txt'; Args=@('-top','-nodecount=12','.local/latest/block-bad.prof') },
        @{ Name='block-bad-source.txt'; Args=@('-list=badCheckout','.local/latest/block-bad.prof') },
        @{ Name='mutex-bad-top.txt'; Args=@('-top','-nodecount=12','.local/latest/mutex-bad.prof') },
        @{ Name='mutex-fixed-diff.txt'; Args=@('-top','-nodecount=12','-diff_base','.local/latest/mutex-bad.prof','.local/latest/mutex-fixed.prof') },
        @{ Name='heap-bad-top.txt'; Args=@('-top','-nodecount=12','.local/latest/heap-bad.prof') },
        @{ Name='heap-bad-source.txt'; Args=@('-list=retainBad','.local/latest/heap-bad.prof') },
        @{ Name='heap-fixed-top.txt'; Args=@('-top','-nodecount=12','.local/latest/heap-fixed.prof') }
    )
    foreach ($item in $commands) {
        $arguments = $item.Args
        $lines = & go tool pprof @arguments
        if ($LASTEXITCODE -ne 0) { throw "pprof failed: $($item.Name)" }
        $lines | Set-Content -LiteralPath (Join-Path '.local/latest' $item.Name) -Encoding utf8
    }
    $traceLines = & go tool trace -d=parsed .local/latest/runtime-trace.out
    if ($LASTEXITCODE -ne 0) { throw 'runtime trace parser failed' }
    $traceLines | Set-Content -LiteralPath '.local/latest/runtime-trace-parsed.txt' -Encoding utf8
    $traceLines | Select-String -Pattern 'TaskBegin|TaskEnd|RegionBegin|RegionEnd|Category="request_id"|queue.global_mutex|dependency.http|m34.checkout' |
        ForEach-Object { $_.Line } | Set-Content -LiteralPath '.local/latest/runtime-trace-events.txt' -Encoding utf8

    # go tool trace -pprof 的 stdout 是 gzip protobuf 二进制。用固定 cmd 重定向保留
    # 字节，避免旧 PowerShell 把它当字符串转码；没有插入任何来自用户输入的命令片段。
    & cmd.exe /d /c 'go tool trace -pprof=sync .local\latest\runtime-trace.out > .local\latest\trace-sync.prof'
    if ($LASTEXITCODE -ne 0) { throw 'trace sync conversion failed' }
    & cmd.exe /d /c 'go tool trace -pprof=net .local\latest\runtime-trace.out > .local\latest\trace-net.prof'
    if ($LASTEXITCODE -ne 0) { throw 'trace net conversion failed' }
    foreach ($type in @('sync','net')) {
        $lines = & go tool pprof -top -nodecount=12 ".local/latest/trace-$type.prof"
        if ($LASTEXITCODE -ne 0) { throw "trace $type pprof failed" }
        $lines | Set-Content -LiteralPath ".local/latest/trace-$type-top.txt" -Encoding utf8
    }
    Write-Output 'PASS: CPU/block/mutex/heap source and top reports; runtime trace parse + sync/net profiles.'
}
finally { Pop-Location }
