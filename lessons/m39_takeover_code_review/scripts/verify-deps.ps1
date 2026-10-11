$ErrorActionPreference = 'Stop'
$m39Module = Split-Path -Parent $PSScriptRoot
$m39Repo = Split-Path -Parent (Split-Path -Parent $m39Module)
$m39Base = 'cf11684d94745063faf6e2c34bf345205e562853'
# 记录原Go命令真实结果；本地replace的缺ziphash不能写成原整命令成功。
go -C $m39Module mod verify
$m39OriginalExit = $LASTEXITCODE
Write-Output "M39_ORIGINAL_MOD_VERIFY_EXIT=$m39OriginalExit"
if ($m39OriginalExit -notin @(0,1)) { throw '原依赖校验出现新的退出状态' }
$m39Paths = @('lessons/m37_order_backend_capstone/go.mod','lessons/m37_order_backend_capstone/go.sum','lessons/m31_grpc_protobuf/go.mod','lessons/m31_grpc_protobuf/go.sum')
$m39Paths += Get-ChildItem -LiteralPath (Join-Path $m39Repo 'lessons/m37_order_backend_capstone/orderbackend') -Filter '*.go' | Where-Object { $_.Name -notlike '*_test.go' } | ForEach-Object { 'lessons/m37_order_backend_capstone/orderbackend/' + $_.Name }
$m39Paths += Get-ChildItem -LiteralPath (Join-Path $m39Repo 'lessons/m31_grpc_protobuf/gen/inventoryv1') -Filter '*.go' | ForEach-Object { 'lessons/m31_grpc_protobuf/gen/inventoryv1/' + $_.Name }
foreach ($m39Path in $m39Paths) {
    $m39Expected = git -C $m39Repo rev-parse ($m39Base + ':' + $m39Path)
    if ($LASTEXITCODE -ne 0) { throw "基线缺文件 $m39Path" }
    $m39Actual = git -C $m39Repo hash-object ('--path=' + $m39Path) (Join-Path $m39Repo $m39Path)
    if ($LASTEXITCODE -ne 0 -or $m39Actual -cne $m39Expected) { throw "实际消费前置漂移 $m39Path" }
    Write-Output "SOURCE_MATCH $m39Path $m39Actual"
}
$m39Remote = @(go -C $m39Module list -m -f '{{if and .Version (not .Replace)}}{{.Path}} {{.Version}}{{end}}' all | Where-Object { $_.Trim() -ne '' })
if ($LASTEXITCODE -ne 0 -or $m39Remote.Count -lt 1) { throw '实际远端图读取失败' }
$m39Temp = Join-Path $m39Module '.local/remote-verify'
New-Item -ItemType Directory -Path $m39Temp -Force | Out-Null
$m39Text = "module m39-remote-verification`n`ngo 1.27.0`n`nrequire (`n" + (($m39Remote | ForEach-Object { "`t" + $_ }) -join "`n") + "`n)`n"
[IO.File]::WriteAllText((Join-Path $m39Temp 'go.mod'),$m39Text,[Text.UTF8Encoding]::new($false))
go -C $m39Temp mod download
if ($LASTEXITCODE -ne 0) { throw '远端图下载失败' }
go -C $m39Temp mod verify
if ($LASTEXITCODE -ne 0) { throw '远端图独立verify失败' }
Write-Output ('M39_REMOTE_GRAPH_VERIFIED=' + $m39Remote.Count)
