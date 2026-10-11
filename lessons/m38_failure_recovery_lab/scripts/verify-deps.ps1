$ErrorActionPreference = 'Stop'
$m38Module = Split-Path -Parent $PSScriptRoot
$m38Repo = Split-Path -Parent (Split-Path -Parent $m38Module)
# 使用主agent冻结的完整第4层基线；校验失败就停止，不能猜测最新源码。
$m38Base = '81c802b18292d52f196f538e7627d5d9bdf6a9d3'
go -C $m38Module mod verify
$m38OriginalExit = $LASTEXITCODE
Write-Output "M38_ORIGINAL_MOD_VERIFY_EXIT=$m38OriginalExit"
if ($m38OriginalExit -ne 1) { throw '本地replace的原verify结果发生变化，请重新核对' }

$m38Paths = @('lessons/m37_order_backend_capstone/go.mod','lessons/m37_order_backend_capstone/go.sum','lessons/m31_grpc_protobuf/go.mod','lessons/m31_grpc_protobuf/go.sum')
$m38Paths += Get-ChildItem -LiteralPath (Join-Path $m38Repo 'lessons/m37_order_backend_capstone/orderbackend') -Filter '*.go' | Where-Object { $_.Name -notlike '*_test.go' } | ForEach-Object { 'lessons/m37_order_backend_capstone/orderbackend/' + $_.Name }
$m38Paths += Get-ChildItem -LiteralPath (Join-Path $m38Repo 'lessons/m31_grpc_protobuf/gen/inventoryv1') -Filter '*.go' | ForEach-Object { 'lessons/m31_grpc_protobuf/gen/inventoryv1/' + $_.Name }
foreach ($m38Path in $m38Paths) {
    $m38Expected = git -C $m38Repo rev-parse ($m38Base + ':' + $m38Path)
    if ($LASTEXITCODE -ne 0) { throw "基线缺文件 $m38Path" }
    $m38Actual = git -C $m38Repo hash-object ('--path=' + $m38Path) (Join-Path $m38Repo $m38Path)
    if ($LASTEXITCODE -ne 0 -or $m38Actual -cne $m38Expected) { throw "实际消费前置源码漂移 $m38Path" }
    Write-Output "SOURCE_MATCH $m38Path $m38Actual"
}

# 按本课实际module图列举远端版本，独立临时module去掉本地replace再执行verify。
# 这条结果只证明该远端图，不改写上面原整命令的exit1，也不收编本地源码。
$m38Remote = @(go -C $m38Module list -m -f '{{if and .Version (not .Replace)}}{{.Path}} {{.Version}}{{end}}' all | Where-Object { $_.Trim() -ne '' })
if ($LASTEXITCODE -ne 0 -or $m38Remote.Count -lt 1) { throw '实际远端module图不可读取' }
$m38Temp = Join-Path $m38Module '.local/remote-verify'
New-Item -ItemType Directory -Path $m38Temp -Force | Out-Null
$m38Text = "module m38-remote-dependency-verification`n`ngo 1.27.0`n`nrequire (`n" + (($m38Remote | ForEach-Object { "`t" + $_ }) -join "`n") + "`n)`n"
[IO.File]::WriteAllText((Join-Path $m38Temp 'go.mod'),$m38Text,[Text.UTF8Encoding]::new($false))
go -C $m38Temp mod download
if ($LASTEXITCODE -ne 0) { throw '远端图下载失败' }
go -C $m38Temp mod verify
if ($LASTEXITCODE -ne 0) { throw '远端独立verify失败' }
Write-Output ('M38_REMOTE_GRAPH_VERIFIED=' + $m38Remote.Count)
