$ErrorActionPreference = 'Stop'
$moduleRoot = Split-Path -Parent $PSScriptRoot
$privateRoot = Join-Path $moduleRoot '.local'
$protoc = Join-Path $privateRoot 'protoc-36.2/bin/protoc.exe'
if (-not (Test-Path -LiteralPath $protoc)) { throw '先用 generate.ps1 -Install 准备固定 protoc' }

# 错误协议只写本模块忽略目录；正常 proto 不混入故意失败的 schema。
$invalidSource = Join-Path $privateRoot 'invalid-reserved.proto'
@'
syntax = "proto3";
message Illegal {
  reserved 3;
  string reused = 3;
}
'@ | Set-Content -LiteralPath $invalidSource -Encoding utf8
$diagnostics = & $protoc "--proto_path=$privateRoot" "--descriptor_set_out=$privateRoot/invalid-reserved.pb" 'invalid-reserved.proto' 2>&1
$expectedFailure = $LASTEXITCODE
$diagnosticText = ($diagnostics | ForEach-Object { $_.ToString() }) -join [Environment]::NewLine
Write-Output $diagnosticText
# 除退出码外还核对实际错误原因；下载损坏、路径错误等非 schema 错误不能算 reserved 实验通过。
if ($expectedFailure -eq 0 -or $diagnosticText -notmatch 'uses reserved number 3') { throw '未观察到期望的 reserved 编号拒绝' }
Write-Output "PASS reserved 编号复用被拒绝，protoc exit=$expectedFailure"
